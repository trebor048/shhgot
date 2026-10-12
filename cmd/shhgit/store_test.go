package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/trebor048/shhgot/core"
	"github.com/trebor048/shhgot/core/store"
)

// --- Slice 2 (findings store) wiring tests -----------------------------------

func storeBoolFlag(b bool) *bool       { return &b }
func storeStringFlag(s string) *string { return &s }

func testMatchEvent() *MatchEvent {
	return &MatchEvent{
		Url:        "https://github.com/owner/repo",
		Matches:    []string{"sk-" + strings.Repeat("a", 48)},
		Signature:  "OpenAI API Key",
		File:       "config/creds.env",
		Stars:      12,
		Source:     core.GITHUB_SOURCE,
		Priority:   3,
		Color:      "#10A37F",
		Secret:     "sk-" + strings.Repeat("a", 48),
		SecretLine: 4,
	}
}

// newStoreTestSession builds a session with a real (temp-file) findings store and
// installs it as the package singleton for the duration of the test.
func newStoreTestSession(t *testing.T, enabled bool) *core.Session {
	t.Helper()
	var (
		st  store.Store
		err error
	)
	if enabled {
		st, err = store.New(store.Config{
			Enabled:      true,
			Path:         filepath.Join(t.TempDir(), "findings.jsonl"),
			DedupeTTL:    time.Hour,
			CompactAfter: 1000,
		})
	} else {
		st, err = store.New(store.Config{})
	}
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	s := &core.Session{
		Log: &core.Logger{},
		Options: &core.Options{
			Live:     storeStringFlag(""),
			NoDedupe: storeBoolFlag(false),
			NoResume: storeBoolFlag(false),
		},
		Config: &core.Config{},
		Store:  st,
	}
	if enabled {
		s.Config.Storage.Enabled = storeBoolFlag(true)
	}

	prev := session
	session = s
	t.Cleanup(func() { session = prev })
	return s
}

func hubMatchCount(t *testing.T) int {
	t.Helper()
	hub := ensureWebHub()
	hub.matchesMutex.RLock()
	defer hub.matchesMutex.RUnlock()
	return len(hub.matches)
}

// A finding recorded by a previous run must not be delivered again: the second
// publish of the same finding adds no new hub entry.
func TestPublishDedupesConfirmedFinding(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	s := newStoreTestSession(t, true)

	ev := testMatchEvent()
	publish(ev)
	publish(ev)

	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 1 })
	recs, err := s.Store.List(store.Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("store holds %d records, want 1", len(recs))
	}
}

// An unconfirmed delivery must NOT be recorded, so the next run re-alerts it.
func TestPublishRepublishesUnconfirmedDelivery(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)

	prevDeliver := deliverWebhook
	deliverWebhook = func(_ *MatchEvent, onConfirm func(bool)) { onConfirm(false) }
	t.Cleanup(func() { deliverWebhook = prevDeliver })

	s := newStoreTestSession(t, true)
	s.Config.Webhook = "https://example.test/hook"

	ev := testMatchEvent()
	publish(ev)
	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 1 })

	if recs, _ := s.Store.List(store.Filter{}); len(recs) != 0 {
		t.Fatalf("unconfirmed delivery was persisted (%d records)", len(recs))
	}

	// Next run: the same finding is considered new again.
	publish(ev)
	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 2 })
}

// A finding whose webhook delivery is confirmed must be recorded and then
// suppressed on the next run. This differs from the enqueue-confirmation the
// old code assumed: only the queue's terminal outcome may write the fingerprint.
func TestPublishPersistsConfirmedWebhookDelivery(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)

	prevDeliver := deliverWebhook
	deliverWebhook = func(_ *MatchEvent, onConfirm func(bool)) { onConfirm(true) }
	t.Cleanup(func() { deliverWebhook = prevDeliver })

	s := newStoreTestSession(t, true)
	s.Config.Webhook = "https://example.test/hook"

	ev := testMatchEvent()
	publish(ev)
	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 1 })

	if recs, _ := s.Store.List(store.Filter{}); len(recs) != 1 {
		t.Fatalf("confirmed delivery wrote %d records, want 1", len(recs))
	}

	// The stored fingerprint now suppresses the repeat.
	publish(ev)
	time.Sleep(50 * time.Millisecond)
	if got := hubMatchCount(t); got != 1 {
		t.Fatalf("confirmed finding was re-published: %d hub matches, want 1", got)
	}
}

// With storage disabled there is no dedupe at all: a repeated finding is
// delivered every time, exactly as before this slice.
func TestPublishWithoutStoreDoesNotDedupe(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	newStoreTestSession(t, false)

	ev := testMatchEvent()
	publish(ev)
	publish(ev)

	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 2 })
}

// --no-dedupe bypasses a stored fingerprint for the current run.
func TestPublishNoDedupeFlagBypassesStore(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	s := newStoreTestSession(t, true)
	s.Options.NoDedupe = storeBoolFlag(true)

	ev := testMatchEvent()
	publish(ev)
	publish(ev)

	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 2 })
}

// TestPublishNoDedupeStillPersists is a regression test for the dedupe/store
// gating bug: --no-dedupe turned the single `dedupe` bool off, which also
// disabled persistence, so a --no-dedupe run exported nothing. It must only
// skip the Seen read; findings are still appended (both duplicates delivered).
func TestPublishNoDedupeStillPersists(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	s := newStoreTestSession(t, true)
	s.Options.NoDedupe = storeBoolFlag(true)

	ev := testMatchEvent()
	publish(ev)
	publish(ev)

	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 2 })

	var out bytes.Buffer
	if err := exportFindings(s.Store, "jsonl", &out); err != nil {
		t.Fatalf("export: %v", err)
	}
	if strings.TrimSpace(out.String()) == "" {
		t.Fatal("--no-dedupe run exported no findings; findings must still be appended")
	}
	recs, err := s.Store.List(store.Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("store holds %d records, want 2 (both duplicates appended)", len(recs))
	}
}

// Export/decrypt round trip: JSONL and CSV render, and an encrypted export read
// back with the right passphrase reproduces each record field-for-field while a
// wrong passphrase fails without emitting anything.
func TestExportDecryptRoundTrip(t *testing.T) {
	s := newStoreTestSession(t, true)

	rec := store.Record{
		ID:          "id-1",
		Fingerprint: "fp-1",
		Timestamp:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Source:      "github",
		URL:         "https://github.com/o/r",
		File:        "a/b.go",
		Signature:   "AWS Access Key",
		Matches:     []string{"AKIA...", "secret"},
		Secret:      "AKIA...",
		Line:        7,
		Stars:       11,
		Priority:    2,
	}
	if err := s.Store.Append(rec); err != nil {
		t.Fatalf("append: %v", err)
	}

	var jsonl bytes.Buffer
	if err := exportFindings(s.Store, "jsonl", &jsonl); err != nil {
		t.Fatalf("jsonl export: %v", err)
	}
	var gotJSONL store.Record
	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonl.String())), &gotJSONL); err != nil {
		t.Fatalf("jsonl decode: %v", err)
	}
	if !reflect.DeepEqual(gotJSONL, rec) {
		t.Fatalf("jsonl round trip mismatch:\n got %#v\nwant %#v", gotJSONL, rec)
	}

	var csvBuf bytes.Buffer
	if err := exportFindings(s.Store, "csv", &csvBuf); err != nil {
		t.Fatalf("csv export: %v", err)
	}
	if !strings.Contains(csvBuf.String(), "fingerprint") || !strings.Contains(csvBuf.String(), rec.Fingerprint) {
		t.Fatalf("csv export missing header/record: %q", csvBuf.String())
	}

	if err := exportFindings(s.Store, "xml", &bytes.Buffer{}); err == nil {
		t.Fatal("unknown export format must be rejected")
	}

	encPath := filepath.Join(t.TempDir(), "findings.enc")
	if err := exportFindingsEncrypted(s.Store, encPath, "correct horse"); err != nil {
		t.Fatalf("encrypted export: %v", err)
	}
	var out bytes.Buffer
	if err := decryptFindings(encPath, "correct horse", &out); err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	var gotDec store.Record
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &gotDec); err != nil {
		t.Fatalf("decrypted decode: %v", err)
	}
	if !reflect.DeepEqual(gotDec, rec) {
		t.Fatalf("decrypted record mismatch:\n got %#v\nwant %#v", gotDec, rec)
	}

	var wrong bytes.Buffer
	if err := decryptFindings(encPath, "wrong passphrase", &wrong); err == nil {
		t.Fatal("wrong passphrase must fail")
	}
	if wrong.Len() != 0 {
		t.Fatalf("wrong passphrase emitted %d bytes", wrong.Len())
	}
}

// --stats renders the store totals: record count, severity breakdown and the
// most frequent signatures/providers, ordered by count then name.
func TestFormatStoreStats(t *testing.T) {
	recs := []store.Record{
		{Signature: "AWS Access Key", Severity: "critical", Provider: "aws"},
		{Signature: "AWS Access Key", Severity: "high", Provider: "aws"},
		{Signature: "Slack Token", Severity: "medium", Provider: "slack"},
		{Signature: "", Severity: ""},
	}
	out := formatStoreStats(recs)
	for _, want := range []string{
		"records:      4",
		"critical 1, high 1, medium 1, low 0, unclassified 1",
		"AWS Access Key (2)",
		"aws (2)",
		"Slack Token (1)",
		"slack (1)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("--stats output missing %q:\n%s", want, out)
		}
	}
	if empty := formatStoreStats(nil); !strings.Contains(empty, "(none)") {
		t.Fatalf("empty --stats output must report none:\n%s", empty)
	}
}

// --stats is a one-shot command: the scanner must not start network sources for
// it (and initPublishInfrastructure must skip it).
func TestStatsIsOneShotCommand(t *testing.T) {
	s := newStoreTestSession(t, true)
	s.Options.Stats = storeBoolFlag(true)
	if !oneShotCommand() {
		t.Fatal("--stats must be a one-shot command")
	}
}

// handleStoreCLI consumes --stats. Web mode keeps exitScanner from exiting the
// test process, so the no-storage no-op path can be exercised.
func TestHandleStoreCLIStatsNoStorage(t *testing.T) {
	s := newStoreTestSession(t, false)
	s.Options.Stats = storeBoolFlag(true)

	prev := modeConfig
	modeConfig = &ModeConfig{Mode: ModeWeb}
	t.Cleanup(func() { modeConfig = prev })

	if !handleStoreCLI() {
		t.Fatal("--stats must be handled by handleStoreCLI")
	}
}

// /health must surface a degraded store so a probe can observe it.
func TestHealthReportsStorageDegraded(t *testing.T) {
	rec := httptest.NewRecorder()
	health(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /health: %v", err)
	}
	if _, ok := body["storage_degraded"]; !ok {
		t.Fatalf("/health missing storage_degraded: %v", body)
	}
	if body["storage_degraded"] != false {
		t.Fatalf("storage_degraded = %v, want false (no degraded store)", body["storage_degraded"])
	}
}
