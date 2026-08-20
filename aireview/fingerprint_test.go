package aireview

import "testing"

func TestFingerprint(t *testing.T) {
	a := Fingerprint("sk-abc123")
	b := Fingerprint("sk-abc123")
	if a != b {
		t.Fatalf("same secret gave different fingerprints: %q vs %q", a, b)
	}
	c := Fingerprint("sk-abc124")
	if a == c {
		t.Fatalf("different secrets gave the same fingerprint %q", a)
	}
	if len(a) != 64 {
		t.Fatalf("fingerprint length = %d, want 64", len(a))
	}
}
