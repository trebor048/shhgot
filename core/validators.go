package core

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

var (
	httpClient = &http.Client{
		Timeout: 10 * time.Second,
	}
	
	// Global webhook queue for rate limiting and retry
	globalWebhookQueue *WebhookQueue
	webhookQueueOnce   sync.Once
)

// GetWebhookQueue returns the singleton webhook queue instance
func GetWebhookQueue() *WebhookQueue {
	webhookQueueOnce.Do(func() {
		// Discord allows 5 requests per 2 seconds per webhook = 400ms between requests
		// Use 500ms to be safe
		globalWebhookQueue = NewWebhookQueue(1000, 500, 300)
		globalWebhookQueue.Start()
	})
	return globalWebhookQueue
}

// ValidateDiscordToken validates a Discord bot token by making an API request
func ValidateDiscordToken(token string) bool {
	if token == "" {
		return false
	}

	// Discord bot tokens should be 59 characters long and follow the pattern: [MN][A-Za-z\d]{23}\.[\w-]{6}\.[\w-]{27}
	if len(token) != 59 {
		return false
	}

	// Basic format validation
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}

	// Validate user ID part (first part after prefix)
	if len(parts[0]) < 2 || (parts[0][0] != 'M' && parts[0][0] != 'N') {
		return false
	}

	// Try to decode the base64 parts to ensure they're valid
	for i, part := range parts[1:] {
		if _, err := base64.StdEncoding.DecodeString(part); err != nil {
			// Try URL-safe base64 as well
			if _, err := base64.URLEncoding.DecodeString(part); err != nil {
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

	// API validation (optional, can be disabled for performance)
	req, err := http.NewRequest("GET", "https://discord.com/api/v10/users/@me", nil)
	if err != nil {
		return false
	}

	req.Header.Set("Authorization", "Bot "+token)
	resp, err := httpClient.Do(req)
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
	url := "https://api.telegram.org/bot" + token + "/getMe"
	resp, err := httpClient.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == 200
}

// ValidateDiscordWebhook validates a Discord webhook URL
func ValidateDiscordWebhook(webhookURL string) bool {
	if webhookURL == "" {
		fmt.Printf("[WARN] Discord webhook URL is empty\n")
		return false
	}

	// Discord webhook URL pattern: https://discord.com/api/webhooks/ID/TOKEN
	if !strings.HasPrefix(webhookURL, "https://discord.com/api/webhooks/") {
		fmt.Printf("[ERROR] Invalid Discord webhook URL format. Expected: https://discord.com/api/webhooks/ID/TOKEN\n")
		return false
	}

	// Extract webhook ID and token
	parts := strings.Split(strings.TrimPrefix(webhookURL, "https://discord.com/api/webhooks/"), "/")
	if len(parts) < 2 {
		fmt.Printf("[ERROR] Discord webhook URL missing ID or token\n")
		return false
	}

	// Validate webhook ID (should be numeric)
	webhookID := parts[0]
	for _, char := range webhookID {
		if char < '0' || char > '9' {
			fmt.Printf("[ERROR] Discord webhook ID must be numeric\n")
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
		fmt.Printf("[ERROR] Failed to marshal validation payload: %v\n", err)
		return false
	}

	resp, err := http.Post(webhookURL, "application/json", strings.NewReader(string(jsonData)))
	if err != nil {
		fmt.Printf("[ERROR] Discord webhook POST failed: %v\n", err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode == 204 || resp.StatusCode == 200 {
		fmt.Printf("[✓] Discord webhook is valid and has proper permissions\n")
		return true
	}

	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("[ERROR] Discord webhook validation failed:\n")
	fmt.Printf("  HTTP Status: %d\n", resp.StatusCode)
	fmt.Printf("  Response: %s\n", string(body))

	switch resp.StatusCode {
	case 401, 403:
		fmt.Printf("  → Permission denied. Check webhook has 'Send Messages' permission in Discord channel.\n")
		return false
	case 404:
		fmt.Printf("  → Webhook not found. Verify the webhook URL is correct and hasn't been deleted.\n")
		return false
	case 429:
		// Rate limited - Discord is temporarily blocking us
		// The webhook is valid, just can't test it right now
		// The webhook queue will handle retries with backoff
		fmt.Printf("  → Rate limited (429). Assuming webhook is valid.\n")
		fmt.Printf("  → The webhook queue system will retry with exponential backoff.\n")
		return true // Consider valid since it's a transient rate limit, not a permission error
	case 400:
		fmt.Printf("  → Bad request. The webhook payload may be invalid.\n")
		return false
	default:
		fmt.Printf("  → Unexpected status code. Check Discord API status.\n")
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
	url := "https://api.telegram.org/bot" + botToken + "/getWebhookInfo"
	resp, err := httpClient.Get(url)
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

	if ok, exists := result["ok"]; !exists || !ok.(bool) {
		return false
	}

	return true
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
		payload = fmt.Sprintf(`{"content": "%s"}`, message)
	} else {
		// Default format (Slack, MatterMost, etc.)
		payload = fmt.Sprintf(webhookPayload, message)
	}

	// Send webhook notification
	resp, err := http.Post(webhookURL, "application/json", strings.NewReader(payload))
	if err != nil {
		// Silently fail to avoid infinite loops
		return
	}
	defer resp.Body.Close()
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

// ValidateSSHKeyFormat validates the format of an SSH private key without connecting
func ValidateSSHKeyFormat(privateKey string) bool {
	if privateKey == "" {
		return false
	}

	privateKey = strings.TrimSpace(privateKey)

	// Check for common SSH key formats
	if strings.Contains(privateKey, "-----BEGIN") {
		// PEM format
		validBeginnings := []string{
			"-----BEGIN RSA PRIVATE KEY-----",
			"-----BEGIN DSA PRIVATE KEY-----",
			"-----BEGIN EC PRIVATE KEY-----",
			"-----BEGIN OPENSSH PRIVATE KEY-----",
			"-----BEGIN PRIVATE KEY-----",
		}

		for _, beginning := range validBeginnings {
			if strings.Contains(privateKey, beginning) {
				// Try to parse it
				_, err := ssh.ParsePrivateKey([]byte(privateKey))
				return err == nil
			}
		}
		return false
	}

	// Try to parse as raw key data
	_, err := ssh.ParsePrivateKey([]byte(privateKey))
	if err == nil {
		return true
	}

	// Try base64 decoding
	if decoded, err := base64.StdEncoding.DecodeString(privateKey); err == nil {
		_, err := ssh.ParsePrivateKey(decoded)
		return err == nil
	}

	return false
}

// ExtractSSHKeys extracts potential SSH private keys from text content
func ExtractSSHKeys(content string) []string {
	var keys []string
	lines := strings.Split(content, "\n")

	var currentKey strings.Builder
	inKeyBlock := false
	keyType := ""

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Check for key block start
		for _, keyStart := range []string{
			"-----BEGIN RSA PRIVATE KEY-----",
			"-----BEGIN DSA PRIVATE KEY-----",
			"-----BEGIN EC PRIVATE KEY-----",
			"-----BEGIN OPENSSH PRIVATE KEY-----",
			"-----BEGIN PRIVATE KEY-----",
		} {
			if strings.HasPrefix(line, keyStart) {
				inKeyBlock = true
				keyType = keyStart
				currentKey.Reset()
				currentKey.WriteString(line)
				currentKey.WriteString("\n")
				break
			}
		}

		// Check for key block end
		if inKeyBlock {
			endMarker := strings.Replace(keyType, "BEGIN", "END", 1)
			if strings.HasPrefix(line, endMarker) {
				currentKey.WriteString(line)
				keys = append(keys, currentKey.String())
				inKeyBlock = false
				currentKey.Reset()
				continue
			}

			// Add line to current key
			currentKey.WriteString(line)
			currentKey.WriteString("\n")
		}
	}

	return keys
}

// SendMatchWebhook sends a match alert to a configured webhook. It builds a
// rich Discord embed for Discord endpoints and falls back to the generic
// webhook_payload template (Slack, Mattermost, custom) otherwise. Unlike the
// original high-priority-only helper it fires for every match.
func SendMatchWebhook(webhookURL, webhookPayload, url, signature, file string, matches []string, color string, priority int, fileContent string) {
	if webhookURL == "" {
		return
	}

	if strings.Contains(webhookURL, "discord.com/api/webhooks") {
		sendDiscordMatchWebhook(webhookURL, url, signature, file, matches, color, priority, fileContent)
		return
	}

	// Generic webhook (Slack, Mattermost, custom endpoints) using the payload
	// template from config (defaults to a Slack-style {"text": "..."}).
	matchesStr := strings.Join(matches, ", ")
	if len(matchesStr) > 1000 {
		matchesStr = matchesStr[:997] + "..."
	}
	
	// For filename-based matches, show the filename if no content matches
	if len(matchesStr) == 0 || matchesStr == "" {
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
	
	// Enhanced message format with clear signature match display
	message := fmt.Sprintf("**%s** | Priority: %d\n**File:** [%s](%s)\n**Repository:** %s\n\n**<match>**\n```\n%s\n```", 
		signature, priority, file, githubLink, url, matchesStr)
	
	// For .env files, embed FULL file content (match all .env variants)
	isEnvFile := fileContent != "" && (
		strings.HasSuffix(strings.ToLower(file), ".env") ||
		strings.Contains(strings.ToLower(file), ".env.") ||
		strings.HasPrefix(strings.ToLower(filepath.Base(file)), "env.") ||
		filepath.Base(strings.ToLower(file)) == ".env")
	
	if isEnvFile {
		// Embed FULL file content for .env files (no truncation)
		message += "\n**📄 .env File Content:**\n```env\n" + fileContent + "\n```"
	}

	if webhookPayload == "" {
		webhookPayload = `{"text": "%s"}`
	}
	payload := fmt.Sprintf(webhookPayload, message)

	resp, err := http.Post(webhookURL, "application/json", strings.NewReader(payload))
	if err != nil {
		fmt.Printf("[ERROR] Generic webhook POST failed: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("[ERROR] Generic webhook returned %d: %s\n", resp.StatusCode, string(body))
	}
}

// sendDiscordMatchWebhook sends a match alert as a rich Discord embed via the webhook queue.
func sendDiscordMatchWebhook(webhookURL string, url string, signature string, file string, matches []string, color string, priority int, fileContent string) {
	// Convert color hex to decimal for Discord embed
	colorInt := parseColorHex(color)

	// Truncate matches if too long
	matchesStr := strings.Join(matches, ", ")
	if len(matchesStr) > 2048 {
		matchesStr = matchesStr[:2045] + "..."
	}

	// Create Discord embed payload
	fields := []map[string]interface{}{}
	
	// Add signature match section with clear formatting
	if len(matchesStr) == 0 || matchesStr == "" {
		// For filename-based matches (like .env files), show the filename
		fields = append(fields, map[string]interface{}{
			"name":  "📄 Matched File",
			"value": fmt.Sprintf("`%s`", file),
		})
	} else {
		// Show the actual matched content in a highlighted <match> block
		matchDisplay := fmt.Sprintf("```\n<match>\n%s\n</match>\n```", matchesStr)
		if len(matchDisplay) > 1024 {
			matchDisplay = fmt.Sprintf("```\n<match>\n%s\n</match>\n```", matchesStr[:900]+"...")
		}
		fields = append(fields, map[string]interface{}{
			"name":   "🔍 Signature Match",
			"value":  matchDisplay,
			"inline": false,
		})
	}

	// For .env files, embed the FULL content in code blocks (no truncation)
	// Match any .env file: .env, .env.local, .env.production, env.staging, etc.
	isEnvFile := fileContent != "" && (
		strings.HasSuffix(strings.ToLower(file), ".env") ||
		strings.Contains(strings.ToLower(file), ".env.") ||
		strings.HasPrefix(strings.ToLower(filepath.Base(file)), "env.") ||
		filepath.Base(strings.ToLower(file)) == ".env")
	
	if isEnvFile {
		// Discord has a 6000 character limit per embed description
		// and 1024 character limit per field value
		// We'll split the content across multiple fields if needed
		const maxFieldLength = 900 // Leave room for code block markers
		
		if len(fileContent) <= maxFieldLength {
			// Single field if content is small enough
			fields = append(fields, map[string]interface{}{
				"name":   "📄 .env File Content",
				"value":  "```env\n" + fileContent + "\n```",
				"inline": false,
			})
		} else {
			// Split into multiple fields for large .env files
			envChunks := chunkString(fileContent, maxFieldLength)
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
		"embeds": []map[string]interface{}{embed},
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		fmt.Printf("[ERROR] Failed to marshal Discord webhook payload: %v\n", err)
		return
	}

	// Use webhook queue for rate limiting and retry logic
	queue := GetWebhookQueue()
	queue.Enqueue(webhookURL, string(jsonPayload))
}

// chunkString splits a string into chunks of max size
func chunkString(s string, maxSize int) []string {
	if len(s) <= maxSize {
		return []string{s}
	}

	var chunks []string
	for len(s) > maxSize {
		chunks = append(chunks, s[:maxSize])
		s = s[maxSize:]
	}
	if len(s) > 0 {
		chunks = append(chunks, s)
	}
	return chunks
}

// findLineNumber finds the line number of the first occurrence of text in content
func findLineNumber(content string, searchText string) int {
	if !strings.Contains(content, searchText) {
		return 1
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

	// Convert hex to int
	var colorInt int
	fmt.Sscanf(hexColor, "%x", &colorInt)
	return colorInt
}
