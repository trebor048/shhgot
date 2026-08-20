package core

import (
	"testing"
)

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
