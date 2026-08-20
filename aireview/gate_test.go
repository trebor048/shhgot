package aireview

import (
	"errors"
	"regexp"
	"testing"
)

type fakeVerifier struct {
	valid bool
	info  string
	err   error
}

func (f fakeVerifier) Check(sigName, secret string) (bool, string, error) {
	return f.valid, f.info, f.err
}

func newTestGate(v Verifier) *Gate {
	sig := Sig{Name: "Google API Key", Re: regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`)}
	return NewGate(
		[]string{"changeme", "your_", "example.com", "abandon abandon"},
		[]string{"AIzaSyAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		[]Sig{sig},
		v,
	)
}

func TestGatePlaceholder(t *testing.T) {
	g := newTestGate(nil)
	j := &Job{Secret: "AIzaSy123", MatchContext: "key = changeme123", Signature: "Google API Key"}
	if v, _ := g.Evaluate(j); v != VerdictPlaceholder {
		t.Fatalf("verdict = %q, want placeholder", v)
	}
}

func TestGateTestFixture(t *testing.T) {
	g := newTestGate(nil)
	j := &Job{Secret: "AIzaSyAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", MatchContext: "", Signature: "Google API Key"}
	if v, _ := g.Evaluate(j); v != VerdictTestFixture {
		t.Fatalf("verdict = %q, want test-fixture", v)
	}
}

func TestGateMalformed(t *testing.T) {
	g := newTestGate(nil)
	j := &Job{Secret: "AIza1", MatchContext: "", Signature: "Google API Key"}
	if v, _ := g.Evaluate(j); v != VerdictMalformed {
		t.Fatalf("verdict = %q, want malformed", v)
	}
}

func TestGateRevoked(t *testing.T) {
	g := newTestGate(fakeVerifier{valid: false, info: "401 unauthorized"})
	j := &Job{Secret: "AIzaSy00000000000000000000000000000000000", MatchContext: "", Signature: "Google API Key"}
	if v, _ := g.Evaluate(j); v != VerdictRevoked {
		t.Fatalf("verdict = %q, want revoked", v)
	}
}

func TestGateInconclusiveVerifier(t *testing.T) {
	g := newTestGate(fakeVerifier{err: errors.New("network down")})
	j := &Job{Secret: "AIzaSy00000000000000000000000000000000000", MatchContext: "", Signature: "Google API Key"}
	if v, _ := g.Evaluate(j); v != VerdictLikelyReal {
		t.Fatalf("verdict = %q, want likely-real (inconclusive)", v)
	}
}

func TestGateLikelyReal(t *testing.T) {
	g := newTestGate(fakeVerifier{valid: true, info: "ok"})
	j := &Job{Secret: "AIzaSy00000000000000000000000000000000000", MatchContext: "", Signature: "Google API Key"}
	if v, _ := g.Evaluate(j); v != VerdictLikelyReal {
		t.Fatalf("verdict = %q, want likely-real", v)
	}
}
