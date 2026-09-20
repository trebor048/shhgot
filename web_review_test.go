package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trebor048/shhgot/aiproviders"
	"github.com/trebor048/shhgot/core"
	"github.com/trebor048/shhgot/reviewstore"
)

// fakeClient is an aiproviders.Client that never touches the network, so the
// review engine can be tested end to end.
type fakeClient struct {
	chunks []string
	err    error
	got    []aiproviders.Message
	calls  int
}

func (f *fakeClient) Name() string { return "fake" }

func (f *fakeClient) Chat(_ context.Context, m []aiproviders.Message) (string, error) {
	f.calls++
	f.got = m
	if f.err != nil {
		return "", f.err
	}
	return strings.Join(f.chunks, ""), nil
}

func (f *fakeClient) Stream(_ context.Context, m []aiproviders.Message, onDelta func(string) error) error {
	f.calls++
	f.got = m
	for _, c := range f.chunks {
		if onDelta != nil {
			if err := onDelta(c); err != nil {
				return err
			}
		}
	}
	return f.err
}

// withReviewEnv installs fresh stores for the duration of one test.
func withReviewEnv(t *testing.T) {
	t.Helper()
	dir := t.TempDir()

	st, err := reviewstore.NewStore(filepath.Join(dir, "reviews"))
	if err != nil {
		t.Fatalf("review store: %v", err)
	}
	reviewStore = st

	settings := aiproviders.NewStore(filepath.Join(dir, "settings.json"))
	// Point the provider at a port that is always closed, so any review a test
	// starts fails at once instead of waiting on a real Ollama that may or may
	// not be running on this machine.
	if err := settings.Set(aiproviders.Settings{
		Provider: "ollama",
		Model:    "test-model",
		BaseURL:  "http://127.0.0.1:1",
	}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	aiSettingsStore = settings

	reviewJobsMu.Lock()
	reviewJobs = map[string]*reviewJob{}
	reviewJobsMu.Unlock()

	webBindIsLoopback = true

	t.Cleanup(func() {
		// Let any review still running finish before the stores disappear: it
		// writes its result back through the package-level store.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			reviewJobsMu.Lock()
			running := len(reviewJobs)
			reviewJobsMu.Unlock()
			if running == 0 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}

		reviewStore = nil
		aiSettingsStore = nil
		reviewJobsMu.Lock()
		reviewJobs = map[string]*reviewJob{}
		reviewJobsMu.Unlock()
	})
}

// localRequest builds a request that satisfies localGuard (loopback Host).
func localRequest(method, target string, body []byte) *http.Request {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, target, bytes.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r.Host = "127.0.0.1:8080"
	return r
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", msg)
}

// --- engine ------------------------------------------------------------------

func TestStartReviewStreamsPersistsAndLabels(t *testing.T) {
	withReviewEnv(t)

	rev := &reviewstore.Review{
		Signature: "AWS Access Key ID",
		Secret:    "AKIAIOSFODNN7EXAMPLE",
		File:      "deploy/terraform.tfvars",
		Repo:      "acme/infra",
		Context:   "aws_access_key_id = AKIAIOSFODNN7EXAMPLE",
		Status:    reviewstore.StatusQueued,
	}
	if err := reviewStore.Create(rev); err != nil {
		t.Fatalf("create: %v", err)
	}

	fake := &fakeClient{chunks: []string{"## What it is\n", "An AWS access key id."}}
	startReview(rev, fake)

	waitFor(t, 3*time.Second, func() bool {
		got, ok := reviewStore.Get(rev.ID)
		return ok && got.Status == reviewstore.StatusDone
	}, "the review to finish")

	got, _ := reviewStore.Get(rev.ID)
	if got.Assessment != "## What it is\nAn AWS access key id." {
		t.Fatalf("assessment = %q", got.Assessment)
	}
	if got.Error != "" {
		t.Fatalf("unexpected error: %q", got.Error)
	}
	if fake.calls != 1 {
		t.Fatalf("provider called %d times, want 1", fake.calls)
	}

	// The prompt must carry the evidence the model needs, and must instruct the
	// model not to echo the credential back.
	if len(fake.got) < 2 {
		t.Fatalf("expected a system and a user message, got %d", len(fake.got))
	}
	user := fake.got[len(fake.got)-1].Content
	for _, want := range []string{"acme/infra", "deploy/terraform.tfvars", "AWS Access Key ID", "AKIAIOSFODNN7EXAMPLE"} {
		if !strings.Contains(user, want) {
			t.Errorf("prompt is missing %q", want)
		}
	}
	if !strings.Contains(fake.got[0].Content, "Never repeat the credential") {
		t.Error("system prompt should tell the model not to echo the secret")
	}
}

func TestStartReviewFailureIsPersisted(t *testing.T) {
	withReviewEnv(t)

	rev := &reviewstore.Review{Signature: "Slack Token", Secret: "xoxb-x", Status: reviewstore.StatusQueued}
	if err := reviewStore.Create(rev); err != nil {
		t.Fatalf("create: %v", err)
	}

	startReview(rev, &fakeClient{err: errors.New("provider exploded")})

	waitFor(t, 3*time.Second, func() bool {
		got, ok := reviewStore.Get(rev.ID)
		return ok && got.Status == reviewstore.StatusFailed
	}, "the review to fail")

	got, _ := reviewStore.Get(rev.ID)
	if !strings.Contains(got.Error, "provider exploded") {
		t.Fatalf("error = %q, want it to mention the provider failure", got.Error)
	}
	if lookupJob(rev.ID) != nil {
		t.Fatal("a finished job must be removed from the in-flight map")
	}
}

func TestReviewJobBrokerDeliversHistoryAndCloses(t *testing.T) {
	job := newReviewJob()

	history, ch, finished := job.subscribe()
	if history != "" || finished {
		t.Fatalf("fresh job: history=%q finished=%v", history, finished)
	}

	job.publish("part one ")
	job.publish("part two")

	for _, want := range []string{"part one ", "part two"} {
		select {
		case got := <-ch:
			if got != want {
				t.Fatalf("delta = %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for a delta")
		}
	}

	job.finish(reviewstore.StatusDone, "")

	// The channel must close so a streaming handler can terminate.
	select {
	case _, open := <-ch:
		if open {
			t.Fatal("expected the subscriber channel to be closed")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the channel to close")
	}

	// A late subscriber still gets everything streamed so far.
	history, late, finished := job.subscribe()
	if history != "part one part two" || !finished {
		t.Fatalf("late subscriber: history=%q finished=%v", history, finished)
	}
	select {
	case _, open := <-late:
		if open {
			t.Fatal("late subscriber channel should already be closed")
		}
	default:
		t.Fatal("late subscriber channel should be closed immediately")
	}
}

// --- review HTTP API ---------------------------------------------------------

func TestReviewAPIHappyPath(t *testing.T) {
	withReviewEnv(t)

	body, _ := json.Marshal(reviewRequest{
		Signature: "GitHub Token",
		Secret:    "ghp_example",
		File:      "config/secrets.rb",
		URL:       "https://github.com/acme/app/blob/main/config/secrets.rb",
	})

	rec := httptest.NewRecorder()
	reviewCollectionHandler(rec, localRequest(http.MethodPost, "/api/review", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body.String())
	}

	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ID == "" {
		t.Fatal("create response has no id")
	}

	// repo is derived from the URL when the client does not send one.
	got, ok := reviewStore.Get(created.ID)
	if !ok {
		t.Fatal("review was not stored")
	}
	if got.Repo != "acme/app" {
		t.Errorf("derived repo = %q, want acme/app", got.Repo)
	}
	if got.Provider != "ollama" || got.Model != "test-model" {
		t.Errorf("provider/model = %q/%q, want the configured provider", got.Provider, got.Model)
	}

	// List.
	rec = httptest.NewRecorder()
	reviewCollectionHandler(rec, localRequest(http.MethodGet, "/api/review", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), created.ID) {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Detail.
	rec = httptest.NewRecorder()
	reviewItemHandler(rec, localRequest(http.MethodGet, "/api/review/"+created.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status = %d", rec.Code)
	}
	var detail reviewView
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.ID != created.ID || detail.Signature != "GitHub Token" {
		t.Fatalf("detail = %+v", detail)
	}

	// Delete.
	rec = httptest.NewRecorder()
	reviewItemHandler(rec, localRequest(http.MethodDelete, "/api/review/"+created.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d", rec.Code)
	}
	if _, ok := reviewStore.Get(created.ID); ok {
		t.Fatal("review survived deletion")
	}
}

// The dashboard polls the list every few seconds and renders only metadata from
// it, so the response must not carry the credential or the assessment text: that
// would put live secrets on a hot path for no reason. The detail endpoint still
// has to carry both, or the detail pane would have nothing to show.
func TestReviewListOmitsCredentialAndAssessment(t *testing.T) {
	withReviewEnv(t)

	rev := &reviewstore.Review{
		Signature:  "Stripe Live Secret Key",
		Secret:     "sk_live_leakedCredentialValue",
		File:       "billing/stripe.go",
		Repo:       "acme/app",
		Status:     reviewstore.StatusQueued,
		Provider:   "ollama",
		Model:      "test-model",
		Context:    "stripe.Key = sk_live_leakedCredentialValue",
		Assessment: "## What it is\nA live Stripe secret key.",
	}
	if err := reviewStore.Create(rev); err != nil {
		t.Fatalf("create: %v", err)
	}

	rec := httptest.NewRecorder()
	reviewCollectionHandler(rec, localRequest(http.MethodGet, "/api/review", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "sk_live_leakedCredentialValue") {
		t.Error("the list response leaked the plaintext credential")
	}
	if strings.Contains(body, "live Stripe secret key") {
		t.Error("the list response carried the assessment text")
	}
	// ...while still carrying everything the list rows actually render.
	for _, want := range []string{"id", "status", "signature", "created_at", "file", "repo", "provider", "model"} {
		if !strings.Contains(body, `"`+want+`"`) {
			t.Errorf("the list response is missing %q, which the list rows render", want)
		}
	}

	// The detail view must still carry both, or the detail pane breaks.
	rec = httptest.NewRecorder()
	reviewItemHandler(rec, localRequest(http.MethodGet, "/api/review/"+rev.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status = %d", rec.Code)
	}
	detail := rec.Body.String()
	if !strings.Contains(detail, "sk_live_leakedCredentialValue") {
		t.Error("the detail response dropped the credential the detail pane displays")
	}
	if !strings.Contains(detail, "live Stripe secret key") {
		t.Error("the detail response dropped the assessment the detail pane displays")
	}
}

// The file viewer and the AI review both key off the same identifier: the
// scanner stores a captured file body under the match id, /api/file is fetched
// with that id, and a review asks for the body with match_id. Nothing enforced
// that they agree, so a change to the id scheme on either side would silently
// starve every review of its file context. This pins them to one value.
func TestMatchIDIsSharedByFileViewerAndReview(t *testing.T) {
	withReviewEnv(t)

	const matchID = "shared-match-id-1"
	const body = "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	storeMatchFile(matchID, "https://github.com/acme/app", "deploy/.env", body, "wJalrXUtnFEMI", 1)

	// Half one: the id the dashboard uses to fetch a file body.
	rec := httptest.NewRecorder()
	getMatchFile(rec, localRequest(http.MethodGet, "/api/file?id="+matchID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/file status = %d, want the stored body", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), body) {
		t.Fatalf("/api/file did not return the stored body: %s", rec.Body.String())
	}

	// Half two: the very same id used to start a review must reach that body.
	req, err := json.Marshal(reviewRequest{
		MatchID:   matchID,
		Signature: "AWS Secret Access Key",
		Secret:    "wJalrXUtnFEMI",
		Context:   "scanner notes",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rec = httptest.NewRecorder()
	reviewCollectionHandler(rec, localRequest(http.MethodPost, "/api/review", req))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	rev, ok := reviewStore.Get(created.ID)
	if !ok {
		t.Fatal("review not stored")
	}
	if !strings.Contains(rev.Context, body) {
		t.Fatalf("the review did not receive the cached file body: %q", rev.Context)
	}
	// The browser's summary is kept, but clearly labelled as notes rather than
	// mistaken for file content.
	if !strings.Contains(rev.Context, "scanner notes") {
		t.Errorf("the scanner notes were dropped: %q", rev.Context)
	}
}

func TestReviewAPICreatesFromStoredMatchFile(t *testing.T) {
	withReviewEnv(t)

	// The scanner stores file content against the match id because clones are
	// deleted after scanning; the review must be able to pick it up.
	const matchID = "match-ctx-1"
	storeMatchFile(matchID, "https://github.com/acme/app", "app/.env", "OPENAI_API_KEY=sk-test-123\nDB_URL=postgres://x", "sk-test-123", 1)

	body, _ := json.Marshal(reviewRequest{MatchID: matchID, Signature: "OpenAI API Key", Secret: "sk-test-123"})
	rec := httptest.NewRecorder()
	reviewCollectionHandler(rec, localRequest(http.MethodPost, "/api/review", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	got, ok := reviewStore.Get(created.ID)
	if !ok {
		t.Fatal("review not stored")
	}
	if !strings.Contains(got.Context, "DB_URL=postgres://x") {
		t.Fatalf("context was not enriched from the stored match file: %q", got.Context)
	}
	if got.File != "app/.env" {
		t.Errorf("file = %q, want it filled from the cached match", got.File)
	}
}

func TestReviewAPIValidationAndRouting(t *testing.T) {
	withReviewEnv(t)

	// Missing both signature and secret.
	body, _ := json.Marshal(reviewRequest{File: "a.txt"})
	rec := httptest.NewRecorder()
	reviewCollectionHandler(rec, localRequest(http.MethodPost, "/api/review", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty review status = %d, want 400", rec.Code)
	}

	// Unknown id.
	rec = httptest.NewRecorder()
	reviewItemHandler(rec, localRequest(http.MethodGet, "/api/review/does-not-exist", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id status = %d, want 404", rec.Code)
	}

	// Unknown sub-resource.
	rec = httptest.NewRecorder()
	reviewItemHandler(rec, localRequest(http.MethodGet, "/api/review/abc/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown action status = %d, want 404", rec.Code)
	}

	// Deleting an unknown review must be a JSON 404, not a 500: the store
	// reports "missing" and "invalid id" identically, and both mean not-found.
	rec = httptest.NewRecorder()
	reviewItemHandler(rec, localRequest(http.MethodDelete, "/api/review/does-not-exist", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete unknown status = %d, want 404", rec.Code)
	}
	var errBody map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil || errBody["error"] == "" {
		t.Fatalf("delete unknown body = %q, want a JSON error object", rec.Body.String())
	}

	// Wrong method.
	rec = httptest.NewRecorder()
	reviewCollectionHandler(rec, localRequest(http.MethodPut, "/api/review", []byte(`{}`)))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT status = %d, want 405", rec.Code)
	}

	// Malformed JSON.
	rec = httptest.NewRecorder()
	reviewCollectionHandler(rec, localRequest(http.MethodPost, "/api/review", []byte("{not json")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON status = %d, want 400", rec.Code)
	}
}

func TestReviewStreamReplaysFinishedReview(t *testing.T) {
	withReviewEnv(t)

	rev := &reviewstore.Review{Signature: "GitHub Token", Secret: "ghp_x", Status: reviewstore.StatusQueued}
	if err := reviewStore.Create(rev); err != nil {
		t.Fatalf("create: %v", err)
	}
	rev.Status = reviewstore.StatusDone
	rev.Assessment = "## What it is\nA GitHub token."
	if err := reviewStore.Update(rev); err != nil {
		t.Fatalf("update: %v", err)
	}

	// No in-flight job, so the handler must replay the persisted result.
	rec := httptest.NewRecorder()
	reviewStreamHandler(rec, localRequest(http.MethodGet, "/api/review/"+rev.ID+"/stream", nil), rev.ID)

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"type":"done"`) || !strings.Contains(body, "A GitHub token.") {
		t.Fatalf("stream body = %q", body)
	}
}

func TestReviewStreamRunningReviewDeliversDeltas(t *testing.T) {
	withReviewEnv(t)

	rev := &reviewstore.Review{Signature: "Stripe Key", Secret: "sk_live_x", Status: reviewstore.StatusQueued}
	if err := reviewStore.Create(rev); err != nil {
		t.Fatalf("create: %v", err)
	}
	rev.Status = reviewstore.StatusRunning
	_ = reviewStore.Update(rev)

	job := newReviewJob()
	reviewJobsMu.Lock()
	reviewJobs[rev.ID] = job
	reviewJobsMu.Unlock()

	rec := httptest.NewRecorder()
	req := localRequest(http.MethodGet, "/api/review/"+rev.ID+"/stream", nil)

	done := make(chan struct{})
	go func() {
		reviewStreamHandler(rec, req, rev.ID)
		close(done)
	}()

	// Give the handler a moment to subscribe, then finish the job.
	waitFor(t, time.Second, func() bool {
		job.mu.Lock()
		defer job.mu.Unlock()
		return len(job.subs) == 1
	}, "the stream handler to subscribe")

	job.publish("## Impact\n")
	job.publish("High.")
	job.finish(reviewstore.StatusDone, "")

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not finish after the job completed")
	}

	body := rec.Body.String()
	for _, want := range []string{"## Impact", "High.", `"type":"done"`} {
		if !strings.Contains(body, want) {
			t.Errorf("stream body is missing %q; got %q", want, body)
		}
	}
}

func TestReviewGuardBlocksCrossOriginAndRemoteHosts(t *testing.T) {
	withReviewEnv(t)

	// Cross-origin browser request.
	r := localRequest(http.MethodGet, "/api/review", nil)
	r.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	localGuard(func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(http.StatusOK) })(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want 403", rec.Code)
	}

	// DNS rebinding: a non-loopback Host while the dashboard is on loopback.
	r = httptest.NewRequest(http.MethodGet, "/api/review", nil)
	r.Host = "attacker.example"
	rec = httptest.NewRecorder()
	localGuard(func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(http.StatusOK) })(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("rebound Host status = %d, want 403", rec.Code)
	}

	// Same-origin, loopback: allowed.
	r = localRequest(http.MethodGet, "/api/review", nil)
	r.Header.Set("Origin", "http://127.0.0.1:8080")
	rec = httptest.NewRecorder()
	localGuard(func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(http.StatusOK) })(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("same-origin status = %d, want 200", rec.Code)
	}

	// No Origin header at all (curl, CLI): allowed.
	r = localRequest(http.MethodGet, "/api/review", nil)
	rec = httptest.NewRecorder()
	localGuard(func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(http.StatusOK) })(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("headerless status = %d, want 200", rec.Code)
	}
}

// A review is flipped to "running" before its goroutine starts and only the
// process that started it can finish it, so a restart orphans anything still
// queued or running. Without reconciliation such a review spins in the
// dashboard forever and, being non-terminal, cannot be retried - only deleted.
// A route that serves captured material and forgets localGuard is readable by any
// page the operator visits: corsMiddleware's Origin==Host test is satisfied by DNS
// rebinding (the attacker's hostname resolves to 127.0.0.1, so both headers carry
// the attacker's name). This walks the registered mux and asserts that every
// secret-bearing route refuses a rebound Host, so a new endpoint cannot silently
// ship without the guard. /health and the static dashboard are deliberately open.
func TestEverySecretRouteRejectsAReboundHost(t *testing.T) {
	withReviewEnv(t)

	// The guard is only active while the dashboard is bound to loopback.
	prev := webBindIsLoopback
	webBindIsLoopback = true
	defer func() { webBindIsLoopback = prev }()

	mux := http.NewServeMux()
	registerRoutes(mux)

	guarded := []string{
		"/api/matches", "/api/file", "/api/logs", "/api/tokens",
		"/api/stats", "/api/activity", "/api/signatures", "/api/push",
		"/api/review", "/api/settings", "/api/settings/test",
	}
	for _, path := range guarded {
		rec := httptest.NewRecorder()
		// A rebound request: the attacker's name in both Host and Origin.
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "attacker.example"
		req.Header.Set("Origin", "http://attacker.example")
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s answered %d for a rebound Host, want 403: it is readable by any visited web page", path, rec.Code)
		}
	}

	// The liveness probe must stay reachable so container health checks work.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Host = "attacker.example"
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden {
		t.Error("/health must stay reachable for health probes")
	}

	// The dashboard page itself must stay reachable by any hostname, or a
	// hosts-file alias or tunnel would lock the operator out of their own UI.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "shhgit.internal"
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden {
		t.Error("the dashboard page must not be blocked by the loopback guard")
	}
}

// The live finding feed must not hand out a wildcard CORS grant: that would let
// any page the operator visits subscribe to it directly, with no rebinding needed.
func TestEventsStreamDoesNotGrantWildcardCORS(t *testing.T) {
	withReviewEnv(t)
	ensureWebHub()

	srv := httptest.NewServer(corsMiddleware(localGuard(eventsHandler)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/events")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Headers arrive before the body: the handler flushes them on connect.
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("status = %d, want 200 for a loopback same-origin subscriber", resp.StatusCode)
	}
	acao := resp.Header.Get("Access-Control-Allow-Origin")
	resp.Body.Close() // disconnects the stream

	if acao == "*" {
		t.Error(`/api/events granted Access-Control-Allow-Origin: *: any web page the operator visits could read the live secret feed`)
	}
}

// A review can receive chat turns while it is still running (AppendMessage is
// documented as safe in that state, and the chat endpoint allows it). The runner
// used to write its whole pre-run snapshot back when it finished, which silently
// destroyed those turns and could leave a transcript beginning with an assistant
// reply to a question that no longer existed.
func TestChatTurnsSurviveTheReviewCompleting(t *testing.T) {
	withReviewEnv(t)

	rev := &reviewstore.Review{Signature: "S", Secret: "x", Status: reviewstore.StatusRunning}
	if err := reviewStore.Create(rev); err != nil {
		t.Fatalf("create: %v", err)
	}

	// A turn arrives while the review is in flight.
	if err := reviewStore.AppendMessage(rev.ID, reviewstore.Message{Role: "user", Content: "asked while running"}); err != nil {
		t.Fatalf("append user: %v", err)
	}
	if err := reviewStore.AppendMessage(rev.ID, reviewstore.Message{Role: "assistant", Content: "answered while running"}); err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	// The runner finishes and records its result.
	if err := reviewStore.SetResult(rev.ID, reviewstore.StatusDone, "## What it is\nassessment", ""); err != nil {
		t.Fatalf("set result: %v", err)
	}

	got, ok := reviewStore.Get(rev.ID)
	if !ok {
		t.Fatal("review vanished")
	}
	if got.Status != reviewstore.StatusDone || got.Assessment == "" {
		t.Errorf("result not recorded: status=%q assessment=%q", got.Status, got.Assessment)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("chat turns = %d, want 2: the review completing destroyed them", len(got.Messages))
	}
	if got.Messages[0].Role != "user" || got.Messages[1].Role != "assistant" {
		t.Errorf("transcript order = %q,%q, want user,assistant", got.Messages[0].Role, got.Messages[1].Role)
	}

	// The running transition must preserve turns too.
	if err := reviewStore.SetResult(rev.ID, reviewstore.StatusRunning, "", ""); err != nil {
		t.Fatalf("set running: %v", err)
	}
	got, _ = reviewStore.Get(rev.ID)
	if len(got.Messages) != 2 {
		t.Errorf("marking the review running dropped turns: %d messages", len(got.Messages))
	}
}

// SetResult must not clobber unrelated fields, and must report a missing review
// the same way the rest of the store does.
func TestSetResultKeepsOtherFieldsAndReportsMissing(t *testing.T) {
	withReviewEnv(t)

	rev := &reviewstore.Review{
		Signature: "S", Secret: "keepme", File: "a.go", Repo: "acme/app",
		Context: "ctx", Provider: "ollama", Model: "m", Status: reviewstore.StatusQueued,
	}
	if err := reviewStore.Create(rev); err != nil {
		t.Fatalf("create: %v", err)
	}
	created := rev.CreatedAt

	if err := reviewStore.SetResult(rev.ID, reviewstore.StatusFailed, "", "boom"); err != nil {
		t.Fatalf("set result: %v", err)
	}
	got, ok := reviewStore.Get(rev.ID)
	if !ok {
		t.Fatal("review vanished")
	}
	if got.Signature != "S" || got.Secret != "keepme" || got.File != "a.go" ||
		got.Repo != "acme/app" || got.Context != "ctx" || got.Provider != "ollama" || got.Model != "m" {
		t.Errorf("SetResult clobbered unrelated fields: %+v", got)
	}
	if got.Error != "boom" || got.Status != reviewstore.StatusFailed {
		t.Errorf("result = %q/%q", got.Status, got.Error)
	}
	if !got.CreatedAt.Equal(created) {
		t.Error("SetResult changed CreatedAt, which must be immutable")
	}
	if !got.UpdatedAt.After(created) && got.UpdatedAt.Before(created) {
		t.Error("SetResult did not stamp UpdatedAt")
	}

	err := reviewStore.SetResult("does-not-exist", reviewstore.StatusDone, "x", "")
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unknown id error = %v, want os.ErrNotExist-compatible", err)
	}
}

// Subscribers that go away must leave the fan-out set, or a browser that
// reconnects repeatedly would slow every later delta down.
func TestUnsubscribeRemovesTheSubscriber(t *testing.T) {
	job := newReviewJob()

	_, ch1, _ := job.subscribe()
	_, ch2, _ := job.subscribe()
	if got := liveSubs(job); got != 2 {
		t.Fatalf("subscribers = %d, want 2", got)
	}

	job.unsubscribe(ch1)
	if got := liveSubs(job); got != 1 {
		t.Errorf("subscribers after unsubscribe = %d, want 1", got)
	}
	// The removed channel is closed, so a reader unblocks instead of waiting.
	if _, open := <-ch1; open {
		t.Error("unsubscribe left the channel open")
	}

	// Idempotent, and safe on a channel that was never registered.
	job.unsubscribe(ch1)
	job.unsubscribe(nil)
	neverSubscribed := make(chan string, 1)
	job.unsubscribe(neverSubscribed)
	if got := liveSubs(job); got != 1 {
		t.Errorf("subscribers = %d, want 1 after repeated unsubscribes", got)
	}

	// finish still closes the remaining subscriber exactly once. The delta
	// published above is buffered first, so drain it before expecting the close.
	job.publish("delta")
	job.finish(reviewstore.StatusDone, "")
	if got := liveSubs(job); got != 0 {
		t.Errorf("subscribers after finish = %d, want 0", got)
	}
	text, status, _ := job.result()
	if text != "delta" || status != reviewstore.StatusDone {
		t.Errorf("result = %q/%q", text, status)
	}
	if got, open := <-ch2; !open || got != "delta" {
		t.Errorf("buffered delta = %q open=%v, want the published delta", got, open)
	}
	if _, open := <-ch2; open {
		t.Error("finish did not close the remaining subscriber")
	}
}

// liveSubs reports the job's current fan-out set size, so a leaked subscriber is
// visible to a test.
func liveSubs(j *reviewJob) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.subs)
}

// The model must not be told that a one-line scanner summary is the file's
// contents: that invites an assessment of code it never saw.
func TestPromptSaysWhetherItHasFileContent(t *testing.T) {
	cases := []struct {
		name    string
		context string
		want    string
		absent  string
	}{
		{
			name:    "captured body with scanner notes",
			context: "package main\n" + scannerNotesMarker + "\nline 4: AKIA...",
			want:    "file content, for context",
			absent:  "no file content was captured",
		},
		{
			name:    "summary only",
			context: noFileBodyMarker + "\nline 4: AKIA...",
			want:    "no file content was captured for this finding; the scanner's summary follows",
			absent:  "file content, for context",
		},
		{
			name:    "record written before the markers existed",
			context: "package main\nfunc main() {}",
			want:    "context supplied with this finding",
			absent:  "file content, for context",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgs := buildReviewMessages(&reviewstore.Review{
				Signature: "AWS key", Secret: "AKIA...", File: "a.go", Repo: "acme/app",
				Context: tc.context,
			}, "system")
			if len(msgs) != 2 {
				t.Fatalf("messages = %d, want 2", len(msgs))
			}
			body := msgs[1].Content
			if !strings.Contains(body, tc.want) {
				t.Errorf("prompt is missing %q:\n%s", tc.want, body)
			}
			if strings.Contains(body, tc.absent) {
				t.Errorf("prompt wrongly claims %q:\n%s", tc.absent, body)
			}
			if !strings.Contains(body, tc.context) {
				t.Error("prompt dropped the stored context")
			}
		})
	}

	// An empty context must still say so instead of leaving the model guessing.
	msgs := buildReviewMessages(&reviewstore.Review{Signature: "S", Context: ""}, "system")
	if !strings.Contains(msgs[1].Content, "No file content was captured") {
		t.Errorf("empty context not reported:\n%s", msgs[1].Content)
	}
}

// An empty bind address must never produce a listening socket on every interface
// with the loopback guard switched off. Whatever host is used, "the guard is on"
// has to agree with "bound to loopback".
func TestEmptyWebHostFailsClosed(t *testing.T) {
	if got := normalizeWebHost(""); got != "127.0.0.1" {
		t.Errorf("normalizeWebHost(\"\") = %q, want 127.0.0.1", got)
	}
	if got := normalizeWebHost("   "); got != "127.0.0.1" {
		t.Errorf("normalizeWebHost(whitespace) = %q, want 127.0.0.1", got)
	}
	if !isLoopbackHost(normalizeWebHost("")) {
		t.Error("an empty --web-host still yields a guard-off binding")
	}
	// A routable address must keep working as an explicit opt-in.
	if got := normalizeWebHost("0.0.0.0"); got != "0.0.0.0" {
		t.Errorf("normalizeWebHost(0.0.0.0) = %q, want it left alone", got)
	}
	if isLoopbackHost("0.0.0.0") {
		t.Error("0.0.0.0 must not be treated as loopback")
	}
}

// A request with no Host at all must be refused rather than served.
func TestGuardRefusesARequestWithNoHost(t *testing.T) {
	prev := webBindIsLoopback
	webBindIsLoopback = true
	defer func() { webBindIsLoopback = prev }()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/matches", nil)
	req.Host = ""
	localGuard(getMatches).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d for a missing Host, want 403", rec.Code)
	}
}

// The stream's first frame carries the whole assessment so far. It must be
// flagged, because EventSource reconnects on its own after a dropped connection:
// a client that cannot tell a history frame from a delta appends the assessment
// twice and the operator reads the same text twice.
func TestReviewStreamFlagsTheHistoryFrame(t *testing.T) {
	withReviewEnv(t)

	rev := &reviewstore.Review{Signature: "S", Secret: "x", Status: reviewstore.StatusRunning}
	if err := reviewStore.Create(rev); err != nil {
		t.Fatalf("create: %v", err)
	}

	job := newReviewJob()
	job.publish("half an assessment")
	reviewJobsMu.Lock()
	reviewJobs[rev.ID] = job
	reviewJobsMu.Unlock()
	defer dropJob(rev.ID)

	mux := http.NewServeMux()
	registerReviewRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/review/" + rev.ID + "/stream")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want an SSE stream", ct)
	}

	var frame map[string]any
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if err := json.Unmarshal([]byte(payload), &frame); err != nil {
			t.Fatalf("decode frame %q: %v", payload, err)
		}
		break
	}
	if frame == nil {
		t.Fatal("no SSE frame arrived")
	}
	if frame["type"] != "delta" {
		t.Errorf("first frame type = %v, want delta", frame["type"])
	}
	if frame["reset"] != true {
		t.Error("the history frame is not flagged reset: a reconnecting client would append it twice")
	}
	if frame["text"] != "half an assessment" {
		t.Errorf("history text = %v, want the text streamed so far", frame["text"])
	}
}

// The guard is the only thing standing between a rebound page and every captured
// secret, so it must fail closed on anything it cannot recognise. Trimming at the
// last colon used to accept "127.0.0.1:evil.test", which is not an address.
func TestIsLoopbackHostFailsClosed(t *testing.T) {
	accept := []string{
		"127.0.0.1",
		"127.0.0.1:8080",
		"localhost",
		"localhost:8080",
		"LocalHost:8080",
		"[::1]",
		"[::1]:8080",
		"127.1.2.3:80",
	}
	for _, h := range accept {
		if !isLoopbackHost(h) {
			t.Errorf("isLoopbackHost(%q) = false, want true", h)
		}
	}

	reject := []string{
		"",
		"   ",
		"127.0.0.1:evil.test",   // the port is not a port
		"[::1].evil.test:51427", // bracketed literal with a suffix
		"127.0.0.1:0x50",        // sneaky port form
		"localhost.:8080",       // a fully qualified name is not "localhost"
		"127.0.0.1.:8080",
		"127.0.0.1.nip.io:8080", // resolves here, but is not a loopback address
		"0.0.0.0:8080",
		"attacker.example",
		"attacker.example:8080",
		"127.1",
		"evil.test@127.0.0.1:8080",
	}
	for _, h := range reject {
		if isLoopbackHost(h) {
			t.Errorf("isLoopbackHost(%q) = true: the guard would accept it", h)
		}
	}
}

// An Origin that is not a real absolute origin must not count as same-origin. No
// browser sends these, but accepting them silently would let a proxy or a raw
// client widen the check.
func TestSameOriginRequiresAnAbsoluteOrigin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/matches", nil)
	req.Host = "127.0.0.1:8080"

	for _, origin := range []string{
		"http://127.0.0.1:8080",
		"HTTP://127.0.0.1:8080",
	} {
		if !sameOrigin(origin, req) {
			t.Errorf("sameOrigin(%q) = false, want true", origin)
		}
	}
	for _, origin := range []string{
		"//127.0.0.1:8080",                // protocol-relative
		"http://evil.test@127.0.0.1:8080", // userinfo, which url.Parse discards
		"ftp://127.0.0.1:8080",            // not a browser origin scheme
		"null",                            // sandboxed document
		"http://127.0.0.1:9999",           // different port
		"http://localhost:8080",           // different host spelling
		"",
		"http://",
	} {
		if sameOrigin(origin, req) {
			t.Errorf("sameOrigin(%q) = true, want false", origin)
		}
	}
}

// An absolute-form request line carries its own authority. Go prefers it over the
// Host header when it fills in r.Host, so a loopback Host must not be enough.
func TestGuardRejectsAnAbsoluteFormRequestLine(t *testing.T) {
	prev := webBindIsLoopback
	webBindIsLoopback = true
	defer func() { webBindIsLoopback = prev }()

	req := httptest.NewRequest(http.MethodGet, "/api/matches", nil)
	req.Host = "127.0.0.1:8080" // the header a proxy would forward
	req.URL.Host = "evil.test"  // the authority the proxy actually dialled

	rec := httptest.NewRecorder()
	localGuard(getMatches).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d for an absolute-form URI naming another host, want 403", rec.Code)
	}
}

// /api/push needs no credentials, so an unbounded body would let any local
// process grow the server's memory at will.
func TestPushRejectsAnOversizedBody(t *testing.T) {
	withReviewEnv(t)
	ensureWebHub()

	big := `{"signature":"x","secret":"y","padding":"` + strings.Repeat("A", maxJSONBody+1024) + `"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/push", strings.NewReader(big))
	req.Host = "127.0.0.1:8080"
	receiveMatch(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d for a %d-byte body, want 400", rec.Code, len(big))
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	return u
}

func TestReconcileInterruptedReviews(t *testing.T) {
	withReviewEnv(t)

	queued := &reviewstore.Review{Signature: "Queued", Secret: "a", Status: reviewstore.StatusQueued}
	running := &reviewstore.Review{Signature: "Running", Secret: "b", Status: reviewstore.StatusRunning}
	done := &reviewstore.Review{Signature: "Done", Secret: "c", Status: reviewstore.StatusDone, Assessment: "kept"}
	failed := &reviewstore.Review{Signature: "Failed", Secret: "d", Status: reviewstore.StatusFailed, Error: "original failure"}
	for _, r := range []*reviewstore.Review{queued, running, done, failed} {
		if err := reviewStore.Create(r); err != nil {
			t.Fatalf("create %s: %v", r.Signature, err)
		}
	}
	// AppendMessage on a running review proves a chat turn is not lost either.
	if err := reviewStore.AppendMessage(running.ID, reviewstore.Message{Role: "user", Content: "was mid-conversation"}); err != nil {
		t.Fatalf("append: %v", err)
	}

	if n := reconcileInterruptedReviews(reviewStore); n != 2 {
		t.Errorf("reconciled = %d, want 2 (the queued and the running review)", n)
	}

	got, ok := reviewStore.Get(queued.ID)
	if !ok {
		t.Fatal("queued review vanished")
	}
	if got.Status != reviewstore.StatusFailed {
		t.Errorf("queued review status = %q, want failed", got.Status)
	}
	if got.Error == "" {
		t.Error("queued review has no explanation for the operator")
	}

	got, ok = reviewStore.Get(running.ID)
	if !ok {
		t.Fatal("running review vanished")
	}
	if got.Status != reviewstore.StatusFailed {
		t.Errorf("running review status = %q, want failed", got.Status)
	}
	if got.Error == "" {
		t.Error("running review has no explanation for the operator")
	}
	if len(got.Messages) != 1 {
		t.Errorf("running review lost its chat history: %d messages, want 1", len(got.Messages))
	}

	// Terminal reviews must be untouched, error text included.
	got, _ = reviewStore.Get(done.ID)
	if got.Status != reviewstore.StatusDone || got.Assessment != "kept" {
		t.Errorf("a finished review was modified: status=%q assessment=%q", got.Status, got.Assessment)
	}
	got, _ = reviewStore.Get(failed.ID)
	if got.Status != reviewstore.StatusFailed || got.Error != "original failure" {
		t.Errorf("a failed review's own error was overwritten: %q", got.Error)
	}

	// Idempotent: a second pass finds nothing to do.
	if n := reconcileInterruptedReviews(reviewStore); n != 0 {
		t.Errorf("second pass reconciled %d, want 0", n)
	}

	// The store must still be usable afterwards.
	list := reviewStore.List()
	if len(list) != 4 {
		t.Errorf("list = %d reviews, want 4", len(list))
	}
}

// A nil store must not panic: initAIReview calls this before the store exists
// in some failure paths.
func TestReconcileHandlesNilStore(t *testing.T) {
	if n := reconcileInterruptedReviews(nil); n != 0 {
		t.Errorf("nil store reconciled %d, want 0", n)
	}
}

func TestReviewChatRequiresMessageAndReview(t *testing.T) {
	withReviewEnv(t)

	rev := &reviewstore.Review{Signature: "S", Secret: "x", Status: reviewstore.StatusDone}
	if err := reviewStore.Create(rev); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Empty message.
	rec := httptest.NewRecorder()
	reviewChatHandler(rec, localRequest(http.MethodPost, "/api/review/"+rev.ID+"/chat", []byte(`{"message":"   "}`)), rev.ID)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty message status = %d, want 400", rec.Code)
	}

	// Unknown review.
	rec = httptest.NewRecorder()
	reviewChatHandler(rec, localRequest(http.MethodPost, "/api/review/nope/chat", []byte(`{"message":"hi"}`)), "nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown review status = %d, want 404", rec.Code)
	}
}

func TestRepoFromURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/acme/app":          "acme/app",
		"https://github.com/acme/app.git":      "acme/app",
		"github.com/acme/app/blob/main/a.go":   "acme/app",
		"https://gitlab.com/acme/app":          "acme/app",
		"git@github.com:acme/app.git":          "acme/app",
		"ssh://git@github.com/acme/app":        "acme/app",
		"acme/app":                             "acme/app",
		"":                                     "",
		"https://example.com/only-one-segment": "only-one-segment",
	}
	for in, want := range cases {
		if got := repoFromURL(in); got != want {
			t.Errorf("repoFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInitAIReviewCreatesStoreAndSeedsFromConfig(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() {
		reviewStore = nil
		aiSettingsStore = nil
		reviewPrompt = defaultReviewSystemPrompt
	})

	cfg := &core.Config{}
	cfg.AIReview = core.AIReviewConfig{
		Provider:  "ollama",
		Model:     "seeded-model",
		ReviewDir: filepath.Join(dir, "ai_review"),
	}
	if err := initAIReview(cfg); err != nil {
		t.Fatalf("initAIReview: %v", err)
	}
	if reviewStore == nil || aiSettingsStore == nil {
		t.Fatal("stores were not initialised")
	}

	got, err := aiSettingsStore.Get()
	if err != nil {
		t.Fatalf("settings get: %v", err)
	}
	if got.Provider != "ollama" {
		t.Fatalf("provider = %q, want the value seeded from config", got.Provider)
	}
	if got.Model != "seeded-model" {
		t.Fatalf("model = %q, want the value seeded from config", got.Model)
	}

	// A second start must not clobber what the operator saved in the dashboard.
	saved := aiproviders.Settings{Provider: "openai", APIKey: "sk-saved", Model: "gpt-4o-mini"}
	if err := aiSettingsStore.Set(saved); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	if err := initAIReview(cfg); err != nil {
		t.Fatalf("second initAIReview: %v", err)
	}
	again, err := aiSettingsStore.Get()
	if err != nil {
		t.Fatalf("settings get: %v", err)
	}
	if again.Provider != "openai" || again.APIKey != "sk-saved" {
		t.Fatalf("saved settings were overwritten by config: %+v", again)
	}
}

func TestInitAIReviewMigratesLegacyChatConfig(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() {
		reviewStore = nil
		aiSettingsStore = nil
		reviewPrompt = defaultReviewSystemPrompt
	})

	// The single-backend chat this release replaced kept its own config file.
	legacy := `{"backend":"deepseek","deepseek_api_key":"sk-legacy","deepseek_model":"deepseek-chat","ollama_url":"","ollama_model":"","system_prompt":"LEGACY PROMPT"}`
	if err := os.WriteFile(filepath.Join(dir, "chat_config.json"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	cfg := &core.Config{}
	cfg.AIReview = core.AIReviewConfig{ReviewDir: dir}
	if err := initAIReview(cfg); err != nil {
		t.Fatalf("initAIReview: %v", err)
	}

	got, err := aiSettingsStore.Get()
	if err != nil {
		t.Fatalf("settings get: %v", err)
	}
	if got.Provider != "deepseek" || got.APIKey != "sk-legacy" || got.Model != "deepseek-chat" {
		t.Fatalf("legacy settings were not migrated: provider=%q keySet=%v model=%q",
			got.Provider, got.APIKey != "", got.Model)
	}
	if reviewPrompt != "LEGACY PROMPT" {
		t.Fatalf("reviewPrompt = %q, want the legacy prompt", reviewPrompt)
	}
}

// The old config did not always record a backend. A key with no backend used to
// produce a provider-less seed, which the settings store rejects - and because
// the failure was returned rather than logged, it disabled AI review entirely on
// an upgrade.
func TestInitAIReviewMigratesLegacyConfigWithNoBackend(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() {
		reviewStore = nil
		aiSettingsStore = nil
		reviewPrompt = defaultReviewSystemPrompt
	})

	legacy := `{"backend":"","deepseek_api_key":"sk-legacy-nobackend","deepseek_model":"deepseek-chat"}`
	if err := os.WriteFile(filepath.Join(dir, "chat_config.json"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	cfg := &core.Config{}
	cfg.AIReview = core.AIReviewConfig{ReviewDir: dir}
	if err := initAIReview(cfg); err != nil {
		t.Fatalf("a backend-less legacy config must not fail startup: %v", err)
	}
	if aiSettingsStore == nil {
		t.Fatal("settings store was not initialised")
	}

	got, err := aiSettingsStore.Get()
	if err != nil {
		t.Fatalf("settings get: %v", err)
	}
	if got.APIKey != "sk-legacy-nobackend" {
		t.Error("the legacy API key was lost")
	}
	if got.Provider == "" {
		t.Error("provider = \"\", want it defaulted, or the store rejects the seed")
	}
	if got.Provider != "deepseek" {
		t.Errorf("provider = %q, want deepseek: the old chat defaulted to it", got.Provider)
	}
}

// An unusable AI section in config.yaml must degrade to the environment and the
// built-in defaults, not take the whole feature down.
func TestInitAIReviewSurvivesAnUnusableSeed(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() {
		reviewStore = nil
		aiSettingsStore = nil
		reviewPrompt = defaultReviewSystemPrompt
	})

	cfg := &core.Config{}
	cfg.AIReview = core.AIReviewConfig{
		Provider:  "not-a-real-provider",
		ReviewDir: filepath.Join(dir, "ai_review"),
	}
	if err := initAIReview(cfg); err != nil {
		t.Fatalf("an unknown provider in config.yaml must not fail startup: %v", err)
	}
	if reviewStore == nil || aiSettingsStore == nil {
		t.Fatal("stores were not initialised despite the unusable seed")
	}
	if _, err := os.Stat(aiSettingsStore.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Error("an unusable seed must not be written to the settings file")
	}
	// The bogus provider must not be in effect: the store falls back to the
	// built-in default. Asserting on the resolved provider keeps this
	// independent of whether a key happens to be in the environment.
	got, err := aiSettingsStore.Get()
	if err != nil {
		t.Fatalf("settings get: %v", err)
	}
	if got.Provider == "not-a-real-provider" {
		t.Error("the unusable seed is still in effect")
	}
	if got.Provider != "deepseek" {
		t.Errorf("provider = %q, want the built-in default", got.Provider)
	}
}
