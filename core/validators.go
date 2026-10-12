package core

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

var (
	// Global webhook queue for rate limiting and retry
	globalWebhookQueue *WebhookQueue
	webhookQueueOnce   sync.Once

	// webhookQueueConfig is applied by ConfigureWebhookQueue and read when the
	// queue is first used. Defaults are Discord-safe: 500 ms spacing keeps one
	// webhook at 4 sends per 2 s, under Discord's 5-per-2-s bucket.
	webhookQueueConfig = WebhookQueueCfg{
		QueueSize:       defaultWebhookQueueSize,
		RateLimitMS:     defaultWebhookRateLimitMS,
		DedupeWindowSec: defaultWebhookDedupeWindowSec,
		BatchSize:       defaultWebhookBatchSize,
	}
	webhookQueueConfigMutex sync.Mutex
)

const (
	defaultWebhookQueueSize       = 1000
	defaultWebhookRateLimitMS     = 500
	defaultWebhookDedupeWindowSec = 300
	defaultWebhookBatchSize       = 1
)

// ConfigureWebhookQueue applies the webhook_queue section of config.yaml. Call
// it after ParseConfig and before the first alert is delivered; a zero or
// negative value keeps the built-in default for that field.
func ConfigureWebhookQueue(cfg WebhookQueueCfg) {
	webhookQueueConfigMutex.Lock()
	defer webhookQueueConfigMutex.Unlock()

	if cfg.QueueSize > 0 {
		webhookQueueConfig.QueueSize = cfg.QueueSize
	}
	if cfg.RateLimitMS > 0 {
		webhookQueueConfig.RateLimitMS = cfg.RateLimitMS
	}
	if cfg.DedupeWindowSec > 0 {
		webhookQueueConfig.DedupeWindowSec = cfg.DedupeWindowSec
	}
	if cfg.BatchSize > 0 {
		webhookQueueConfig.BatchSize = cfg.BatchSize
	}
}

// GetWebhookQueue returns the singleton webhook queue instance
func GetWebhookQueue() *WebhookQueue {
	webhookQueueOnce.Do(func() {
		webhookQueueConfigMutex.Lock()
		cfg := webhookQueueConfig
		webhookQueueConfigMutex.Unlock()

		globalWebhookQueue = NewWebhookQueue(cfg.QueueSize, cfg.RateLimitMS, cfg.DedupeWindowSec, cfg.BatchSize)
		globalWebhookQueue.Start()
	})
	return globalWebhookQueue
}

// DrainWebhookQueue closes the singleton queue and waits up to timeout for
// the buffered alerts to go out, so a one-shot run neither loses findings nor
// skips their persist-on-confirm. Nil-safe (no queue was ever used) and
// reports whether the queue emptied in time.
func DrainWebhookQueue(timeout time.Duration) bool {
	if globalWebhookQueue == nil {
		return true
	}
	return globalWebhookQueue.Drain(timeout)
}

// validateDiscordTokenFormat checks a Discord bot token's shape without any
// network call: 59 characters, [MN] prefix, and three dot-separated segments
// where the second is 6 characters and the third is 27.
func validateDiscordTokenFormat(token string) bool {
	// Discord bot tokens should be 59 characters long and follow the pattern: [MN][A-Za-z\d]{23}\.[\w-]{6}\.[\w-]{27}
	if len(token) != 59 {
		return false
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}

	// Validate user ID part (first part after prefix)
	if len(parts[0]) < 2 || (parts[0][0] != 'M' && parts[0][0] != 'N') {
		return false
	}

	// Try to decode the base64 parts to ensure they're valid. Discord encodes
	// these segments as unpadded base64url, so the padded StdEncoding and
	// URLEncoding both reject them (a 6- or 27-character segment is not a
	// multiple of 4). The raw encodings are what actually decode them; without
	// this the check always failed and no Discord token was ever verified.
	for i, part := range parts[1:] {
		if _, err := base64.RawURLEncoding.DecodeString(part); err != nil {
			// Tolerate the standard alphabet too, in case of a non-Discord token.
			if _, err := base64.RawStdEncoding.DecodeString(part); err != nil {
				return false
			}
		}
		if i == 0 && len(part) != 6 { // Second part should be 6 chars
			return false
		}
		if i == 1 && len(part) != 27 { // Third part should be 27 chars
			return false
		}
	}
	return true
}

// ValidateDiscordToken validates a Discord bot token by making an API request
func ValidateDiscordToken(token string) bool {
	if !validateDiscordTokenFormat(token) {
		return false
	}

	// API validation (optional, can be disabled for performance)
	req, err := http.NewRequest("GET", discordAPIBase+"/users/@me", nil)
	if err != nil {
		return false
	}

	req.Header.Set("Authorization", "Bot "+token)
	resp, err := validatorHTTPDoer().Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == 200
}

// ValidateTelegramToken validates a Telegram bot token by making an API request
func ValidateTelegramToken(token string) bool {
	if token == "" {
		return false
	}

	// Telegram bot tokens should follow the pattern: [0-9]{8,12}:[A-Za-z0-9_-]{35}
	parts := strings.Split(token, ":")
	if len(parts) != 2 {
		return false
	}

	// Validate bot ID part (numbers)
	botID := parts[0]
	if len(botID) < 8 || len(botID) > 12 {
		return false
	}
	for _, char := range botID {
		if char < '0' || char > '9' {
			return false
		}
	}

	// Validate token part
	botToken := parts[1]
	if len(botToken) != 35 {
		return false
	}
	for _, char := range botToken {
		if !((char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_' || char == '-') {
			return false
		}
	}

	// API validation (optional, can be disabled for performance)
	url := telegramAPIBase + "/bot" + token + "/getMe"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false
	}
	resp, err := validatorHTTPDoer().Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == 200
}

// ValidateDiscordWebhook validates a Discord webhook URL
func ValidateDiscordWebhook(webhookURL string) bool {
	if webhookURL == "" {
		Say("[WARN] Discord webhook URL is empty\n")
		return false
	}

	// Discord webhook URL pattern: https://discord.com/api/webhooks/ID/TOKEN
	if !strings.HasPrefix(webhookURL, "https://discord.com/api/webhooks/") {
		Say("[ERROR] Invalid Discord webhook URL format. Expected: https://discord.com/api/webhooks/ID/TOKEN\n")
		return false
	}

	// Extract webhook ID and token
	parts := strings.Split(strings.TrimPrefix(webhookURL, "https://discord.com/api/webhooks/"), "/")
	if len(parts) < 2 {
		Say("[ERROR] Discord webhook URL missing ID or token\n")
		return false
	}

	// Validate webhook ID (should be numeric)
	webhookID := parts[0]
	for _, char := range webhookID {
		if char < '0' || char > '9' {
			Say("[ERROR] Discord webhook ID must be numeric\n")
			return false
		}
	}

	// Test the webhook by sending a validation embed
	testPayload := map[string]interface{}{
		"embeds": []map[string]interface{}{
			{
				"title":       "shhgit - Webhook Validation",
				"description": "Testing webhook connectivity and permissions",
				"color":       65280, // Green
			},
		},
	}

	jsonData, err := json.Marshal(testPayload)
	if err != nil {
		Say("[ERROR] Failed to marshal validation payload: %v\n", err)
		return false
	}

	// The shared client carries a bounded timeout; http.DefaultClient has none,
	// so a hung Discord endpoint used to stall startup (this runs from
	// ParseConfig).
	req, err := http.NewRequest("POST", webhookURL, strings.NewReader(string(jsonData)))
	if err != nil {
		Say("[ERROR] Discord webhook request failed: %v\n", err)
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := validatorHTTPDoer().Do(req)
	if err != nil {
		Say("[ERROR] Discord webhook POST failed: %v\n", err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode == 204 || resp.StatusCode == 200 {
		Say("[✓] Discord webhook is valid and has proper permissions\n")
		return true
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	Say("[ERROR] Discord webhook validation failed:\n")
	Say("  HTTP Status: %d\n", resp.StatusCode)
	Say("  Response: %s\n", string(body))

	switch resp.StatusCode {
	case 401, 403:
		Say("  → Permission denied. Check webhook has 'Send Messages' permission in Discord channel.\n")
		return false
	case 404:
		Say("  → Webhook not found. Verify the webhook URL is correct and hasn't been deleted.\n")
		return false
	case 429:
		// Rate limited - Discord is temporarily blocking us
		// The webhook is valid, just can't test it right now
		// The webhook queue will handle retries with backoff
		Say("  → Rate limited (429). Assuming webhook is valid.\n")
		Say("  → The webhook queue system will retry with exponential backoff.\n")
		return true // Consider valid since it's a transient rate limit, not a permission error
	case 400:
		Say("  → Bad request. The webhook payload may be invalid.\n")
		return false
	default:
		Say("  → Unexpected status code. Check Discord API status.\n")
		return false
	}
}

// ValidateTelegramWebhook validates a Telegram webhook setup
func ValidateTelegramWebhook(botToken string) bool {
	if botToken == "" {
		return false
	}

	// Use the token validation function first
	if !ValidateTelegramToken(botToken) {
		return false
	}

	// Check webhook info
	url := telegramAPIBase + "/bot" + botToken + "/getWebhookInfo"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false
	}
	resp, err := validatorHTTPDoer().Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return false
	}

	// Parse response to check if webhook is properly configured
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false
	}

	// Assert with the two-value form: a response whose "ok" is not a JSON
	// boolean (a string, a number) must report "not configured" rather than
	// panic the caller.
	if ok, isBool := result["ok"].(bool); !isBool || !ok {
		return false
	}

	return true
}

// jsonEscapeForTemplate returns s encoded as a JSON string with the surrounding
// quotes removed, so it can be substituted into a JSON payload template such as
// {"text": "%s"}. Scanned content (a matched secret, a file name, a repository
// URL) can carry a quote, backslash or newline; embedding it raw produced
// invalid JSON and let repo content inject fields into the operator's endpoint.
func jsonEscapeForTemplate(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return string(b[1 : len(b)-1])
}

// buildWebhookPayload renders a webhook payload template with message, escaping
// message as a JSON string first. An empty template falls back to the Slack
// style {"text": "%s"} default. The escaped message is passed to Sprintf as an
// argument, so a "%" inside it is never reinterpreted as a format verb.
func buildWebhookPayload(template, message string) string {
	if template == "" {
		template = `{"text": "%s"}`
	}
	return fmt.Sprintf(template, jsonEscapeForTemplate(message))
}

// SendValidationWebhook sends a webhook notification about token validation results
func SendValidationWebhook(webhookURL, webhookPayload, tokenType, token, repository, filename string, isValid bool) {
	if webhookURL == "" {
		return
	}

	var status string
	var emoji string
	if isValid {
		status = "VALID"
		emoji = "✅"
	} else {
		status = "INVALID"
		emoji = "❌"
	}

	// Create validation alert message
	message := fmt.Sprintf("%s **Token Validation Alert**\n\n**Type:** %s\n**Status:** %s\n**Repository:** %s\n**File:** %s\n**Token:** %s...",
		emoji, tokenType, status, repository, filename, maskToken(token))

	// Determine payload format based on webhook service
	var payload string
	if strings.Contains(webhookURL, "discord.com/api/webhooks") {
		// Discord format
		b, err := json.Marshal(map[string]string{"content": message})
		if err != nil {
			return
		}
		payload = string(b)
	} else {
		// Default format (Slack, MatterMost, etc.)
		payload = buildWebhookPayload(webhookPayload, message)
	}

	// Send webhook notification through the bounded client so a slow endpoint
	// cannot hold the goroutine indefinitely.
	req, err := http.NewRequest("POST", webhookURL, strings.NewReader(payload))
	if err != nil {
		// Silently fail to avoid infinite loops
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := SharedHTTPClient().Do(req)
	if err != nil {
		// Silently fail to avoid infinite loops
		return
	}
	defer resp.Body.Close()
	// Drain so the connection can be reused; the status is intentionally not
	// treated as fatal (this notification is fire-and-forget), but leaving the
	// body unread disabled keep-alive for the shared client.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
}

// maskToken masks a token for logging purposes
func maskToken(token string) string {
	if len(token) <= 8 {
		return strings.Repeat("*", len(token))
	}
	return token[:4] + strings.Repeat("*", len(token)-8) + token[len(token)-4:]
}

// ValidateSSHKey attempts to validate an SSH private key by trying to establish SSH connections
// to common hosts and testing if the key can authenticate
func ValidateSSHKey(privateKey string) bool {
	if privateKey == "" {
		return false
	}

	// Clean the private key - remove extra whitespace and ensure proper format
	privateKey = strings.TrimSpace(privateKey)

	// Parse the private key to ensure it's valid format
	signer, err := ssh.ParsePrivateKey([]byte(privateKey))
	if err != nil {
		// Try to parse with different key formats
		if strings.Contains(privateKey, "-----BEGIN") {
			// It's a PEM format key, but parsing failed
			return false
		}

		// Try to handle raw key data (like from base64 encoded content)
		if len(privateKey) > 100 {
			// Try to decode as base64 first
			if decoded, err := base64.StdEncoding.DecodeString(privateKey); err == nil {
				if signer, err := ssh.ParsePrivateKey(decoded); err == nil {
					return testSSHKey(signer)
				}
			}
		}
		return false
	}

	return testSSHKey(signer)
}

// testSSHKey attempts to test the SSH key against common targets
func testSSHKey(signer ssh.Signer) bool {
	// Common SSH targets to test against
	// These are public services that allow SSH connections for testing
	targets := []SSHTestTarget{
		{Host: "github.com", Port: 22, Username: "git"},
		{Host: "gitlab.com", Port: 22, Username: "git"},
		{Host: "bitbucket.org", Port: 22, Username: "git"},
		// Add some common internal targets if needed
		// {Host: "localhost", Port: 22, Username: "root"},
	}

	// Test against each target with a short timeout
	for _, target := range targets {
		if testSSHConnection(signer, target) {
			return true // Key works for at least one target
		}
	}

	return false
}

// SSHTestTarget represents a target for SSH testing
type SSHTestTarget struct {
	Host     string
	Port     int
	Username string
}

// testSSHConnection attempts to establish an SSH connection using the provided key
func testSSHConnection(signer ssh.Signer, target SSHTestTarget) bool {
	config := &ssh.ClientConfig{
		User: target.Username,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // Insecure but necessary for testing
		Timeout:         5 * time.Second,
	}

	// Attempt to connect
	conn, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", target.Host, target.Port), config)
	if err != nil {
		// Connection failed, but this doesn't necessarily mean the key is invalid
		// It could be network issues, firewall, etc.
		return false
	}
	defer conn.Close()

	// If we can establish a session, the key is likely valid
	session, err := conn.NewSession()
	if err != nil {
		// Session creation failed, but connection succeeded
		// This could mean the key is valid but the user doesn't have access
		return false
	}
	defer session.Close()

	// Try to execute a simple command
	err = session.Run("echo 'ssh_key_test'")
	if err != nil {
		// Command failed, but we got a session, which means authentication worked
		// This is actually a good sign - the key is valid
		return true
	}

	// Everything worked perfectly
	return true
}

// ValidateCryptoKey derives the address for a recognised private key and checks
// its on-chain balance. Returns (isFundedOrUnverified, label).
//
// A key whose format is recognised is no longer accepted unconditionally: if a
// node answers and the balance is zero, the result is false and the match is
// discarded. If no node can be reached the key is kept and labelled unverified,
// so a network outage never hides a real leak.
func ValidateCryptoKey(privateKey string) (bool, string) {
	if privateKey == "" {
		return false, ""
	}

	privateKey = strings.TrimSpace(privateKey)

	// EVM private key (64 hex chars, optionally with 0x prefix).
	ethKey := strings.TrimPrefix(privateKey, "0x")
	if isEVMKeyFormat(ethKey) {
		return checkEthereumBalance(ethKey)
	}

	// Bitcoin WIF (51-52 base58 chars, K/L/5 prefix).
	if isWIFKeyFormat(privateKey) {
		return checkBitcoinBalance(privateKey)
	}

	// Base64 encoded 32-byte key (Solana and others export this way).
	if decoded, err := base64.StdEncoding.DecodeString(privateKey); err == nil && len(decoded) == 32 {
		if isEVMKeyFormat(hex.EncodeToString(decoded)) {
			return checkEthereumBalance(hex.EncodeToString(decoded))
		}
	}

	// Unrecognised shape: keep the finding rather than silently dropping it.
	return true, "Crypto (unverified)"
}

// isEVMKeyFormat reports whether s is a 64-character hex string.
func isEVMKeyFormat(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// isWIFKeyFormat reports whether s has the shape of a mainnet Bitcoin WIF key:
// 51-52 characters beginning with K, L (compressed) or 5 (uncompressed).
func isWIFKeyFormat(s string) bool {
	if len(s) < 51 || len(s) > 52 {
		return false
	}
	switch s[0] {
	case 'K', 'L', '5':
		return true
	default:
		return false
	}
}

// SendMatchWebhook sends a match alert to a configured webhook. It builds a
// rich Discord embed for Discord endpoints and falls back to the generic
// webhook_payload template (Slack, Mattermost, custom) otherwise. Unlike the
// original high-priority-only helper it fires for every match.
// severity is the finding's dynamic severity; when non-empty it overrides the
// signature colour as the Discord embed accent, so the alert colour reflects the
// verified impact rather than the rule's static priority. An empty severity (a
// record written before Slice 6, or a verifier that did not run) keeps color.
//
// onDelivery is forwarded to the webhook queue and fires once with the final
// delivery outcome, so the caller can record a cross-run dedupe fingerprint
// only when the alert actually went out. It may be nil.
func SendMatchWebhook(webhookURL, webhookPayload, url, signature, file string, matches []string, color string, priority int, fileContent string, severity string, onDelivery func(ok bool)) {
	if webhookURL == "" {
		notifyDelivery(onDelivery, true)
		return
	}

	if strings.Contains(webhookURL, "discord.com/api/webhooks") {
		sendDiscordMatchWebhook(webhookURL, url, signature, file, matches, color, priority, fileContent, severity, onDelivery)
		return
	}

	// Generic webhook (Slack, Mattermost, custom endpoints) using the payload
	// template from config (defaults to a Slack-style {"text": "..."}).
	matchesStr := strings.Join(matches, ", ")
	if len(matchesStr) > 1000 {
		matchesStr = truncateForWebhook(matchesStr, 997)
	}

	// For filename-based matches, show the filename if no content matches
	if matchesStr == "" {
		matchesStr = fmt.Sprintf("Filename: %s", file)
	}

	// Generate GitHub link - only add line anchor if we have a valid line number
	var githubLink string
	if len(matches) > 0 && fileContent != "" {
		lineNum := findLineNumber(fileContent, matches[0])
		if lineNum > 0 {
			githubLink = generateGitHubBlobLink(url, file, lineNum)
		} else {
			// No valid line number, just link to file without anchor
			githubLink = generateGitHubBlobLinkNoAnchor(url, file)
		}
	} else {
		// No matches or content, link to file without anchor
		githubLink = generateGitHubBlobLinkNoAnchor(url, file)
	}

	// Check if we should ping for this match (AI keys, DB connection strings)
	shouldPing := shouldPingForMatch(signature)
	userMention := ""
	if shouldPing && session != nil && session.Config != nil && session.Config.WebhookDiscordUserID != "" {
		userMention = "<@" + session.Config.WebhookDiscordUserID + ">\n"
	}

	// Enhanced message format with clear signature match display
	message := fmt.Sprintf("%s**%s** | Priority: %d\n**File:** [%s](%s)\n**Repository:** %s\n\n```\n%s\n```",
		userMention, signature, priority, file, githubLink, url, matchesStr)

	// For .env files, embed FULL file content (match all .env variants)
	isEnvFile := fileContent != "" && (strings.HasSuffix(strings.ToLower(file), ".env") ||
		strings.Contains(strings.ToLower(file), ".env.") ||
		strings.HasPrefix(strings.ToLower(filepath.Base(file)), "env.") ||
		filepath.Base(strings.ToLower(file)) == ".env")

	if isEnvFile {
		// Embed the head of the .env content. The full file can be up to
		// MaximumFileSize (256 KB), but a provider rejects an oversized body
		// outright, which used to lose the entire alert for a large .env. The
		// cap keeps the alert deliverable; truncateEnvContent marks any cut.
		message += "\n**📄 .env File Content:**\n```env\n" + truncateEnvContent(fileContent, maxEnvEmbedChars) + "\n```"
	}

	payload := buildWebhookPayload(webhookPayload, message)

	// Deliver through the shared queue so a generic endpoint gets the same
	// deduplication, spacing and retry as Discord. This used to POST inline,
	// which meant Slack or a custom endpoint had none of that.
	GetWebhookQueue().Enqueue(webhookURL, payload, onDelivery)
}

// shouldPingForMatch returns true if the signature should trigger a Discord mention
// This is used for high-priority items like AI API keys and database connection strings
func shouldPingForMatch(signature string) bool {
	sigLower := strings.ToLower(signature)
	// AI API key patterns (all get pinged)
	aiPatterns := []string{
		"api key", "anthropic", "openai", "claude", "deepseek", "mistral",
		"huggingface", "hf ", "together", "replicate", "perplexity", "cohere",
		"gemini", "bard", "ollama", "groq", "xai", "grok", "zhipu", "glm",
		"azure openai", "vertex ai", "makersuite", "palm", "llama", "moonshot",
		"kimi", "siliconflow", "sambanova", "qwen", "fireworks", "elevenlabs",
		"assemblyai", "deepgram", "anyscale", "aleph", "ai21", "davinci",
		"codex", "gpt-", "gpt3", "gpt4", "stability", "nvidia", "cerebras",
		"cloudflare", "workers", "stripe", "twilio", "sendgrid", "mailgun",
		"resend", "postmark", "hubspot", "zendesk", "intercom", "brevo", "loops",
	}
	// DB connection string patterns (all get pinged)
	dbPatterns := []string{
		"connection string", "connection_string", "database connection",
		"db_connection", "db_url", "database_url", "db password", "db_password",
		"database password", "database_password", "root password", "root_password",
		"postgres", "postgresql", "mysql", "mongodb", "redis", "mariadb",
		"sqlite", "mssql", "oracle",
	}

	for _, p := range aiPatterns {
		if strings.Contains(sigLower, p) {
			return true
		}
	}
	for _, p := range dbPatterns {
		if strings.Contains(sigLower, p) {
			return true
		}
	}
	return false
}

// sendDiscordMatchWebhook sends a match alert as a rich Discord embed via the webhook queue.
func sendDiscordMatchWebhook(webhookURL string, url string, signature string, file string, matches []string, color string, priority int, fileContent string, severity string, onDelivery func(ok bool)) {
	// The finding's severity, when known, wins over the signature colour so the
	// embed accent tracks the verified impact.
	colorInt := EmbedColor(severity, color)

	// Truncate matches if too long
	matchesStr := strings.Join(matches, ", ")
	if len(matchesStr) > 2048 {
		matchesStr = truncateForWebhook(matchesStr, 2045)
	}

	// Check if we should ping for this match (AI keys, DB connection strings)
	shouldPing := shouldPingForMatch(signature)
	userMention := ""
	if shouldPing && session != nil && session.Config != nil && session.Config.WebhookDiscordUserID != "" {
		userMention = "<@" + session.Config.WebhookDiscordUserID + ">\n"
	}

	// Create Discord embed payload
	// Ensure fields array is never nil (Discord rejects nil fields)
	fields := []map[string]interface{}{}

	// Add signature match section with clear formatting
	if len(matchesStr) == 0 || matchesStr == "" {
		// For filename-based matches (like .env files), show the filename
		fields = append(fields, map[string]interface{}{
			"name":   "📄 Matched File",
			"value":  fmt.Sprintf("`%s`", file),
			"inline": false,
		})
	} else {
		// Show the actual matched content in a code block
		matchDisplay := fmt.Sprintf("```\n%s\n```", matchesStr)
		if len(matchDisplay) > 1024 {
			matchDisplay = fmt.Sprintf("```\n%s\n```", truncateForWebhook(matchesStr, 900))
		}
		fields = append(fields, map[string]interface{}{
			"name":   "🔍 Signature Match",
			"value":  matchDisplay,
			"inline": false,
		})
	}

	// For .env files, embed the FULL content in code blocks (no truncation)
	// Match any .env file: .env, .env.local, .env.production, env.staging, etc.
	isEnvFile := fileContent != "" && (strings.HasSuffix(strings.ToLower(file), ".env") ||
		strings.Contains(strings.ToLower(file), ".env.") ||
		strings.HasPrefix(strings.ToLower(filepath.Base(file)), "env.") ||
		filepath.Base(strings.ToLower(file)) == ".env")

	if isEnvFile {
		// Discord allows at most 25 fields and 6000 characters across an
		// embed, and 1024 characters per field value, so the full file (up to
		// MaximumFileSize) cannot be embedded: an oversized embed is rejected
		// with a 400 and the alert is lost after the queue's retries. Embed the
		// head of the content across a bounded number of fields instead.
		const maxFieldLength = 900 // Leave room for code block markers
		const maxEnvFields = 4     // Bounded so the whole embed stays under 6000 chars

		envContent := truncateEnvContent(fileContent, maxFieldLength*maxEnvFields)

		if len(envContent) <= maxFieldLength {
			// Single field if content is small enough
			fields = append(fields, map[string]interface{}{
				"name":   "📄 .env File Content",
				"value":  "```env\n" + envContent + "\n```",
				"inline": false,
			})
		} else {
			// Split into multiple fields for large .env files
			envChunks := chunkString(envContent, maxFieldLength)
			for i, chunk := range envChunks {
				fieldName := fmt.Sprintf("📄 .env File Content (Part %d/%d)", i+1, len(envChunks))
				if i == 0 {
					fieldName = fmt.Sprintf("📄 .env File Content (1/%d)", len(envChunks))
				}
				fields = append(fields, map[string]interface{}{
					"name":   fieldName,
					"value":  "```env\n" + chunk + "\n```",
					"inline": false,
				})
			}
		}
	}

	// Generate GitHub link - only add line anchor if we have a valid line number
	var githubLink string
	if fileContent != "" && len(matches) > 0 {
		lineNum := findLineNumber(fileContent, matches[0])
		if lineNum > 0 {
			githubLink = generateGitHubBlobLink(url, file, lineNum)
		} else {
			githubLink = generateGitHubBlobLinkNoAnchor(url, file)
		}
	} else {
		// No valid line number, just link to file without anchor
		githubLink = generateGitHubBlobLinkNoAnchor(url, file)
	}

	// Create description with clickable link
	description := fmt.Sprintf("**Priority:** %d\n**File:** [%s](%s)\n**Repository:** %s",
		priority, file, githubLink, url)

	embed := map[string]interface{}{
		"title":       "🔍 " + signature,
		"description": description,
		"color":       colorInt,
		"fields":      fields,
		"footer": map[string]string{
			"text": "shhgit - Secret Detection",
		},
		"url":       githubLink,
		"timestamp": time.Now().Format(time.RFC3339),
	}

	payload := map[string]interface{}{
		"content": userMention,
		"embeds":  []map[string]interface{}{embed},
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		Say("[ERROR] Failed to marshal Discord webhook payload: %v\n", err)
		notifyDelivery(onDelivery, false)
		return
	}

	// Use webhook queue for rate limiting and retry logic
	queue := GetWebhookQueue()
	queue.Enqueue(webhookURL, string(jsonPayload), onDelivery)
}

// truncateForWebhook caps s at max bytes without splitting a multibyte rune,
// appending an ellipsis when it had to cut. The webhook fields carry scanned
// content which may be UTF-8; a raw byte slice could cut a rune in half and put
// invalid UTF-8 into the JSON body.
func truncateForWebhook(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return truncateToRuneBoundary(s, max) + "..."
}

// maxEnvEmbedChars bounds how much .env file content is embedded in a generic
// (Slack/Mattermost) webhook alert. The full file can be up to MaximumFileSize
// (256 KB), but Slack rejects text over 40000 characters, so embedding all of
// it lost the entire alert for a large .env. A few KB holds the head of any
// realistic .env; truncateEnvContent marks anything it had to cut.
const maxEnvEmbedChars = 3500

// envTruncationMarker is appended by truncateEnvContent when it cuts content.
const envTruncationMarker = "\n... (truncated)"

// truncateEnvContent caps s at max bytes without splitting a multi-byte UTF-8
// rune, appending a marker when it had to cut. max <= 0 means no cap. The
// marker is budgeted into max: appending it after slicing at max returned
// max+len(marker) bytes, which let the .env chunker emit more chunks than the
// section's field budget allowed.
func truncateEnvContent(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max - len(envTruncationMarker)
	if cut <= 0 {
		// No room for the marker within the cap; honour the cap alone.
		return truncateToRuneBoundary(s, max)
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + envTruncationMarker
}

// chunkString splits a string into chunks of at most maxSize bytes, cutting on
// rune boundaries so a multibyte character is never split between chunks.
func chunkString(s string, maxSize int) []string {
	if maxSize <= 0 || len(s) <= maxSize {
		return []string{s}
	}

	var chunks []string
	for len(s) > maxSize {
		chunk := truncateToRuneBoundary(s, maxSize)
		if chunk == "" {
			// A single rune longer than maxSize would otherwise stall the loop.
			chunk = s[:maxSize]
		}
		chunks = append(chunks, chunk)
		s = s[len(chunk):]
	}
	if len(s) > 0 {
		chunks = append(chunks, s)
	}
	return chunks
}

// findLineNumber finds the line number of the first occurrence of text in content
func findLineNumber(content string, searchText string) int {
	// 0 means "not found", which is what the callers test with lineNum > 0 to
	// decide between an anchored and an unanchored link. Returning 1 here made
	// the unanchored branch unreachable and pointed every not-found link at line 1.
	if !strings.Contains(content, searchText) {
		return 0
	}

	idx := strings.Index(content, searchText)
	// Count newlines before this index
	lineNum := 1 + strings.Count(content[:idx], "\n")
	return lineNum
}

// generateGitHubBlobLink generates a GitHub blob URL (for viewing with line anchors) from repo URL, file path, and line number
func generateGitHubBlobLink(repoURL string, filePath string, lineNum int) string {
	// Convert GitHub URL to blob URL with line anchor
	// https://github.com/user/repo.git -> https://github.com/user/repo/blob/main/file.path#L15
	// https://github.com/user/repo -> https://github.com/user/repo/blob/main/file.path#L15

	// Extract user/repo from URL
	repoURL = strings.TrimSuffix(repoURL, ".git")
	parts := strings.Split(repoURL, "/")
	if len(parts) < 4 {
		return repoURL // Fallback to original URL if parsing fails
	}

	user := parts[len(parts)-2]
	repo := parts[len(parts)-1]

	// Build GitHub blob URL with line anchor (assumes main/master branch)
	blobURL := fmt.Sprintf("https://github.com/%s/%s/blob/main/%s#L%d", user, repo, filePath, lineNum)

	return blobURL
}

// generateGitHubBlobLinkNoAnchor generates a GitHub blob URL without a line anchor
func generateGitHubBlobLinkNoAnchor(repoURL string, filePath string) string {
	// Convert GitHub URL to blob URL without line anchor
	// https://github.com/user/repo.git -> https://github.com/user/repo/blob/main/file.path

	// Extract user/repo from URL
	repoURL = strings.TrimSuffix(repoURL, ".git")
	parts := strings.Split(repoURL, "/")
	if len(parts) < 4 {
		return repoURL // Fallback to original URL if parsing fails
	}

	user := parts[len(parts)-2]
	repo := parts[len(parts)-1]

	// Build GitHub blob URL without line anchor (assumes main/master branch)
	blobURL := fmt.Sprintf("https://github.com/%s/%s/blob/main/%s", user, repo, filePath)

	return blobURL
}

// parseColorHex converts a hex color string to Discord embed color integer
func parseColorHex(hexColor string) int {
	// Remove # if present
	hexColor = strings.TrimPrefix(hexColor, "#")

	// Default to red if invalid
	if len(hexColor) != 6 {
		return 15158332 // Red
	}

	// Convert hex to int. ParseUint rejects a trailing non-hex character (which
	// fmt.Sscanf("%x") silently ignored, parsing "12GH45" as 0x12), so a
	// malformed colour falls back to red instead of a partial value.
	colorInt, err := strconv.ParseUint(hexColor, 16, 24)
	if err != nil {
		return 15158332 // Red
	}
	return int(colorInt)
}
