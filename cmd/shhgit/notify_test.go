package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trebor048/shhgot/core"
	"github.com/trebor048/shhgot/core/notify"
	"github.com/trebor048/shhgot/core/store"
)

// resetNotifyForTest clears the process-wide notification state and restores it
// on cleanup, so a test can never leak a dispatcher into another test's
// publish().
func resetNotifyForTest(t *testing.T) {
	t.Helper()
	notifyMu.Lock()
	prevD, prevNames, prevDeg := notifyDispatcher, notifyChannels, notifyDegraded
	notifyDispatcher, notifyChannels, notifyDegraded = nil, nil, false
	notifyMu.Unlock()
	t.Cleanup(func() {
		notifyMu.Lock()
		notifyDispatcher, notifyChannels, notifyDegraded = prevD, prevNames, prevDeg
		notifyMu.Unlock()
	})
}

// A published finding is delivered to a configured notification channel in
// addition to the dashboard/webhook path.
func TestPublishDeliversToNotifyChannel(t *testing.T) {
	resetNotifyForTest(t)
	hub := ensureWebHub()
	resetHubForTest(hub)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	newSearchTestSession(t, false)
	session.Config.Notify = core.NotifyConfig{Channels: []core.ChannelConfig{{
		Type: "slack",
		URL:  srv.URL,
	}}}
	initNotifications()

	publish(testMatchEvent())

	waitForCond(t, 2*time.Second, func() bool { return atomic.LoadInt32(&hits) == 1 })
	if n := hubMatchCount(t); n != 1 {
		t.Fatalf("hub holds %d matches, want 1", n)
	}
}

// Test delivery is opt-in: it stays silent unless notify.test_on_start is true.
func TestNotifyTestDeliveryOffByDefault(t *testing.T) {
	resetNotifyForTest(t)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	newSearchTestSession(t, false)
	session.Config.Notify = core.NotifyConfig{Channels: []core.ChannelConfig{{
		Type: "slack",
		URL:  srv.URL,
	}}}
	d, _, ok := session.Config.NotifySetup()
	if !ok || d == nil {
		t.Fatal("NotifySetup returned no dispatcher")
	}
	d.SetDoer(notify.HTTPDoer{Client: srv.Client()})
	notifyMu.Lock()
	notifyDispatcher = d
	notifyMu.Unlock()

	maybeTestNotifications()
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("test delivery ran by default: %d requests", n)
	}

	session.Config.Notify.TestOnStart = searchBoolPtr(true)
	maybeTestNotifications()
	waitForCond(t, 2*time.Second, func() bool { return atomic.LoadInt32(&hits) == 1 })
}

// The digest is a no-op, with one logged note, when findings storage is off.
func TestDailyDigestNoOpsWithoutStorage(t *testing.T) {
	s := newStoreTestSession(t, false)
	s.Log.SetDebug(true)

	var lines []string
	s.Log.LogWriter = func(line string) { lines = append(lines, line) }

	if startDailyDigest() {
		t.Fatal("digest must not start without findings storage")
	}
	if !strings.Contains(strings.Join(lines, ""), "disabled") {
		t.Fatalf("digest logged no note: %q", strings.Join(lines, ""))
	}
}

// The daily digest is delivered to every configured channel, one event per
// non-empty severity bucket.
func TestDailyDigestDeliversToChannel(t *testing.T) {
	resetNotifyForTest(t)
	s := newStoreTestSession(t, true)

	for _, rec := range []store.Record{
		{Signature: "AWS Access Key", Severity: "critical"},
		{Signature: "AWS Access Key", Severity: "high"},
		{Signature: "AWS Access Key", Severity: "high"},
	} {
		if err := s.Store.Append(rec); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	s.Config.Notify = core.NotifyConfig{Channels: []core.ChannelConfig{{
		Type: "slack",
		URL:  srv.URL,
	}}}
	d, _, ok := s.Config.NotifySetup()
	if !ok || d == nil {
		t.Fatal("NotifySetup returned no dispatcher")
	}
	d.SetDoer(notify.HTTPDoer{Client: srv.Client()})
	notifyMu.Lock()
	notifyDispatcher = d
	notifyMu.Unlock()

	logDigest(s.Store)

	// Two occupied buckets (critical, high) => two deliveries.
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Fatalf("digest delivered %d channel messages, want 2", n)
	}
}

// /health exposes the configured channel names and the degraded flag.
func TestHealthReportsNotifyChannels(t *testing.T) {
	resetNotifyForTest(t)
	newSearchTestSession(t, false)
	session.Config.Notify = core.NotifyConfig{Channels: []core.ChannelConfig{{
		Type: "slack",
		URL:  "https://hooks.slack.example/T/B",
	}}}
	initNotifications()

	rec := httptest.NewRecorder()
	health(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /health: %v", err)
	}
	obj, ok := body["notify"].(map[string]any)
	if !ok {
		t.Fatalf("/health missing notify object: %v", body)
	}
	if obj["enabled"] != true {
		t.Fatalf("notify.enabled = %v, want true", obj["enabled"])
	}
	channels, _ := obj["channels"].([]any)
	if len(channels) != 1 || channels[0] != "slack" {
		t.Fatalf("notify.channels = %v, want [slack]", obj["channels"])
	}
}
