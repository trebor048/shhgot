package core

import (
	"errors"
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
	BlacklistedStrings           []string          `yaml:"blacklisted_strings"`
	BlacklistedExtensions        []string          `yaml:"blacklisted_extensions"`
	BlacklistedPaths             []string          `yaml:"blacklisted_paths"`
	BlacklistedEntropyExtensions []string          `yaml:"blacklisted_entropy_extensions"`
	Signatures                   []ConfigSignature `yaml:"signatures"`
	LogFormat                    string            `yaml:"logFormat,omitempty"`
	Cleanup                      CleanupConfig     `yaml:"cleanup,omitempty"`
	MatchLogDir                  string            `yaml:"match_log_dir,omitempty"`
	MatchLogEnabled              *bool             `yaml:"match_log_enabled,omitempty"`
}

type CleanupConfig struct {
	MaxDiskUsageMB      uint64 `yaml:"max_disk_usage_mb,omitempty"`
	CleanupThresholdMB  uint64 `yaml:"cleanup_threshold_mb,omitempty"`
	CleanupIntervalSecs int    `yaml:"cleanup_interval_secs,omitempty"`
}

type ConfigSignature struct {
	Name            string   `yaml:"name"`
	Part            string   `yaml:"part"`
	Match           string   `yaml:"match,omitempty"`
	Regex           string   `yaml:"regex,omitempty"`
	Verifier        string   `yaml:"verifier,omitempty"`
	Priority        int      `yaml:"priority,omitempty"`
	Color           string   `yaml:"color,omitempty"`
	ExcludeInModes  []string `yaml:"exclude_in_modes,omitempty"`
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
			return config, err
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
				return config, err
			}
		}
	}

	err = yaml.Unmarshal(data, config)
	if err != nil {
		return config, err
	}

	if len(*options.Local) <= 0 && (len(config.GitHubAccessTokens) < 1 || strings.TrimSpace(strings.Join(config.GitHubAccessTokens, "")) == "") {
		return config, errors.New("You need to provide at least one GitHub Access Token. See https://help.github.com/en/articles/creating-a-personal-access-token-for-the-command-line")
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

func (c *Config) UnmarshalYAML(unmarshal func(interface{}) error) error {
	*c = Config{}
	type plain Config

	err := unmarshal((*plain)(c))

	if err != nil {
		return err
	}

	return nil
}
