package main

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trebor048/shhgot/core"
	"github.com/trebor048/shhgot/core/verifiers"
)

// --- Slice 3 (GitHub code search) wiring tests -------------------------------

func searchBoolPtr(b bool) *bool        { return &b }
func searchUintPtr(n uint) *uint        { return &n }
func searchIntPtr(n int) *int           { return &n }
func searchFloatPtr(f float64) *float64 { return &f }
func searchStringPtr(s string) *string  { return &s }

// newSearchTestSession installs a minimal session whose one signature matches a
// fake OpenAI key and whose GitHub token is present, so runCodeSearch reaches
// the network without configuration warnings. The session is restored on
// cleanup.
func newSearchTestSession(t *testing.T, enabled bool) *core.Session {
	t.Helper()
	s := &core.Session{
		Log: &core.Logger{},
		Options: &core.Options{
			MaximumFileSize:  searchUintPtr(256),
			EntropyThreshold: searchFloatPtr(0),
			PathChecks:       searchBoolPtr(true),
			CsvPath:          searchStringPtr(""),
			SearchQuery:      searchStringPtr(""),
			Live:             searchStringPtr(""),
			Local:            searchStringPtr(""),
			Export:           searchBoolPtr(false),
			TestTokens:       searchBoolPtr(false),
			ScanKeys:         searchStringPtr(""),
			Search:           searchBoolPtr(false),
		},
		Config: &core.Config{
			GitHubAccessTokens: []string{"test-token"},
			Signatures: []core.ConfigSignature{{
				Name:     "Test Credential",
				Part:     core.PartContents,
				Regex:    `sk-[A-Za-z0-9]{20,}`,
				Priority: 3,
				Color:    "#10A37F",
			}},
		},
	}
	s.Config.Search.Enabled = searchBoolPtr(enabled)
	s.Signatures = core.GetSignatures(s)

	prev := session
	session = s
	t.Cleanup(func() { session = prev })
	return s
}

// installCodeSearchSeams points the search and download paths at test servers
// for the duration of one test.
func installCodeSearchSeams(t *testing.T, baseURL string, doer core.Doer, hosts []string) {
	t.Helper()
	prevBase, prevDoer, prevHosts := codeSearchBaseURL, codeSearchDoer, codeSearchAllowedHosts
	codeSearchBaseURL, codeSearchDoer, codeSearchAllowedHosts = baseURL, doer, hosts
	t.Cleanup(func() {
		codeSearchBaseURL, codeSearchDoer, codeSearchAllowedHosts = prevBase, prevDoer, prevHosts
	})
}

// searchResponse renders a one-item code-search page whose raw_url points at
// rawURL. The item's path is config/creds.env.
func searchResponse(rawURL string) map[string]any {
	return map[string]any{
		"total_count":        1,
		"incomplete_results": false,
		"items": []map[string]any{{
			"name":     "creds.env",
			"path":     "config/creds.env",
			"sha":      "abc123",
			"html_url": "https://github.com/acme/repo/blob/main/config/creds.env",
			"raw_url":  rawURL,
			"repository": map[string]any{
				"full_name":        "acme/repo",
				"default_branch":   "main",
				"stargazers_count": 7,
			},
		}},
	}
}

// A hit whose body holds a key is published exactly once with source
// github-search.
func TestRunCodeSearchPublishesMatch(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	newSearchTestSession(t, true)
	session.Config.Search.Queries = []string{`"sk-" in:file`}

	payload := "TOKEN=sk-" + strings.Repeat("a", 40)
	var downloadHits int32
	rawSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&downloadHits, 1)
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(rawSrv.Close)

	searchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(searchResponse(rawSrv.URL + "/config/creds.env"))
	}))
	t.Cleanup(searchSrv.Close)

	installCodeSearchSeams(t, searchSrv.URL, searchSrv.Client(), []string{rawSrv.Listener.Addr().String()})

	runCodeSearch()

	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 1 })
	hub.matchesMutex.RLock()
	got := *hub.matches[0]
	hub.matchesMutex.RUnlock()

	if got.Source != "github-search" {
		t.Fatalf("match source = %q, want github-search", got.Source)
	}
	if got.Signature != "Test Credential" {
		t.Fatalf("match signature = %q, want Test Credential", got.Signature)
	}
	if got.File != "config/creds.env" {
		t.Fatalf("match file = %q, want config/creds.env", got.File)
	}
	if n := atomic.LoadInt32(&downloadHits); n != 1 {
		t.Fatalf("download server saw %d requests, want 1", n)
	}
}

// scanContents published an AI finding before it called TestMatchedTokens, so
// for a signature without a verifier: entry the first occurrence carried the
// priority-fallback severity into the webhook colour, the store record and the
// dashboard, and only the dashboard was later corrected. Validation must run
// before publish so the published severity is the verified one.
func TestScanContentsValidatesBeforePublishing(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	resetOutcomeCache(t)

	s := newSearchTestSession(t, false)
	secret := "sk-or-v1-" + strings.Repeat("a", 60)

	// An AI signature that names an AI provider but has no verifier: entry, so
	// the explicit TestMatchedTokens call is the only verification it gets. A
	// priority of 0 would fall back to "low" if the outcome were not known.
	s.Config.Signatures = []core.ConfigSignature{{
		Name:     "OpenRouter API Key",
		Part:     core.PartContents,
		Regex:    `sk-or-v1-[a-z0-9]{60,}`,
		Priority: 0,
		Color:    "#10A37F",
	}}
	s.Signatures = core.GetSignatures(s)

	// A nil logger keeps TestAndLogToken from writing logs/priority3.log.
	s.TokenValidator = core.NewTokenValidator(nil)
	s.TokenValidator.OnOutcome = func(token, provider string, o verifiers.Outcome) {
		rememberOutcome(token, provider, o)
	}

	// Seed the validator's result cache so the registry verifier resolves its
	// outcome without a network call.
	v, ok := verifiers.Lookup("openrouter")
	if !ok {
		t.Fatal("the openrouter verifier is not registered")
	}
	req, err := v.Build(verifiers.Credential{Key: secret}, "")
	if err != nil || req == nil || req.URL == nil {
		t.Fatalf("cannot derive the openrouter endpoint: %v", err)
	}
	base := req.URL.Scheme + "://" + req.URL.Host
	cache := core.NewValidationCache(core.CacheConfig{Enabled: true})
	cache.Put(secret, base, core.CachedValidation{
		Status:  string(verifiers.Valid),
		Tier:    "GPT-4",
		Balance: "$150.00",
	})
	s.TokenValidator.Cache = cache

	body := []byte("OPENROUTER_KEY=" + secret + "\n")
	scanContents("https://github.com/acme/repo", "main", "config/creds.env", "", 7, body, core.GITHUB_SEARCH_SOURCE)

	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 1 })

	hub.matchesMutex.RLock()
	m := *hub.matches[0]
	hub.matchesMutex.RUnlock()

	if m.Severity != "critical" {
		t.Fatalf("published severity = %q, want critical from the verified funded key", m.Severity)
	}
	if m.Verified == nil || !*m.Verified {
		t.Fatalf("published verified = %v, want true", m.Verified)
	}
}

// searchResponseItems renders a full page of code-search items whose raw_urls
// point at rawBase/path for each path.
func searchResponseItems(rawBase string, paths ...string) map[string]any {
	items := make([]map[string]any, 0, len(paths))
	for _, p := range paths {
		items = append(items, map[string]any{
			"name":     p,
			"path":     p,
			"sha":      "sha-" + p,
			"html_url": "https://github.com/acme/repo/blob/main/" + p,
			"raw_url":  rawBase + "/" + p,
			"repository": map[string]any{
				"full_name":        "acme/repo",
				"default_branch":   "main",
				"stargazers_count": 1,
			},
		})
	}
	return map[string]any{
		"total_count":        len(items),
		"incomplete_results": false,
		"items":              items,
	}
}

// TestRunCodeSearchProcessesPartialHitsOnError reproduces the partial-hit loss:
// SearchCode deliberately returns the hits collected before a mid-pagination
// failure, but runCodeSearch ignored them when err was non-nil, so the entire
// first page was discarded. The first page's hits must still be scanned and
// published even though the second page fails.
func TestRunCodeSearchProcessesPartialHitsOnError(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	s := newSearchTestSession(t, true)
	s.Config.Search.Queries = []string{"q"}
	s.Config.Search.PerPage = searchIntPtr(2)
	s.Config.Search.MaxPages = searchIntPtr(2)
	s.Config.Search.SleepMS = searchIntPtr(0)

	payload := "TOKEN=sk-" + strings.Repeat("a", 40)
	rawSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, payload)
	}))
	t.Cleanup(rawSrv.Close)

	searchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "1":
			_ = json.NewEncoder(w).Encode(searchResponseItems(rawSrv.URL, "config/a.env", "config/b.env"))
		case "2":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))
	t.Cleanup(searchSrv.Close)

	installCodeSearchSeams(t, searchSrv.URL, searchSrv.Client(), []string{rawSrv.Listener.Addr().String()})

	runCodeSearch()

	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 2 })
	if n := hubMatchCount(t); n != 2 {
		t.Fatalf("published %d matches from the partial first page, want 2", n)
	}
}

// A search result whose raw URL is off the allowlist must cost zero download
// requests and publish nothing: the Fetcher rejects it before any request.
func TestRunCodeSearchOffAllowlistRawURLIsNotFetched(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	newSearchTestSession(t, true)
	session.Config.Search.Queries = []string{"q"}

	var blockedHits int32
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&blockedHits, 1)
		_, _ = io.WriteString(w, "TOKEN=sk-"+strings.Repeat("a", 40))
	}))
	t.Cleanup(blocked.Close)

	searchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(searchResponse(blocked.URL + "/config/creds.env"))
	}))
	t.Cleanup(searchSrv.Close)

	// The allowlist deliberately excludes the host the raw URL points at.
	installCodeSearchSeams(t, searchSrv.URL, searchSrv.Client(), []string{"raw.githubusercontent.com"})

	runCodeSearch()

	if n := atomic.LoadInt32(&blockedHits); n != 0 {
		t.Fatalf("off-allowlist host saw %d requests, want 0", n)
	}
	if n := hubMatchCount(t); n != 0 {
		t.Fatalf("published %d matches, want 0", n)
	}
}

// With the config disabled the start guard launches nothing at all, so the
// search API sees zero requests.
func TestRunCodeSearchDisabledIssuesNoRequests(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	newSearchTestSession(t, false)

	var searchHits int32
	searchSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&searchHits, 1)
		_ = json.NewEncoder(w).Encode(searchResponse("https://raw.githubusercontent.com/acme/repo/main/x"))
	}))
	t.Cleanup(searchSrv.Close)

	installCodeSearchSeams(t, searchSrv.URL, searchSrv.Client(), []string{"127.0.0.1"})

	if codeSearchEnabled() {
		t.Fatal("codeSearchEnabled must be false for a disabled config")
	}
	startCodeSearch()

	if n := atomic.LoadInt32(&searchHits); n != 0 {
		t.Fatalf("disabled search saw %d requests, want 0", n)
	}
	if n := hubMatchCount(t); n != 0 {
		t.Fatalf("disabled search published %d matches, want 0", n)
	}
}

// The --search flag enables the source even when the config section is off.
func TestCodeSearchFlagEnables(t *testing.T) {
	newSearchTestSession(t, false)
	if codeSearchEnabled() {
		t.Fatal("disabled config without the flag must not enable search")
	}
	session.Options.Search = searchBoolPtr(true)
	if !codeSearchEnabled() {
		t.Fatal("--search must enable the search source")
	}
}

// maxFileSizeBytes converts KB to bytes, clamping to avoid int64 overflow.
func TestMaxFileSizeBytesOverflow(t *testing.T) {
	// A value larger than MaxInt64/1024 should be clamped to MaxInt64.
	clamped := maxFileSizeBytes(^uint(0))
	if clamped != math.MaxInt64 {
		t.Errorf("maxFileSizeBytes(^uint(0)) = %d, want %d", clamped, math.MaxInt64)
	}

	// A normal value should convert correctly.
	got := maxFileSizeBytes(256)
	if got != 256*1024 {
		t.Errorf("maxFileSizeBytes(256) = %d, want %d", got, 256*1024)
	}

	// Zero should return zero.
	if maxFileSizeBytes(0) != 0 {
		t.Errorf("maxFileSizeBytes(0) = %d, want 0", maxFileSizeBytes(0))
	}
}
