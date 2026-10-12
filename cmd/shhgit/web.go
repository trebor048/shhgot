package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/trebor048/shhgot/core"
	"github.com/trebor048/shhgot/core/verifiers"
)

// Match represents a detected secret match.
//
// Priority is the signature's static rule priority; Severity is the dynamic
// finding severity derived from the verifier outcome (or the priority when the
// secret was never verified). They are distinct fields on purpose: the
// dashboard labels priority as "rule priority" and severity as "verified
// severity" so neither can be mistaken for the other.
type Match struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Source    string    `json:"source"`
	URL       string    `json:"url"`
	File      string    `json:"file"`
	Signature string    `json:"signature"`
	Matches   []string  `json:"matches"`
	Secret    string    `json:"secret,omitempty"` // first raw secret value found
	Line      int       `json:"line,omitempty"`   // 1-based line of the first secret occurrence
	HasFile   bool      `json:"has_file,omitempty"`
	Stars     int       `json:"stars"`
	Priority  int       `json:"priority"`
	Color     string    `json:"color"`

	// BaseURL is the ranked relay/Azure endpoint extracted around the finding
	// (Slice 4); Context is the bounded window of file lines around the secret.
	// Both are empty unless scanning.context_lines > 0.
	BaseURL  string `json:"base_url,omitempty"`
	Context  string `json:"context,omitempty"`
	Severity string `json:"severity,omitempty"`
	// Verified reports whether a live verifier accepted the secret. nil means
	// no verifier outcome is known (never verified, or verification pending).
	Verified *bool `json:"verified,omitempty"`
}

// MatchFileDetail carries the full file content behind a match so the
// dashboard can render a "view file" modal with syntax highlighting and the
// secret highlighted. Content is captured at scan time because cloned repos
// are deleted after scanning.
type MatchFileDetail struct {
	ID         string `json:"id"`
	URL        string `json:"url"`
	File       string `json:"file"`
	Content    string `json:"content"`
	Secret     string `json:"secret"`
	SecretLine int    `json:"secret_line"`
	Truncated  bool   `json:"truncated"`
}

// TokenResult records one live token-validation outcome for the web UI. The
// first four fields are the basic verdict; the rest carry the verifier's richer
// metadata (Slice 5.7) and are empty/zero when the legacy validation path
// produced the verdict.
type TokenResult struct {
	Token     string    `json:"token"`
	Valid     bool      `json:"valid"`
	Provider  string    `json:"provider"`
	Status    string    `json:"status,omitempty"`
	Category  string    `json:"category,omitempty"`
	Identity  string    `json:"identity,omitempty"`
	Tier      string    `json:"tier,omitempty"`
	Balance   string    `json:"balance,omitempty"`
	RPM       int       `json:"rpm,omitempty"`
	HighValue bool      `json:"high_value,omitempty"`
	Severity  string    `json:"severity,omitempty"`
	LatencyMS int64     `json:"latency_ms,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// Stats holds aggregated statistics.
type Stats struct {
	TotalMatches       int             `json:"total_matches"`
	MatchesBySource    map[string]int  `json:"matches_by_source"`
	MatchesBySignature map[string]int  `json:"matches_by_signature"`
	MatchesByPriority  map[int]int     `json:"matches_by_priority"`
	MatchesBySeverity  map[string]int  `json:"matches_by_severity"`
	TopSignatures      []SignatureStat `json:"top_signatures"`
	LastUpdated        time.Time       `json:"last_updated"`
	// AIFilterDegraded reports whether the AI false-positive filter's classifier
	// has failed enough times in a row that its verdicts are unreliable. It is
	// false when the filter is disabled. Populated by getStats, because the hub
	// itself has no filter reference.
	AIFilterDegraded bool `json:"ai_filter_degraded"`
}

// SignatureStat represents a signature with its match count.
type SignatureStat struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// snapshot returns a deep copy of the statistics that is safe to encode after
// matchesMutex has been released.
//
// json.Marshal walks maps, so handing the live value to an encoder while
// WebHub.run mutates it is a data race that aborts the whole process with
// "fatal error: concurrent map read and map write" — not a recoverable 500.
// Every caller that encodes outside the lock must use this copy.
func (s Stats) snapshot() Stats {
	cp := s
	cp.MatchesBySource = copyStringIntMap(s.MatchesBySource)
	cp.MatchesBySignature = copyStringIntMap(s.MatchesBySignature)
	cp.MatchesBySeverity = copyStringIntMap(s.MatchesBySeverity)
	cp.MatchesByPriority = make(map[int]int, len(s.MatchesByPriority))
	for k, v := range s.MatchesByPriority {
		cp.MatchesByPriority[k] = v
	}
	cp.TopSignatures = append([]SignatureStat(nil), s.TopSignatures...)
	return cp
}

func copyStringIntMap(m map[string]int) map[string]int {
	cp := make(map[string]int, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

// WebHub manages matches and broadcasts.
// WebHub owns the match history and the counters behind the JSON API.
//
// Live pushes to browsers go through feedClients (the SSE handler), not through
// here. This struct used to carry a second per-client fan-out - a clients map
// plus register/unregister channels and a WebClient type - that nothing ever
// registered into, so the map was permanently empty and the fan-out loop below
// never delivered anything.
type WebHub struct {
	broadcast    chan *Match
	matches      []*Match
	stats        Stats
	matchesMutex sync.RWMutex
}

var (
	webHub     *WebHub
	hubOnce    sync.Once
	maxMatches = 10000

	// Log ring buffer served at /api/logs.
	logMu     sync.Mutex
	logBuffer []string
	maxLogs   = 1000

	// Token validation results served at /api/tokens.
	tokenMu      sync.Mutex
	tokenResults []TokenResult
	maxTokens    = 1000

	// Full file content behind matches, served at /api/file and used by the
	// dashboard's "view file" modal. Bounded: only the most recent matches
	// keep their file content, and each file is capped in size.
	fileMu            sync.Mutex
	fileDetails       = make(map[string]*MatchFileDetail)
	fileOrder         []string
	maxFileDetails    = 100
	maxStoreFileBytes = 128 * 1024
)

// storeMatchFile keeps the full file content behind a match ID so the
// dashboard can render it on demand. Content is truncated to
// maxStoreFileBytes and only the maxFileDetails most recent files are kept.
func storeMatchFile(id string, url string, file string, content string, secret string, secretLine int) {
	if id == "" || content == "" {
		return
	}
	truncated := false
	if len(content) > maxStoreFileBytes {
		// Cut on a rune boundary: slicing at an arbitrary byte split a multi-byte
		// character in half, and json.Marshal then replaced the fragment with
		// U+FFFD, so the file viewer showed a stray replacement glyph at the cut.
		cut := maxStoreFileBytes
		for cut > 0 && !utf8.RuneStart(content[cut]) {
			cut--
		}
		content = content[:cut]
		truncated = true
	}
	d := &MatchFileDetail{
		ID:         id,
		URL:        url,
		File:       file,
		Content:    content,
		Secret:     secret,
		SecretLine: secretLine,
		Truncated:  truncated,
	}
	fileMu.Lock()
	// Re-storing an id must refresh its position in the eviction order, not just
	// its content: a file captured again later was still holding its original,
	// older slot, so it could be evicted before files that were actually seen
	// earlier. Drop any previous occurrence and append, making this id the most
	// recent.
	for i, oid := range fileOrder {
		if oid == id {
			fileOrder = append(fileOrder[:i], fileOrder[i+1:]...)
			break
		}
	}
	fileOrder = append(fileOrder, id)
	fileDetails[id] = d
	for len(fileOrder) > maxFileDetails {
		old := fileOrder[0]
		fileOrder = fileOrder[1:]
		delete(fileDetails, old)
	}
	fileMu.Unlock()
}

// getMatchFile serves the stored file content for one match ID.
func getMatchFile(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "missing id")
		return
	}
	fileMu.Lock()
	d, ok := fileDetails[id]
	fileMu.Unlock()
	if !ok {
		// JSON like the rest of the API, including this route's own errors: the
		// dashboard ignores the body, but a plain-text one contradicted the
		// documented contract that /api/... answers 404 with JSON.
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(d)
}

// FeedEvent is a single event pushed to connected dashboards over SSE.
type FeedEvent struct {
	Type     string            `json:"type"`
	Match    *Match            `json:"match,omitempty"`
	Matches  []*Match          `json:"matches,omitempty"`
	Log      string            `json:"log,omitempty"`
	Token    *TokenResult      `json:"token,omitempty"`
	Activity *ActivitySnapshot `json:"activity,omitempty"`
	Stats    *Stats            `json:"stats,omitempty"`
	Review   *ReviewEvent      `json:"review,omitempty"`
}

// ReviewEvent is the lightweight review summary broadcast to dashboards when a
// review changes state. The dashboard refetches the full review from
// /api/review/<id> when it needs the body.
type ReviewEvent struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Error    string `json:"error,omitempty"`
}

var (
	feedMu      sync.Mutex
	feedClients = make(map[chan []byte]bool)
)

// broadcastFeed marshals an event and non-blocking sends it to every
// connected dashboard (dropped for slow clients). With no dashboard attached
// there is nothing to send, so the marshal - the expensive part, run for every
// log line - is skipped entirely.
func broadcastFeed(ev FeedEvent) {
	feedMu.Lock()
	if len(feedClients) == 0 {
		feedMu.Unlock()
		return
	}
	feedMu.Unlock()

	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	feedMu.Lock()
	for ch := range feedClients {
		select {
		case ch <- data:
		default:
		}
	}
	feedMu.Unlock()
}

// configureWebLimits applies the performance.max_* ring-buffer caps. They are
// package vars read by the hub and the stores, so this must run before the web
// server starts.
func configureWebLimits(p core.PerformanceConfig) {
	maxMatches = p.Int(p.MaxMatches, maxMatches)
	maxLogs = p.Int(p.MaxLogLines, maxLogs)
	maxTokens = p.Int(p.MaxTokenResults, maxTokens)
	maxFileDetails = p.Int(p.MaxFileDetails, maxFileDetails)
	if kb := p.Int(p.MaxFileDetailKB, 0); kb > 0 {
		// Clamp before the *1024: an operator-supplied kb near MaxInt overflows
		// to a negative maxStoreFileBytes, which then panics on content[:cut].
		const maxDetailKB = 1 << 30 // 1 TiB of bytes; far above any sane buffer
		if kb > maxDetailKB {
			kb = maxDetailKB
		}
		maxStoreFileBytes = kb * 1024
	}
}

var ansiRe = regexp.MustCompile("[\u001B\u009B][[\\]()#;?]*(?:(?:(?:[a-zA-Z\\d]*(?:;[a-zA-Z\\d]*)*)?\u0007)|(?:(?:\\d{1,4}(?:;\\d{0,4})*)?[\\dA-PRZcf-ntqry=><~]))")

// stripAnsi removes ANSI colour/control sequences from a log line.
func stripAnsi(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// appendLogLine appends a raw (ANSI-coloured) log line to the web log ring
// buffer. The dashboard renders the ANSI escapes client-side so the Logs tab
// matches the terminal colours exactly (including OSC-8 hyperlinks).
// stripAnsi is retained for callers that need plain text.
func appendLogLine(line string) {
	cleaned := strings.TrimSuffix(line, "\n")
	logMu.Lock()
	logBuffer = append(logBuffer, cleaned)
	if len(logBuffer) > maxLogs {
		logBuffer = logBuffer[len(logBuffer)-maxLogs:]
	}
	logMu.Unlock()
	broadcastFeed(FeedEvent{Type: "log", Log: cleaned})
}

// pushTokenResult records a live token-validation outcome for the web UI.
func pushTokenResult(token string, valid bool, provider string) {
	res := TokenResult{
		Token:     token,
		Valid:     valid,
		Provider:  provider,
		Timestamp: time.Now(),
	}
	tokenMu.Lock()
	tokenResults = append(tokenResults, res)
	if len(tokenResults) > maxTokens {
		tokenResults = tokenResults[len(tokenResults)-maxTokens:]
	}
	tokenMu.Unlock()
	broadcastFeed(FeedEvent{Type: "token", Token: &res})
}

// pushTokenOutcome enriches the most recent TokenResult for token with the
// verifier's full outcome, or appends one when OnTokenResult recorded nothing
// first. It is the OnOutcome half of the token path: OnTokenResult keeps its
// published signature, and this adds status/category/identity/tier/balance/RPM/
// severity without changing it.
//
// priority is the rule priority of the finding this secret belongs to, passed
// through from updateHubMatchOutcome. core.Classify only consults it when the
// outcome does not itself decide the severity (quota-limited/unverified), so
// this keeps the Tokens tab's label identical to the Matches tab's label
// instead of always falling back to priority 0.
func pushTokenOutcome(token, provider string, o verifiers.Outcome, priority int) {
	res := TokenResult{
		Token:     token,
		Valid:     o.Status == verifiers.Valid || o.Status == verifiers.QuotaLimited,
		Provider:  provider,
		Status:    string(o.Status),
		Category:  verifierCategory(provider),
		Identity:  o.Identity,
		Tier:      o.Tier,
		Balance:   o.Balance,
		RPM:       o.RPM,
		HighValue: o.HighValue,
		Severity:  core.Classify(priority, &o).String(),
		Timestamp: time.Now(),
	}
	tokenMu.Lock()
	updated := false
	for i := len(tokenResults) - 1; i >= 0; i-- {
		if tokenResults[i].Token != token {
			continue
		}
		res.Timestamp = tokenResults[i].Timestamp
		tokenResults[i] = res
		updated = true
		break
	}
	if !updated {
		tokenResults = append(tokenResults, res)
		if len(tokenResults) > maxTokens {
			tokenResults = tokenResults[len(tokenResults)-maxTokens:]
		}
	}
	tokenMu.Unlock()
	broadcastFeed(FeedEvent{Type: "token", Token: &res})
}

// syncTokenSeverity overwrites the severity of the Tokens-tab row for the masked
// token so it agrees with the Matches tab. applyVerifier (and therefore
// OnOutcome, which pushes the row) runs during matching, before publish stores
// the match and knows its rule priority, so the row was first classified with
// priority 0. publish() calls this once it has the finding's real severity,
// passing the same value it gave the match.
func syncTokenSeverity(token, severity string) {
	if token == "" || severity == "" {
		return
	}
	tokenMu.Lock()
	var updated *TokenResult
	for i := len(tokenResults) - 1; i >= 0; i-- {
		if tokenResults[i].Token != token {
			continue
		}
		if tokenResults[i].Severity == severity {
			tokenMu.Unlock()
			return
		}
		cp := tokenResults[i]
		cp.Severity = severity
		tokenResults[i] = cp
		updated = &cp
		break
	}
	tokenMu.Unlock()
	if updated != nil {
		broadcastFeed(FeedEvent{Type: "token", Token: updated})
	}
}

// verifierCategory resolves a detected provider display name ("OpenAI") to the
// registry verifier's category ("AI"), or "" when nothing matches. It maps the
// provider through core's provider->verifier table first: comparing the display
// name directly against verifier names only worked where the two coincided, and
// returned "" for Google/Gemini, xAI/Grok, Together AI, Kimi/Moonshot,
// Qwen/Alibaba and Docker Hub.
func verifierCategory(provider string) string {
	name, ok := core.VerifierNameForProvider(provider)
	if !ok {
		return ""
	}
	if v, found := verifiers.Lookup(name); found {
		return v.Category
	}
	return ""
}

// updateHubMatchOutcome refreshes the Severity and Verified fields of every
// match in the hub whose secret begins with the masked token a verifier just
// resolved. The callback only carries a masked token, so the comparison is on
// the unmasked prefix and is best-effort by design.
//
// The hub slice is updated copy-on-write: an entry is replaced, never mutated
// in place. A snapshot or SSE encoder that already captured the old pointer is
// then guaranteed to see a consistent struct, so this cannot race them.
//
// It returns the rule priority of the first matching finding, or 0 when none is
// held, so the caller can classify the Tokens tab row with the same priority
// and the two tabs cannot disagree.
func updateHubMatchOutcome(maskedToken string, o verifiers.Outcome) int {
	hub := webHub
	if hub == nil {
		return 0
	}
	prefix := strings.TrimSuffix(maskedToken, "...")
	if prefix == "" {
		return 0
	}
	priority := 0
	found := false
	var broadcastMatches []*Match
	hub.matchesMutex.Lock()
	for i, m := range hub.matches {
		if m == nil || len(m.Secret) < len(prefix) || !strings.HasPrefix(m.Secret, prefix) {
			continue
		}
		if !found {
			priority = m.Priority
			found = true
		}
		cp := *m
		newSeverity := core.Classify(cp.Priority, &o).String()
		// Keep the severity histogram the sidebar reads in step with the
		// reclassification: the match was counted under its old severity when
		// the hub stored it.
		if newSeverity != cp.Severity {
			if hub.stats.MatchesBySeverity[cp.Severity] > 0 {
				hub.stats.MatchesBySeverity[cp.Severity]--
				if hub.stats.MatchesBySeverity[cp.Severity] == 0 {
					delete(hub.stats.MatchesBySeverity, cp.Severity)
				}
			}
			hub.stats.MatchesBySeverity[newSeverity]++
		}
		cp.Severity = newSeverity
		// A quota-limited credential is real, just throttled: core treats it as
		// kept (outcomeValid), so it counts as verified here too.
		verified := o.Status == verifiers.Valid || o.Status == verifiers.QuotaLimited
		cp.Verified = &verified
		hub.matches[i] = &cp
		broadcastMatches = append(broadcastMatches, &cp)
	}
	statsCopy := hub.stats.snapshot()
	matchesCopy := append([]*Match(nil), hub.matches...)
	hub.matchesMutex.Unlock()

	// Dashboards only learn about new matches through the live feed; an updated
	// severity/verified flag on an existing match would otherwise stay stale
	// until the page was refreshed. Send a snapshot event so every client
	// resyncs in place.
	if len(broadcastMatches) > 0 {
		act := activity.Snapshot()
		broadcastFeed(FeedEvent{
			Type:     "snapshot",
			Matches:  matchesCopy,
			Stats:    &statsCopy,
			Activity: &act,
		})
	}
	return priority
}

// ensureWebHub lazily creates the singleton web hub and starts its run loop.
func ensureWebHub() *WebHub {
	hubOnce.Do(func() {
		webHub = newWebHub()
		go webHub.run()
	})
	return webHub
}

func newWebHub() *WebHub {
	return &WebHub{
		broadcast: make(chan *Match, 256),
		matches:   make([]*Match, 0, maxMatches),
		stats: Stats{
			MatchesBySource:    make(map[string]int),
			MatchesBySignature: make(map[string]int),
			MatchesByPriority:  make(map[int]int),
			MatchesBySeverity:  make(map[string]int),
		},
	}
}

func (h *WebHub) run() {
	for match := range h.broadcast {
		h.matchesMutex.Lock()
		h.matches = append(h.matches, match)
		if len(h.matches) > maxMatches {
			h.matches = h.matches[len(h.matches)-maxMatches:]
		}

		h.stats.TotalMatches++
		h.stats.MatchesBySource[match.Source]++
		h.stats.MatchesBySignature[match.Signature]++
		h.stats.MatchesByPriority[match.Priority]++
		h.stats.MatchesBySeverity[match.Severity]++
		h.stats.LastUpdated = time.Now()

		h.updateTopSignatures()
		statsCopy := h.stats.snapshot()
		h.matchesMutex.Unlock()

		broadcastFeed(FeedEvent{Type: "match", Match: match, Stats: &statsCopy})
	}
}

func (h *WebHub) updateTopSignatures() {
	type kv struct {
		Key   string
		Value int
	}
	var ss []kv
	for k, v := range h.stats.MatchesBySignature {
		ss = append(ss, kv{k, v})
	}
	sort.Slice(ss, func(i, j int) bool { return ss[i].Value > ss[j].Value })
	limit := 10
	if len(ss) < limit {
		limit = len(ss)
	}
	h.stats.TopSignatures = make([]SignatureStat, limit)
	for i := 0; i < limit; i++ {
		h.stats.TopSignatures[i] = SignatureStat{Name: ss[i].Key, Count: ss[i].Value}
	}
}

// ReceiveMatch handles POST requests with match data (external CLI push).
func receiveMatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var match Match
	// Cap the body the same way the review and settings APIs do: this route is
	// reachable without credentials, so an unbounded decode would let any local
	// process - or a rebound page - grow the process's memory at will.
	// Permissive on field names (external tools push here) but strict on
	// trailing data.
	if err := decodeJSONPermissive(w, r, &match); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	if match.ID == "" {
		match.ID = newMatchID()
	}
	if match.Timestamp.IsZero() {
		match.Timestamp = time.Now()
	}

	ensureWebHub().broadcast <- &match
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// GetStats returns current statistics.
func getStats(w http.ResponseWriter, r *http.Request) {
	hub := ensureWebHub()
	hub.matchesMutex.RLock()
	stats := hub.stats.snapshot()
	hub.matchesMutex.RUnlock()

	// The hub tracks matches, not the AI filter, so the degraded flag is read
	// here rather than copied through the hub's own snapshot.
	stats.AIFilterDegraded = aiFilterDegraded()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// GetMatches returns all matches with optional filtering.
func getMatches(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	signature := r.URL.Query().Get("signature")
	priority := r.URL.Query().Get("priority")
	severity := r.URL.Query().Get("severity")

	hub := ensureWebHub()
	hub.matchesMutex.RLock()
	defer hub.matchesMutex.RUnlock()

	filtered := make([]*Match, 0)
	for _, m := range hub.matches {
		if source != "" && m.Source != source {
			continue
		}
		if signature != "" && m.Signature != signature {
			continue
		}
		if priority != "" && fmt.Sprintf("%d", m.Priority) != priority {
			continue
		}
		if severity != "" && m.Severity != severity {
			continue
		}
		filtered = append(filtered, m)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(filtered)
}

// WsHandler returns a snapshot of matches and stats.
func wsHandler(w http.ResponseWriter, r *http.Request) {
	hub := ensureWebHub()
	hub.matchesMutex.RLock()
	// Copy both the slice and the statistics: everything below is encoded
	// after the lock is released.
	data := map[string]interface{}{
		"matches": append([]*Match(nil), hub.matches...),
		"stats":   hub.stats.snapshot(),
	}
	hub.matchesMutex.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

// GetLogs returns the recent scanner log lines.
func getLogs(w http.ResponseWriter, r *http.Request) {
	logMu.Lock()
	logs := make([]string, len(logBuffer))
	copy(logs, logBuffer)
	logMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"logs": logs})
}

// GetTokens returns the recent token-validation results.
func getTokens(w http.ResponseWriter, r *http.Request) {
	tokenMu.Lock()
	tokens := make([]TokenResult, len(tokenResults))
	copy(tokens, tokenResults)
	tokenMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"tokens": tokens})
}

// GetSignatures returns the distinct signature names with counts.
func getSignatures(w http.ResponseWriter, r *http.Request) {
	hub := ensureWebHub()
	hub.matchesMutex.RLock()
	sigs := make([]SignatureStat, 0)
	for name, count := range hub.stats.MatchesBySignature {
		sigs = append(sigs, SignatureStat{Name: name, Count: count})
	}
	hub.matchesMutex.RUnlock()

	sort.Slice(sigs, func(i, j int) bool { return sigs[i].Count > sigs[j].Count })

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"signatures": sigs})
}

// GetActivity returns the current download/scan activity snapshot.
func getActivity(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(activity.Snapshot())
}

// skipRepo abandons an in-flight repository at the operator's request, so a
// single repository that hangs or is unwanted does not have to be waited out.
//
// It only records the skip: the scanner checks the set before cloning, again
// after a clone and once per file during a scan. A clone already in flight cannot
// be interrupted, so the row disappears from the dashboard immediately while the
// work stops at the next checkpoint.
func skipRepo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var in struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	url := strings.TrimSpace(in.URL)
	if url == "" {
		writeJSONError(w, http.StatusBadRequest, "url is required")
		return
	}

	activity.Skip(url)
	writeJSON(w, http.StatusOK, map[string]string{"status": "skipped", "url": url})
}

// GetScanProgress returns the scanner's aggregate progress counters
// (repositories scanned, files processed, matches found, throughput, ETA).
//
// This is the endpoint the dashboard's scan-progress panel polls. It exposes
// the repository and file currently being scanned, which is why it carries the
// same localGuard as the rest of the captured-material API.
func getScanProgress(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.GlobalScanProgress.GetSnapshot())
}

// GetRegexStats returns the regex engine's cache and compilation statistics for
// the dashboard's performance panel.
//
// The optimizer is created by the scanner's session, so a request that arrives
// before it exists (or in a build that never initialises it) must answer with
// zeroed counters rather than dereferencing a nil singleton.
func getRegexStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if core.GlobalRegexOptimizer == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"patterns_compiled": 0,
			"patterns_total":    0,
			"max_workers":       0,
		})
		return
	}
	json.NewEncoder(w).Encode(core.GlobalRegexOptimizer.Stats())
}

// getMetrics returns the process-wide metrics collector's snapshot. Without a
// format parameter the response is JSON (core.DefaultMetrics.Snapshot()); with
// ?format=prometheus it is the Prometheus text exposition format. An optional
// ?subsystem=<name> narrows the JSON (and prometheus) output to one subsystem.
//
// Like the rest of the captured-material API it carries localGuard even though
// it exposes no secrets: it reveals scan throughput and timings, and a rebound
// page should not be able to fingerprint the running instance for free.
func getMetrics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var snapshot map[string]core.LatencyStats
	if subsystem := strings.TrimSpace(q.Get("subsystem")); subsystem != "" {
		snapshot = map[string]core.LatencyStats{subsystem: core.DefaultMetrics.LatencyStats(subsystem)}
	} else {
		snapshot = core.DefaultMetrics.Snapshot()
	}
	if strings.EqualFold(strings.TrimSpace(q.Get("format")), "prometheus") {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprint(w, prometheusMetrics(snapshot))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snapshot)
}

// prometheusMetrics renders a metrics snapshot in the Prometheus text
// exposition format. Subsystem names are sorted and the quantile set is fixed,
// so the output is stable run to run.
func prometheusMetrics(snapshot map[string]core.LatencyStats) string {
	names := make([]string, 0, len(snapshot))
	for name := range snapshot {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("# HELP shhgit_latency_seconds Latency quantiles per subsystem, in seconds.\n")
	b.WriteString("# TYPE shhgit_latency_seconds gauge\n")
	for _, name := range names {
		s := snapshot[name]
		for _, q := range []struct {
			label string
			value time.Duration
		}{{"0.5", s.P50}, {"0.95", s.P95}, {"0.99", s.P99}} {
			fmt.Fprintf(&b, "shhgit_latency_seconds{subsystem=%q,quantile=%q} %g\n", name, q.label, q.value.Seconds())
		}
	}
	b.WriteString("# HELP shhgit_latency_seconds_total Total observed latency per subsystem, in seconds.\n")
	b.WriteString("# TYPE shhgit_latency_seconds_total counter\n")
	for _, name := range names {
		fmt.Fprintf(&b, "shhgit_latency_seconds_total{subsystem=%q} %g\n", name, snapshot[name].Sum.Seconds())
	}
	b.WriteString("# HELP shhgit_operations_total Completed operations per subsystem.\n")
	b.WriteString("# TYPE shhgit_operations_total counter\n")
	for _, name := range names {
		fmt.Fprintf(&b, "shhgit_operations_total{subsystem=%q} %d\n", name, snapshot[name].Ops)
	}
	b.WriteString("# HELP shhgit_errors_total Errors per subsystem.\n")
	b.WriteString("# TYPE shhgit_errors_total counter\n")
	for _, name := range names {
		fmt.Fprintf(&b, "shhgit_errors_total{subsystem=%q} %d\n", name, snapshot[name].Errors)
	}
	return b.String()
}

// eventsHandler streams live FeedEvents to the dashboard over Server-Sent
// Events. The client reconnects automatically if the connection drops.
func eventsHandler(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	// No Access-Control-Allow-Origin here. This stream carries live findings, so
	// a wildcard let any page the operator visited subscribe to it, and setting it
	// after corsMiddleware also overwrote that middleware's per-origin value. The
	// dashboard subscribes same-origin, which needs no header at all.

	ch := make(chan []byte, 128)
	feedMu.Lock()
	feedClients[ch] = true
	feedMu.Unlock()
	defer func() {
		feedMu.Lock()
		delete(feedClients, ch)
		feedMu.Unlock()
	}()

	// Bound each write so a client that stops reading (but holds the TCP
	// connection open) cannot block this handler forever, which would leak the
	// goroutine, the socket and the feedClients entry. The deadline is refreshed
	// before every write and heartbeat.
	rc := http.NewResponseController(w)
	touchDeadline := func() { _ = rc.SetWriteDeadline(time.Now().Add(30 * time.Second)) }

	// Initial comment + a snapshot handshake so the client can sync state.
	touchDeadline()
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	// Send a snapshot of the current state on connect. This runs on every
	// (re)connect, so a dashboard that lost the stream - or one that connected
	// after matches had already been found - resyncs in place instead of
	// silently missing everything broadcast while it was away.
	{
		hub := ensureWebHub()
		hub.matchesMutex.RLock()
		snap, err := json.Marshal(map[string]interface{}{
			"type":     "snapshot",
			"activity": activity.Snapshot(),
			"matches":  append([]*Match(nil), hub.matches...),
			"stats":    hub.stats.snapshot(),
		})
		hub.matchesMutex.RUnlock()
		if err != nil {
			log.Printf("[web] events snapshot marshal error: %v", err)
			return
		}
		touchDeadline()
		fmt.Fprintf(w, "data: %s\n\n", snap)
		flusher.Flush()
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case data := <-ch:
			touchDeadline()
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			touchDeadline()
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// Health check endpoint.
func health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":             "healthy",
		"storage_degraded":   storageDegraded(),
		"ai_filter_degraded": aiFilterDegraded(),
		"ai_filter":          aiFilterStatus(),
		"notify":             notifyHealth(),
	})
}

// notifyHealth reports the configured alert channels and whether the most
// recent delivery attempt failed. It stays a JSON object with an empty channel
// list when notifications are unconfigured, so a probe can always parse it.
func notifyHealth() map[string]any {
	names, degraded := notifyStatusSnapshot()
	return map[string]any{
		"enabled":  len(names) > 0,
		"channels": names,
		"degraded": degraded,
	}
}

// storageDegraded reports whether the persistent findings store has fallen back
// to running without dedupe after a write failure. It is surfaced on /health so
// a monitoring probe can observe it; false when the store is absent or disabled.
func storageDegraded() bool {
	if session == nil || session.Store == nil {
		return false
	}
	return session.Store.Degraded()
}

// CORS middleware.
//
// Same-origin only. The dashboard serves captured secrets, so a wildcard
// Access-Control-Allow-Origin meant any web page the operator happened to visit
// could read /api/matches and inject fake findings through /api/push. Requests
// with no Origin header (curl, the CLI push path, same-origin navigations) are
// still allowed.
func corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			if !sameOrigin(origin, r) {
				w.Header().Set("Vary", "Origin")
				http.Error(w, "cross-origin requests are not allowed", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}

		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next(w, r)
	}
}

// localGuard restricts a route to local callers.
//
// The dashboard exposes captured secrets, and the settings API accepts an API
// key, so these routes are not safe to publish. Two checks apply:
//
//   - A same-origin check defeats CSRF: a page the operator visits cannot read
//     the API through their browser (same-origin requests, curl and the CLI send
//     no Origin header and pass).
//   - When the server is bound to a loopback address, a Host check defeats DNS
//     rebinding, where an attacker's hostname resolves to 127.0.0.1 and would
//     otherwise look same-origin. If the operator explicitly binds a routable
//     address they have opted into network exposure, so only the origin check
//     applies.
func localGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if webBindIsLoopback {
			if !isLoopbackHost(r.Host) {
				writeJSONError(w, http.StatusForbidden, "forbidden: this endpoint is local-only")
				return
			}
			// An absolute-form request line ("GET http://host/path") carries its
			// own authority, which net/http prefers: it assigns r.Host from the
			// URI and discards the Host header, so that header cannot steer
			// anything. Check the URI's authority as well, or a proxy or a raw
			// client could name a loopback Host in the header while the request
			// is really addressed elsewhere.
			if r.URL != nil && r.URL.Host != "" && !isLoopbackHost(r.URL.Host) {
				writeJSONError(w, http.StatusForbidden, "forbidden: this endpoint is local-only")
				return
			}
		}
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r) {
			writeJSONError(w, http.StatusForbidden, "cross-origin request blocked")
			return
		}
		next(w, r)
	}
}

// sameOrigin reports whether an Origin header names the same origin the request
// arrived at.
//
// It requires a real absolute origin: a protocol-relative value ("//host") or
// one carrying userinfo ("http://user@host") parses to a matching Host, and
// while no browser sends either, neither is something to accept silently.
func sameOrigin(origin string, r *http.Request) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.User != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// requireGet rejects non-GET/HEAD requests on read-only API endpoints so a
// POST or DELETE to /api/matches cannot be mistaken for a successful read.
func requireGet(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		next(w, r)
	}
}

// webBindIsLoopback records whether the dashboard was bound to loopback, which
// decides how strict localGuard is. Set by StartWebServer.
var webBindIsLoopback = true

// normalizeWebHost substitutes the documented default for an empty bind address.
//
// An empty host turns into ":8080", which listens on every interface, while
// isLoopbackHost("") is false - so passing it would expose the dashboard and
// disable the loopback guard in a single step. Callers should be able to assume
// that an empty value is safe.
func normalizeWebHost(host string) string {
	if strings.TrimSpace(host) == "" {
		return "127.0.0.1"
	}
	return host
}

// isLoopbackHost reports whether a Host header (optionally with a port) names
// the local machine.
//
// It fails closed: anything that is not recognisably a loopback address or
// "localhost" is rejected, including values whose host and port cannot be told
// apart. Trimming at the last colon used to turn "127.0.0.1:evil.test" into
// "127.0.0.1" and accept it, which matters because a fronting proxy or a raw
// client can send any Host at all.
func isLoopbackHost(host string) bool {
	h := strings.TrimSpace(host)
	if h == "" {
		return false
	}

	if hostOnly, port, err := net.SplitHostPort(h); err == nil {
		// A bracketed IPv6 literal passes through SplitHostPort; anything else
		// must carry a numeric port, so a rogue suffix is not mistaken for one.
		if _, perr := strconv.Atoi(port); perr != nil {
			return false
		}
		h = hostOnly
	} else if strings.Contains(h, ":") && !strings.HasPrefix(h, "[") {
		// A colon with no usable port: not an address we should trust.
		return false
	}
	h = strings.Trim(h, "[]")

	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// StartWebServer starts the web server. It expects the scanner to be running
// in-process (feeds via ensureWebHub through publish()) and serves the
// embedded dark-mode dashboard at "/".
func StartWebServer(host, port string) error {
	ensureWebHub()

	// An empty host would become ":8080", which listens on every interface, and
	// it is not a loopback address - so the guard below would be switched off at
	// the same moment the dashboard became reachable from the network. Fail
	// closed to the documented default instead.
	rooted := normalizeWebHost(host)
	if rooted != host {
		log.Printf("[web] empty --web-host: binding %s instead of every interface", rooted)
	}
	host = rooted

	// localGuard's Host check only makes sense while the dashboard is bound to
	// loopback; binding a routable address is an explicit opt-in to exposure.
	// Set before registering so no request can be served with a stale value.
	webBindIsLoopback = isLoopbackHost(host)

	mux := http.NewServeMux()
	registerRoutes(mux)

	addr := host + ":" + port
	log.Printf("[web] Dashboard ready at http://%s", addr)
	// Explicit timeouts: a bare ListenAndServe leaves ReadHeaderTimeout at zero,
	// so a slowloris client can hold half-open connections forever. No
	// WriteTimeout is set deliberately - it would kill the long-lived SSE
	// streams, which carry their own per-write deadline.
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return srv.ListenAndServe()
}

// registerRoutes installs every handler on mux.
//
// It is separate from StartWebServer so tests can assert the route table - in
// particular that each route capable of revealing captured material carries the
// loopback guard - without opening a socket.
func registerRoutes(mux *http.ServeMux) {
	// Every route that can reveal captured material runs behind localGuard, not
	// just the cross-origin check in corsMiddleware. corsMiddleware compares
	// Origin to Host, which DNS rebinding defeats by construction: the attacker's
	// page is served from attacker.example, that name is rebound to 127.0.0.1, and
	// the request then arrives with Origin == Host == attacker.example, looking
	// perfectly same-origin. localGuard adds the loopback Host check that closes
	// it, and a mismatch stops being a mere CORS refusal - it is a 403.
	//
	// /api/push is guarded too: without it a rebound page could inject fabricated
	// findings into the operator's match list.
	mux.HandleFunc("/api/push", corsMiddleware(localGuard(receiveMatch)))
	mux.HandleFunc("/api/stats", corsMiddleware(localGuard(requireGet(getStats))))
	mux.HandleFunc("/api/suppressed", corsMiddleware(localGuard(requireGet(getSuppressed))))
	mux.HandleFunc("/api/matches", corsMiddleware(localGuard(requireGet(getMatches))))
	mux.HandleFunc("/api/ws", corsMiddleware(localGuard(requireGet(wsHandler))))
	mux.HandleFunc("/api/logs", corsMiddleware(localGuard(requireGet(getLogs))))
	mux.HandleFunc("/api/tokens", corsMiddleware(localGuard(requireGet(getTokens))))
	mux.HandleFunc("/api/signatures", corsMiddleware(localGuard(requireGet(getSignatures))))
	mux.HandleFunc("/api/activity", corsMiddleware(localGuard(requireGet(getActivity))))
	mux.HandleFunc("/api/activity/skip", corsMiddleware(localGuard(skipRepo)))
	mux.HandleFunc("/api/scan/progress", corsMiddleware(localGuard(requireGet(getScanProgress))))
	mux.HandleFunc("/api/stats/regex", corsMiddleware(localGuard(requireGet(getRegexStats))))
	mux.HandleFunc("/api/metrics", corsMiddleware(localGuard(requireGet(getMetrics))))
	mux.HandleFunc("/api/file", corsMiddleware(localGuard(requireGet(getMatchFile))))
	mux.HandleFunc("/api/events", corsMiddleware(localGuard(requireGet(eventsHandler))))
	registerReviewRoutes(mux)
	registerSettingsRoutes(mux)

	// /health stays open: it reveals nothing and container and uptime probes
	// depend on it. The dashboard itself carries no secrets and stays reachable
	// by whatever hostname the operator uses, so that a hosts-file alias or a
	// tunnel does not lock them out of their own UI.
	mux.HandleFunc("/health", corsMiddleware(requireGet(health)))
	mux.HandleFunc("/", serveEmbeddedWeb)
}

// serveEmbeddedWeb serves the embedded web interface.
//
// Since the dashboard was rebuilt as a React SPA, "/" and "/assets/..." come
// from the compiled bundle embedded in the binary (see serveWebUI in webui.go).
// The original single-file dashboard is still embedded and remains the
// fallback if that bundle is unavailable; it can also be requested explicitly
// with ?legacy=1 or by setting SHHGIT_LEGACY_UI=1 for the whole process.
func serveEmbeddedWeb(w http.ResponseWriter, r *http.Request) {
	if uiLegacyRequested(r) {
		serveLegacyDashboard(w, r)
		return
	}
	serveWebUI(w, r)
}
