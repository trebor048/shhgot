package core

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/github"
	"github.com/trebor048/shhgot/core/store"
	"github.com/trebor048/shhgot/core/verifiers"
	"golang.org/x/oauth2"
)

type Session struct {
	Version          string
	Log              *Logger
	Options          *Options
	Config           *Config
	Signatures       []Signature
	Repositories     chan GitResource
	Gists            chan string
	Comments         chan Comment
	Context          context.Context
	Clients          chan *GitHubClientWrapper
	ExhaustedClients chan *GitHubClientWrapper
	CsvWriter        *csv.Writer
	CsvFile          *os.File
	MatchLogger      *MatchLogger
	CleanupManager   *CleanupManager
	TokenValidator   *TokenValidator
	Progress         *ProgressManager
	Store            store.Store
	FoundTokens      map[string]bool
	TokenMutex       sync.Mutex
	RemovedTokens    map[string]bool
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
	s.InitLogger()
	s.InitProgress()
	s.InitThreads()
	s.InitSignatures()
	s.InitGitHubClients()
	s.InitCsvWriter()
	s.InitMatchLogger()
	s.InitCleanupManager()
	s.InitTokenValidator()
	s.InitStore()
	s.InitRegexOptimizer()
}

func (s *Session) InitLogger() {
	s.Log = &Logger{}
	s.Log.SetDebug(*s.Options.Debug)
	s.Log.SetSilent(*s.Options.Silent)
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

		// Don't validate tokens during init - they'll be tested on first use
		// This prevents hangs on startup if GitHub is slow/down
		for _, token := range s.Config.GitHubAccessTokens {
			ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
			tc := oauth2.NewClient(s.Context, ts)

			client := github.NewClient(tc)
			client.UserAgent = fmt.Sprintf("%s v%s", Name, Version)

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
					// Put it back without spawning a sender goroutine: under
					// sustained rate limiting the pool stays full and every
					// 50 ms poll leaked one goroutine blocked on the send
					// forever. Parking this waiter (which has nothing better
					// to do while every token is exhausted) fixes the leak
					// with identical pool behavior.
					requeued := false
					for !requeued {
						select {
						case s.ExhaustedClients <- client:
							requeued = true
						case <-time.After(50 * time.Millisecond):
						}
					}
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
//
// The return is non-blocking: pool capacity covers every client in flight, so
// a full channel means a duplicate return and parking here would wedge the
// worker forever. The duplicate is dropped with a warning, mirroring
// AddGitHubToken's pool top-up.
func (s *Session) FreeClient(client *GitHubClientWrapper) {
	if s.IsTokenRemoved(client.Token) {
		return
	}
	if client.RateLimitedUntil.After(time.Now()) {
		select {
		case s.ExhaustedClients <- client:
		default:
			s.Log.Warn("Exhausted client pool is full; dropping a duplicate %s[..] return", MaskToken(client.Token))
		}
		return
	}
	select {
	case s.Clients <- client:
	default:
		s.Log.Warn("GitHub client pool is full; dropping a duplicate %s[..] return", MaskToken(client.Token))
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

// GitHubTokens returns a copy of the configured GitHub tokens. The copy is
// taken under TokenMutex because AddGitHubToken, RemoveUnauthorizedToken and
// the token pruner rewrite the slice mid-run; ranging the live slice directly
// is a data race.
func (s *Session) GitHubTokens() []string {
	s.TokenMutex.Lock()
	defer s.TokenMutex.Unlock()
	if s.Config == nil {
		return nil
	}
	return append([]string(nil), s.Config.GitHubAccessTokens...)
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
	low := s.lowImpact()

	// A negative --threads is treated like the unset 0 rather than reaching
	// InitGitHubClients, where a negative value becomes a negative channel
	// capacity and make(chan T, n) panics.
	if *s.Options.Threads <= 0 {
		numCPUs := runtime.NumCPU()
		if low && numCPUs > 2 {
			// Leave half the machine free so the scan does not make it unusable.
			numCPUs /= 2
		}
		s.Options.Threads = &numCPUs
	}

	// GOMAXPROCS bounds the whole process, not just the workers, so it is the
	// lever that decides whether a scan starves the desktop. A normal run keeps
	// one extra P of headroom; a low-impact run does not.
	cpus := *s.Options.Threads
	if !low {
		cpus++
	}
	if cpus < 1 {
		cpus = 1
	}
	runtime.GOMAXPROCS(cpus)

	if low {
		ReduceProcessPriority()
	}
}

// lowImpact reports whether this run should trade throughput for a responsive
// machine: --low-impact, or performance.low_impact in config.yaml.
func (s *Session) lowImpact() bool {
	if s.Options != nil && s.Options.LowImpact != nil && *s.Options.LowImpact {
		return true
	}
	if s.Config != nil {
		return s.Config.Performance.Bool(s.Config.Performance.LowImpact, false)
	}
	return false
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
	if err != nil {
		// Without the return, csv.NewWriter(nil *os.File) was stored and the
		// first WriteToCsv panicked on a nil file handle.
		LogIfError("Could not create/open CSV file", err)
		return
	}

	s.CsvWriter = csv.NewWriter(file)
	s.CsvFile = file

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

	s.CleanupManager = NewCleanupManager(*s.Options.TempDirectory, maxDiskUsageMB, s.Log)
	s.CleanupManager.cleanupIntervalSecs = cleanupIntervalSecs
	if s.Config.Cleanup.CleanupThresholdMB > 0 {
		s.CleanupManager.cleanupThresholdMB = s.Config.Cleanup.CleanupThresholdMB
	}
	s.Log.Debug("Cleanup manager initialized with max disk usage: %dMB, threshold: %dMB, interval: %ds", maxDiskUsageMB, s.CleanupManager.cleanupThresholdMB, cleanupIntervalSecs)
}

func (s *Session) WriteToCsv(line []string) {
	if *s.Options.CsvPath == "" || s.CsvWriter == nil {
		return
	}

	if err := s.CsvWriter.Write(line); err != nil {
		LogIfError("Could not write CSV row", err)
		return
	}
	s.CsvWriter.Flush()
	if err := s.CsvWriter.Error(); err != nil {
		LogIfError("Could not flush CSV writer", err)
	}
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

		// Fold the "scanning" section into the options the pipeline reads
		// (file/repo size caps, clone timeout). Must happen before Start(), whose
		// workers capture these values.
		session.Config.ApplyScanningOverrides(session.Options)

		// Webhook pacing comes from config. Without this the queue used a
		// hard-coded spacing value that its delivery loop never actually applied,
		// so bursts of matches went out back-to-back and tripped Discord's limit.
		ConfigureWebhookQueue(session.Config.WebhookQueue)

		// Rebuild the shared outbound HTTP client from config before Start()
		// constructs the validator that binds it. Without this the singleton was
		// built from an empty config, so every http.* key was silently ignored.
		ConfigureSharedHTTPClient(session.Config)

		// Recreate the queue channels at the configured buffer sizes now that
		// the config is parsed (no consumer has touched them yet). Absent or
		// <= 0 queue_buffer_size keeps the built-in 1000/100/1000 defaults.
		if n := session.Config.Performance.Int(session.Config.Performance.QueueBufferSize, 0); n > 0 {
			gistBuf := n / 10
			if gistBuf < 1 {
				// A small queue_buffer_size (e.g. 5) made this 0, turning the
				// gist channel unbuffered while the other two stayed buffered.
				gistBuf = 1
			}
			session.Repositories = make(chan GitResource, n)
			session.Gists = make(chan string, gistBuf)
			session.Comments = make(chan Comment, n)
		}

		session.Start()
	})

	return session
}

func (s *Session) InitTokenValidator() {
	s.TokenValidator = NewTokenValidator(s.Log)
	// Apply the "validators:" section. Without this the cache stayed nil and
	// every validators.* key (cache_enabled in particular) was a no-op, while
	// max_concurrency never bounded anything.
	if s.Config != nil {
		s.TokenValidator.Cache = s.Config.NewValidationCache()
		s.TokenValidator.SetConcurrency(s.Config.ValidatorConcurrency())
	}
	s.Log.Debug("Token validator initialized")
}

// InitStore opens the persistent findings store described by config. The store
// is disabled unless storage.enabled is true, so this is a no-op store by
// default. A store that cannot be opened (or a journal that cannot be read)
// degrades to a disabled store: the scan must never be blocked by it.
func (s *Session) InitStore() {
	cfg := store.Config{}
	if s.Config != nil {
		cfg = s.Config.StoreConfig()
	}
	st, err := store.New(cfg)
	if err != nil {
		s.Log.Warn("Findings store unavailable; continuing without dedupe or resume: %s", err)
		st, _ = store.New(store.Config{})
	}
	s.Store = st
	if cfg.Enabled && st.Degraded() {
		s.Log.Warn("Findings store is degraded; cross-run dedupe is disabled for this run")
	}
	if cfg.Enabled {
		s.Log.Debug("Findings store enabled at %s", cfg.Path)
	}
}

// CloseStore flushes and closes the findings store. It is safe to call more
// than once and with a nil store.
func (s *Session) CloseStore() {
	// Release the CSV descriptor too: InitCsvWriter opened the file and the
	// handle was previously never closed, leaking it until process exit.
	if s.CsvWriter != nil {
		s.CsvWriter.Flush()
		s.CsvWriter = nil
	}
	if s.CsvFile != nil {
		_ = s.CsvFile.Close()
		s.CsvFile = nil
	}
	if s.Store != nil {
		_ = s.Store.Close()
	}
}

// SaveCursor persists a named scan cursor. It is a no-op when no store is
// configured, so cursor bookkeeping never gates the scan.
func (s *Session) SaveCursor(name string, v any) error {
	if s.Store == nil {
		return nil
	}
	return s.Store.SaveCursor(name, v)
}

// LoadCursor restores a named scan cursor, returning store.ErrNotFound when it
// was never written or no store is configured.
func (s *Session) LoadCursor(name string, out any) error {
	if s.Store == nil {
		return store.ErrNotFound
	}
	return s.Store.LoadCursor(name, out)
}

// resumeEnabled reports whether persisted scan cursors may be reused. --no-resume
// forces every run to start fresh.
func (s *Session) resumeEnabled() bool {
	return s.Options == nil || s.Options.NoResume == nil || !*s.Options.NoResume
}

// newGitHubValidationClient builds the API client AddGitHubToken validates a
// candidate token with. It is a package variable (mirroring pruneGitHubUserURL
// in the main package) so tests can point it at a stub server instead of
// api.github.com.
var newGitHubValidationClient = func(ctx context.Context, token string) *github.Client {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)
	client.UserAgent = fmt.Sprintf("%s v%s", Name, Version)
	return client
}

// AddGitHubToken adds a new GitHub token to the pool dynamically.
//
// Neither the validation call nor the pool top-up happens while TokenMutex is
// held. That lock also guards IsTokenRemoved and hasUsableTokens, which every
// GetClient call needs, so holding it across a timeout-less HTTP request - or
// across a channel send that can block - froze the entire scanner.
func (s *Session) AddGitHubToken(token string) bool {
	s.TokenMutex.Lock()
	if s.FoundTokens[token] {
		s.TokenMutex.Unlock()
		return false
	}
	// Every bearer format the shipped signatures recognise (classic PAT,
	// OAuth, user/server app tokens, refresh token, fine-grained PAT) is
	// accepted: they all authenticate a GET /user call. Rejecting some here
	// while the extractor and detector accept them silently skipped validation.
	githubTokenPrefixes := []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_"}
	knownPrefix := false
	for _, p := range githubTokenPrefixes {
		if strings.HasPrefix(token, p) {
			knownPrefix = true
			break
		}
	}
	if !knownPrefix {
		s.TokenMutex.Unlock()
		return false
	}
	s.FoundTokens[token] = true
	s.TokenMutex.Unlock()

	// Bound the validation call: s.Context carries no deadline of its own.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client := newGitHubValidationClient(ctx, token)

	if _, _, err := client.Users.Get(ctx, ""); err != nil {
		s.Log.Warn("Failed to validate new token %s[..]: %s", MaskToken(token), err)
		// Roll the claim back. The token was marked FoundTokens before the
		// validating request; a transient failure (timeout, rate limit, DNS
		// blip) must not permanently refuse an otherwise-valid token, so a
		// later encounter re-queues it for validation.
		s.TokenMutex.Lock()
		delete(s.FoundTokens, token)
		s.TokenMutex.Unlock()
		return false
	}

	// The token validated against the GitHub API: persist it like every other
	// verified credential. Previously a valid GitHub token only got an Info
	// line and never reached logs/priority3.log, so validated GitHub tokens
	// never showed up in the logs.
	if s.TokenValidator != nil {
		if err := s.TokenValidator.SaveValidToken(token, ProviderGithub, "Valid GitHub Token"); err != nil {
			s.Log.Warn("Failed to save valid GitHub token %s[..]: %s", MaskToken(token), err)
		} else {
			s.Log.LogWithColor("#10A37F", "✓ %s token is VALID!", ProviderGithub)
			s.Log.Important("Response: %s", "Valid GitHub Token")
			s.Log.Important("✅ Valid token saved to logs/priority3.log")
		}
		// Announce the verdict through the validator's callback contract,
		// exactly as TestAndLogToken does. Without this a validated GitHub
		// token never reached the web Tokens tab or the outcome cache, and
		// its finding kept the priority-fallback severity.
		masked := token
		if len(token) > 20 {
			masked = token[:17] + "..."
		}
		if s.TokenValidator.OnTokenResult != nil {
			s.TokenValidator.OnTokenResult(masked, true, ProviderGithub)
		}
		if s.TokenValidator.OnOutcome != nil {
			s.TokenValidator.OnOutcome(masked, ProviderGithub, verifiers.Outcome{Status: verifiers.Valid, Detail: "Valid GitHub Token"})
		}
	}

	s.TokenMutex.Lock()
	s.Config.GitHubAccessTokens = append(s.Config.GitHubAccessTokens, token)
	// A previously-revoked token that now validates successfully is usable
	// again, so clear its removed marker.
	delete(s.RemovedTokens, token)
	s.TokenMutex.Unlock()

	// Top the pool up without ever blocking. InitGitHubClients leaves exactly
	// *Threads free slots per configured token, but this loop used to run one
	// extra time (i <= Threads), so its final send blocked forever on a full
	// channel while TokenMutex was still held - a deadlock that hung the scan.
	// The default arm also covers a nil Clients channel, which is what --local
	// leaves behind.
	for i := 0; i < *s.Options.Threads; i++ {
		select {
		case s.Clients <- &GitHubClientWrapper{client, token, time.Now().Add(-1 * time.Second)}:
		default:
			s.Log.Warn("GitHub client pool is full; %s[..] is added but not pooled this run", MaskToken(token))
			return true
		}
	}

	s.Log.Info("Added new GitHub token: %s[..]", MaskToken(token))
	return true
}

// InitRegexOptimizer initializes the global regex optimizer
func (s *Session) InitRegexOptimizer() {
	maxWorkers := *s.Options.Threads * 2 // 2x threads for better parallelism
	timeoutMs := 5000

	InitGlobalOptimizer(maxWorkers, timeoutMs)
	s.Log.Debug("Regex optimizer initialized: %d workers", maxWorkers)

	// Pre-compile all signature patterns. Signatures without a regex (or whose
	// regex failed to compile at load time) are skipped, so the count below has
	// to be of what was actually handed to the optimizer - reporting
	// len(s.Signatures) claimed patterns that were never compiled.
	compiled := 0
	for _, sig := range s.Signatures {
		re := sig.GetRegex()
		if re == nil {
			continue
		}
		if _, err := GlobalRegexOptimizer.CompilePattern(
			re.String(),
			sig.Name(),
			sig.GetPriority(),
			sig.GetPart(),
		); err != nil {
			// CompilePattern already logged the pattern and its error.
			continue
		}
		compiled++
	}
	s.Log.Info("Pre-compiled %d regex patterns", compiled)
}
