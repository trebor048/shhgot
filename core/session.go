package core

import (
	"context"
	"encoding/csv"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/github"
	"golang.org/x/oauth2"
)

// TokenValidationResult holds one token's validation outcome.
type TokenValidationResult struct {
	Token  string
	Valid  bool
	Error  string
}

type Session struct {
	sync.Mutex

	Version                string
	Log                    *Logger
	Options                *Options
	Config                 *Config
	Signatures             []Signature
	Repositories           chan GitResource
	Gists                  chan string
	Comments               chan Comment
	Context                context.Context
	Clients                chan *GitHubClientWrapper
	ExhaustedClients       chan *GitHubClientWrapper
	CsvWriter              *csv.Writer
	MatchLogger            *MatchLogger
	CleanupManager         *CleanupManager
	TokenValidator         *TokenValidator
	Progress               *ProgressManager
	FoundTokens            map[string]bool
	TokenMutex             sync.Mutex
	TokenValidationResults []TokenValidationResult
}

var (
	session     *Session
	sessionSync sync.Once
	err         error
)

func (s *Session) Start() {
	rand.Seed(time.Now().Unix())

	s.InitLogger()
	s.InitProgress()
	s.InitThreads()
	s.InitSignatures()
	s.InitGitHubClients()
	s.InitCsvWriter()
	s.InitMatchLogger()
	s.InitCleanupManager()
	s.InitTokenValidator()
}

func (s *Session) InitLogger() {
	s.Log = &Logger{}
	s.Log.SetDebug(*s.Options.Debug)
	s.Log.SetSilent(*s.Options.Silent)
	s.Log.SetConfig(s.Config)
}

func (s *Session) InitProgress() {
	s.Progress = NewProgressManager()
}

func (s *Session) InitSignatures() {
	s.Signatures = GetSignatures(s)
}

func (s *Session) InitGitHubClients() {
	if len(*s.Options.Local) <= 0 {
		chanSize := *s.Options.Threads * (len(s.Config.GitHubAccessTokens) + 1)
		s.Clients = make(chan *GitHubClientWrapper, chanSize)
		s.ExhaustedClients = make(chan *GitHubClientWrapper, chanSize)
		s.TokenValidationResults = make([]TokenValidationResult, 0)

		for _, token := range s.Config.GitHubAccessTokens {
			ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
			tc := oauth2.NewClient(s.Context, ts)

			client := github.NewClient(tc)
			client.UserAgent = fmt.Sprintf("%s v%s", Name, Version)
			_, _, err := client.Users.Get(s.Context, "")

			if err != nil {
				if _, ok := err.(*github.ErrorResponse); ok {
					s.TokenValidationResults = append(s.TokenValidationResults, TokenValidationResult{
						Token: token[:10],
						Valid: false,
						Error: err.Error(),
					})
					continue
				}
			}

			s.TokenValidationResults = append(s.TokenValidationResults, TokenValidationResult{
				Token: token[:10],
				Valid: true,
			})

			for i := 0; i <= *s.Options.Threads; i++ {
				s.Clients <- &GitHubClientWrapper{client, token, time.Now().Add(-1 * time.Second)}
			}
		}

		if len(s.Clients) < 1 {
			s.Log.Fatal("No valid GitHub tokens provided. Quitting!")
		}
	}
}

func (s *Session) GetClient() *GitHubClientWrapper {
	for {
		// Try to get an available client without blocking
		select {
		case client := <-s.Clients:
			s.Log.Debug("Using client with token: %s", client.Token[:10])
			return client
		case <-time.After(50 * time.Millisecond):
			// Check exhausted clients
			select {
			case client := <-s.ExhaustedClients:
				sleepTime := time.Until(client.RateLimitedUntil)
				if sleepTime > 0 {
					s.Log.Debug("Token %s[..] still rate limited for %s", client.Token[:10], sleepTime.String())
					// Put it back
					go func() { s.ExhaustedClients <- client }()
				} else {
					s.Log.Debug("Token %s[..] rate limit reset, returning to pool", client.Token[:10])
					return client
				}
			default:
				// No exhausted clients, continue waiting
			}
		}
	}
}

// FreeClient returns the GitHub Client to the pool of available,
// non-rate-limited channel of clients in the session
func (s *Session) FreeClient(client *GitHubClientWrapper) {
	if client.RateLimitedUntil.After(time.Now()) {
		s.ExhaustedClients <- client
	} else {
		s.Clients <- client
	}
}

func (s *Session) InitThreads() {
	if *s.Options.Threads == 0 {
		numCPUs := runtime.NumCPU()
		s.Options.Threads = &numCPUs
	}

	runtime.GOMAXPROCS(*s.Options.Threads + 1)
}

func (s *Session) InitCsvWriter() {
	if *s.Options.CsvPath == "" {
		return
	}

	writeHeader := false
	if !PathExists(*s.Options.CsvPath) {
		writeHeader = true
	}

	file, err := os.OpenFile(*s.Options.CsvPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	LogIfError("Could not create/open CSV file", err)

	s.CsvWriter = csv.NewWriter(file)

	if writeHeader {
		s.WriteToCsv([]string{"Repository name", "Signature name", "Matching file", "Matches"})
	}
}

// InitMatchLogger initializes the per-match-type append log writer. The
// directory defaults to logs/matches and the feature is on by default.
func (s *Session) InitMatchLogger() {
	dir := s.Config.MatchLogDir
	if dir == "" {
		dir = filepath.Join("logs", "matches")
	}

	enabled := true
	if s.Config.MatchLogEnabled != nil {
		enabled = *s.Config.MatchLogEnabled
	}

	s.MatchLogger = NewMatchLogger(dir, enabled)
}

// LogMatch forwards one detected match to the match logger. It is a no-op when
// the logger is nil or disabled.
func (s *Session) LogMatch(signatureName, url, fileName string, matches []string) {
	if s.MatchLogger == nil {
		return
	}
	s.MatchLogger.LogMatch(signatureName, url, fileName, matches)
}

func (s *Session) InitCleanupManager() {
	// Use config values if provided, otherwise use defaults
	maxDiskUsageMB := uint64(5120)   // Default 5GB
	cleanupIntervalSecs := 30        // Default 30 seconds
	
	if s.Config.Cleanup.MaxDiskUsageMB > 0 {
		maxDiskUsageMB = s.Config.Cleanup.MaxDiskUsageMB
	}
	if s.Config.Cleanup.CleanupIntervalSecs > 0 {
		cleanupIntervalSecs = s.Config.Cleanup.CleanupIntervalSecs
	}
	
	s.CleanupManager = NewCleanupManager(*s.Options.TempDirectory, maxDiskUsageMB)
	s.CleanupManager.cleanupIntervalSecs = cleanupIntervalSecs
	s.Log.Debug("Cleanup manager initialized with max disk usage: %dMB, interval: %ds", maxDiskUsageMB, cleanupIntervalSecs)
}

func (s *Session) WriteToCsv(line []string) {
	if *s.Options.CsvPath == "" {
		return
	}

	s.CsvWriter.Write(line)
	s.CsvWriter.Flush()
}

func GetSession() *Session {
	sessionSync.Do(func() {
		session = &Session{
			Context:      context.Background(),
			Repositories: make(chan GitResource, 1000),
			Gists:        make(chan string, 100),
			Comments:     make(chan Comment, 1000),
			FoundTokens:  make(map[string]bool),
		}

		if session.Options, err = ParseOptions(); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		if session.Config, err = ParseConfig(session.Options); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		session.Start()
	})

	return session
}

func (s *Session) InitTokenValidator() {
	s.TokenValidator = NewTokenValidator(s.Log)
	s.Log.Debug("Token validator initialized")
}

// AddGitHubToken adds a new GitHub token to the pool dynamically
func (s *Session) AddGitHubToken(token string) bool {
	s.TokenMutex.Lock()
	defer s.TokenMutex.Unlock()

	// Check if already added
	if s.FoundTokens[token] {
		return false
	}

	// Validate token format
	if !strings.HasPrefix(token, "ghp_") && !strings.HasPrefix(token, "github_pat_") {
		return false
	}

	s.FoundTokens[token] = true

	// Test the token
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(s.Context, ts)
	client := github.NewClient(tc)
	client.UserAgent = fmt.Sprintf("%s v%s", Name, Version)

	_, _, err := client.Users.Get(s.Context, "")
	if err != nil {
		s.Log.Warn("Failed to validate new token %s[..]: %s", token[:10], err)
		return false
	}

	// Add to config
	s.Config.GitHubAccessTokens = append(s.Config.GitHubAccessTokens, token)

	// Add to client pool
	for i := 0; i <= *s.Options.Threads; i++ {
		s.Clients <- &GitHubClientWrapper{client, token, time.Now().Add(-1 * time.Second)}
	}

	s.Log.Info("Added new GitHub token: %s[..]", token[:10])
	return true
}
