package aireview

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestWatcherNotifiesOnWorkflowTerminal(t *testing.T) {
	svc, _ := newTestService(t, nil)
	svc.StartWatcher(20 * time.Millisecond)
	defer svc.watchStop()
	j, _ := svc.Flag("https://github.com/o/r", "a.env", "Generic Key", "sk-w", "", 0, false)
	// simulate the DSH workflow finishing the job (direct file write)
	j.State = StateDone
	j.Stage = 5
	j.StageName = "Report synthesis"
	if err := svc.Jobs.Update(j); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		list, _ := svc.Notify.List()
		found := false
		for _, n := range list {
			if n.JobID == j.ID && n.Kind == NotifyCompleted {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("watcher never emitted completed notification")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWatcherWebhookFired(t *testing.T) {
	var mu sync.Mutex
	posted := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		posted++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	root := t.TempDir()
	gate := NewGate([]string{"changeme"}, nil, nil, nil)
	svc, err := NewService(root, srv.URL, "http://localhost:8080", fakeClone, gate)
	if err != nil {
		t.Fatal(err)
	}
	svc.StartWatcher(20 * time.Millisecond)
	defer svc.watchStop()
	j, _ := svc.Flag("https://github.com/o/r", "a.env", "Generic Key", "sk-w2", "", 0, false)
	j.State = StateDone
	if err := svc.Jobs.Update(j); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := posted
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("webhook never fired")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWatcherNoDuplicateNotifications(t *testing.T) {
	svc, _ := newTestService(t, nil)
	svc.StartWatcher(10 * time.Millisecond)
	defer svc.watchStop()
	j, _ := svc.Flag("https://github.com/o/r", "a.env", "Generic Key", "sk-w3", "", 0, false)
	j.State = StateDone
	if err := svc.Jobs.Update(j); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		list, _ := svc.Notify.List()
		n := 0
		for _, x := range list {
			if x.JobID == j.ID && x.Kind == NotifyCompleted {
				n++
			}
		}
		return n
	}
	// wait until the first notification lands
	deadline := time.Now().Add(3 * time.Second)
	for count() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("watcher never emitted completed notification")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// let the watcher tick several more times: the emitted guard must
	// suppress any duplicate notifications for the same job/state
	time.Sleep(100 * time.Millisecond)
	if n := count(); n != 1 {
		t.Fatalf("got %d completed notifications, want exactly 1 (no duplicates)", n)
	}
}
