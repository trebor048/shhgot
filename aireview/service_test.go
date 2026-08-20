package aireview

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestService(t *testing.T, gate *Gate) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	if gate == nil {
		gate = NewGate([]string{"changeme"}, nil, nil, nil)
	}
	svc, err := NewService(root, "", "", fakeClone, gate)
	if err != nil {
		t.Fatal(err)
	}
	return svc, root
}

func TestServiceFlagCreatesJob(t *testing.T) {
	svc, _ := newTestService(t, nil)
	j, err := svc.Flag("https://github.com/o/r", "a.env", "Generic Key", "sk-abc", "KEY=sk-abc", 3, true)
	if err != nil {
		t.Fatal(err)
	}
	if j.State != StateQueued || j.CaseID == "" {
		t.Fatalf("job = %+v", j)
	}
	if j.SecretFingerprint != Fingerprint("sk-abc") {
		t.Fatalf("fingerprint mismatch")
	}
	if _, err := svc.Cases.Get(j.SecretFingerprint); err != nil {
		t.Fatalf("case not created: %v", err)
	}
}

func TestServiceFlagDudCaseShortCircuits(t *testing.T) {
	svc, root := newTestService(t, nil)
	// create a case with a definitive verdict first
	fp := Fingerprint("sk-dud")
	if err := os.MkdirAll(filepath.Join(root, "cases", fp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := svc.Cases.Create(&Case{Fingerprint: fp, Verdict: VerdictRevoked, FirstSeen: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	j, err := svc.Flag("https://github.com/o/r", "a.env", "Generic Key", "sk-dud", "", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if j.State != StateDud || !j.ReviewNeeded || j.Verdict != VerdictRevoked {
		t.Fatalf("expected cached dud short-circuit, got %+v", j)
	}
}

func TestServiceStartGateDud(t *testing.T) {
	svc, _ := newTestService(t, nil)
	j, err := svc.Flag("https://github.com/o/r", "a.env", "Generic Key", "changeme123", "", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(j.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Jobs.Get(j.ID)
	if got.State != StateDud || got.Verdict != VerdictPlaceholder || !got.ReviewNeeded {
		t.Fatalf("gate should short-circuit to dud, got %+v", got)
	}
	// no clone happened
	if got.ClonePath != "" {
		t.Fatalf("dud job must not clone, got %q", got.ClonePath)
	}
}

func TestServiceStartClones(t *testing.T) {
	svc, root := newTestService(t, nil)
	j, err := svc.Flag("https://github.com/o/r2", "a.env", "Generic Key", "sk-real-123", "", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(j.ID); err != nil {
		t.Fatal(err)
	}
	// wait for the background clone
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, _ := svc.Jobs.Get(j.ID)
		if got.ClonePath != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("clone did not complete")
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, _ := svc.Jobs.Get(j.ID)
	if got.State != StateRunning || got.Verdict != VerdictLikelyReal {
		t.Fatalf("job = %+v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "clones", "o-r2", ".cloned")); err != nil {
		t.Fatal(err)
	}
}

func TestServicePauseResumeStop(t *testing.T) {
	svc, _ := newTestService(t, nil)
	j, _ := svc.Flag("https://github.com/o/r", "a.env", "Generic Key", "sk-x", "", 0, false)
	if err := svc.Start(j.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Pause(j.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Jobs.Get(j.ID)
	if got.State != StatePaused {
		t.Fatalf("state = %s", got.State)
	}
	if err := svc.Resume(j.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Stop(j.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = svc.Jobs.Get(j.ID)
	if got.State != StateCancelled {
		t.Fatalf("state = %s", got.State)
	}
	if err := svc.Stop(j.ID); err == nil {
		t.Fatal("stopping a cancelled job must fail")
	}
}

func TestServiceArchive(t *testing.T) {
	svc, root := newTestService(t, nil)
	j, _ := svc.Flag("https://github.com/o/r", "a.env", "Generic Key", "sk-a", "", 0, false)
	zipPath, err := svc.Archive(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(zipPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "archive", j.ID+".zip")); err != nil {
		t.Fatal(err)
	}
}

func TestServiceEvents(t *testing.T) {
	svc, _ := newTestService(t, nil)
	var mu sync.Mutex
	seen := 0
	svc.SetOnEvent(func(j *Job) { mu.Lock(); seen++; mu.Unlock() })
	j, _ := svc.Flag("https://github.com/o/r", "a.env", "Generic Key", "changeme1", "", 0, false)
	_ = svc.Start(j.ID)
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if seen == 0 {
		t.Fatal("expected at least one event")
	}
}
