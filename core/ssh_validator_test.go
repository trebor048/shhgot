package core

import (
	"testing"
)

func TestValidateSSHKeyFormat(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		expected bool
	}{
		{
			name:     "Valid RSA key",
			key:      "-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA4f5wg5l2hKsTeNem/V41fGnJm6gOdrj8ym3rFkEjWT2btZb5\n-----END RSA PRIVATE KEY-----",
			expected: false, // This is not a complete key, should fail parsing
		},
		{
			name:     "Empty key",
			key:      "",
			expected: false,
		},
		{
			name:     "Invalid format",
			key:      "not a private key",
			expected: false,
		},
		{
			name:     "Valid OpenSSH key format",
			key:      "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW\n-----END OPENSSH PRIVATE KEY-----",
			expected: false, // This is not a complete key, should fail parsing
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateSSHKeyFormat(tt.key)
			if result != tt.expected {
				t.Errorf("ValidateSSHKeyFormat() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestExtractSSHKeys(t *testing.T) {
	content := `Some text here
-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEA4f5wg5l2hKsTeNem/V41fGnJm6gOdrj8ym3rFkEjWT2btZb5
-----END RSA PRIVATE KEY-----
More text
-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
-----END OPENSSH PRIVATE KEY-----
Final text`

	keys := ExtractSSHKeys(content)
	if len(keys) != 2 {
		t.Errorf("ExtractSSHKeys() extracted %d keys, want 2", len(keys))
	}

	for i, key := range keys {
		if !contains(key, "-----BEGIN") || !contains(key, "-----END") {
			t.Errorf("Extracted key %d doesn't have proper BEGIN/END markers", i)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) &&
		(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
			findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
