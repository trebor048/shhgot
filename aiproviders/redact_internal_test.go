package aiproviders

import (
	"errors"
	"strings"
	"testing"
)

// Provider errors are stored with the review and broadcast on the live feed, so
// an upstream that echoes the request must not be able to put a credential in
// them. The key is only sent as an Authorization header, which makes a "Bearer
// <token>" echo the realistic case.
func TestRedactRemovesEchoedCredentials(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		key     string
		want    string
		absent  string
		present string
	}{
		{
			name:   "long key anywhere in the message",
			in:     `custom status 401: {"error":"bad key sk-LONG-REDACT-ME-1234"}`,
			key:    "sk-LONG-REDACT-ME-1234",
			want:   "[redacted]",
			absent: "sk-LONG-REDACT-ME-1234",
		},
		{
			name:   "a short key straight after the header name",
			in:     "custom status 401: bad key Bearer abc",
			key:    "abc",
			want:   "Bearer [redacted]",
			absent: "Bearer abc",
		},
		{
			name:   "a short key echoed in a field",
			in:     `{"error":"invalid api key abc supplied"}`,
			key:    "abc",
			want:   "[redacted]",
			absent: `key abc`,
		},
		{
			name:    "a short key must not be matched inside a longer word",
			in:      "connection to basic-auth proxy abcdef failed",
			key:     "abc",
			want:    "basic-auth proxy abcdef failed",
			absent:  "[redacted]",
			present: "abcdef",
		},
		{
			name:    "uppercase bearer is caught too",
			in:      "upstream said BEARER sk-live-abcdefgh rejected",
			key:     "sk-live-abcdefgh",
			want:    "[redacted]",
			absent:  "sk-live-abcdefgh",
			present: "rejected",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactString(tc.in, tc.key)
			if !strings.Contains(got, tc.want) {
				t.Errorf("redactString(%q) = %q, want it to contain %q", tc.in, got, tc.want)
			}
			if tc.absent != "" && strings.Contains(got, tc.absent) {
				t.Errorf("redactString(%q) = %q, still contains %q", tc.in, got, tc.absent)
			}
			if tc.present != "" && !strings.Contains(got, tc.present) {
				t.Errorf("redactString(%q) = %q, lost %q", tc.in, got, tc.present)
			}
		})
	}
}

func TestRedactLeavesOrdinaryMessagesAlone(t *testing.T) {
	for _, in := range []string{
		"",
		"connection refused",
		"API key is required for this provider: deepseek",
		"custom status 404: model not found",
	} {
		if got := redactString(in, "sk-some-key-value"); got != in {
			t.Errorf("redactString(%q) = %q, want it unchanged", in, got)
		}
	}
}

// redactError must hand back the original error when there is nothing to hide,
// so wrapped errors keep working with errors.Is.
func TestRedactErrorKeepsTheOriginalWhenClean(t *testing.T) {
	original := errors.New("connection refused")
	if got := redactError(original, "sk-key"); got != original {
		t.Errorf("redactError returned a new error for a clean message: %v", got)
	}
	if redactError(nil, "sk-key") != nil {
		t.Error("redactError(nil) must stay nil")
	}

	leaky := errors.New("bad key Bearer sk-leaky-123456")
	masked := redactError(leaky, "sk-leaky-123456")
	if strings.Contains(masked.Error(), "sk-leaky-123456") {
		t.Errorf("redactError leaked the key: %v", masked)
	}
}
