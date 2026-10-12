package core

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"

	"gopkg.in/yaml.v3"
)

type Config struct {
	GitHubAccessTokens           []string          `yaml:"github_access_tokens"`
	AITokens                     []string          `yaml:"ai_tokens"`
	Webhook                      string            `yaml:"webhook,omitempty"`
	WebhookPayload               string            `yaml:"webhook_payload,omitempty"`
	WebhookQueue                 WebhookQueueCfg   `yaml:"webhook_queue,omitempty"`
	WebhookDiscordUserID         string            `yaml:"webhook_discord_user_id,omitempty"`
	BlacklistedStrings           []string          `yaml:"blacklisted_strings"`
	BlacklistedExtensions        []string          `yaml:"blacklisted_extensions"`
	BlacklistedPaths             []string          `yaml:"blacklisted_paths"`
	BlacklistedEntropyExtensions []string          `yaml:"blacklisted_entropy_extensions"`
	Signatures                   []ConfigSignature `yaml:"signatures"`
	LogFormat                    string            `yaml:"logFormat,omitempty"`
	TypewriterEffect             bool              `yaml:"typewriter_effect,omitempty"`
	TypewriterDelayMS            int               `yaml:"typewriter_delay_ms,omitempty"`
	Performance                  PerformanceConfig `yaml:"performance,omitempty"`
	Scanning                     ScanningConfig    `yaml:"scanning,omitempty"`
	Cleanup                      CleanupConfig     `yaml:"cleanup,omitempty"`
	MatchLogDir                  string            `yaml:"match_log_dir,omitempty"`
	MatchLogEnabled              *bool             `yaml:"match_log_enabled,omitempty"`
	AIReview                     AIReviewConfig    `yaml:"ai_review,omitempty"`

	HTTP       HTTPConfig       `yaml:"http,omitempty"`
	Storage    StorageConfig    `yaml:"storage,omitempty"`
	Search     SearchConfig     `yaml:"search,omitempty"`
	AIFilter   AIFilterConfig   `yaml:"ai_filter,omitempty"`
	Notify     NotifyConfig     `yaml:"notify,omitempty"`
	Sources    SourcesConfig    `yaml:"sources,omitempty"`
	Validators ValidatorsConfig `yaml:"validators,omitempty"`
	Metrics    MetricsConfig    `yaml:"metrics,omitempty"`
}

// MetricsConfig gates the in-process metrics collector (core.DefaultMetrics).
// Metrics are on by default; set enabled: false to skip the per-operation
// latency/counter bookkeeping entirely.
type MetricsConfig struct {
	Enabled *bool `yaml:"enabled,omitempty"`
}

// MetricsEnabled reports whether metrics collection is on. An absent key
// defaults to true, so metrics are collected unless explicitly disabled.
func (c *Config) MetricsEnabled() bool {
	if c == nil {
		return true
	}
	return boolOr(c.Metrics.Enabled, true)
}

// metricsEnabled is the process-wide gate read on the hot paths. It mirrors the
// loaded config and defaults to true so a zero-value setup still collects.
var metricsEnabled atomic.Bool

func init() { metricsEnabled.Store(true) }

// SetMetricsEnabled turns metric collection on or off process-wide. It is called
// once at startup from ConfigureSharedHTTPClient after the config is loaded.
func SetMetricsEnabled(v bool) { metricsEnabled.Store(v) }

// MetricsCollectionEnabled reports whether the hot paths should record metrics.
func MetricsCollectionEnabled() bool { return metricsEnabled.Load() }

// intOr returns the configured value, or def when the key is absent or <= 0.
func intOr(v *int, def int) int {
	if v != nil && *v > 0 {
		return *v
	}
	return def
}

// boolOr returns the configured value, or def when the key is absent.
func boolOr(v *bool, def bool) bool {
	if v != nil {
		return *v
	}
	return def
}

// HTTPConfig tunes the shared outbound HTTP client (core/httpclient).
type HTTPConfig struct {
	Retries                 *int     `yaml:"retries,omitempty"`
	BackoffInitialMS        *int     `yaml:"backoff_initial_ms,omitempty"`
	BackoffMaxMS            *int     `yaml:"backoff_max_ms,omitempty"`
	Jitter                  *bool    `yaml:"jitter,omitempty"`
	TimeoutSeconds          *int     `yaml:"timeout_seconds,omitempty"`
	PacingMS                *int     `yaml:"pacing_ms,omitempty"`
	BreakerFailureThreshold *int     `yaml:"breaker_failure_threshold,omitempty"`
	BreakerCooldownSeconds  *int     `yaml:"breaker_cooldown_seconds,omitempty"`
	BreakerHalfOpenProbes   *int     `yaml:"breaker_half_open_probes,omitempty"`
	ProtectedHosts          []string `yaml:"protected_hosts,omitempty"`
	// ProxyURL is a single outbound proxy (http, https, socks5 or socks5h) used
	// for every request. Proxies takes precedence when both are set.
	ProxyURL string `yaml:"proxy_url,omitempty"`
	// Proxies rotates one proxy per request; a member that fails
	// proxy_ban_after times consecutively is benched. When every member is
	// benched the client falls back to a direct connection.
	Proxies []string `yaml:"proxies,omitempty"`
	// ProxyBanAfter overrides the consecutive-failure count that benches a
	// Proxies member. Absent/zero uses the pool default (3).
	ProxyBanAfter *int `yaml:"proxy_ban_after,omitempty"`
}

// StorageConfig controls the persistent findings store (core/store). Disabled by
// default so an existing operator's output is unchanged until they opt in.
type StorageConfig struct {
	Enabled          *bool  `yaml:"enabled,omitempty"`
	Path             string `yaml:"path,omitempty"`
	DedupeTTLSeconds *int   `yaml:"dedupe_ttl_seconds,omitempty"`
	MaxDedupeEntries *int   `yaml:"max_dedupe_entries,omitempty"`
	CompactAfter     *int   `yaml:"compact_after,omitempty"`
}

// SearchConfig controls the GitHub code-search discovery source. Off by default.
type SearchConfig struct {
	Enabled    *bool    `yaml:"enabled,omitempty"`
	Queries    []string `yaml:"queries,omitempty"`
	QueryFile  string   `yaml:"query_file,omitempty"`
	PerPage    *int     `yaml:"per_page,omitempty"`
	MaxPages   *int     `yaml:"max_pages,omitempty"`
	MaxResults *int     `yaml:"max_results,omitempty"`
	SleepMS    *int     `yaml:"sleep_ms,omitempty"`
	MinStars   *int     `yaml:"min_stars,omitempty"`
}

// AIFilterConfig controls the opt-in false-positive pre-filter (core/aifilter).
type AIFilterConfig struct {
	Enabled      *bool  `yaml:"enabled,omitempty"`
	QuickFilter  *bool  `yaml:"quick_filter,omitempty"`
	Provider     string `yaml:"provider,omitempty"`
	Model        string `yaml:"model,omitempty"`
	BaseURL      string `yaml:"base_url,omitempty"`
	Workers      *int   `yaml:"workers,omitempty"`
	CacheEntries *int   `yaml:"cache_entries,omitempty"`
	CacheTTLSecs *int   `yaml:"cache_ttl_seconds,omitempty"`
}

// NotifyConfig configures extra alert channels and delivery policy.
type NotifyConfig struct {
	TestOnStart *bool            `yaml:"test_on_start,omitempty"`
	QuietHours  QuietHoursConfig `yaml:"quiet_hours,omitempty"`
	Channels    []ChannelConfig  `yaml:"channels,omitempty"`
}

// QuietHoursConfig suppresses non-critical notifications in a local time window.
type QuietHoursConfig struct {
	Enabled *bool  `yaml:"enabled,omitempty"`
	Start   string `yaml:"start,omitempty"`
	End     string `yaml:"end,omitempty"`
}

// ChannelConfig is one alert destination.
//
// The url field carries whatever the channel needs beyond a plain endpoint; the
// per-type formats are documented on buildNotifyChannel in config_notify.go.
// Secret is a channel-specific secondary credential: the DingTalk HMAC signing
// secret, and the WxPusher recipient list (comma-separated UIDs).
type ChannelConfig struct {
	Type        string `yaml:"type"`
	URL         string `yaml:"url,omitempty"`
	Secret      string `yaml:"secret,omitempty"`
	MinSeverity string `yaml:"min_severity,omitempty"`
	Enabled     *bool  `yaml:"enabled,omitempty"`
}

// SourcesConfig enables non-GitHub discovery sources.
type SourcesConfig struct {
	GitLab        GitLabSourceConfig      `yaml:"gitlab,omitempty"`
	GitHubCommits QuerySourceConfig       `yaml:"github_commits,omitempty"`
	GitHubIssues  QuerySourceConfig       `yaml:"github_issues,omitempty"`
	Pastebin      PastebinSourceConfig    `yaml:"pastebin,omitempty"`
	SearchCode    SearchCodeSourceConfig  `yaml:"searchcode,omitempty"`
	Sourcegraph   SourcegraphSourceConfig `yaml:"sourcegraph,omitempty"`
	GrepApp       QuerySourceConfig       `yaml:"grepapp,omitempty"`
	Psbdmp        QuerySourceConfig       `yaml:"psbdmp,omitempty"`
	Bitbucket     BitbucketSourceConfig   `yaml:"bitbucket,omitempty"`
}

// QuerySourceConfig controls a query-driven discovery source: each configured
// query is issued in turn and results are capped per pass. It is shared by the
// GitHub commit/issue search, grep.app and psbdmp sources, which have the same
// shape. Off by default.
type QuerySourceConfig struct {
	Enabled    *bool    `yaml:"enabled,omitempty"`
	Queries    []string `yaml:"queries,omitempty"`
	MaxResults *int     `yaml:"max_results,omitempty"`
	Interval   *int     `yaml:"interval_seconds,omitempty"`
}

// BitbucketSourceConfig controls the Bitbucket workspace source. Bitbucket has
// no public discovery endpoint, so the operator names the workspaces to walk.
// Off by default.
type BitbucketSourceConfig struct {
	Enabled    *bool    `yaml:"enabled,omitempty"`
	Workspaces []string `yaml:"workspaces,omitempty"`
	MaxRepos   *int     `yaml:"max_repos,omitempty"`
	MaxFiles   *int     `yaml:"max_files,omitempty"`
	Interval   *int     `yaml:"interval_seconds,omitempty"`
}

// GitLabSourceConfig controls the GitLab public-snippet source and, when
// search_enabled is set, the authenticated blobs search source. Off by default.
type GitLabSourceConfig struct {
	Enabled  *bool `yaml:"enabled,omitempty"`
	PerPage  *int  `yaml:"per_page,omitempty"`
	Interval *int  `yaml:"interval_seconds,omitempty"`
	// SearchEnabled turns on the blobs (code search) source, which needs a
	// token and a Premium/Ultimate instance for advanced search.
	SearchEnabled *bool `yaml:"search_enabled,omitempty"`
	// Token is the GitLab personal access token sent on blob-search and project
	// requests.
	Token string `yaml:"token,omitempty"`
}

// PastebinSourceConfig controls the Pastebin Scraping API source. Off by
// default. The Scraping API needs a Pastebin Pro account with the host's IP
// whitelisted; api_key only gates the source on/off (the endpoint is not
// key-authenticated).
type PastebinSourceConfig struct {
	Enabled  *bool  `yaml:"enabled,omitempty"`
	APIKey   string `yaml:"api_key,omitempty"`
	Limit    *int   `yaml:"limit,omitempty"`
	Interval *int   `yaml:"interval_seconds,omitempty"`
}

// SearchCodeSourceConfig controls the searchcode.com source. Off by default.
type SearchCodeSourceConfig struct {
	Enabled  *bool    `yaml:"enabled,omitempty"`
	Queries  []string `yaml:"queries,omitempty"`
	PerPage  *int     `yaml:"per_page,omitempty"`
	MaxPages *int     `yaml:"max_pages,omitempty"`
	Interval *int     `yaml:"interval_seconds,omitempty"`
}

// SourcegraphSourceConfig controls the Sourcegraph public stream-search
// source. Off by default.
type SourcegraphSourceConfig struct {
	Enabled    *bool    `yaml:"enabled,omitempty"`
	Queries    []string `yaml:"queries,omitempty"`
	MaxResults *int     `yaml:"max_results,omitempty"`
	Interval   *int     `yaml:"interval_seconds,omitempty"`
	Token      string   `yaml:"token,omitempty"`
}

// ValidatorsConfig tunes live credential validation.
type ValidatorsConfig struct {
	CacheEnabled       *bool `yaml:"cache_enabled,omitempty"`
	CacheTTLSecs       *int  `yaml:"cache_ttl_seconds,omitempty"`
	CacheMaxEntries    *int  `yaml:"cache_max_entries,omitempty"`
	DomainCooldownSecs *int  `yaml:"domain_cooldown_seconds,omitempty"`
	MaxConcurrency     *int  `yaml:"max_concurrency,omitempty"`
	MinIntervalMS      *int  `yaml:"min_interval_ms,omitempty"`
}

// PerformanceConfig wires the "performance" section of config.yaml into the
// scan pipeline. Every field is a pointer so an absent key keeps the
// compiled-in default; an explicit value is honored (with a sane floor).
// Before this wiring existed the whole section was silently ignored.
type PerformanceConfig struct {
	MaxRepositoryThreads *int `yaml:"max_repository_threads,omitempty"`
	MaxGistThreads       *int `yaml:"max_gist_threads,omitempty"`
	MaxCommentThreads    *int `yaml:"max_comment_threads,omitempty"`
	APIPagesPerCycle     *int `yaml:"api_pages_per_cycle,omitempty"`
	APIPerPage           *int `yaml:"api_per_page,omitempty"`
	APISleepSeconds      *int `yaml:"api_sleep_seconds,omitempty"`
	WorkerPoolSize       *int `yaml:"worker_pool_size,omitempty"`
	QueueBufferSize      *int `yaml:"queue_buffer_size,omitempty"`
	MaxFileCount         *int `yaml:"max_file_count,omitempty"`

	// Web dashboard ring-buffer caps. Lowering them trims resident memory on a
	// long scan; each defaults to a value tuned for a small workstation.
	MaxMatches      *int `yaml:"max_matches,omitempty"`
	MaxLogLines     *int `yaml:"max_log_lines,omitempty"`
	MaxTokenResults *int `yaml:"max_token_results,omitempty"`
	MaxFileDetails  *int `yaml:"max_file_details,omitempty"`
	MaxFileDetailKB *int `yaml:"max_file_detail_kb,omitempty"`

	// LowImpact trades throughput for a usable machine: it halves the worker
	// count and drops the process to below-normal CPU priority.
	LowImpact *bool `yaml:"low_impact,omitempty"`
}

// Int returns the configured value, or def when the key is absent or <= 0.
func (p PerformanceConfig) Int(v *int, def int) int {
	if v != nil && *v > 0 {
		return *v
	}
	return def
}

// Bool returns the configured value, or def when the key is absent. An explicit
// false is meaningful, so unlike Int there is no "<= 0" case.
func (p PerformanceConfig) Bool(v *bool, def bool) bool {
	if v != nil {
		return *v
	}
	return def
}

// ScanningConfig wires the "scanning" section of config.yaml into the scan
// pipeline. yaml.Unmarshal silently drops keys with no matching field, so
// before this struct existed the entire section was ignored: max_file_count
// under "scanning" had no effect and repositories with thousands of files were
// still walked in full.
//
// Every field is a pointer so an absent key keeps the compiled-in default (or
// the value passed on the command line); an explicit positive value wins.
// max_file_count is also still read from the legacy performance.max_file_count
// key - see Config.MaxFileCount.
type ScanningConfig struct {
	MaxFileSizeMB    *int  `yaml:"max_file_size_mb,omitempty"`
	MaxRepoSizeMB    *int  `yaml:"max_repo_size_mb,omitempty"`
	MaxFileCount     *int  `yaml:"max_file_count,omitempty"`
	CloneDepth       *int  `yaml:"clone_depth,omitempty"`
	CloneTimeoutSecs *int  `yaml:"clone_timeout_seconds,omitempty"`
	ScanTimeoutSecs  *int  `yaml:"scan_timeout_seconds,omitempty"`
	SkipForks        *bool `yaml:"skip_forks,omitempty"`
	SkipArchived     *bool `yaml:"skip_archived,omitempty"`
	// UseNativeGit prefers a git binary on PATH over the pure-Go clone. Defaults
	// to true; set false to force the built-in implementation.
	UseNativeGit *bool `yaml:"use_native_git,omitempty"`
	// ScanBinary re-enables scanning binary files. Defaults to false: text
	// signatures cannot match them, so reading them is wasted I/O, but a project
	// that embeds secrets in binaries can set this true.
	ScanBinary *bool `yaml:"scan_binary,omitempty"`
	// RescanRepositories restores per-push cloning: by default a repository is
	// cloned and scanned at most once per run (the events feed repeats the same
	// pushes constantly). Set true if you must re-scan every pushed commit.
	RescanRepositories *bool `yaml:"rescan_repositories,omitempty"`
	// MaxScanMB bounds the bytes read per repository (0 = no limit). Wall-clock
	// limits are unpredictable under load; a byte budget is not.
	MaxScanMB *int `yaml:"max_scan_mb,omitempty"`
	// ContextLines is the number of lines captured around a match for display
	// and AI review. 0 (default) disables context capture.
	ContextLines *int `yaml:"context_lines,omitempty"`
}

// Int returns the configured value, or def when the key is absent or <= 0.
func (s ScanningConfig) Int(v *int, def int) int {
	if v != nil && *v > 0 {
		return *v
	}
	return def
}

// Bool returns the configured value, or def when the key is absent. Unlike Int
// there is no "<= 0" case: an explicit false is a meaningful setting.
func (s ScanningConfig) Bool(v *bool, def bool) bool {
	if v != nil {
		return *v
	}
	return def
}

// MaxFileCount resolves the per-repository file cap. A value in the "scanning"
// section wins; the legacy performance.max_file_count key is still honoured so
// an existing config keeps working; otherwise def is returned. The caller
// supplies def so the compiled-in default lives in one place.
func (c *Config) MaxFileCount(def int) int {
	if c == nil {
		return def
	}
	if n := c.Scanning.Int(c.Scanning.MaxFileCount, 0); n > 0 {
		return n
	}
	return c.Performance.Int(c.Performance.MaxFileCount, def)
}

// ApplyScanningOverrides folds the "scanning" section into the CLI Options the
// scan pipeline already reads. config.yaml is in MB and the options are in KB,
// so the size keys are converted; only positive values override, leaving the
// flag value alone when the key is absent.
//
// This runs once, right after ParseConfig, before any worker reads the options.
func (c *Config) ApplyScanningOverrides(o *Options) {
	if c == nil || o == nil {
		return
	}
	if mb := c.Scanning.Int(c.Scanning.MaxFileSizeMB, 0); mb > 0 {
		v := uint(mb) * 1024 // config is MB, the option is KB
		o.MaximumFileSize = &v
	}
	if mb := c.Scanning.Int(c.Scanning.MaxRepoSizeMB, 0); mb > 0 {
		v := uint(mb) * 1024
		o.MaximumRepositorySize = &v
	}
	if secs := c.Scanning.Int(c.Scanning.CloneTimeoutSecs, 0); secs > 0 {
		v := uint(secs)
		o.CloneRepositoryTimeout = &v
	}
}

// AIReviewConfig supplies the startup defaults for the AI security review.
//
// The values below are only a seed: they are written into ai_review/settings.json
// the first time shhgit runs, after which the dashboard's Settings tab owns them.
//
// Precedence, highest first:
//  1. what the operator saves in the dashboard's Settings tab (persisted to
//     ai_review/settings.json) - which on a first run is the seeded value below,
//  2. the environment variables read by aiproviders (SHHGIT_AI_PROVIDER,
//     SHHGIT_AI_API_KEY, SHHGIT_AI_BASE_URL, SHHGIT_AI_MODEL, the per-provider
//     SHHGIT_AI_<PROVIDER>_* forms, and the DEEPSEEK_API_KEY / OPENAI_API_KEY
//     aliases), used per field for anything the saved settings leave blank,
//  3. the built-in per-provider defaults.
//
// Provider is one of deepseek, openai, custom or ollama.
type AIReviewConfig struct {
	Provider     string `yaml:"provider,omitempty" json:"provider"`
	APIKey       string `yaml:"api_key,omitempty" json:"api_key"`
	BaseURL      string `yaml:"base_url,omitempty" json:"base_url"`
	Model        string `yaml:"model,omitempty" json:"model"`
	SystemPrompt string `yaml:"system_prompt,omitempty" json:"system_prompt"`

	// ReviewDir is where reviews and AI settings are stored. Defaults to
	// ai_review, which is gitignored because reviews contain plaintext secrets.
	ReviewDir string `yaml:"review_dir,omitempty" json:"review_dir"`

	// Legacy single-backend keys. Still honoured so an existing config.yaml
	// keeps working; the keys above win when both are set.
	Backend       string `yaml:"backend,omitempty" json:"backend"`
	DeepseekKey   string `yaml:"deepseek_api_key,omitempty" json:"deepseek_api_key"`
	DeepseekModel string `yaml:"deepseek_model,omitempty" json:"deepseek_model"`
	OllamaURL     string `yaml:"ollama_url,omitempty" json:"ollama_url"`
	OllamaModel   string `yaml:"ollama_model,omitempty" json:"ollama_model"`
}

type CleanupConfig struct {
	MaxDiskUsageMB      uint64 `yaml:"max_disk_usage_mb,omitempty"`
	CleanupThresholdMB  uint64 `yaml:"cleanup_threshold_mb,omitempty"`
	CleanupIntervalSecs int    `yaml:"cleanup_interval_secs,omitempty"`
}

// WebhookQueueCfg configures webhook delivery with deduplication and rate limiting
type WebhookQueueCfg struct {
	QueueSize       int   `yaml:"queue_size,omitempty"`
	RateLimitMS     int64 `yaml:"rate_limit_ms,omitempty"`
	DedupeWindowSec int   `yaml:"dedupe_window_sec,omitempty"`
	// BatchSize coalesces up to this many queued Discord findings into a single
	// message (Discord allows ten embeds per message). 1 disables batching.
	BatchSize int `yaml:"batch_size,omitempty"`
}

type ConfigSignature struct {
	Name           string   `yaml:"name"`
	Part           string   `yaml:"part"`
	Match          string   `yaml:"match,omitempty"`
	Regex          string   `yaml:"regex,omitempty"`
	Verifier       string   `yaml:"verifier,omitempty"`
	Priority       int      `yaml:"priority,omitempty"`
	Color          string   `yaml:"color,omitempty"`
	ExcludeInModes []string `yaml:"exclude_in_modes,omitempty"`
	// NotRegex is an optional RE2 guard tested against each candidate match
	// (the matched text for contents rules, the path string for path rules).
	// A hit discards that candidate. It replaces PCRE negative lookaheads
	// ((?!...)), which Go's RE2 engine cannot compile: put the broad pattern
	// in Regex and the false-positive guard here.
	NotRegex string `yaml:"not_regex,omitempty"`
}

func ParseConfig(options *Options) (*Config, error) {
	config := &Config{}
	var (
		data []byte
		err  error
	)

	if len(*options.ConfigPath) > 0 {
		data, err = os.ReadFile(path.Join(*options.ConfigPath, "config.yaml"))
		if err != nil {
			return config, fmt.Errorf("no config.yaml found in %s (copy config.yaml.example there and fill in a GitHub token, or drop --config-path to search the default locations): %w", *options.ConfigPath, err)
		}
	} else {
		// Trying to first find the configuration next to executable
		// Helps e.g. with Drone where workdir is different than shhgit dir
		// A failing os.Executable only means dir becomes "." and the read below
		// falls through to the working-directory attempt anyway.
		ex, _ := os.Executable()
		dir := filepath.Dir(ex)
		data, err = os.ReadFile(path.Join(dir, "config.yaml"))
		if err != nil {
			dir, _ = os.Getwd()
			data, err = os.ReadFile(path.Join(dir, "config.yaml"))
			if err != nil {
				return config, fmt.Errorf("no config.yaml found next to the binary or in the working directory (copy config.yaml.example to config.yaml and fill in a GitHub token): %w", err)
			}
		}
	}

	err = yaml.Unmarshal(data, config)
	if err != nil {
		return config, fmt.Errorf("config.yaml is not valid YAML: %w", err)
	}

	// Expand environment references BEFORE validating. A token written as
	// "$GITHUB_TOKEN" is a non-empty string, so it used to pass this check even
	// when the variable was unset, and the scanner then started with an empty
	// token that failed every API call with a 401 instead of saying why.
	for i := 0; i < len(config.GitHubAccessTokens); i++ {
		config.GitHubAccessTokens[i] = os.ExpandEnv(config.GitHubAccessTokens[i])
	}
	// ai_tokens accepts the same "$VAR" form as github_access_tokens. Without
	// expansion a "$OPENAI_API_KEY" entry reached the token tester literally and
	// was reported as an invalid token.
	for i := 0; i < len(config.AITokens); i++ {
		config.AITokens[i] = os.ExpandEnv(config.AITokens[i])
	}

	if len(*options.Local) <= 0 && (len(config.GitHubAccessTokens) < 1 || strings.TrimSpace(strings.Join(config.GitHubAccessTokens, "")) == "") {
		return config, errors.New("config.yaml needs at least one GitHub access token under github_access_tokens (see https://help.github.com/en/articles/creating-a-personal-access-token-for-the-command-line), or pass --local to scan a local directory instead")
	}

	if len(config.Webhook) > 0 {
		config.Webhook = os.ExpandEnv(config.Webhook)

		// Validate webhook URL based on the service
		if strings.Contains(config.Webhook, "discord.com/api/webhooks") {
			if !ValidateDiscordWebhook(config.Webhook) {
				return config, errors.New("discord webhook validation failed; check the webhook URL and permissions")
			}
		} else if strings.Contains(config.Webhook, "api.telegram.org") || strings.HasPrefix(config.Webhook, "TELEGRAM_BOT_TOKEN:") {
			// Extract the bot token from the config prefix or the documented
			// https://api.telegram.org/bot<token>/... URL form.
			var botToken string
			if strings.HasPrefix(config.Webhook, "TELEGRAM_BOT_TOKEN:") {
				botToken = strings.TrimPrefix(config.Webhook, "TELEGRAM_BOT_TOKEN:")
			} else {
				botToken = telegramTokenFromWebhookURL(config.Webhook)
			}
			if botToken != "" && !ValidateTelegramWebhook(botToken) {
				return config, errors.New("telegram webhook validation failed; check the bot token and webhook configuration")
			}
		}
	}

	return config, nil
}

// telegramTokenFromWebhookURL extracts the bot token from a Telegram API URL of
// the documented form https://api.telegram.org/bot<token>/<method>. It returns
// "" when the path carries no bot token, so a misconfigured URL is skipped
// rather than validated against a bogus value.
func telegramTokenFromWebhookURL(webhook string) string {
	u, err := url.Parse(webhook)
	if err != nil {
		return ""
	}
	const prefix = "/bot"
	if !strings.HasPrefix(u.Path, prefix) {
		return ""
	}
	token := strings.TrimPrefix(u.Path, prefix)
	if i := strings.IndexByte(token, '/'); i >= 0 {
		token = token[:i]
	}
	return token
}

// ConfigFilePath resolves the config.yaml path using the same precedence as
// ParseConfig: an explicit --config path, then the executable's directory,
// then the working directory. Returns "" if no candidate exists. Used by the
// startup GitHub-token pruning to locate the file to edit.
func ConfigFilePath(options *Options) string {
	if len(*options.ConfigPath) > 0 {
		return path.Join(*options.ConfigPath, "config.yaml")
	}
	if ex, err := os.Executable(); err == nil {
		p := path.Join(filepath.Dir(ex), "config.yaml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// The working-directory candidate is verified like the executable one: this
	// function promises to return "" when no candidate exists, and returning an
	// unchecked path made the caller try to rewrite a file that was not there.
	if dir, err := os.Getwd(); err == nil {
		p := path.Join(dir, "config.yaml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func (c *Config) UnmarshalYAML(unmarshal func(interface{}) error) error {
	*c = Config{}
	type plain Config

	err := unmarshal((*plain)(c))

	if err != nil {
		return err
	}

	return nil
}
