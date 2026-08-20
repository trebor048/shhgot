package aireview

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *JobStore {
	t.Helper()
	s, err := NewJobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestJobStoreCreateGet(t *testing.T) {
	s := newTestStore(t)
	j := &Job{ID: "j1", RepoURL: "https://github.com/a/b", Secret: "s", State: StateQueued}
	if err := s.Create(j); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("j1")
	if err != nil {
		t.Fatal(err)
	}
	if got.RepoURL != j.RepoURL || got.State != StateQueued {
		t.Fatalf("got %+v", got)
	}
	if err := s.Create(&Job{ID: "j1"}); err == nil {
		t.Fatal("expected duplicate create to fail")
	}
	if _, err := s.Get("nope"); !os.IsNotExist(err) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
}

func TestJobStoreUpdate(t *testing.T) {
	s := newTestStore(t)
	j := &Job{ID: "j2", State: StateQueued}
	if err := s.Create(j); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	j.State = StateRunning
	if err := s.Update(j); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("j2")
	if got.State != StateRunning {
		t.Fatalf("state = %s", got.State)
	}
	if !got.UpdatedAt.After(j.CreatedAt) {
		t.Fatalf("UpdatedAt %v not after CreatedAt %v", got.UpdatedAt, j.CreatedAt)
	}
}

func TestJobStoreListOrder(t *testing.T) {
	s := newTestStore(t)
	a := &Job{ID: "a", CreatedAt: time.Now().Add(-time.Hour)}
	b := &Job{ID: "b", CreatedAt: time.Now()}
	if err := s.Create(a); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(b); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "b" || list[1].ID != "a" {
		t.Fatalf("order wrong: %+v", list)
	}
}

func TestJobStoreLogs(t *testing.T) {
	s := newTestStore(t)
	if err := s.Create(&Job{ID: "j3"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendLog("j3", "line one"); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendLog("j3", "line two"); err != nil {
		t.Fatal(err)
	}
	tail, err := s.ReadLogTail("j3", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tail, "line two") || strings.Contains(tail, "line one") {
		t.Fatalf("tail wrong: %q", tail)
	}
	if _, err := os.Stat(filepath.Join(s.jobDir("j3"), "logs.txt")); err != nil {
		t.Fatal(err)
	}
}
