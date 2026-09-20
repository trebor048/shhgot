package core

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	GitHubAccessTokens           []string          `yaml:"github_access_tokens"`
	AITokens                     []string          `yaml:"ai_tokens"`
	Webhook                      string            `yaml:"webhook,omitempty"`
	WebhookAITokens              string            `yaml:"webhook_ai_tokens,omitempty"`
	WebhookCrypto                string            `yaml:"webhook_crypto,omitempty"`
	WebhookPayload               string            `yaml:"webhook_payload,omitempty"`
	WebhookQueue                 WebhookQueueCfg   `yaml:"webhook_queue,omitempty"`
	BlacklistedStrings           []string          `yaml:"blacklisted_strings"`
	BlacklistedExtensions        []string          `yaml:"blacklisted_extensions"`
	BlacklistedPaths             []string          `yaml:"blacklisted_paths"`
	BlacklistedEntropyExtensions []string          `yaml:"blacklisted_entropy_extensions"`
	Signatures                   []ConfigSignature `yaml:"signatures"`
	LogFormat                    string            `yaml:"logFormat,omitempty"`
	Performance                  PerformanceConfig `yaml:"performance,omitempty"`
	Cleanup                      CleanupConfig     `yaml:"cleanup,omitempty"`
	MatchLogDir                  string            `yaml:"match_log_dir,omitempty"`
	MatchLogEnabled              *bool             `yaml:"match_log_enabled,omitempty"`
	AIReview                     AIReviewConfig    `yaml:"ai_review,omitempty"`
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
}

// Int returns the configured value, or def when the key is absent or <= 0.
func (p PerformanceConfig) Int(v *int, def int) int {
	if v != nil && *v > 0 {
		return *v
	}
	return def
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
	Enabled           bool   `yaml:"enabled,omitempty"`
	QueueSize         int    `yaml:"queue_size,omitempty"`
	RateLimitMS       int64  `yaml:"rate_limit_ms,omitempty"`
	DedupeWindowSec   int    `yaml:"dedupe_window_sec,omitempty"`
	OutputFilePath    string `yaml:"output_file_path,omitempty"`
	FileOutputEnabled bool   `yaml:"file_output_enabled,omitempty"`
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
		data, err = ioutil.ReadFile(path.Join(*options.ConfigPath, "config.yaml"))
		if err != nil {
			return config, fmt.Errorf("no config.yaml found in %s (copy config.yaml.example there and fill in a GitHub token, or drop --config-path to search the default locations): %w", *options.ConfigPath, err)
		}
	} else {
		// Trying to first find the configuration next to executable
		// Helps e.g. with Drone where workdir is different than shhgit dir
		ex, err := os.Executable()
		dir := filepath.Dir(ex)
		data, err = ioutil.ReadFile(path.Join(dir, "config.yaml"))
		if err != nil {
			dir, _ = os.Getwd()
			data, err = ioutil.ReadFile(path.Join(dir, "config.yaml"))
			if err != nil {
				return config, fmt.Errorf("no config.yaml found next to the binary or in the working directory (copy config.yaml.example to config.yaml and fill in a GitHub token): %w", err)
			}
		}
	}

	err = yaml.Unmarshal(data, config)
	if err != nil {
		return config, fmt.Errorf("config.yaml is not valid YAML: %w", err)
	}

	if len(*options.Local) <= 0 && (len(config.GitHubAccessTokens) < 1 || strings.TrimSpace(strings.Join(config.GitHubAccessTokens, "")) == "") {
		return config, errors.New("config.yaml needs at least one GitHub access token under github_access_tokens (see https://help.github.com/en/articles/creating-a-personal-access-token-for-the-command-line), or pass --local to scan a local directory instead")
	}

	for i := 0; i < len(config.GitHubAccessTokens); i++ {
		config.GitHubAccessTokens[i] = os.ExpandEnv(config.GitHubAccessTokens[i])
	}

	if len(config.Webhook) > 0 {
		config.Webhook = os.ExpandEnv(config.Webhook)

		// Validate webhook URL based on the service
		if strings.Contains(config.Webhook, "discord.com/api/webhooks") {
			if !ValidateDiscordWebhook(config.Webhook) {
				return config, errors.New("Discord webhook validation failed. Please check the webhook URL and permissions.")
			}
		} else if strings.Contains(config.Webhook, "api.telegram.org") || strings.HasPrefix(config.Webhook, "TELEGRAM_BOT_TOKEN:") {
			// Extract bot token from webhook URL or config
			var botToken string
			if strings.HasPrefix(config.Webhook, "TELEGRAM_BOT_TOKEN:") {
				botToken = strings.TrimPrefix(config.Webhook, "TELEGRAM_BOT_TOKEN:")
			}
			if botToken != "" && !ValidateTelegramWebhook(botToken) {
				return config, errors.New("Telegram webhook validation failed. Please check the bot token and webhook configuration.")
			}
		}
	}

	return config, nil
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
	if dir, err := os.Getwd(); err == nil {
		return path.Join(dir, "config.yaml")
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
