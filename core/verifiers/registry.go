// Package verifiers is a self-contained, core-free registry of credential
// verification endpoints ("verifiers") and their outcome classifiers.
//
// It imports only the Go standard library. Callers inject the HTTP client
// (a Doer) and the provider base URL, so tests can point a verifier at an
// httptest server instead of the live network. This package does not import
// github.com/trebor048/shhgot/core.
package verifiers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// maxBodyBytes bounds how much of a provider response is read into memory.
const maxBodyBytes = 64 << 10

// Status is the verified state of a credential.
type Status string

const (
	// Valid means the provider accepted the credential.
	Valid Status = "valid"
	// Invalid means the provider rejected the credential (401/403) or answered
	// a definitive authentication failure.
	Invalid Status = "invalid"
	// QuotaLimited means the credential is accepted but rate limited (429).
	QuotaLimited Status = "quota_limited"
	// Unverified means the outcome could not be determined (missing companion
	// values, unexpected status) and should fall back to signature priority.
	Unverified Status = "unverified"
	// Dead means the endpoint could not be reached; callers may override this.
	Dead Status = "dead"
)

// Credential holds the values a verifier may need. Not every provider uses
// every field.
type Credential struct {
	Key       string
	BaseURL   string
	SID       string
	Email     string
	AppID     string
	Subdomain string
	Domain    string
	Username  string
	Password  string
}

// Outcome is the result of running a verifier against a provider.
type Outcome struct {
	Status    Status
	Detail    string
	Identity  string
	Tier      string
	Balance   string
	RPM       int
	HighValue bool
	Extra     map[string]string
}

// Doer is the injectable HTTP client seam.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Verifier describes how to build and interpret a provider check.
type Verifier struct {
	Name        string
	Category    string
	KeyPrefixes []string
	Needs       []string
	Build       func(c Credential, base string) (*http.Request, error)
	Test        func(resp *http.Response, body []byte) Outcome
}

var (
	regMu    sync.RWMutex
	registry = make(map[string]Verifier)
)

// Register adds a verifier to the registry. It fails on an empty name, a nil
// Build/Test, or a duplicate name.
func Register(v Verifier) error {
	if strings.TrimSpace(v.Name) == "" {
		return errors.New("verifiers: empty verifier name")
	}
	if v.Build == nil {
		return fmt.Errorf("verifiers: %s has a nil Build", v.Name)
	}
	if v.Test == nil {
		return fmt.Errorf("verifiers: %s has a nil Test", v.Name)
	}
	regMu.Lock()
	defer regMu.Unlock()
	if _, exists := registry[v.Name]; exists {
		return fmt.Errorf("verifiers: duplicate verifier %q", v.Name)
	}
	registry[v.Name] = v
	return nil
}

// add is the init-time registration helper. A failure here is a programming
// error (duplicate name, nil hook), so it panics during package init.
func add(v Verifier) {
	if err := Register(v); err != nil {
		panic(err)
	}
}

// Lookup returns the verifier registered under name.
func Lookup(name string) (Verifier, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	v, ok := registry[name]
	return v, ok
}

// All returns every registered verifier, sorted by Name for determinism.
func All() []Verifier {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Verifier, 0, len(registry))
	for _, v := range registry {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Detect returns the verifier whose longest KeyPrefix matches keyOrPrefix.
// Detection is case-sensitive and prefers the most specific (longest) prefix;
// ties are broken by the sorted registry order so the result is deterministic.
func Detect(keyOrPrefix string) (Verifier, bool) {
	if keyOrPrefix == "" {
		return Verifier{}, false
	}
	var (
		best    Verifier
		bestLen = -1
	)
	for _, v := range All() {
		for _, p := range v.KeyPrefixes {
			if p == "" || !strings.HasPrefix(keyOrPrefix, p) {
				continue
			}
			if len(p) > bestLen {
				best, bestLen = v, len(p)
			}
		}
	}
	if bestLen < 0 {
		return Verifier{}, false
	}
	return best, true
}

// Verify builds and executes a verifier against base. An empty base falls back
// to c.BaseURL and then to the provider's documented default. When Needs lists
// companion fields that are absent from c, no request is made and the outcome
// is Unverified.
//
// A transport error is returned to the caller alongside a Dead outcome; a
// 429/401/403 response is classified by the verifier's Test as QuotaLimited or
// Invalid.
func (v Verifier) Verify(doer Doer, c Credential, base string) (Outcome, error) {
	if v.Build == nil || v.Test == nil {
		return Outcome{Status: Unverified, Detail: "incomplete verifier"}, nil
	}
	if missing := c.missing(v.Needs); len(missing) > 0 {
		return Outcome{
			Status: Unverified,
			Detail: "missing companion credential: " + strings.Join(missing, ", "),
		}, nil
	}
	if base == "" {
		base = c.BaseURL
	}
	req, err := v.Build(c, base)
	if err != nil {
		return Outcome{Status: Unverified, Detail: "build request: " + err.Error()}, err
	}
	if doer == nil {
		// http.DefaultClient has no timeout, so a nil doer must not silently
		// enable an unbounded request.
		return Outcome{Status: Unverified, Detail: "no HTTP doer configured"}, nil
	}
	resp, err := doer.Do(req)
	if err != nil {
		return Outcome{Status: Dead, Detail: "connection error: " + err.Error()}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return Outcome{Status: Dead, Detail: "read response: " + err.Error()}, err
	}
	return v.Test(resp, body), nil
}

// field resolves a Needs name to its Credential value.
func (c Credential) field(name string) string {
	switch name {
	case "Key":
		return c.Key
	case "SID":
		return c.SID
	case "Email":
		return c.Email
	case "AppID":
		return c.AppID
	case "Subdomain":
		return c.Subdomain
	case "Domain":
		return c.Domain
	case "Username":
		return c.Username
	case "Password":
		return c.Password
	case "BaseURL":
		return c.BaseURL
	default:
		return ""
	}
}

// missing returns the Needs names whose credential value is empty.
func (c Credential) missing(needs []string) []string {
	var out []string
	for _, n := range needs {
		if strings.TrimSpace(c.field(n)) == "" {
			out = append(out, n)
		}
	}
	return out
}

// bearer sets the Authorization: Bearer header.
func bearer(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
}

// basicAuth sets HTTP basic authentication.
func basicAuth(req *http.Request, user, pass string) {
	req.SetBasicAuth(user, pass)
}

// succeeded reports whether the response is a 2xx status.
func succeeded(resp *http.Response) bool {
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// mapStatus classifies the statuses shared by every provider: 429 is
// QuotaLimited and 401/403 is Invalid. The second result reports whether a
// classification was made.
func mapStatus(resp *http.Response) (Outcome, bool) {
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return Outcome{Status: QuotaLimited, Detail: "rate limited (429)"}, true
	case http.StatusUnauthorized, http.StatusForbidden:
		return Outcome{Status: Invalid, Detail: "authentication failed"}, true
	}
	return Outcome{}, false
}

// unexpected classifies any other non-2xx status as Unverified.
func unexpected(resp *http.Response) Outcome {
	return Outcome{Status: Unverified, Detail: fmt.Sprintf("unexpected HTTP %d", resp.StatusCode)}
}

// decodeJSON decodes body into v, reporting success.
func decodeJSON(body []byte, v any) bool {
	return json.Unmarshal(body, v) == nil
}

// jsonString renders s as a JSON string literal (including surrounding quotes).
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// intHeader parses an integer response header, returning 0 when absent.
func intHeader(resp *http.Response, name string) int {
	raw := strings.TrimSpace(resp.Header.Get(name))
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return n
}

// validOutcome builds a common valid outcome.
func validOutcome(detail string) Outcome {
	return Outcome{Status: Valid, Detail: detail}
}
