package aireview

import (
	"regexp"
	"strings"
	"time"
)

// Verifier checks whether a credential is still valid with its provider.
// err != nil means the check was inconclusive (network/rate limit) and must
// NOT be treated as revoked.
type Verifier interface {
	Check(sigName, secret string) (valid bool, info string, err error)
}

// Sig is a compiled detection signature used by the format re-check.
type Sig struct {
	Name string
	Re   *regexp.Regexp
}

// Gate is the deterministic Stage-0 pre-validation gate. It runs before any
// clone and before any LLM spend.
type Gate struct {
	placeholders []string // case-insensitive substrings
	testKeys     []string // exact known test keys
	sigs         []Sig
	verifier     Verifier
	now          func() time.Time
}

// NewGate builds the gate. verifier may be nil (revocation check skipped).
func NewGate(placeholders, testKeys []string, sigs []Sig, v Verifier) *Gate {
	return &Gate{placeholders: placeholders, testKeys: testKeys, sigs: sigs, verifier: v, now: time.Now}
}

// Evaluate runs the full Stage-0 gate: placeholder scan, format re-check,
// then revocation check. Returns (verdict, reason).
func (g *Gate) Evaluate(j *Job) (verdict, reason string) {
	low := strings.ToLower(j.Secret + "\n" + j.MatchContext)
	for _, p := range g.placeholders {
		if strings.Contains(low, strings.ToLower(p)) {
			return VerdictPlaceholder, "placeholder/test pattern matched in secret or context"
		}
	}
	for _, k := range g.testKeys {
		if j.Secret == k {
			return VerdictTestFixture, "exact known test key"
		}
	}
	for _, s := range g.sigs {
		if s.Name == j.Signature && s.Re != nil && !s.Re.MatchString(j.Secret) {
			return VerdictMalformed, "secret does not match the signature's format"
		}
	}
	if g.verifier != nil {
		valid, info, err := g.verifier.Check(j.Signature, j.Secret)
		if err == nil && !valid {
			return VerdictRevoked, "provider auth-check rejected the credential: " + info
		}
	}
	return VerdictLikelyReal, "passed placeholder, format, and revocation checks"
}
