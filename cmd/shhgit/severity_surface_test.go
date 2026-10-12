package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trebor048/shhgot/core"
	"github.com/trebor048/shhgot/core/verifiers"
)

func sevIntPtr(n int) *int { return &n }

// testSeverityMatch builds a hub-only match with the Slice 6 fields populated.
func testSeverityMatch(id, severity string, priority int) *Match {
	verified := severity != ""
	return &Match{
		ID:        id,
		Timestamp: time.Now(),
		Source:    "github",
		URL:       "https://github.com/o/r",
		File:      "creds.env",
		Signature: "OpenAI API Key",
		Matches:   []string{"sk-" + strings.Repeat("a", 48)},
		Secret:    "sk-" + strings.Repeat("a", 48),
		Line:      3,
		Stars:     5,
		Priority:  priority,
		Severity:  severity,
		Verified:  &verified,
	}
}

func broadcastSeverityMatch(t *testing.T, m *Match) {
	t.Helper()
	hub := ensureWebHub()
	select {
	case hub.broadcast <- m:
	case <-time.After(time.Second):
		t.Fatal("hub.broadcast full")
	}
	waitForCond(t, 2*time.Second, func() bool {
		hub.matchesMutex.RLock()
		defer hub.matchesMutex.RUnlock()
		return len(hub.matches) > 0
	})
}

// TestMatchesAPIExposesPriorityAndSeverityDistinctly pins the wire shape:
// a finding carries both the rule priority and the verified severity, plus the
// base URL / context captured in Slice 4.
func TestMatchesAPIExposesPriorityAndSeverityDistinctly(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)

	m := testSeverityMatch("sev-distinct", "critical", 1)
	m.BaseURL = "https://localhost/one-api"
	m.Context = "line before\nSECRET=sk-x\nline after"
	broadcastSeverityMatch(t, m)

	rec := httptest.NewRecorder()
	getMatches(rec, httptest.NewRequest(http.MethodGet, "/api/matches", nil))

	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode /api/matches: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d matches, want 1", len(got))
	}
	item := got[0]
	if item["priority"].(float64) != 1 {
		t.Errorf("priority = %v, want 1", item["priority"])
	}
	if item["severity"] != "critical" {
		t.Errorf("severity = %v, want critical", item["severity"])
	}
	if item["verified"] != true {
		t.Errorf("verified = %v, want true", item["verified"])
	}
	if item["base_url"] != m.BaseURL {
		t.Errorf("base_url = %v, want %q", item["base_url"], m.BaseURL)
	}
	if item["context"] != m.Context {
		t.Errorf("context = %v, want %q", item["context"], m.Context)
	}
}

// TestStatsExposeMatchesBySeverity proves /api/stats emits the severity
// histogram the dashboard sidebar counts from; before this the key was read by
// the UI but never populated, so every count rendered as zero.
func TestStatsExposeMatchesBySeverity(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)

	broadcastSeverityMatch(t, testSeverityMatch("sev-crit", "critical", 3))
	waitForCond(t, 2*time.Second, func() bool {
		hub.matchesMutex.RLock()
		defer hub.matchesMutex.RUnlock()
		return len(hub.matches) == 1
	})
	broadcastSeverityMatch(t, testSeverityMatch("sev-med-a", "medium", 1))
	waitForCond(t, 2*time.Second, func() bool {
		hub.matchesMutex.RLock()
		defer hub.matchesMutex.RUnlock()
		return len(hub.matches) == 2
	})
	broadcastSeverityMatch(t, testSeverityMatch("sev-med-b", "medium", 1))
	waitForCond(t, 2*time.Second, func() bool {
		hub.matchesMutex.RLock()
		defer hub.matchesMutex.RUnlock()
		return len(hub.matches) == 3
	})

	rec := httptest.NewRecorder()
	getStats(rec, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	var stats Stats
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode /api/stats: %v", err)
	}
	if stats.MatchesBySeverity["critical"] != 1 || stats.MatchesBySeverity["medium"] != 2 {
		t.Fatalf("matches_by_severity = %v, want critical:1 medium:2", stats.MatchesBySeverity)
	}
}

// TestOutcomeUpdatesSeverityHistogram proves a later verifier outcome moves the
// match's severity count with it, so the sidebar histogram stays accurate.
func TestOutcomeUpdatesSeverityHistogram(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)

	m := testSeverityMatch("hist-update", "", 1)
	m.Verified = nil
	broadcastSeverityMatch(t, m)
	waitForCond(t, 2*time.Second, func() bool {
		hub.matchesMutex.RLock()
		defer hub.matchesMutex.RUnlock()
		return len(hub.matches) == 1
	})

	hub.matchesMutex.RLock()
	before := hub.stats.MatchesBySeverity[""]
	hub.matchesMutex.RUnlock()
	if before != 1 {
		t.Fatalf("empty-severity count before verification = %d, want 1", before)
	}

	updateHubMatchOutcome(maskedOutcomeKey(m.Secret), verifiers.Outcome{
		Status:  verifiers.Valid,
		Tier:    "GPT-4",
		Balance: "$150.00",
	})

	hub.matchesMutex.RLock()
	hist := copyStringIntMap(hub.stats.MatchesBySeverity)
	hub.matchesMutex.RUnlock()
	if hist["critical"] != 1 || hist[""] != 0 {
		t.Fatalf("histogram after verification = %v, want critical:1 and no empty bucket", hist)
	}
}

// TestPushTokenOutcomeUsesFindingPriority proves the Tokens tab labels a
// quota-limited credential with the same severity as the Matches tab: both must
// classify with the finding's rule priority, not a hard-coded 0.
func TestPushTokenOutcomeUsesFindingPriority(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)

	tokenMu.Lock()
	prev := tokenResults
	tokenResults = nil
	tokenMu.Unlock()
	t.Cleanup(func() {
		tokenMu.Lock()
		tokenResults = prev
		tokenMu.Unlock()
	})

	m := testSeverityMatch("prio-consistency", "", 3) // priority 3 -> critical fallback
	broadcastSeverityMatch(t, m)
	waitForCond(t, 2*time.Second, func() bool {
		hub.matchesMutex.RLock()
		defer hub.matchesMutex.RUnlock()
		return len(hub.matches) == 1
	})

	// QuotaLimited has no verifier-derived severity, so Classify falls back to
	// the rule priority. Both surfaces must therefore agree.
	outcome := verifiers.Outcome{Status: verifiers.QuotaLimited}
	priority := updateHubMatchOutcome(maskedOutcomeKey(m.Secret), outcome)
	pushTokenOutcome(maskedOutcomeKey(m.Secret), "OpenAI", outcome, priority)

	hub.matchesMutex.RLock()
	matchSeverity := hub.matches[0].Severity
	hub.matchesMutex.RUnlock()
	if matchSeverity != "critical" {
		t.Fatalf("hub match severity = %q, want critical (priority 3)", matchSeverity)
	}

	rec := httptest.NewRecorder()
	getTokens(rec, httptest.NewRequest(http.MethodGet, "/api/tokens", nil))
	var out struct {
		Tokens []TokenResult `json:"tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode /api/tokens: %v", err)
	}
	if len(out.Tokens) != 1 {
		t.Fatalf("got %d token rows, want 1", len(out.Tokens))
	}
	if out.Tokens[0].Severity != matchSeverity {
		t.Fatalf("token severity = %q, hub match severity = %q; tabs disagree", out.Tokens[0].Severity, matchSeverity)
	}
}

// TestMatchesAPISeverityFilter proves ?severity= is an exact match and composes
// with the other filters.
func TestMatchesAPISeverityFilter(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)

	broadcastSeverityMatch(t, testSeverityMatch("sev-crit", "critical", 3))
	waitForCond(t, 2*time.Second, func() bool {
		hub.matchesMutex.RLock()
		defer hub.matchesMutex.RUnlock()
		return len(hub.matches) == 1
	})
	broadcastSeverityMatch(t, testSeverityMatch("sev-med", "medium", 1))
	waitForCond(t, 2*time.Second, func() bool {
		hub.matchesMutex.RLock()
		defer hub.matchesMutex.RUnlock()
		return len(hub.matches) == 2
	})
	broadcastSeverityMatch(t, testSeverityMatch("sev-low", "low", 0))
	waitForCond(t, 2*time.Second, func() bool {
		hub.matchesMutex.RLock()
		defer hub.matchesMutex.RUnlock()
		return len(hub.matches) == 3
	})

	decode := func(query string) []*Match {
		t.Helper()
		rec := httptest.NewRecorder()
		getMatches(rec, httptest.NewRequest(http.MethodGet, "/api/matches?"+query, nil))
		var got []*Match
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode /api/matches?%s: %v", query, err)
		}
		return got
	}

	if got := decode("severity=critical"); len(got) != 1 || got[0].Severity != "critical" {
		t.Fatalf("severity=critical returned %+v, want only the critical finding", got)
	}
	if got := decode("severity=bogus"); len(got) != 0 {
		t.Fatalf("severity=bogus returned %d findings, want 0", len(got))
	}
	if got := decode(""); len(got) != 3 {
		t.Fatalf("no filter returned %d findings, want 3", len(got))
	}
}

// TestPublishComputesSeverityContextAndBaseURL drives the real publish path: the
// event's priority is classified into a severity, and (with context_lines on)
// the context window and relay base URL are attached to the dashboard match.
func TestPublishComputesSeverityContextAndBaseURL(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)

	s := newStoreTestSession(t, false)
	s.Config.Scanning.ContextLines = sevIntPtr(2)

	secret := "sk-" + strings.Repeat("b", 48)
	body := strings.Join([]string{
		"alpha",
		"beta",
		"OPENAI_KEY=" + secret,
		"endpoint = https://localhost/one-api/v1",
		"gamma",
		"delta",
	}, "\n")

	publish(&MatchEvent{
		Source:      core.LOCAL_SOURCE,
		Url:         "https://github.com/o/r",
		Signature:   "OpenAI API Key",
		File:        "creds.env",
		Matches:     []string{secret},
		Priority:    1, // medium when no verifier outcome is known
		Secret:      secret,
		SecretLine:  3,
		FileContent: body,
	})

	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 1 })

	hub.matchesMutex.RLock()
	m := *hub.matches[0]
	hub.matchesMutex.RUnlock()

	if m.Severity != "medium" {
		t.Errorf("severity = %q, want medium (priority 1 fallback)", m.Severity)
	}
	if m.Priority != 1 {
		t.Errorf("priority = %d, want 1", m.Priority)
	}
	if m.Verified != nil {
		t.Errorf("verified = %v, want nil when no outcome is known", *m.Verified)
	}
	if !strings.Contains(m.Context, "OPENAI_KEY="+secret) || !strings.Contains(m.Context, "beta") {
		t.Errorf("context window is missing data: %q", m.Context)
	}
	if lines := strings.Count(m.Context, "\n") + 1; lines > 5 {
		t.Errorf("context has %d lines, want at most 5 (2*2+1)", lines)
	}
	if m.BaseURL != "https://localhost/one-api" {
		t.Errorf("base_url = %q, want https://localhost/one-api", m.BaseURL)
	}
}

// TestPublishHonoursDisabledContext proves the default (context_lines = 0) adds
// no context or base URL, so an existing operator's output is unchanged.
func TestPublishHonoursDisabledContext(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)
	newStoreTestSession(t, false) // Config.Scanning.ContextLines stays nil -> 0

	secret := "sk-" + strings.Repeat("c", 48)
	publish(&MatchEvent{
		Source:      core.LOCAL_SOURCE,
		Url:         "https://github.com/o/r",
		Signature:   "OpenAI API Key",
		File:        "creds.env",
		Matches:     []string{secret},
		Priority:    2,
		Secret:      secret,
		SecretLine:  1,
		FileContent: "OPENAI_KEY=" + secret + "\nendpoint = https://localhost/one-api",
	})

	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 1 })

	hub.matchesMutex.RLock()
	m := *hub.matches[0]
	hub.matchesMutex.RUnlock()

	if m.Context != "" {
		t.Errorf("context = %q, want empty with context_lines disabled", m.Context)
	}
	if m.BaseURL != "" {
		t.Errorf("base_url = %q, want empty with context_lines disabled", m.BaseURL)
	}
	if m.Severity != "high" {
		t.Errorf("severity = %q, want high (priority 2 fallback)", m.Severity)
	}
}

// TestOutcomeUpdatesHubMatchSeverity covers the OnOutcome update path: a valid
// funded top-tier key reclassifies an already-published match to critical and
// marks it verified.
func TestOutcomeUpdatesHubMatchSeverity(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)

	m := testSeverityMatch("sev-update", "", 1) // severity empty until verified
	m.Verified = nil
	broadcastSeverityMatch(t, m)

	updateHubMatchOutcome(maskedOutcomeKey(m.Secret), verifiers.Outcome{
		Status:  verifiers.Valid,
		Tier:    "GPT-4",
		Balance: "$150.00",
	})

	hub.matchesMutex.RLock()
	got := *hub.matches[0]
	hub.matchesMutex.RUnlock()

	if got.Severity != "critical" {
		t.Errorf("severity = %q, want critical", got.Severity)
	}
	if got.Verified == nil || !*got.Verified {
		t.Errorf("verified = %v, want true", got.Verified)
	}
	if got.Priority != 1 {
		t.Errorf("priority = %d, want it unchanged at 1", got.Priority)
	}
}

// TestInstallOutcomeRecorderCoversTerminalMode proves every mode records the
// verifier outcome. Terminal mode has no UI callback, so before this the
// outcome cache stayed empty and publish() used the priority fallback even for
// a verified funded key.
func TestInstallOutcomeRecorderCoversTerminalMode(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)

	s := newStoreTestSession(t, false)
	s.TokenValidator = core.NewTokenValidator(s.Log)
	if s.TokenValidator.OnOutcome != nil {
		t.Fatal("precondition: a fresh validator has no OnOutcome callback")
	}

	installOutcomeRecorder()
	if s.TokenValidator.OnOutcome == nil {
		t.Fatal("terminal mode must install an OnOutcome recorder")
	}

	secret := "sk-" + strings.Repeat("d", 48)
	// TestAndLogToken fires OnOutcome with the masked token; mimic that here.
	s.TokenValidator.OnOutcome(maskedOutcomeKey(secret), "OpenAI", verifiers.Outcome{
		Status:  verifiers.Valid,
		Tier:    "GPT-4",
		Balance: "$150.00",
	})

	publish(&MatchEvent{
		Source:     core.LOCAL_SOURCE,
		Url:        "https://github.com/o/r",
		Signature:  "OpenAI API Key",
		File:       "creds.env",
		Matches:    []string{secret},
		Secret:     secret,
		Priority:   0,
		SecretLine: 1,
	})

	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 1 })

	hub.matchesMutex.RLock()
	m := *hub.matches[0]
	hub.matchesMutex.RUnlock()

	if m.Severity != "critical" {
		t.Errorf("severity = %q, want critical from the verified funded key", m.Severity)
	}
	if m.Verified == nil || !*m.Verified {
		t.Errorf("verified = %v, want true", m.Verified)
	}
}

// TestPushTokenOutcomeEnrichesTokenResult proves the enriched TokenResult is
// what the Tokens tab reads: one row per token, carrying the verifier metadata.
func TestPushTokenOutcomeEnrichesTokenResult(t *testing.T) {
	tokenMu.Lock()
	prev := tokenResults
	tokenResults = nil
	tokenMu.Unlock()
	t.Cleanup(func() {
		tokenMu.Lock()
		tokenResults = prev
		tokenMu.Unlock()
	})

	pushTokenResult("sk-masked...", true, "OpenAI")
	pushTokenOutcome("sk-masked...", "OpenAI", verifiers.Outcome{
		Status:    verifiers.Valid,
		Identity:  "acme",
		Tier:      "GPT-4",
		Balance:   "$150.00",
		RPM:       500,
		HighValue: true,
	}, 3)

	rec := httptest.NewRecorder()
	getTokens(rec, httptest.NewRequest(http.MethodGet, "/api/tokens", nil))
	var out struct {
		Tokens []TokenResult `json:"tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode /api/tokens: %v", err)
	}
	if len(out.Tokens) != 1 {
		t.Fatalf("got %d token rows, want exactly 1 (enriched in place)", len(out.Tokens))
	}
	tr := out.Tokens[0]
	if tr.Status != "valid" || tr.Tier != "GPT-4" || tr.Balance != "$150.00" ||
		tr.RPM != 500 || !tr.HighValue || tr.Identity != "acme" || tr.Category != "AI" {
		t.Errorf("enriched token = %+v", tr)
	}
	if tr.Severity != "critical" {
		t.Errorf("token severity = %q, want critical", tr.Severity)
	}
}

// TestVerifierCategoryMapsProviderDisplayNames is a regression test for
// verifierCategory comparing the provider display name directly against
// registry verifier names: the two differ for these providers, so the category
// came back empty and the Tokens tab rendered no category at all.
func TestVerifierCategoryMapsProviderDisplayNames(t *testing.T) {
	cases := []struct {
		provider string
		want     string
	}{
		{core.ProviderGoogle, "AI"},
		{core.ProviderXAI, "AI"},
		{core.ProviderTogether, "AI"},
		{core.ProviderKimi, "AI"},
		{core.ProviderQwen, "AI"},
		{core.ProviderDockerHub, "VCS"},
	}
	for _, c := range cases {
		if got := verifierCategory(c.provider); got != c.want {
			t.Errorf("verifierCategory(%q) = %q, want %q", c.provider, got, c.want)
		}
	}
}

// TestTokensTabSeverityAgreesWithMatchesTab reproduces the real ordering: the
// verifier (and therefore OnOutcome) fires during matching, before publish has
// stored the finding, so updateHubMatchOutcome cannot know the rule priority
// and pushTokenOutcome classifies with priority 0. Once publish computes the
// finding's real severity it must copy it onto the already-created Tokens row,
// so the Matches and Tokens tabs agree.
func TestTokensTabSeverityAgreesWithMatchesTab(t *testing.T) {
	hub := ensureWebHub()
	resetHubForTest(hub)

	tokenMu.Lock()
	prevTokens := tokenResults
	tokenResults = nil
	tokenMu.Unlock()
	t.Cleanup(func() {
		tokenMu.Lock()
		tokenResults = prevTokens
		tokenMu.Unlock()
	})

	s := newStoreTestSession(t, false)
	s.TokenValidator = core.NewTokenValidator(s.Log)
	s.TokenValidator.OnOutcome = func(token string, provider string, o verifiers.Outcome) {
		rememberOutcome(token, provider, o)
		priority := updateHubMatchOutcome(token, o)
		pushTokenOutcome(token, provider, o, priority)
	}

	secret := "sk-" + strings.Repeat("f", 48)
	// QuotaLimited carries no verifier-derived severity, so Classify falls back
	// to the priority the callback knows at this point: 0, because the finding
	// is not in the hub yet.
	s.TokenValidator.OnOutcome(maskedOutcomeKey(secret), "OpenAI", verifiers.Outcome{Status: verifiers.QuotaLimited})

	publish(&MatchEvent{
		Source:     core.LOCAL_SOURCE,
		Url:        "https://github.com/o/r",
		Signature:  "OpenAI API Key",
		File:       "creds.env",
		Matches:    []string{secret},
		Secret:     secret,
		Priority:   3,
		SecretLine: 1,
	})

	waitForCond(t, 2*time.Second, func() bool { return hubMatchCount(t) == 1 })

	hub.matchesMutex.RLock()
	matchSeverity := hub.matches[0].Severity
	hub.matchesMutex.RUnlock()
	if matchSeverity != "critical" {
		t.Fatalf("match severity = %q, want critical (priority 3)", matchSeverity)
	}

	rec := httptest.NewRecorder()
	getTokens(rec, httptest.NewRequest(http.MethodGet, "/api/tokens", nil))
	var out struct {
		Tokens []TokenResult `json:"tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode /api/tokens: %v", err)
	}
	if len(out.Tokens) != 1 {
		t.Fatalf("got %d token rows, want 1", len(out.Tokens))
	}
	if out.Tokens[0].Severity != matchSeverity {
		t.Fatalf("token severity = %q, match severity = %q; tabs disagree", out.Tokens[0].Severity, matchSeverity)
	}
}
