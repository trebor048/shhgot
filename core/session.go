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
	Token string
	Valid bool
	Error string
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
	RemovedTokens          map[string]bool
	TokenValidationResults []TokenValidationResult
}

var (
	session     *Session
	sessionSync sync.Once
	err         error
)

// TokenPersistRemover is installed by the main package to delete a token from
// the on-disk config.yaml when GitHub reports it as unauthorized (HTTP 401)
// mid-run. core has no knowledge of the config file format, so the actual
// rewrite lives in main; when the hook is nil only the in-memory token is
// dropped.
var TokenPersistRemover func(token string) error

// MaskToken returns a log-safe prefix of a token so secrets never reach the
// console or logs in full. Short tokens are returned unchanged.
func MaskToken(token string) string {
	if len(token) <= 10 {
		return token
	}
	return token[:10]
}

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
	s.InitRegexOptimizer()
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

		// Don't validate tokens during init - they'll be tested on first use
		// This prevents hangs on startup if GitHub is slow/down
		for _, token := range s.Config.GitHubAccessTokens {
			ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
			tc := oauth2.NewClient(s.Context, ts)

			client := github.NewClient(tc)
			client.UserAgent = fmt.Sprintf("%s v%s", Name, Version)

			// Mark as needing validation but add to pool immediately
			s.TokenValidationResults = append(s.TokenValidationResults, TokenValidationResult{
				Token: token[:10],
				Valid: true, // Assume valid; will be tested on use
			})

			for i := 0; i < *s.Options.Threads; i++ {
				s.Clients <- &GitHubClientWrapper{client, token, time.Now().Add(-1 * time.Second)}
			}
		}

		if len(s.Config.GitHubAccessTokens) < 1 {
			s.Log.Fatal("No GitHub tokens provided. Quitting!")
		}
	}
}

func (s *Session) GetClient() *GitHubClientWrapper {
	for {
		// Without any configured token there is nothing left to scan with;
		// exit instead of spinning on an empty pool.
		if !s.hasUsableTokens() {
			s.Log.Fatal("All GitHub tokens were removed as invalid (401). Quitting!")
		}

		// Try to get an available client without blocking
		select {
		case client := <-s.Clients:
			// A token revoked mid-run must never be handed out again.
			if s.IsTokenRemoved(client.Token) {
				continue
			}
			s.Log.Debug("Using client with token: %s", MaskToken(client.Token))
			return client
		case <-time.After(50 * time.Millisecond):
			// Check exhausted clients
			select {
			case client := <-s.ExhaustedClients:
				if s.IsTokenRemoved(client.Token) {
					continue
				}
				sleepTime := time.Until(client.RateLimitedUntil)
				if sleepTime > 0 {
					s.Log.Debug("Token %s[..] still rate limited for %s", MaskToken(client.Token), sleepTime.String())
					// Put it back
					go func() { s.ExhaustedClients <- client }()
				} else {
					s.Log.Debug("Token %s[..] rate limit reset, returning to pool", MaskToken(client.Token))
					return client
				}
			default:
				// No exhausted clients, continue waiting
			}
		}
	}
}

// FreeClient returns the GitHub Client to the pool of available,
// non-rate-limited channel of clients in the session. Clients whose token was
// revoked mid-run are dropped instead of recycled.
func (s *Session) FreeClient(client *GitHubClientWrapper) {
	if s.IsTokenRemoved(client.Token) {
		return
	}
	if client.RateLimitedUntil.After(time.Now()) {
		s.ExhaustedClients <- client
	} else {
		s.Clients <- client
	}
}

// IsTokenRemoved reports whether a token was dropped at runtime after GitHub
// rejected it with HTTP 401.
func (s *Session) IsTokenRemoved(token string) bool {
	s.TokenMutex.Lock()
	defer s.TokenMutex.Unlock()
	return s.RemovedTokens[token]
}

// hasUsableTokens reports whether any GitHub token remains configured.
func (s *Session) hasUsableTokens() bool {
	s.TokenMutex.Lock()
	defer s.TokenMutex.Unlock()
	return s.Config != nil && len(s.Config.GitHubAccessTokens) > 0
}

// RemoveUnauthorizedToken handles a mid-run HTTP 401 (bad credentials) for
// token: the token is dropped from the in-memory config and marked so every
// pooled client using it is skipped, and it is persisted out of config.yaml via
// the TokenPersistRemover hook. The first 401 wins; repeat calls are no-ops, so
// concurrent API calls sharing a revoked token only rewrite the file once.
func (s *Session) RemoveUnauthorizedToken(token string) {
	s.TokenMutex.Lock()
	if s.RemovedTokens[token] {
		s.TokenMutex.Unlock()
		return
	}
	s.RemovedTokens[token] = true

	var existing []string
	if s.Config != nil {
		existing = s.Config.GitHubAccessTokens
	}
	kept := make([]string, 0, len(existing))
	for _, t := range existing {
		if t != token {
			kept = append(kept, t)
		}
	}
	if s.Config != nil {
		s.Config.GitHubAccessTokens = kept
	}
	remaining := len(kept)
	s.TokenMutex.Unlock()

	// File I/O happens outside the lock: it is slow and must not block the
	// client pool.
	if TokenPersistRemover != nil {
		if err := TokenPersistRemover(token); err != nil {
			s.Log.Warn("Could not remove unauthorized token %s[..] from config.yaml: %s", MaskToken(token), err)
		}
	}

	s.Log.Warn("GitHub token %s[..] returned HTTP 401 (bad credentials) — removed from config.yaml and disabled for this run", MaskToken(token))
	if remaining == 0 {
		s.Log.Warn("No GitHub tokens remain; the scanner will stop.")
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
	maxDiskUsageMB := uint64(5120) // Default 5GB
	cleanupIntervalSecs := 30      // Default 30 seconds

	if s.Config.Cleanup.MaxDiskUsageMB > 0 {
		maxDiskUsageMB = s.Config.Cleanup.MaxDiskUsageMB
	}
	if s.Config.Cleanup.CleanupIntervalSecs > 0 {
		cleanupIntervalSecs = s.Config.Cleanup.CleanupIntervalSecs
	}

	s.CleanupManager = NewCleanupManager(*s.Options.TempDirectory, maxDiskUsageMB)
	s.CleanupManager.cleanupIntervalSecs = cleanupIntervalSecs
	if s.Config.Cleanup.CleanupThresholdMB > 0 {
		s.CleanupManager.cleanupThresholdMB = s.Config.Cleanup.CleanupThresholdMB
	}
	s.Log.Debug("Cleanup manager initialized with max disk usage: %dMB, threshold: %dMB, interval: %ds", maxDiskUsageMB, s.CleanupManager.cleanupThresholdMB, cleanupIntervalSecs)
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
			Context:       context.Background(),
			Repositories:  make(chan GitResource, 1000),
			Gists:         make(chan string, 100),
			Comments:      make(chan Comment, 1000),
			FoundTokens:   make(map[string]bool),
			RemovedTokens: make(map[string]bool),
		}

		if session.Options, err = ParseOptions(); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		if session.Config, err = ParseConfig(session.Options); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		// Recreate the queue channels at the configured buffer sizes now that
		// the config is parsed (no consumer has touched them yet). Absent or
		// <= 0 queue_buffer_size keeps the built-in 1000/100/1000 defaults.
		if n := session.Config.Performance.Int(session.Config.Performance.QueueBufferSize, 0); n > 0 {
			session.Repositories = make(chan GitResource, n)
			session.Gists = make(chan string, n/10)
			session.Comments = make(chan Comment, n)
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

	// A previously-revoked token that now validates successfully is usable
	// again, so clear its removed marker.
	delete(s.RemovedTokens, token)

	// Add to client pool
	for i := 0; i <= *s.Options.Threads; i++ {
		s.Clients <- &GitHubClientWrapper{client, token, time.Now().Add(-1 * time.Second)}
	}

	s.Log.Info("Added new GitHub token: %s[..]", token[:10])
	return true
}

// InitRegexOptimizer initializes the global regex optimizer
func (s *Session) InitRegexOptimizer() {
	maxWorkers := *s.Options.Threads * 2 // 2x threads for better parallelism
	timeoutMs := 5000

	InitGlobalOptimizer(maxWorkers, timeoutMs)
	s.Log.Debug("Regex optimizer initialized: %d workers", maxWorkers)

	// Pre-compile all signature patterns
	for _, sig := range s.Signatures {
		if sig.GetRegex() != nil {
			GlobalRegexOptimizer.CompilePattern(
				sig.GetRegex().String(),
				sig.Name(),
				sig.GetPriority(),
				sig.GetPart(),
			)
		}
	}
	s.Log.Info("Pre-compiled %d regex patterns", len(s.Signatures))
}
