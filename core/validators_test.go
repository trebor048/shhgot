package core

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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

// A scanned secret, file name or repository can contain a quote, backslash or
// newline. Building the webhook body with fmt.Sprintf embedded those raw, so the
// JSON was invalid and repo content could inject fields into the operator's
// endpoint. The rendered payload must always be valid JSON.
func TestSendValidationWebhookProducesValidJSON(t *testing.T) {
	body := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body <- string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// The quote/newline is injected through the repository field, which carries
	// untrusted scanned content into the message.
	SendValidationWebhook(srv.URL, `{"text": "%s"}`, "OpenAI", "sk-secret", "repo\"\ninjected\":true", "creds.env", true)

	select {
	case b := <-body:
		var doc map[string]string
		if err := json.Unmarshal([]byte(b), &doc); err != nil {
			t.Fatalf("validation webhook body is not valid JSON: %v\nbody=%s", err, b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no request reached the test server")
	}
}

// buildWebhookPayload is the single escaper behind the generic match webhook,
// the validation webhook and the logger's webhook.
func TestBuildWebhookPayloadEscapesMessage(t *testing.T) {
	message := "a\"b\\c\nd {injected:true}"
	got := buildWebhookPayload(`{"text": "%s"}`, message)

	var doc map[string]string
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("payload is not valid JSON: %v\nbody=%s", err, got)
	}
	if doc["text"] != message {
		t.Errorf("text = %q, want %q", doc["text"], message)
	}
}

// The generic branch of SendMatchWebhook carries the raw matched secret, so the
// delivered body must stay valid JSON even when the secret contains quotes and
// newlines.
func TestSendMatchWebhookGenericProducesValidJSON(t *testing.T) {
	body := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body <- string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	secret := "AKIA\"secret\nline"
	SendMatchWebhook(srv.URL, `{"text": "%s"}`, "https://github.com/o/r", "AWS Key", "creds.env", []string{secret}, "#ff0000", 5, "", "", nil)

	select {
	case b := <-body:
		if !json.Valid([]byte(b)) {
			t.Fatalf("match webhook body is not valid JSON: %s", b)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no request reached the test server")
	}
}

// chunkString sliced at a raw byte offset, so a multibyte rune straddling the
// boundary was split between two chunks and rendered as mojibake in the embed.
func TestChunkStringKeepsRunesIntact(t *testing.T) {
	s := strings.Repeat("é", 5) // 10 bytes, 5 two-byte runes
	chunks := chunkString(s, 3)
	if got := strings.Join(chunks, ""); got != s {
		t.Fatalf("chunks lost data: %q, want %q", got, s)
	}
	for i, c := range chunks {
		if !utf8.ValidString(c) {
			t.Errorf("chunk %d splits a rune: %q", i, c)
		}
	}
}

// truncateForWebhook is the rune-safe cap used by the webhook match fields.
func TestTruncateForWebhookKeepsRunesIntact(t *testing.T) {
	s := strings.Repeat("é", 4) // 8 bytes, 4 runes
	got := truncateForWebhook(s, 3)
	if !utf8.ValidString(got) {
		t.Fatalf("truncateForWebhook split a rune: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("truncateForWebhook = %q, want an ellipsis suffix", got)
	}
	if len(got) > 6 {
		t.Fatalf("truncateForWebhook length = %d, want <= 6", len(got))
	}
}

// truncateEnvContent appended its "... (truncated)" marker after cutting at
// max, so the result exceeded the requested cap. The .env chunker budgets
// maxFieldLength*maxEnvFields bytes, and the overrun could push the content
// over a chunk boundary and emit one field more than the cap allowed.
func TestTruncateEnvContentHonoursCap(t *testing.T) {
	const max = 100
	got := truncateEnvContent(strings.Repeat("A", 500), max)
	if len(got) > max {
		t.Fatalf("truncateEnvContent returned %d bytes for a %d-byte cap", len(got), max)
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("truncated output must be marked: %q", got)
	}
}

// parseColorHex used fmt.Sscanf("%x"), which accepts a partial hex prefix
// ("12GH45" parsed as 0x12) instead of falling back to red.
func TestParseColorHexRejectsPartialHex(t *testing.T) {
	const red = 15158332
	if got := parseColorHex("12GH45"); got != red {
		t.Errorf("parseColorHex(%q) = %d, want fallback red %d", "12GH45", got, red)
	}
	if got := parseColorHex("#10a37f"); got != 0x10a37f {
		t.Errorf("parseColorHex(%q) = %d, want %d", "#10a37f", got, 0x10a37f)
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
