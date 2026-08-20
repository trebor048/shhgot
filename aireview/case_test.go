package aireview

import (
	"os"
	"testing"
	"time"
)

func TestCaseStore(t *testing.T) {
	cs, err := NewCaseStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fp := Fingerprint("sk-dup")
	if _, err := cs.Get(fp); !os.IsNotExist(err) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
	c := &Case{Fingerprint: fp, FirstSeen: time.Now().UTC()}
	if err := cs.Create(c); err != nil {
		t.Fatal(err)
	}
	if err := cs.AddJob(fp, "j1"); err != nil {
		t.Fatal(err)
	}
	if err := cs.AddJob(fp, "j1"); err != nil {
		t.Fatal(err) // idempotent
	}
	if err := cs.AddJob(fp, "j2"); err != nil {
		t.Fatal(err)
	}
	got, err := cs.Get(fp)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Jobs) != 2 {
		t.Fatalf("jobs = %v, want [j1 j2]", got.Jobs)
	}
	got.Verdict = VerdictRevoked
	if err := cs.Update(got); err != nil {
		t.Fatal(err)
	}
	again, _ := cs.Get(fp)
	if again.Verdict != VerdictRevoked {
		t.Fatalf("verdict = %q", again.Verdict)
	}
}

func TestCaseFresh(t *testing.T) {
	now := time.Now()
	c := &Case{Verification: &Verification{CheckedAt: now.Add(-time.Hour)}}
	if !c.Fresh(24*time.Hour, now) {
		t.Fatal("1h old verification should be fresh within 24h TTL")
	}
	if c.Fresh(time.Minute, now) {
		t.Fatal("1h old verification should be stale within 1m TTL")
	}
	if (&Case{}).Fresh(24*time.Hour, now) {
		t.Fatal("nil verification must not be fresh")
	}
}
