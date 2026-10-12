package verifiers

import (
	"net/http"
	"testing"
)

// TestVerifyNilDoerIsUnverified verifies a nil doer does not fall back to
// http.DefaultClient (which has no timeout) but is reported unverified.
func TestVerifyNilDoerIsUnverified(t *testing.T) {
	v := Verifier{
		Name: "test",
		Build: func(c Credential, base string) (*http.Request, error) {
			return http.NewRequest(http.MethodGet, "https://example.com", nil)
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			return Outcome{Status: Valid}
		},
	}
	o, _ := v.Verify(nil, Credential{Key: "k"}, "")
	if o.Status != Unverified {
		t.Fatalf("status = %s, want %s for a nil doer", o.Status, Unverified)
	}
}
