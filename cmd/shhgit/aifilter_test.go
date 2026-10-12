package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trebor048/shhgot/aiproviders"
	"github.com/trebor048/shhgot/core"
	"github.com/trebor048/shhgot/core/store"
)

// fakeFilterClient is the counting generate seam for the classifier: its Chat
// method is what the filter calls instead of a real provider.
type fakeFilterClient struct {
	mu    sync.Mutex
	calls int
	reply func(prompt string) (string, error)
}

func (c *fakeFilterClient) Name() string { return "fake" }

func (c *fakeFilterClient) Chat(_ context.Context, msgs []aiproviders.Message) (string, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	if c.reply == nil {
		return "", nil
	}
	content := ""
	if len(msgs) > 0 {
		content = msgs[len(msgs)-1].Content
	}
	return c.reply(content)
}

func (c *fakeFilterClient) Stream(_ context.Context, _ []aiproviders.Message, _ func(string) error) error {
	return nil
}

func (c *fakeFilterClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// suppressReply is a confident placeholder verdict, which the classifier
// suppresses on.
func suppressReply(string) (string, error) {
	return `{"confidence":"HIGH","key_type":"PLACEHOLDER","provider":"openai","reasoning":"looks like a fixture","is_likely_real":false}`, nil
}

func aiFilterBool(b bool) *bool { return &b }
func aiFilterInt(n int) *int    { return &n }

// installTestFilter installs f as the process-wide filter for the duration of
// the test and stops its workers afterwards.
func installTestFilter(t *testing.T, f *findingFilter) {
	t.Helper()
	prev := getAIFilter()
	setAIFilter(f)
	t.Cleanup(func() {
		setAIFilter(prev)
		f.Close()
	})
}

// TestAIFilterPlaceholderSuppressedWithoutLLMCall drives the real publish path:
// an obvious placeholder is removed by the deterministic quick filter, so the
// LLM backend is never contacted, and the finding shows in /api/suppressed.
func TestAIFilterPlaceholderSuppressedWithoutLLMCall(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	newStoreTestSession(t, false)

	client := &fakeFilterClient{reply: suppressReply}
	f := newFindingFilter(core.AIFilterConfig{
		Enabled:     aiFilterBool(true),
		QuickFilter: aiFilterBool(true),
		Workers:     aiFilterInt(1),
	}, client)
	installTestFilter(t, f)

	publish(&MatchEvent{
		Source:    core.LOCAL_SOURCE,
		Url:       "https://github.com/o/r",
		Signature: "OpenAI API Key",
		File:      "config.py",
		Matches:   []string{"sk-test-your-api-key-here"},
		Secret:    "sk-test-your-api-key-here",
		Priority:  1,
	})

	waitForCond(t, 2*time.Second, func() bool { return len(getAIFilter().Suppressed()) == 1 })

	if got := hubMatchCount(t); got != 0 {
		t.Fatalf("suppressed placeholder reached the dashboard: %d matches", got)
	}
	if got := client.callCount(); got != 0 {
		t.Fatalf("quick filter made %d LLM call(s), want 0", got)
	}

	rec := httptest.NewRecorder()
	getSuppressed(rec, httptest.NewRequest(http.MethodGet, "/api/suppressed", nil))
	var listing suppressedListing
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decode /api/suppressed: %v", err)
	}
	list := listing.Suppressed
	if len(list) != 1 || list[0].Reason != "placeholder-pattern" || list[0].Stage != "quick" {
		t.Fatalf("unexpected suppressed listing: %+v", list)
	}
}

// TestAIFilterBackendErrorKeepsFinding proves the classifier fails open: when
// the backend errors the finding is still published.
func TestAIFilterBackendErrorKeepsFinding(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	newStoreTestSession(t, false)

	client := &fakeFilterClient{reply: func(string) (string, error) {
		return "", fmt.Errorf("connection refused")
	}}
	f := newFindingFilter(core.AIFilterConfig{
		Enabled:     aiFilterBool(true),
		QuickFilter: aiFilterBool(true),
		Workers:     aiFilterInt(1),
	}, client)
	installTestFilter(t, f)

	const secret = "sk-proj-K7mQ2vNp9Rt4Wx6Lz3Bd8Gh5Jc1Fa"
	publish(&MatchEvent{
		Source:      core.LOCAL_SOURCE,
		Url:         "https://github.com/o/r",
		Signature:   "OpenAI API Key",
		File:        "src/config.py",
		Matches:     []string{secret},
		Secret:      secret,
		Priority:    2,
		SecretLine:  1,
		FileContent: "OPENAI_KEY=" + secret,
	})

	waitForCond(t, 2*time.Second, func() bool {
		return hubMatchCount(t) == 1 && client.callCount() >= 1
	})
	if got := len(getAIFilter().Suppressed()); got != 0 {
		t.Fatalf("a classifier error suppressed %d finding(s), want 0", got)
	}
}

// TestAIFilterSuppressedAbsentFromMatchesAndStore proves a suppressed finding is
// visible only through /api/suppressed: it never reaches /api/matches, the
// findings store or the webhook.
func TestAIFilterSuppressedAbsentFromMatchesAndStore(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	s := newStoreTestSession(t, true)
	s.Config.Webhook = "https://example.test/hook"

	var deliveries int32
	prevDeliver := deliverWebhook
	deliverWebhook = func(_ *MatchEvent, onConfirm func(bool)) {
		atomic.AddInt32(&deliveries, 1)
		onConfirm(true)
	}
	t.Cleanup(func() { deliverWebhook = prevDeliver })

	client := &fakeFilterClient{reply: suppressReply}
	f := newFindingFilter(core.AIFilterConfig{
		Enabled:     aiFilterBool(true),
		QuickFilter: aiFilterBool(true),
		Workers:     aiFilterInt(1),
	}, client)
	installTestFilter(t, f)

	const secret = "sk-proj-K7mQ2vNp9Rt4Wx6Lz3Bd8Gh5Jc1Fa"
	publish(&MatchEvent{
		Source:    core.LOCAL_SOURCE,
		Url:       "https://github.com/o/r",
		Signature: "OpenAI API Key",
		File:      "src/config.py",
		Matches:   []string{secret},
		Secret:    secret,
		Priority:  1,
	})

	waitForCond(t, 2*time.Second, func() bool { return len(getAIFilter().Suppressed()) == 1 })

	if got := hubMatchCount(t); got != 0 {
		t.Fatalf("/api/matches is backed by %d hub match(es), want 0", got)
	}
	rec := httptest.NewRecorder()
	getMatches(rec, httptest.NewRequest(http.MethodGet, "/api/matches", nil))
	var matches []*Match
	if err := json.Unmarshal(rec.Body.Bytes(), &matches); err != nil {
		t.Fatalf("decode /api/matches: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("/api/matches returned %d finding(s), want 0", len(matches))
	}
	if recs, err := s.Store.List(store.Filter{}); err != nil || len(recs) != 0 {
		t.Fatalf("store holds %d record(s) (err %v), want 0", len(recs), err)
	}
	if got := atomic.LoadInt32(&deliveries); got != 0 {
		t.Fatalf("suppressed finding was delivered %d time(s), want 0", got)
	}

	rec = httptest.NewRecorder()
	getSuppressed(rec, httptest.NewRequest(http.MethodGet, "/api/suppressed", nil))
	var listing suppressedListing
	if err := json.Unmarshal(rec.Body.Bytes(), &listing); err != nil {
		t.Fatalf("decode /api/suppressed: %v", err)
	}
	list := listing.Suppressed
	if len(list) != 1 || list[0].Stage != "llm" || list[0].Reason != "llm:PLACEHOLDER" {
		t.Fatalf("unexpected suppressed listing: %+v", list)
	}
}

// TestAIFilterTimeoutKeepsFinding proves the bounded consult keeps the finding
// rather than blocking on a slow backend.
func TestAIFilterTimeoutKeepsFinding(t *testing.T) {
	client := &fakeFilterClient{reply: func(string) (string, error) {
		time.Sleep(200 * time.Millisecond)
		return suppressReply("")
	}}
	f := newFindingFilter(core.AIFilterConfig{
		Enabled:     aiFilterBool(true),
		QuickFilter: aiFilterBool(true),
		Workers:     aiFilterInt(1),
	}, client)
	t.Cleanup(f.Close)
	f.consultTimeout = time.Millisecond

	const secret = "sk-proj-K7mQ2vNp9Rt4Wx6Lz3Bd8Gh5Jc1Fa"
	verdict := f.Consider(&MatchEvent{
		Source:   core.LOCAL_SOURCE,
		File:     "src/config.py",
		Matches:  []string{secret},
		Secret:   secret,
		Priority: 1,
	}, "")
	if verdict.Suppress {
		t.Fatal("a timed-out consult suppressed the finding, want keep")
	}
}

// TestAIFilterDegradedAfterFailures proves repeated classify errors trip the
// degraded flag exposed on /api/stats and /health.
func TestAIFilterDegradedAfterFailures(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)

	client := &fakeFilterClient{reply: func(string) (string, error) {
		return "", fmt.Errorf("backend down")
	}}
	f := newFindingFilter(core.AIFilterConfig{
		Enabled:     aiFilterBool(true),
		QuickFilter: aiFilterBool(false), // exercise the LLM path only
		Workers:     aiFilterInt(1),
	}, client)
	installTestFilter(t, f)

	for i := 0; i < defaultDegradedAfter; i++ {
		secret := fmt.Sprintf("ghp_9fK2mQ7vNp4Rt8Wx3Lz6Bd1Gh5Jc%d", i)
		f.Consider(&MatchEvent{
			Source:   core.LOCAL_SOURCE,
			File:     "src/config.py",
			Matches:  []string{secret},
			Secret:   secret,
			Priority: 1,
		}, "")
	}

	waitForCond(t, 2*time.Second, func() bool { return f.Degraded() })

	rec := httptest.NewRecorder()
	getStats(rec, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	var stats Stats
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode /api/stats: %v", err)
	}
	if !stats.AIFilterDegraded {
		t.Fatal("/api/stats did not report ai_filter_degraded")
	}

	rec = httptest.NewRecorder()
	health(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /health: %v", err)
	}
	if body["ai_filter_degraded"] != true {
		t.Fatalf("/health ai_filter_degraded = %v, want true", body["ai_filter_degraded"])
	}
}

// A provider switch in ai_filter must not inherit the saved provider's endpoint.
func TestResolveFilterSettingsSwitchesProviderCleanly(t *testing.T) {
	st := aiproviders.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	if err := st.Set(aiproviders.Settings{
		Provider: aiproviders.ProviderDeepSeek,
		APIKey:   "sk-saved",
		BaseURL:  "https://api.deepseek.com/v1",
		Model:    "deepseek-chat",
	}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	got, ok := resolveFilterSettings(core.AIFilterConfig{Provider: aiproviders.ProviderOllama}, st)
	if !ok {
		t.Fatal("resolveFilterSettings returned ok=false for a named provider")
	}
	if got.Provider != aiproviders.ProviderOllama {
		t.Fatalf("provider = %q, want ollama", got.Provider)
	}
	if got.APIKey != "sk-saved" {
		t.Fatalf("saved API key was not reused: %q", got.APIKey)
	}
	if strings.Contains(got.BaseURL, "deepseek") || strings.Contains(got.Model, "deepseek") {
		t.Fatalf("provider switch inherited the previous endpoint: %+v", got)
	}
}

// /api/suppressed reveals captured material, so it must carry the same loopback
// guard as the rest of the findings API: a rebound Host is rejected.
func TestSuppressedRouteIsLocalGuarded(t *testing.T) {
	prev := webBindIsLoopback
	webBindIsLoopback = true
	t.Cleanup(func() { webBindIsLoopback = prev })

	mux := http.NewServeMux()
	registerRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/suppressed", nil)
	req.Host = "attacker.example"
	req.Header.Set("Origin", "http://attacker.example")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("/api/suppressed answered %d for a rebound Host, want 403", rec.Code)
	}
}

// TestAIFilterAccessorsAreConcurrencySafe exercises the write side (scanner
// startup installing the filter) and the read side (a publishing source)
// together; under -race it guards the atomic accessors.
func TestAIFilterAccessorsAreConcurrencySafe(t *testing.T) {
	f := newFindingFilter(core.AIFilterConfig{Enabled: aiFilterBool(true)}, nil)
	if f == nil {
		t.Fatal("an enabled filter must be non-nil")
	}
	t.Cleanup(func() {
		setAIFilter(nil)
		f.Close()
	})

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			setAIFilter(f)
		}()
		go func() {
			defer wg.Done()
			_ = getAIFilter()
		}()
	}
	wg.Wait()
}

// TestInitPublishInfrastructureInitialisesSinks proves the scanner installs the
// AI filter and outcome recorder before any source can publish, instead of
// assigning them later in executeScanner while source goroutines run.
func TestInitPublishInfrastructureInitialisesSinks(t *testing.T) {
	s := newStoreTestSession(t, false)
	s.Config.AIFilter.Enabled = aiFilterBool(true)
	s.TokenValidator = core.NewTokenValidator(s.Log)

	prev := getAIFilter()
	t.Cleanup(func() { setAIFilter(prev) })

	initPublishInfrastructure()

	if getAIFilter() == nil {
		t.Fatal("initPublishInfrastructure did not install the AI filter before sources start")
	}
	if s.TokenValidator.OnOutcome == nil {
		t.Fatal("initPublishInfrastructure did not install the outcome recorder")
	}
	getAIFilter().Close()
}

// A disabled filter suppresses nothing and never builds a classifier.
func TestAIFilterDisabledIsNoop(t *testing.T) {
	prev := getAIFilter()
	setAIFilter(nil)
	t.Cleanup(func() { setAIFilter(prev) })

	client := &fakeFilterClient{reply: suppressReply}
	f := newFindingFilter(core.AIFilterConfig{
		QuickFilter: aiFilterBool(true),
	}, client)
	if f != nil {
		t.Fatal("a disabled filter was constructed, want nil")
	}
	if got := client.callCount(); got != 0 {
		t.Fatalf("disabled filter made %d LLM call(s), want 0", got)
	}
	if got := aiFilterDegraded(); got {
		t.Fatalf("nil filter reported degraded=%v", got)
	}
}
