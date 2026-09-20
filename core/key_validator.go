package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

type KeyValidator struct {
	log       *Logger
	logFile   *os.File
	keysFound map[string]bool
}

type ValidatedKey struct {
	Key      string
	Provider string
	Valid    bool
	Error    string
}

func NewKeyValidator(log *Logger) *KeyValidator {
	return &KeyValidator{
		log:       log,
		keysFound: make(map[string]bool),
	}
}

// OpenLogFile opens a file for logging keys
func (kv *KeyValidator) OpenLogFile(filename string) error {
	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	kv.logFile = f
	return nil
}

// LogKey logs a single API key to file
func (kv *KeyValidator) LogKey(key, provider string, valid bool) {
	if kv.logFile == nil {
		return
	}

	status := "INVALID"
	if valid {
		status = "VALID"
	}

	line := fmt.Sprintf("%s|%s|%s\n", key, provider, status)
	kv.logFile.WriteString(line)
}

// CloseLogFile closes the log file
func (kv *KeyValidator) CloseLogFile() {
	if kv.logFile != nil {
		kv.logFile.Close()
	}
}

// ValidateKey checks if an API key is valid by making a test request
func (kv *KeyValidator) ValidateKey(key string) *ValidatedKey {
	key = strings.TrimSpace(key)

	// Skip if already processed
	if kv.keysFound[key] {
		return nil
	}
	kv.keysFound[key] = true

	// Detect provider and validate
	if strings.HasPrefix(key, "sk-ant-api03-") {
		return kv.validateAnthropic(key)
	} else if strings.HasPrefix(key, "sk-proj-") {
		return kv.validateOpenAI(key)
	} else if strings.HasPrefix(key, "sk-") && len(key) > 48 && !strings.Contains(key, "_") {
		return kv.validateOpenAI(key)
	} else if strings.HasPrefix(key, "sk_live_") || strings.HasPrefix(key, "sk_test_") {
		return kv.validateStripe(key)
	} else if strings.HasPrefix(key, "pk_live_") || strings.HasPrefix(key, "pk_test_") {
		return kv.validateStripePublic(key)
	} else if strings.HasPrefix(key, "rk_live_") || strings.HasPrefix(key, "rk_test_") {
		return kv.validateStripeRestricted(key)
	} else if strings.HasPrefix(key, "AKIA") && len(key) == 20 {
		return kv.validateAWSKey(key)
	} else if strings.HasPrefix(key, "AIza") {
		return kv.validateGoogleAPI(key)
	} else if strings.HasPrefix(key, "xai-") {
		return kv.validateXAI(key)
	} else if strings.HasPrefix(key, "sk-or-v1-") {
		return kv.validateOpenRouter(key)
	} else if strings.HasPrefix(key, "hf_") {
		return kv.validateHuggingFace(key)
	}

	return &ValidatedKey{
		Key:      key,
		Provider: "UNKNOWN",
		Valid:    false,
		Error:    "Unknown key format",
	}
}

func (kv *KeyValidator) validateAnthropic(key string) *ValidatedKey {
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", "https://api.anthropic.com/v1/models", nil)
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := client.Do(req)
	if err != nil {
		return &ValidatedKey{
			Key:      key,
			Provider: "ANTHROPIC",
			Valid:    false,
			Error:    err.Error(),
		}
	}
	defer resp.Body.Close()

	valid := resp.StatusCode == 200
	return &ValidatedKey{
		Key:      key,
		Provider: "ANTHROPIC",
		Valid:    valid,
		Error:    fmt.Sprintf("HTTP %d", resp.StatusCode),
	}
}

func (kv *KeyValidator) validateOpenAI(key string) *ValidatedKey {
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", "https://api.openai.com/v1/models", nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key))

	resp, err := client.Do(req)
	if err != nil {
		return &ValidatedKey{
			Key:      key,
			Provider: "OPENAI",
			Valid:    false,
			Error:    err.Error(),
		}
	}
	defer resp.Body.Close()

	valid := resp.StatusCode == 200
	return &ValidatedKey{
		Key:      key,
		Provider: "OPENAI",
		Valid:    valid,
		Error:    fmt.Sprintf("HTTP %d", resp.StatusCode),
	}
}

func (kv *KeyValidator) validateAWSKey(key string) *ValidatedKey {
	// AWS keys need secret key too, just validate format
	if FastMatch("aws_key", key) {
		return &ValidatedKey{
			Key:      key,
			Provider: "AWS",
			Valid:    true,
			Error:    "",
		}
	}
	return &ValidatedKey{
		Key:      key,
		Provider: "AWS",
		Valid:    false,
		Error:    "Invalid AWS key format",
	}
}

func (kv *KeyValidator) validateGoogleAPI(key string) *ValidatedKey {
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", fmt.Sprintf("https://www.googleapis.com/customsearch/v1?key=%s&q=test", key), nil)

	resp, err := client.Do(req)
	if err != nil {
		return &ValidatedKey{
			Key:      key,
			Provider: "GOOGLE_API",
			Valid:    false,
			Error:    err.Error(),
		}
	}
	defer resp.Body.Close()

	valid := resp.StatusCode != 403 && resp.StatusCode != 401
	return &ValidatedKey{
		Key:      key,
		Provider: "GOOGLE_API",
		Valid:    valid,
		Error:    fmt.Sprintf("HTTP %d", resp.StatusCode),
	}
}

func (kv *KeyValidator) validateXAI(key string) *ValidatedKey {
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", "https://api.x.ai/v1/models", nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key))

	resp, err := client.Do(req)
	if err != nil {
		return &ValidatedKey{
			Key:      key,
			Provider: "XAI",
			Valid:    false,
			Error:    err.Error(),
		}
	}
	defer resp.Body.Close()

	valid := resp.StatusCode == 200
	return &ValidatedKey{
		Key:      key,
		Provider: "XAI",
		Valid:    valid,
		Error:    fmt.Sprintf("HTTP %d", resp.StatusCode),
	}
}

func (kv *KeyValidator) validateOpenRouter(key string) *ValidatedKey {
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", "https://openrouter.ai/api/v1/models", nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key))

	resp, err := client.Do(req)
	if err != nil {
		return &ValidatedKey{
			Key:      key,
			Provider: "OPENROUTER",
			Valid:    false,
			Error:    err.Error(),
		}
	}
	defer resp.Body.Close()

	valid := resp.StatusCode == 200
	return &ValidatedKey{
		Key:      key,
		Provider: "OPENROUTER",
		Valid:    valid,
		Error:    fmt.Sprintf("HTTP %d", resp.StatusCode),
	}
}

func (kv *KeyValidator) validateHuggingFace(key string) *ValidatedKey {
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", "https://huggingface.co/api/whoami", nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", key))

	resp, err := client.Do(req)
	if err != nil {
		return &ValidatedKey{
			Key:      key,
			Provider: "HUGGINGFACE",
			Valid:    false,
			Error:    err.Error(),
		}
	}
	defer resp.Body.Close()

	valid := resp.StatusCode == 200
	return &ValidatedKey{
		Key:      key,
		Provider: "HUGGINGFACE",
		Valid:    valid,
		Error:    fmt.Sprintf("HTTP %d", resp.StatusCode),
	}
}

func (kv *KeyValidator) validateStripe(key string) *ValidatedKey {
	client := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", "https://api.stripe.com/v1/account", nil)
	req.SetBasicAuth(key, "")

	resp, err := client.Do(req)
	if err != nil {
		return &ValidatedKey{
			Key:      key,
			Provider: "STRIPE_SECRET",
			Valid:    false,
			Error:    err.Error(),
		}
	}
	defer resp.Body.Close()

	valid := resp.StatusCode == 200

	// Try to extract account info
	if valid {
		var result map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&result)
		if id, ok := result["id"].(string); ok {
			kv.log.Debug("Stripe account: %s", id)
		}
	}

	return &ValidatedKey{
		Key:      key,
		Provider: "STRIPE_SECRET",
		Valid:    valid,
		Error:    fmt.Sprintf("HTTP %d", resp.StatusCode),
	}
}

func (kv *KeyValidator) validateStripePublic(key string) *ValidatedKey {
	// Public keys can't be directly validated, just check format
	if FastMatch("stripe_public", key) {
		return &ValidatedKey{
			Key:      key,
			Provider: "STRIPE_PUBLIC",
			Valid:    true,
			Error:    "",
		}
	}
	return &ValidatedKey{
		Key:      key,
		Provider: "STRIPE_PUBLIC",
		Valid:    false,
		Error:    "Invalid format",
	}
}

func (kv *KeyValidator) validateStripeRestricted(key string) *ValidatedKey {
	// Restricted keys can't be directly validated, just check format
	if FastMatch("stripe_restricted", key) {
		return &ValidatedKey{
			Key:      key,
			Provider: "STRIPE_RESTRICTED",
			Valid:    true,
			Error:    "",
		}
	}
	return &ValidatedKey{
		Key:      key,
		Provider: "STRIPE_RESTRICTED",
		Valid:    false,
		Error:    "Invalid format",
	}
}

// ScanDirectoryForKeys scans a directory for API keys and validates them
func (kv *KeyValidator) ScanDirectoryForKeys(dir string, session *Session) []ValidatedKey {
	var validatedKeys []ValidatedKey
	files := GetMatchingFiles(dir)

	keyPatterns := map[string]*regexp.Regexp{
		"OPENAI":            regexp.MustCompile(`sk-[a-zA-Z0-9]{48}`),
		"ANTHROPIC":         regexp.MustCompile(`sk-ant-api03-[a-zA-Z0-9_-]{95}`),
		"AWS":               regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		"GOOGLE_API":        regexp.MustCompile(`AIza[0-9A-Za-z_-]{35,40}`),
		"XAI":               regexp.MustCompile(`xai-[A-Za-z0-9]{80}`),
		"OPENROUTER":        regexp.MustCompile(`sk-or-v1-[a-z0-9]{64}`),
		"HUGGINGFACE":       regexp.MustCompile(`hf_[A-Za-z0-9]{34}`),
		"STRIPE_SECRET":     regexp.MustCompile(`sk_(live|test)_[a-zA-Z0-9]{24,}`),
		"STRIPE_PUBLIC":     regexp.MustCompile(`pk_(live|test)_[a-zA-Z0-9]{24,}`),
		"STRIPE_RESTRICTED": regexp.MustCompile(`rk_(live|test)_[a-zA-Z0-9]{24,}`),
		"GITHUB":            regexp.MustCompile(`(ghp_[A-Za-z0-9]{36}|github_pat_[0-9a-zA-Z_]{22}[0-9a-zA-Z_]{64})`),
	}

	foundKeys := make(map[string]bool)

	for _, file := range files {
		// Contents are loaded lazily, so the field is nil until GetContents runs. This
		// loop read the raw field, which is why --scan-keys always reported "0 keys
		// found" and wrote an empty log. One load per file also lets every pattern
		// reuse the same buffer instead of re-reading the file.
		contents := string(file.GetContents())
		for provider, pattern := range keyPatterns {
			matches := pattern.FindAllString(contents, -1)
			for _, match := range matches {
				if !foundKeys[match] {
					foundKeys[match] = true

					// If it's a GitHub token and we have a session, try to add it
					if provider == "GITHUB" && session != nil {
						if session.AddGitHubToken(match) {
							kv.log.Info("Added new GitHub token to scanning pool: %s[..]", match[:10])
						}
					}

					validated := kv.ValidateKey(match)
					if validated != nil {
						validatedKeys = append(validatedKeys, *validated)
						kv.LogKey(match, validated.Provider, validated.Valid)
						kv.log.Info("Found %s key: %s (Valid: %v)", provider, match[:20]+"...", validated.Valid)
					}
				}
			}
		}
	}

	return validatedKeys
}
