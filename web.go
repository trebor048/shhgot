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
)

// Match represents a detected secret match.
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

// TokenResult records one live token-validation outcome for the web UI.
type TokenResult struct {
	Token     string    `json:"token"`
	Valid     bool      `json:"valid"`
	Provider  string    `json:"provider"`
	Timestamp time.Time `json:"timestamp"`
}

// Stats holds aggregated statistics.
type Stats struct {
	TotalMatches       int             `json:"total_matches"`
	MatchesBySource    map[string]int  `json:"matches_by_source"`
	MatchesBySignature map[string]int  `json:"matches_by_signature"`
	MatchesByPriority  map[int]int     `json:"matches_by_priority"`
	TopSignatures      []SignatureStat `json:"top_signatures"`
	LastUpdated        time.Time       `json:"last_updated"`
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
type WebHub struct {
	clients      map[*WebClient]bool
	broadcast    chan *Match
	register     chan *WebClient
	unregister   chan *WebClient
	matches      []*Match
	stats        Stats
	matchesMutex sync.RWMutex
}

// WebClient represents a connected client.
type WebClient struct {
	hub  *WebHub
	send chan interface{}
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
	maxFileDetails    = 300
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
		content = content[:maxStoreFileBytes]
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
	if _, exists := fileDetails[id]; !exists {
		fileOrder = append(fileOrder, id)
	}
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
// connected dashboard (dropped for slow clients).
func broadcastFeed(ev FeedEvent) {
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
		clients:    make(map[*WebClient]bool),
		broadcast:  make(chan *Match, 256),
		register:   make(chan *WebClient),
		unregister: make(chan *WebClient),
		matches:    make([]*Match, 0, maxMatches),
		stats: Stats{
			MatchesBySource:    make(map[string]int),
			MatchesBySignature: make(map[string]int),
			MatchesByPriority:  make(map[int]int),
		},
	}
}

func (h *WebHub) run() {
	for {
		select {
		case client := <-h.register:
			h.clients[client] = true
			h.matchesMutex.RLock()
			matchData := map[string]interface{}{
				"type":    "history",
				"matches": h.matches,
				"stats":   h.stats,
			}
			h.matchesMutex.RUnlock()
			select {
			case client.send <- matchData:
			case <-time.After(1 * time.Second):
			}

		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}

		case match := <-h.broadcast:
			h.matchesMutex.Lock()
			h.matches = append(h.matches, match)
			if len(h.matches) > maxMatches {
				h.matches = h.matches[len(h.matches)-maxMatches:]
			}

			h.stats.TotalMatches++
			h.stats.MatchesBySource[match.Source]++
			h.stats.MatchesBySignature[match.Signature]++
			h.stats.MatchesByPriority[match.Priority]++
			h.stats.LastUpdated = time.Now()

			h.updateTopSignatures()
			statsCopy := h.stats.snapshot()
			h.matchesMutex.Unlock()

			broadcastFeed(FeedEvent{Type: "match", Match: match, Stats: &statsCopy})

			// statsCopy, not h.stats: this value is encoded later by the
			// per-client writer goroutine, outside the lock.
			data := map[string]interface{}{
				"type":  "match",
				"match": match,
				"stats": statsCopy,
			}
			for client := range h.clients {
				select {
				case client.send <- data:
				default:
					go func(c *WebClient) {
						h.unregister <- c
					}(client)
				}
			}
		}
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
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var match Match
	// Cap the body the same way the review and settings APIs do: this route is
	// reachable without credentials, so an unbounded decode would let any local
	// process - or a rebound page - grow the process's memory at will.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody)).Decode(&match); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// GetMatches returns all matches with optional filtering.
func getMatches(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	signature := r.URL.Query().Get("signature")
	priority := r.URL.Query().Get("priority")

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

	// Initial comment + a snapshot handshake so the client can sync state.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	// Send a snapshot of the current state on connect so the UI fills in
	// without waiting for the next event.
	{
		snap, _ := json.Marshal(map[string]interface{}{
			"type": "snapshot", "activity": activity.Snapshot(),
		})
		fmt.Fprintf(w, "data: %s\n\n", snap)
		flusher.Flush()
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case data := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// Health check endpoint.
func health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
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

		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
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
	return http.ListenAndServe(addr, mux)
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
	mux.HandleFunc("/api/stats", corsMiddleware(localGuard(getStats)))
	mux.HandleFunc("/api/matches", corsMiddleware(localGuard(getMatches)))
	mux.HandleFunc("/api/ws", corsMiddleware(localGuard(wsHandler)))
	mux.HandleFunc("/api/logs", corsMiddleware(localGuard(getLogs)))
	mux.HandleFunc("/api/tokens", corsMiddleware(localGuard(getTokens)))
	mux.HandleFunc("/api/signatures", corsMiddleware(localGuard(getSignatures)))
	mux.HandleFunc("/api/activity", corsMiddleware(localGuard(getActivity)))
	mux.HandleFunc("/api/file", corsMiddleware(localGuard(getMatchFile)))
	mux.HandleFunc("/api/events", corsMiddleware(localGuard(eventsHandler)))
	registerReviewRoutes(mux)
	registerSettingsRoutes(mux)

	// /health stays open: it reveals nothing and container and uptime probes
	// depend on it. The dashboard itself carries no secrets and stays reachable
	// by whatever hostname the operator uses, so that a hosts-file alias or a
	// tunnel does not lock them out of their own UI.
	mux.HandleFunc("/health", corsMiddleware(health))
	mux.HandleFunc("/", serveEmbeddedWeb)
}

// serveEmbeddedWeb serves the embedded web interface.
func serveEmbeddedWeb(w http.ResponseWriter, r *http.Request) {
	// Only "/" is the dashboard. Unknown paths used to receive a 200 carrying
	// dashboard HTML, so a missing API route looked like success to any JSON
	// client and typos were impossible to spot.
	if r.URL.Path != "/" {
		switch {
		case r.URL.Path == "/favicon.ico":
			w.WriteHeader(http.StatusNoContent)
		case strings.HasPrefix(r.URL.Path, "/api/"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"error":"no such endpoint","path":%q}`, r.URL.Path)
		default:
			http.NotFound(w, r)
		}
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Never cache the dashboard: a stale cached copy from an older build has
	// caused "matches not showing" reports when the new UI was already live.
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, dashboardHTML)
}
