package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ValidationResult contains detailed validation information
type ValidationResult struct {
	Token           string                 `json:"token"`
	Provider        string                 `json:"provider"`
	Valid           bool                   `json:"valid"`
	Confidence      int                    `json:"confidence"` // 0-100
	Error           string                 `json:"error"`
	Scope           []string               `json:"scope,omitempty"`
	Permissions     []string               `json:"permissions,omitempty"`
	ExpiresAt       *time.Time             `json:"expires_at,omitempty"`
	CreatedAt       *time.Time             `json:"created_at,omitempty"`
	LastUsed        *time.Time             `json:"last_used,omitempty"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
	ValidationTime  time.Duration          `json:"validation_time"`
	RateLimitStatus string                 `json:"rate_limit_status,omitempty"`
}

// Validator is the interface for all token validators
type Validator interface {
	Validate(token string) *ValidationResult
	CanValidate(token string) bool
	GetProvider() string
}

// CachedValidator wraps a validator with caching
type CachedValidator struct {
	validator  Validator
	cache      map[string]*ValidationResult
	cacheTTL   time.Duration
	cacheTimes map[string]time.Time
	mu         sync.RWMutex
}

// NewCachedValidator creates a new cached validator
func NewCachedValidator(validator Validator, ttl time.Duration) *CachedValidator {
	return &CachedValidator{
		validator:  validator,
		cache:      make(map[string]*ValidationResult),
		cacheTTL:   ttl,
		cacheTimes: make(map[string]time.Time),
	}
}

// Validate validates a token with caching
func (cv *CachedValidator) Validate(token string) *ValidationResult {
	cv.mu.RLock()
	if result, exists := cv.cache[token]; exists {
		if time.Since(cv.cacheTimes[token]) < cv.cacheTTL {
			cv.mu.RUnlock()
			return result
		}
	}
	cv.mu.RUnlock()

	result := cv.validator.Validate(token)

	cv.mu.Lock()
	cv.cache[token] = result
	cv.cacheTimes[token] = time.Now()
	cv.mu.Unlock()

	return result
}

// CanValidate checks if the validator can validate this token
func (cv *CachedValidator) CanValidate(token string) bool {
	return cv.validator.CanValidate(token)
}

// GetProvider returns the provider name
func (cv *CachedValidator) GetProvider() string {
	return cv.validator.GetProvider()
}

// ─────────────────────────────────────────────────────────────
// OpenAI Validator
// ─────────────────────────────────────────────────────────────

type OpenAIValidator struct {
	client *http.Client
}

func NewOpenAIValidator() *OpenAIValidator {
	return &OpenAIValidator{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (v *OpenAIValidator) CanValidate(token string) bool {
	return strings.HasPrefix(token, "sk-") && len(token) > 40
}

func (v *OpenAIValidator) GetProvider() string {
	return "OPENAI"
}

func (v *OpenAIValidator) Validate(token string) *ValidationResult {
	start := time.Now()
	result := &ValidationResult{
		Token:    token,
		Provider: v.GetProvider(),
		Metadata: make(map[string]interface{}),
	}

	req, _ := http.NewRequest("GET", "https://api.openai.com/v1/models", nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

	resp, err := v.client.Do(req)
	if err != nil {
		result.Valid = false
		result.Error = err.Error()
		result.Confidence = 0
		result.ValidationTime = time.Since(start)
		return result
	}
	defer resp.Body.Close()

	result.Valid = resp.StatusCode == 200
	result.Confidence = 85
	result.ValidationTime = time.Since(start)

	if result.Valid {
		result.Scope = []string{"models.list"}
		result.Permissions = []string{"read"}
	} else {
		result.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		result.Confidence = 50
	}

	return result
}

// ─────────────────────────────────────────────────────────────
// Anthropic Claude Validator
// ─────────────────────────────────────────────────────────────

type AnthropicValidator struct {
	client *http.Client
}

func NewAnthropicValidator() *AnthropicValidator {
	return &AnthropicValidator{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (v *AnthropicValidator) CanValidate(token string) bool {
	return strings.HasPrefix(token, "sk-ant-api03-")
}

func (v *AnthropicValidator) GetProvider() string {
	return "ANTHROPIC"
}

func (v *AnthropicValidator) Validate(token string) *ValidationResult {
	start := time.Now()
	result := &ValidationResult{
		Token:    token,
		Provider: v.GetProvider(),
		Metadata: make(map[string]interface{}),
	}

	req, _ := http.NewRequest("GET", "https://api.anthropic.com/v1/models", nil)
	req.Header.Set("x-api-key", token)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := v.client.Do(req)
	if err != nil {
		result.Valid = false
		result.Error = err.Error()
		result.Confidence = 0
		result.ValidationTime = time.Since(start)
		return result
	}
	defer resp.Body.Close()

	result.Valid = resp.StatusCode == 200
	result.Confidence = 85
	result.ValidationTime = time.Since(start)

	if result.Valid {
		result.Scope = []string{"models.list"}
		result.Permissions = []string{"read"}
	} else {
		result.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		result.Confidence = 50
	}

	return result
}

// ─────────────────────────────────────────────────────────────
// AWS Validator
// ─────────────────────────────────────────────────────────────

type AWSValidator struct {
	client *http.Client
}

func NewAWSValidator() *AWSValidator {
	return &AWSValidator{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (v *AWSValidator) CanValidate(token string) bool {
	return strings.HasPrefix(token, "AKIA") && len(token) == 20
}

func (v *AWSValidator) GetProvider() string {
	return "AWS"
}

func (v *AWSValidator) Validate(token string) *ValidationResult {
	start := time.Now()
	result := &ValidationResult{
		Token:    token,
		Provider: v.GetProvider(),
		Metadata: make(map[string]interface{}),
	}

	// AWS keys need secret key too, just validate format
	if FastMatch("aws_key", token) {
		result.Valid = true
		result.Confidence = 70 // Format valid but can't verify without secret key
		result.Error = "Format valid - requires secret key for full validation"
		result.Scope = []string{"unknown"}
		result.Permissions = []string{"unknown"}
	} else {
		result.Valid = false
		result.Confidence = 0
		result.Error = "Invalid AWS key format"
	}

	result.ValidationTime = time.Since(start)
	return result
}

// ─────────────────────────────────────────────────────────────
// Stripe Validator
// ─────────────────────────────────────────────────────────────

type StripeValidator struct {
	client *http.Client
}

func NewStripeValidator() *StripeValidator {
	return &StripeValidator{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (v *StripeValidator) CanValidate(token string) bool {
	return (strings.HasPrefix(token, "sk_live_") || strings.HasPrefix(token, "sk_test_")) && len(token) > 20
}

func (v *StripeValidator) GetProvider() string {
	return "STRIPE"
}

func (v *StripeValidator) Validate(token string) *ValidationResult {
	start := time.Now()
	result := &ValidationResult{
		Token:    token,
		Provider: v.GetProvider(),
		Metadata: make(map[string]interface{}),
	}

	req, _ := http.NewRequest("GET", "https://api.stripe.com/v1/account", nil)
	req.SetBasicAuth(token, "")

	resp, err := v.client.Do(req)
	if err != nil {
		result.Valid = false
		result.Error = err.Error()
		result.Confidence = 0
		result.ValidationTime = time.Since(start)
		return result
	}
	defer resp.Body.Close()

	result.Valid = resp.StatusCode == 200
	result.Confidence = 90
	result.ValidationTime = time.Since(start)

	if result.Valid {
		var accountData map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&accountData)
		result.Scope = []string{"account.read"}
		result.Permissions = []string{"read"}
		if id, ok := accountData["id"].(string); ok {
			result.Metadata["account_id"] = id
		}
	} else {
		result.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		result.Confidence = 50
	}

	return result
}

// ─────────────────────────────────────────────────────────────
// GitHub Validator
// ─────────────────────────────────────────────────────────────

type GitHubValidator struct {
	client *http.Client
}

func NewGitHubValidator() *GitHubValidator {
	return &GitHubValidator{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (v *GitHubValidator) CanValidate(token string) bool {
	return strings.HasPrefix(token, "ghp_") || strings.HasPrefix(token, "github_pat_") || strings.HasPrefix(token, "gho_")
}

func (v *GitHubValidator) GetProvider() string {
	return "GITHUB"
}

func (v *GitHubValidator) Validate(token string) *ValidationResult {
	start := time.Now()
	result := &ValidationResult{
		Token:    token,
		Provider: v.GetProvider(),
		Metadata: make(map[string]interface{}),
	}

	req, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := v.client.Do(req)
	if err != nil {
		result.Valid = false
		result.Error = err.Error()
		result.Confidence = 0
		result.ValidationTime = time.Since(start)
		return result
	}
	defer resp.Body.Close()

	result.Valid = resp.StatusCode == 200
	result.Confidence = 95
	result.ValidationTime = time.Since(start)

	if result.Valid {
		var userData map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&userData)
		result.Scope = []string{"user", "repo"}
		result.Permissions = []string{"read", "write"}
		if login, ok := userData["login"].(string); ok {
			result.Metadata["username"] = login
		}
	} else {
		result.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		result.Confidence = 50
	}

	return result
}

// ─────────────────────────────────────────────────────────────
// Discord Validator
// ─────────────────────────────────────────────────────────────

type DiscordValidator struct {
	client *http.Client
}

func NewDiscordValidator() *DiscordValidator {
	return &DiscordValidator{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (v *DiscordValidator) CanValidate(token string) bool {
	return (strings.HasPrefix(token, "M") || strings.HasPrefix(token, "N")) && len(token) == 59
}

func (v *DiscordValidator) GetProvider() string {
	return "DISCORD"
}

func (v *DiscordValidator) Validate(token string) *ValidationResult {
	start := time.Now()
	result := &ValidationResult{
		Token:    token,
		Provider: v.GetProvider(),
		Metadata: make(map[string]interface{}),
	}

	req, _ := http.NewRequest("GET", "https://discord.com/api/v10/users/@me", nil)
	req.Header.Set("Authorization", "Bot "+token)

	resp, err := v.client.Do(req)
	if err != nil {
		result.Valid = false
		result.Error = err.Error()
		result.Confidence = 0
		result.ValidationTime = time.Since(start)
		return result
	}
	defer resp.Body.Close()

	result.Valid = resp.StatusCode == 200
	result.Confidence = 95
	result.ValidationTime = time.Since(start)

	if result.Valid {
		var userData map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&userData)
		result.Scope = []string{"bot"}
		result.Permissions = []string{"read", "write"}
		if username, ok := userData["username"].(string); ok {
			result.Metadata["username"] = username
		}
	} else {
		result.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		result.Confidence = 50
	}

	return result
}

// ─────────────────────────────────────────────────────────────
// Validator Registry
// ─────────────────────────────────────────────────────────────

type ValidatorRegistry struct {
	mu sync.RWMutex
	// CHANGE THIS: Use the interface 'Validator', not the concrete struct 'TokenValidator'
	validators []Validator
}

func NewValidatorRegistry() *ValidatorRegistry {
	registry := &ValidatorRegistry{
		validators: make([]Validator, 0),
	}

	// Register default validators
	registry.Register(NewOpenAIValidator())
	registry.Register(NewAnthropicValidator())
	registry.Register(NewAWSValidator())
	registry.Register(NewStripeValidator())
	registry.Register(NewGitHubValidator())
	registry.Register(NewDiscordValidator())

	return registry
}

// Register adds a validator to the registry
func (vr *ValidatorRegistry) Register(validator Validator) {
	vr.mu.Lock()
	defer vr.mu.Unlock()
	vr.validators = append(vr.validators, validator)
}

// Validate finds the appropriate validator and validates the token
func (vr *ValidatorRegistry) Validate(token string) *ValidationResult {
	vr.mu.RLock()
	defer vr.mu.RUnlock()

	for _, validator := range vr.validators {
		if validator.CanValidate(token) {
			return validator.Validate(token)
		}
	}

	return &ValidationResult{
		Token:      token,
		Provider:   "UNKNOWN",
		Valid:      false,
		Confidence: 0,
		Error:      "No validator found for this token format",
	}
}

// ValidateMultiple validates multiple tokens concurrently
func (vr *ValidatorRegistry) ValidateMultiple(tokens []string) []*ValidationResult {
	results := make([]*ValidationResult, len(tokens))
	var wg sync.WaitGroup

	for i, token := range tokens {
		wg.Add(1)
		go func(idx int, t string) {
			defer wg.Done()
			results[idx] = vr.Validate(t)
		}(i, token)
	}

	wg.Wait()
	return results
}

// GetValidatorForProvider returns a validator for a specific provider
func (vr *ValidatorRegistry) GetValidatorForProvider(provider string) Validator {
	vr.mu.RLock()
	defer vr.mu.RUnlock()

	for _, validator := range vr.validators {
		if validator.GetProvider() == provider {
			return validator
		}
	}

	return nil
}
