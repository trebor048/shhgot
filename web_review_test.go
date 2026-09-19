package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
