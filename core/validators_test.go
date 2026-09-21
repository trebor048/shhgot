package core

import (
	"strings"
	"testing"
)

// Discord encodes the token's segments as unpadded base64url, so the padded
// StdEncoding/URLEncoding both rejected every real token and the verifier
// always returned false. The format check must accept a well-formed token.
func TestValidateDiscordTokenFormat(t *testing.T) {
	valid := "M" + strings.Repeat("a", 23) + ".abc123." + strings.Repeat("A", 26) + "1"
	if len(valid) != 59 {
		t.Fatalf("fixture length = %d, want 59", len(valid))
	}
	if !validateDiscordTokenFormat(valid) {
		t.Errorf("validateDiscordTokenFormat(%q) = false, want true", valid)
	}

	cases := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"wrong length", "Mabc.def123.ghi"},
		{"wrong prefix", "X" + valid[1:]},
		{"middle not six chars", "M" + strings.Repeat("a", 23) + ".abcdefg." + strings.Repeat("A", 25) + "1"},
		{"invalid base64 middle", "M" + strings.Repeat("a", 23) + ".abc12!." + strings.Repeat("A", 26) + "1"},
	}
	for _, tc := range cases {
		if validateDiscordTokenFormat(tc.token) {
			t.Errorf("%s: validateDiscordTokenFormat(%q) = true, want false", tc.name, tc.token)
		}
	}
}

func TestValidateDiscordWebhook(t *testing.T) {
	tests := []struct {
		name     string
		webhook  string
		expected bool
	}{
		{
			name:     "Valid Discord webhook URL format",
			webhook:  "https://discord.com/api/webhooks/123456789012345678/abcdef1234567890abcdef1234567890abcdef1234567890",
			expected: false, // Will fail API test but pass format validation
		},
		{
			name:     "Invalid Discord webhook - wrong domain",
			webhook:  "https://example.com/webhooks/123456789012345678/token",
			expected: false,
		},
		{
			name:     "Invalid Discord webhook - missing parts",
			webhook:  "https://discord.com/api/webhooks/123456789012345678",
			expected: false,
		},
		{
			name:     "Empty webhook",
			webhook:  "",
			expected: false,
		},
		{
			name:     "Invalid Discord webhook - non-numeric ID",
			webhook:  "https://discord.com/api/webhooks/abc123/token123",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateDiscordWebhook(tt.webhook)
			if result != tt.expected {
				t.Errorf("ValidateDiscordWebhook(%q) = %v, want %v", tt.webhook, result, tt.expected)
			}
		})
	}
}

func TestValidateTelegramWebhook(t *testing.T) {
	tests := []struct {
		name     string
		token    string
		expected bool
	}{
		{
			name:     "Invalid Telegram token - wrong format",
			token:    "invalid_token",
			expected: false,
		},
		{
			name:     "Invalid Telegram token - too short",
			token:    "123:short",
			expected: false,
		},
		{
			name:     "Empty token",
			token:    "",
			expected: false,
		},
		{
			name:     "Valid format but will fail API test",
			token:    "1234567890:ABC123defGHI456jklMNO789pqrSTU012vwxYZ",
			expected: false, // Valid format but will fail API validation
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateTelegramWebhook(tt.token)
			if result != tt.expected {
				t.Errorf("ValidateTelegramWebhook(%q) = %v, want %v", tt.token, result, tt.expected)
			}
		})
	}
}

// A line number of 0 means "not found" and makes the caller emit an unanchored
// link; returning 1 (as it used to) made that branch unreachable and pointed
// every not-found link at line 1.
func TestFindLineNumber(t *testing.T) {
	content := "line one\nline two\nsecret here\nline four"
	cases := []struct {
		search string
		want   int
	}{
		{"line one", 1},
		{"secret here", 3},
		{"not present anywhere", 0},
		{"", 1},
	}
	for _, tc := range cases {
		if got := findLineNumber(content, tc.search); got != tc.want {
			t.Errorf("findLineNumber(%q) = %d, want %d", tc.search, got, tc.want)
		}
	}
}
