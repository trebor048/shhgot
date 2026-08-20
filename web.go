package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/eth0izzle/shhgit/aireview"
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
	logMu      sync.Mutex
	logBuffer  []string
	maxLogs    = 1000

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
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	fileMu.Lock()
	d, ok := fileDetails[id]
	fileMu.Unlock()
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
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
	AI       *aireview.Job     `json:"ai,omitempty"`
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
			statsCopy := h.stats
			h.matchesMutex.Unlock()

			broadcastFeed(FeedEvent{Type: "match", Match: match, Stats: &statsCopy})

			data := map[string]interface{}{
				"type":  "match",
				"match": match,
				"stats": h.stats,
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
	if err := json.NewDecoder(r.Body).Decode(&match); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if match.ID == "" {
		match.ID = fmt.Sprintf("%d", time.Now().UnixNano())
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
	stats := hub.stats
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
	data := map[string]interface{}{
		"matches": hub.matches,
		"stats":   hub.stats,
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
	w.Header().Set("Access-Control-Allow-Origin", "*")

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
func corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next(w, r)
	}
}

// StartWebServer starts the web server. It expects the scanner to be running
// in-process (feeds via ensureWebHub through publish()) and serves the
// embedded dark-mode dashboard at "/".
func StartWebServer(host, port string) error {
	ensureWebHub()

	// API endpoints
	http.HandleFunc("/api/push", corsMiddleware(receiveMatch))
	http.HandleFunc("/api/stats", corsMiddleware(getStats))
	http.HandleFunc("/api/matches", corsMiddleware(getMatches))
	http.HandleFunc("/api/ws", corsMiddleware(wsHandler))
	http.HandleFunc("/api/logs", corsMiddleware(getLogs))
	http.HandleFunc("/api/tokens", corsMiddleware(getTokens))
	http.HandleFunc("/api/signatures", corsMiddleware(getSignatures))
	http.HandleFunc("/api/activity", corsMiddleware(getActivity))
	http.HandleFunc("/api/file", corsMiddleware(getMatchFile))
	http.HandleFunc("/api/events", corsMiddleware(eventsHandler))
	registerAIRoutes()
	http.HandleFunc("/health", corsMiddleware(health))

	// Serve embedded frontend
	http.HandleFunc("/", serveEmbeddedWeb)

	addr := host + ":" + port
	log.Printf("[web] Dashboard ready at http://%s", addr)
	return http.ListenAndServe(addr, nil)
}

// serveEmbeddedWeb serves the embedded web interface.
func serveEmbeddedWeb(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, getEmbeddedDashboard())
}

// getEmbeddedDashboard returns the HTML dashboard (dark-mode, self-contained).
func getEmbeddedDashboard() string {
	return strings.Join([]string{
		`<!DOCTYPE html>`,
		`<html lang="en">`,
		`<head>`,
		`<meta charset="UTF-8">`,
		`<meta name="viewport" content="width=device-width, initial-scale=1.0">`,
		`<title>shhgit — live secret scanner</title>`,
		`<style>`,
		`:root{`,
		`  --bg:#0b0f14; --bg2:#0f141b; --panel:#131a23; --panel2:#182130;`,
		`  --border:#1f2a37; --border2:#283545;`,
		`  --text:#e6edf3; --muted:#8b98a5; --dim:#5b6b7a;`,
		`  --cyan:#22d3ee; --green:#22c55e; --red:#ef4444; --orange:#f97316; --yellow:#eab308;`,
		`}`,
		`*{box-sizing:border-box;margin:0;padding:0}`,
		`html,body{height:100%}`,
		`body{background:var(--bg);color:var(--text);font-family:ui-sans-serif,system-ui,-apple-system,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;font-size:14px;overflow:hidden}`,
		`a{color:var(--cyan);text-decoration:none}`,
		`a:hover{text-decoration:underline}`,
		`::-webkit-scrollbar{width:10px;height:10px}`,
		`::-webkit-scrollbar-track{background:var(--bg)}`,
		`::-webkit-scrollbar-thumb{background:var(--border2);border-radius:5px}`,
		`::-webkit-scrollbar-thumb:hover{background:#33445a}`,
		`.app{display:flex;height:100vh;flex-direction:column;position:relative;z-index:2}`,
		`@keyframes aurora{0%{background-position:0% 50%}50%{background-position:100% 50%}100%{background-position:0% 50%}}`,
		`@keyframes floatParticles{0%{transform:translateY(0);opacity:0}10%{opacity:.7}90%{opacity:.5}100%{transform:translateY(-110vh);opacity:0}}`,
		`@keyframes glowPulse{0%,100%{box-shadow:0 0 6px rgba(34,211,238,.3)}50%{box-shadow:0 0 18px rgba(34,211,238,.8)}}`,
		`@keyframes shimmer{0%{background-position:-200% 0}100%{background-position:200% 0}}`,
		`@keyframes fadeUp{from{opacity:0;transform:translateY(8px)}to{opacity:1;transform:translateY(0)}}`,
		`@keyframes fadeIn{from{opacity:0}to{opacity:1}}`,
		`@keyframes leaveDown{from{opacity:1;transform:translateY(0)}to{opacity:0;transform:translateY(-6px)}}`,
		`/* new list items slide/fade in; removed ones fade up and out */`,
		`.appear{animation:fadeUp .3s ease both}`,
		`.leaving{animation:leaveDown .18s ease both}`,
		`.list-empty{text-align:center;color:var(--dim);padding:40px 20px;font-size:14px;font-style:italic}`,
		`/* animated aurora background */`,
		`body::before{content:'';position:fixed;inset:0;z-index:0;pointer-events:none;background:linear-gradient(135deg,#0b0f14 0%,#0f1b26 25%,#10141d 50%,#0d1a24 75%,#0b0f14 100%);background-size:300% 300%;animation:aurora 18s ease infinite;}`,
		`body::after{content:'';position:fixed;inset:0;z-index:0;pointer-events:none;background:radial-gradient(circle at 20% 20%,rgba(34,211,238,.06),transparent 40%),radial-gradient(circle at 80% 60%,rgba(34,197,94,.05),transparent 40%),radial-gradient(circle at 50% 90%,rgba(239,68,68,.04),transparent 45%);}`,
		`/* floating particles */`,
		`.particle{position:fixed;bottom:-10px;border-radius:50%;z-index:1;pointer-events:none;animation:floatParticles linear infinite;opacity:0;}`,
		`header{display:flex;align-items:center;gap:16px;padding:12px 20px;background:linear-gradient(90deg,rgba(15,20,27,.92),rgba(15,20,27,.65));backdrop-filter:blur(6px);border-bottom:1px solid var(--border);flex-shrink:0;position:relative;z-index:2;animation:glowPulse 4s ease-in-out infinite;border-bottom:1px solid rgba(34,211,238,.25);}`,
		`.brand{display:flex;align-items:center;gap:10px;font-weight:700;font-size:18px}`,
		`.brand .logo{color:var(--cyan);text-shadow:0 0 12px rgba(34,211,238,.8);animation:glowPulse 3s ease-in-out infinite}`,
		`.badge{font-size:11px;font-weight:600;padding:2px 8px;border-radius:10px;background:linear-gradient(90deg,rgba(34,211,238,.15),rgba(34,197,94,.15));color:var(--cyan);border:1px solid rgba(34,211,238,.35);animation:shimmer 4s linear infinite;background-size:200% 100%;}`,
		`.spacer{flex:1}`,
		`.pill{display:flex;align-items:center;gap:6px;font-size:12px;padding:4px 12px;border-radius:12px;background:var(--panel);border:1px solid var(--border);color:var(--muted);transition:border-color .2s,box-shadow .2s,transform .2s}`,
		`.pill:hover{border-color:rgba(34,211,238,.4);box-shadow:0 0 10px rgba(34,211,238,.2);transform:translateY(-1px)}`,
		`.pill b{color:var(--text)}`,
		`.dot{width:8px;height:8px;border-radius:50%;background:var(--green);box-shadow:0 0 0 0 rgba(34,197,94,.6);animation:pulse 2s infinite}`,
		`.dot.idle{background:var(--dim);box-shadow:none;animation:none}`,
		`.status-pill{min-width:150px;justify-content:center}`,
		`@keyframes pulse{0%{box-shadow:0 0 0 0 rgba(34,197,94,.6)}70%{box-shadow:0 0 0 7px rgba(34,197,94,0)}100%{box-shadow:0 0 0 0 rgba(34,197,94,0)}}`,
		`.body{display:flex;flex:1;overflow:hidden;position:relative;z-index:2}`,
		`aside{width:280px;background:rgba(15,20,27,.82);backdrop-filter:blur(6px);border-right:1px solid var(--border);overflow-y:auto;padding:16px;flex-shrink:0}`,
		`aside h3{font-size:11px;text-transform:uppercase;letter-spacing:.08em;color:var(--dim);margin:18px 0 8px}`,
		`aside h3:first-child{margin-top:0}`,
		`.stat-grid{display:grid;grid-template-columns:1fr 1fr;gap:8px}`,
		`.stat{background:var(--panel);border:1px solid var(--border);border-radius:10px;padding:10px 12px;transition:border-color .2s,box-shadow .2s,transform .2s}`,
		`.stat:hover{border-color:rgba(34,211,238,.35);box-shadow:0 0 14px rgba(34,211,238,.15);transform:translateY(-2px)}`,
		`.stat .n{font-size:22px;font-weight:700}`,
		`.stat .l{font-size:11px;color:var(--muted);margin-top:2px}`,
		`.stat.crit .n{color:var(--red);text-shadow:0 0 10px rgba(239,68,68,.5)} .stat.high .n{color:var(--orange);text-shadow:0 0 10px rgba(249,115,22,.5)} .stat.med .n{color:var(--yellow);text-shadow:0 0 10px rgba(234,179,8,.5)} .stat.low .n{color:var(--green);text-shadow:0 0 10px rgba(34,197,94,.5)}`,
		`.filter-row{margin-bottom:8px}`,
		`.filter-row label{display:block;font-size:11px;color:var(--muted);margin-bottom:4px}`,
		`input[type=text],select{width:100%;background:var(--panel);border:1px solid var(--border);color:var(--text);border-radius:8px;padding:7px 10px;font-size:13px;outline:none;transition:border-color .2s,box-shadow .2s}`,
		`input[type=text]:focus,select:focus{border-color:var(--cyan);box-shadow:0 0 10px rgba(34,211,238,.25)}`,
		`.chk{display:flex;align-items:center;gap:8px;padding:5px 0;font-size:13px;color:var(--text);cursor:pointer}`,
		`.chk input{accent-color:var(--cyan)}`,
		`.sig-item{display:flex;justify-content:space-between;align-items:center;padding:6px 8px;border-radius:6px;cursor:pointer;font-size:12px;color:var(--muted);transition:background .2s,color .2s,transform .2s}`,
		`.sig-item:hover{background:var(--panel);color:var(--text);transform:translateX(3px)}`,
		`.sig-item.active{background:var(--panel2);color:var(--cyan)}`,
		`.sig-item .c{font-size:11px;background:var(--panel2);border-radius:8px;padding:1px 7px}`,
		`main{flex:1;display:flex;flex-direction:column;overflow:hidden}`,
		`.tabs{display:flex;gap:2px;padding:8px 16px 0;border-bottom:1px solid var(--border);background:rgba(15,20,27,.7);backdrop-filter:blur(4px);flex-shrink:0}`,
		`.tab{padding:9px 16px;font-size:13px;font-weight:600;color:var(--muted);cursor:pointer;border-bottom:2px solid transparent;border-radius:8px 8px 0 0;transition:color .2s,border-color .2s,background .2s,transform .2s}`,
		`.tab:hover{color:var(--text);transform:translateY(-1px)}`,
		`.tab.active{color:var(--text);border-bottom-color:var(--cyan);background:var(--panel);text-shadow:0 0 10px rgba(34,211,238,.4)}`,
		`.tab .cnt{font-size:10px;background:var(--panel2);padding:1px 6px;border-radius:8px;margin-left:5px;color:var(--cyan)}`,
		`.toolbar{display:flex;align-items:center;gap:10px;padding:10px 16px;border-bottom:1px solid var(--border);flex-shrink:0;color:var(--muted);font-size:12px}`,
		`.content{flex:1;overflow-y:auto;padding:16px}`,
		`/* ---- matches list: clean flat rows, no grouping ---- */`,
		`.mrow{background:var(--panel);border:1px solid var(--border);border-left:3px solid var(--border2);border-radius:8px;margin-bottom:5px;overflow:hidden;transition:border-color .2s,box-shadow .2s}`,
		`.mrow:hover{border-color:rgba(34,211,238,.35);box-shadow:0 0 8px rgba(34,211,238,.1)}`,
		`.mrow.crit{border-left-color:var(--red)} .mrow.high{border-left-color:var(--orange)} .mrow.med{border-left-color:var(--yellow)} .mrow.low{border-left-color:var(--green)}`,
		`.mrow.crit .sig{color:var(--red)} .mrow.high .sig{color:var(--orange)} .mrow.med .sig{color:var(--yellow)} .mrow.low .sig{color:var(--green)}`,
		`.mrow-head{display:grid;grid-template-columns:10px minmax(140px,1.3fr) minmax(110px,1fr) minmax(140px,1.7fr) 46px 62px 12px;align-items:center;gap:10px;padding:8px 12px;cursor:pointer;transition:background .15s}`,
		`.mrow-head:hover{background:var(--panel2)}`,
		`.mrow .dot{width:8px;height:8px;border-radius:50%;justify-self:center}`,
		`.mrow.crit .dot{background:var(--red);box-shadow:0 0 6px rgba(239,68,68,.85)} .mrow.high .dot{background:var(--orange)} .mrow.med .dot{background:var(--yellow)} .mrow.low .dot{background:var(--green)}`,
		`.sig{font-weight:600;font-size:12px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;min-width:0}`,
		`.repo{font-size:11px;color:var(--cyan);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;min-width:0}`,
		`.file{font-size:10.5px;color:var(--orange);font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;min-width:0}`,
		`.time{font-size:10px;color:var(--dim);text-align:right;white-space:nowrap}`,
		`.stars{font-size:10px;color:var(--muted);text-align:right;white-space:nowrap}`,
		`.chev{font-size:10px;color:var(--dim);justify-self:center;transition:transform .2s}`,
		`.mrow-body{display:none;border-top:1px solid var(--border);padding:8px 12px 10px;background:var(--bg2)}`,
		`.mrow.open .mrow-body{display:block}`,
		`.mrow.open .chev{transform:rotate(180deg)}`,
		`.metarow{display:flex;align-items:center;gap:14px;flex-wrap:wrap;font-size:11px;color:var(--muted);margin-bottom:6px}`,
		`.metarow a{color:var(--cyan);text-decoration:underline;word-break:break-all}`,
		`.toks{margin-top:8px}`,
		`.tok{display:flex;align-items:center;gap:8px;background:var(--bg);border:1px solid var(--border);border-radius:6px;padding:6px 10px;margin-bottom:6px;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;word-break:break-all;transition:border-color .2s}`,
		`.tok:hover{border-color:rgba(34,197,94,.45)}`,
		`.tok .v{flex:1;color:var(--green)}`,
		`.copy{background:var(--panel2);border:1px solid var(--border);color:var(--muted);font-size:11px;padding:3px 9px;border-radius:6px;cursor:pointer;flex-shrink:0;transition:color .2s,border-color .2s,box-shadow .2s}`,
		`.copy:hover{color:var(--cyan);border-color:var(--cyan);box-shadow:0 0 8px rgba(34,211,238,.3)}`,
		`.empty{text-align:center;color:var(--dim);padding:60px 20px;font-size:14px}`,
		`.log-line{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;white-space:pre-wrap;word-break:break-all;padding:2px 0;color:#cfd6dd;border-bottom:1px solid var(--bg);transition:color .2s,background .2s}`,
		`.log-line a{text-decoration:underline}`,
		`.log-line:hover{color:var(--text);background:rgba(34,211,238,.04)}`,
		`.tk{display:flex;align-items:center;gap:10px;background:var(--panel);border:1px solid var(--border);border-radius:8px;padding:10px 14px;margin-bottom:8px;transition:border-color .2s,transform .2s}`,
		`.tk:hover{transform:translateX(3px)}`,
		`.tk.ok:hover{border-color:rgba(34,197,94,.4)} .tk.bad:hover{border-color:rgba(239,68,68,.4)}`,
		`.tk .prov{font-weight:600;font-size:13px;flex-shrink:0}`,
		`.tk .tokv{flex:1;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px;word-break:break-all}`,
		`.tk.ok .tokv{color:var(--green)} .tk.bad .tokv{color:var(--red)}`,
		`.status-tag{font-size:11px;font-weight:700;padding:2px 8px;border-radius:8px;flex-shrink:0}`,
		`.status-tag.ok{color:var(--green);background:rgba(34,197,94,.12)}`,
		`.status-tag.bad{color:var(--red);background:rgba(239,68,68,.12)}`,
		`.act-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:8px;margin-bottom:14px}`,
		`.act-stat{background:var(--panel);border:1px solid var(--border);border-radius:10px;padding:10px 12px;transition:border-color .2s,box-shadow .2s,transform .2s}`,
		`.act-stat:hover{border-color:rgba(34,211,238,.35);box-shadow:0 0 12px rgba(34,211,238,.15);transform:translateY(-1px)}`,
		`.act-stat .n{font-size:20px;font-weight:700;color:var(--cyan)}`,
		`.act-stat .l{font-size:11px;color:var(--muted);margin-top:2px}`,
		`.act-sec{font-size:12px;color:var(--dim);margin:10px 0 6px;display:flex;align-items:center;gap:6px}`,
		`.act-sec .live-dot{width:7px;height:7px;border-radius:50%;background:var(--cyan);animation:pulse 1.5s infinite}`,
		`.act-item{display:flex;align-items:center;gap:10px;background:var(--panel);border:1px solid var(--border);border-radius:8px;padding:9px 12px;margin-bottom:6px}`,
		`.act-item .u{flex:1;font-size:12px;color:var(--text);word-break:break-all}`,
		`.act-item .since{font-size:11px;color:var(--dim);flex-shrink:0}`,
		`.act-item .phase{font-size:10px;font-weight:700;padding:2px 8px;border-radius:8px;flex-shrink:0}`,
		`.phase.dl{color:var(--cyan);background:rgba(34,211,238,.12)}`,
		`.phase.sc{color:var(--yellow);background:rgba(234,179,8,.12)}`,
		`.live-box{background:var(--panel);border:1px solid var(--border);border-radius:10px;padding:10px;margin-bottom:4px}`,
		`.live-sec{display:flex;align-items:center;gap:6px;font-size:11px;font-weight:600;color:var(--muted);text-transform:uppercase;letter-spacing:.05em;margin:6px 0 4px}`,
		`.live-sec:first-child{margin-top:0}`,
		`.live-dot.yellow{background:var(--yellow);box-shadow:0 0 8px rgba(234,179,8,.7);animation:pulse 2s infinite}`,
		`.live-list{max-height:110px;overflow-y:auto;margin-bottom:2px}`,
		`.live-item{display:flex;align-items:center;gap:6px;font-size:11px;color:var(--muted);padding:2px 0;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}`,
		`.live-item .u{flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}`,
		`.live-item .s{color:var(--dim);font-size:10px;flex-shrink:0}`,
		`.live-empty{font-size:11px;color:var(--dim);font-style:italic;padding:2px 0}`,
		`.live-dot{width:7px;height:7px;border-radius:50%;background:var(--cyan);animation:pulse 1.5s infinite;box-shadow:0 0 6px rgba(34,211,238,.7);flex-shrink:0}`,
		`/* secret strip on match cards */`,
		`.secstrip{display:flex;align-items:center;gap:8px;padding:5px 10px;background:rgba(34,197,94,.05);border-top:1px solid var(--bg);border-bottom:1px solid var(--bg)}`,
		`.secstrip .sec-lab{font-size:9px;font-weight:700;letter-spacing:.08em;text-transform:uppercase;color:var(--green);flex-shrink:0}`,
		`.secstrip .secval{flex:1;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:11px;color:var(--green);word-break:break-all;transition:filter .15s;min-width:0}`,
		`.secstrip .secval:hover{color:#7ee787}`,
		`.secstrip .secval.hide{filter:blur(7px);user-select:none}`,
		`.secstrip .secval.hide:hover{filter:blur(3px)}`,
		`.hidebtn{background:var(--panel2);border:1px solid var(--border);color:var(--muted);font-size:11px;padding:3px 9px;border-radius:6px;cursor:pointer;flex-shrink:0;transition:color .2s,border-color .2s}`,
		`.hidebtn:hover{color:var(--yellow);border-color:rgba(234,179,8,.5)}`,
		`.lnb{font-size:10px;color:var(--yellow);background:rgba(234,179,8,.1);border:1px solid rgba(234,179,8,.3);padding:1px 6px;border-radius:8px;flex-shrink:0;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}`,
		`.viewbtn{background:var(--panel2);border:1px solid var(--border);color:var(--cyan);font-size:11px;padding:3px 9px;border-radius:6px;cursor:pointer;flex-shrink:0;transition:color .2s,border-color .2s,box-shadow .2s}`,
		`.viewbtn:hover{color:var(--cyan);border-color:var(--cyan);box-shadow:0 0 8px rgba(34,211,238,.35)}`,
		`.where{font-size:11px;color:var(--muted);margin:6px 0 2px}`,
		`.where .filelink{cursor:pointer}`,
		`/* file modal */`,
		`.ovl{position:fixed;inset:0;background:rgba(5,8,12,.74);backdrop-filter:blur(4px);z-index:50;display:flex;align-items:center;justify-content:center;animation:fadeIn .15s ease both}`,
		`.modal{width:min(1120px,95vw);height:min(86vh,880px);background:var(--bg2);border:1px solid var(--border2);border-radius:14px;display:flex;flex-direction:column;overflow:hidden;box-shadow:0 24px 70px rgba(0,0,0,.65);animation:fadeUp .18s ease both}`,
		`.modal-head{display:flex;align-items:center;gap:12px;padding:12px 16px;border-bottom:1px solid var(--border);background:var(--panel);flex-wrap:wrap}`,
		`.m-repo{font-size:13px;font-weight:600;color:var(--cyan);word-break:break-all}`,
		`.m-file{font-size:12px;color:var(--orange);font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;word-break:break-all}`,
		`.m-sec{font-size:11px;color:var(--green);font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;word-break:break-all;background:rgba(34,197,94,.08);border:1px solid rgba(34,197,94,.25);border-radius:6px;padding:2px 8px;max-width:340px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}`,
		`.modal-tools{display:flex;align-items:center;gap:8px;padding:8px 16px;border-bottom:1px solid var(--border);background:var(--panel2);flex-wrap:wrap}`,
		`.m-lang{font-size:10px;font-weight:700;letter-spacing:.08em;text-transform:uppercase;color:var(--dim);flex-shrink:0}`,
		`.mbtn{background:var(--bg);border:1px solid var(--border);color:var(--muted);font-size:11px;padding:3px 10px;border-radius:8px;cursor:pointer;transition:color .2s,border-color .2s,box-shadow .2s}`,
		`.mbtn:hover{color:var(--text);border-color:var(--border2)}`,
		`.mbtn.active{color:var(--cyan);border-color:rgba(34,211,238,.5);box-shadow:0 0 8px rgba(34,211,238,.25)}`,
		`.trunc-note{font-size:10px;color:var(--orange);flex-shrink:0}`,
		`.modal-body{flex:1;overflow:auto;background:#0a0e13;padding:0}`,
		`.close{background:transparent;border:none;color:var(--muted);font-size:16px;cursor:pointer;flex-shrink:0;padding:2px 6px;border-radius:6px;transition:color .2s,background .2s}`,
		`.close:hover{color:var(--red);background:rgba(239,68,68,.1)}`,
		`.code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12.5px;line-height:1.55;padding:10px 0}`,
		`.code-line{display:flex;min-width:max-content}`,
		`.code-line:hover{background:rgba(255,255,255,.028)}`,
		`.code-line .ln{flex:0 0 56px;text-align:right;padding-right:14px;color:var(--dim);user-select:none;position:sticky;left:0;background:var(--bg2);z-index:1}`,
		`.code-line .ct{padding:0 16px;white-space:pre}`,
		`.hl-line{background:rgba(239,68,68,.13);box-shadow:inset 3px 0 0 var(--red)}`,
		`.hl-line .ln{color:var(--red);font-weight:700;background:rgba(239,68,68,.13)}`,
		`.sec{background:rgba(239,68,68,.32);color:#fff;border-radius:3px;padding:0 2px;box-shadow:0 0 0 1px rgba(239,68,68,.65);font-weight:600}`,
		`/* syntax highlight palette (github-dark-ish) */`,
		`.tok-c{color:#7d8b99;font-style:italic}`,
		`.tok-s{color:#7ee787}`,
		`.tok-k{color:#ff7b72}`,
		`.tok-v{color:#79c0ff}`,
		`.tok-a{color:#d2a8ff}`,
		`.tok-t{color:#ffa657}`,
		`.tok-b{color:#79c0ff;font-weight:600}`,
		`.tok-n{color:#f2cc60}`,
		`</style>`,
		`</head>`,
		`<body>`,
		`<div class="app">`,
		`<header>`,
		`<div class="brand"><span class="logo">&#128269;</span> shhgit <span class="badge">live</span></div>`,
		`<div class="spacer"></div>`,
		`<div class="pill">total <b id="total">0</b></div>`,
		`<div class="pill">crit <b id="crit" style="color:var(--red)">0</b></div>`,
		`<div class="pill">high <b id="high" style="color:var(--orange)">0</b></div>`,
		`<div class="pill">med <b id="med" style="color:var(--yellow)">0</b></div>`,
		`<div class="pill">low <b id="low" style="color:var(--green)">0</b></div>`,
		`<div class="pill"><span class="dot"></span> live</div>`,
		`<div class="pill status-pill" id="livepill" title="Live scan activity"><span class="dot idle" id="livedot"></span><span id="livetext">starting...</span></div>`,
		`</header>`,
		`<div class="body">`,
		`<aside>`,
		`<h3>Overview</h3>`,
		`<div class="stat-grid">`,
		`<div class="stat crit"><div class="n" id="s-crit">0</div><div class="l">Critical</div></div>`,
		`<div class="stat high"><div class="n" id="s-high">0</div><div class="l">High</div></div>`,
		`<div class="stat med"><div class="n" id="s-med">0</div><div class="l">Medium</div></div>`,
		`<div class="stat low"><div class="n" id="s-low">0</div><div class="l">Low</div></div>`,
		`</div>`,
		`<h3>Live</h3>`,
		`<div class="live-box">`,
		`<div class="live-sec"><span class="live-dot"></span> Fetching (<span id="live-fetch-count">0</span>)</div>`,
		`<div id="live-fetch" class="live-list"><div class="live-empty">none</div></div>`,
		`<div class="live-sec"><span class="live-dot yellow"></span> Scanning (<span id="live-scan-count">0</span>)</div>`,
		`<div id="live-scan" class="live-list"><div class="live-empty">none</div></div>`,
		`</div>`,
		`<h3>Filters</h3>`,
		`<div class="filter-row">`,
		`<label>Search</label>`,
		`<input type="text" id="search" placeholder="repo, file, token..." oninput="state.q=this.value;render()">`,
		`</div>`,
		`<div class="filter-row">`,
		`<label>Source</label>`,
		`<select id="src" onchange="state.source=this.value;render()"><option value="">All</option></select>`,
		`</div>`,
		`<div class="filter-row">`,
		`<label>Signature</label>`,
		`<select id="sig" onchange="state.sig=this.value;render()"><option value="">All</option></select>`,
		`</div>`,
		`<label class="chk"><input type="checkbox" id="p3" checked onchange="state.p3=this.checked;render()"> Critical</label>`,
		`<label class="chk"><input type="checkbox" id="p2" checked onchange="state.p2=this.checked;render()"> High</label>`,
		`<label class="chk"><input type="checkbox" id="p1" checked onchange="state.p1=this.checked;render()"> Medium</label>`,
		`<label class="chk"><input type="checkbox" id="p0" checked onchange="state.p0=this.checked;render()"> Low</label>`,
		`<h3>Top signatures</h3>`,
		`<div id="sigs"></div>`,
		`</aside>`,
		`<main>`,
		`<div class="tabs">`,
		`<div class="tab active" id="tab-matches" onclick="setTab('matches')">Matches <span class="cnt" id="cnt-matches">0</span></div>`,
		`<div class="tab" id="tab-tokens" onclick="setTab('tokens')">Tokens <span class="cnt" id="cnt-tokens">0</span></div>`,
		`<div class="tab" id="tab-activity" onclick="setTab('activity')">Activity <span class="cnt" id="cnt-activity">0</span></div>`,
		`<div class="tab" id="tab-logs" onclick="setTab('logs')">Logs <span class="cnt" id="cnt-logs">0</span></div>`,
		`</div>`,
		`<div class="toolbar"><span id="showing"></span><span class="spacer" style="flex:1"></span><label class="chk" style="padding:0"><input type="checkbox" id="pause"> pause</label></div>`,
		`<div class="content" id="panel-matches"></div>`,
		`<div class="content" id="panel-tokens" style="display:none"></div>`,
		`<div class="content" id="panel-activity" style="display:none"></div>`,
		`<div class="content" id="panel-logs" style="display:none"></div>`,
		`</main>`,
		`</div>`,
		`</div>`,
		`<script>`,
		`function makeParticles(){var colors=['34,211,238','34,197,94','249,115,22','167,139,250'];for(var i=0;i<26;i++){var p=document.createElement('div');p.className='particle';var size=(Math.random()*3+1.5).toFixed(1);p.style.width=size+'px';p.style.height=size+'px';p.style.left=(Math.random()*100).toFixed(2)+'%';p.style.background='rgba('+colors[i%colors.length]+',.7)';p.style.boxShadow='0 0 6px rgba('+colors[i%colors.length]+',.9)';p.style.animationDuration=(Math.random()*14+10).toFixed(1)+'s';p.style.animationDelay=(Math.random()*12).toFixed(1)+'s';document.body.appendChild(p);}}makeParticles();`,
		`var state={q:'',source:'',sig:'',p3:true,p2:true,p1:true,p0:true,tab:'matches'};`,
		`var matches=[],tokens=[],logs=[],stats={},activityData={fetching:[],scanning:[]};`,
		`function setTab(t){state.tab=t;document.querySelectorAll('.tab').forEach(function(e){e.classList.remove('active')});document.getElementById('tab-'+t).classList.add('active');document.getElementById('panel-matches').style.display=t==='matches'?'block':'none';document.getElementById('panel-tokens').style.display=t==='tokens'?'block':'none';document.getElementById('panel-activity').style.display=t==='activity'?'block':'none';document.getElementById('panel-logs').style.display=t==='logs'?'block':'none';}`,
		`function esc(s){return String(s==null?'':s).replace(/[&<>"]/g,function(c){return{'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]})}`,
		`function prioLabel(p){return p===3?'crit':p===2?'high':p===1?'med':'low'}`,
		`function prioFilter(m){if(m.priority===3&&!state.p3)return false;if(m.priority===2&&!state.p2)return false;if(m.priority===1&&!state.p1)return false;if(m.priority===0&&!state.p0)return false;return true}`,
		`function matchFilter(m){if(!prioFilter(m))return false;if(state.source&&m.source!==state.source)return false;if(state.sig&&m.signature!==state.sig)return false;if(state.q){var hay=(m.url+' '+(m.file||'')+' '+(m.signature||'')+' '+(m.matches||[]).join(' ')).toLowerCase();if(hay.indexOf(state.q.toLowerCase())<0)return false;}return true}`,
		`function render(){`,
		`var shown=matches.filter(matchFilter);`,
		`document.getElementById('cnt-matches').textContent=shown.length;`,
		`document.getElementById('showing').textContent=shown.length+' matches / '+matches.length+' total';`,
		`var el=document.getElementById('panel-matches');`,
		`if(shown.length===0){if(!el.firstChild||el.firstChild.className!=='empty')el.innerHTML='<div class="empty">No matches yet - waiting for the scanner...</div>';}`,
		`else{`,
		`if(el.firstChild&&el.firstChild.className==='empty')el.innerHTML='';`,
		`syncList(el,shown.slice(0,500),function(m){return m.id},matchBuild);`,
		`}`,
		`renderTokens();renderLogs();renderActivity();`,
		`}`,
		`function renderTokens(){`,
		`var tt=document.getElementById('panel-tokens');`,
		`document.getElementById('cnt-tokens').textContent=tokens.length;`,
		`if(tokens.length===0){if(!tt.firstChild||tt.firstChild.className!=='empty')tt.innerHTML='<div class="empty">No token validation results yet.</div>';}`,
		`else{`,
		`if(tt.firstChild&&tt.firstChild.className==='empty')tt.innerHTML='';`,
		`syncList(tt,tokens.slice(0,500),function(t){return t.token+'|'+t.valid},tokenBuild);`,
		`}`,
		`}`,
		`function renderLogs(){`,
		`var ll=document.getElementById('panel-logs');`,
		`document.getElementById('cnt-logs').textContent=logs.length;`,
		`if(logs.length===0){if(!ll.firstChild||ll.firstChild.className!=='empty')ll.innerHTML='<div class="empty">No log output yet.</div>';}`,
		`else{`,
		`if(ll.firstChild&&ll.firstChild.className==='empty')ll.innerHTML='';`,
		`var shownLogs=logs.slice(-400).reverse();`,
		`syncList(ll,shownLogs,function(l){return l},logBuild);`,
		`}`,
		`}`,
		`function matchBuild(m){`,
		`var p=prioLabel(m.priority);`,
		`var d=document.createElement('div');d.className='mrow '+p;d.setAttribute('data-id',m.id);`,
		`var secret=m.secret||(m.matches&&m.matches[0])||'';`,
		`var head=document.createElement('div');head.className='mrow-head';`,
		`head.innerHTML='<span class="dot"></span><span class="sig" title="'+esc(m.signature)+'">'+esc(m.signature)+'</span><span class="repo" title="'+esc(m.url)+'">'+esc(short(m.url,32))+'</span><span class="file" title="'+esc(m.file)+'">'+esc(short(m.file,36))+(m.line?' <span class="lnb">L'+m.line+'</span>':'')+'</span><span class="stars">&#9733; '+m.stars+'</span><span class="time">'+new Date(m.timestamp).toLocaleTimeString()+'</span><span class="chev">&#9660;</span>';`,
		`var body=document.createElement('div');body.className='mrow-body';`,
		`var strip=document.createElement('div');strip.className='secstrip';`,
		`strip.innerHTML='<span class="sec-lab">&#128273; secret</span><code class="secval" data-full="'+esc(secret)+'" title="'+esc(secret)+'">'+esc(short(secret,220))+'</code><button class="copy" title="copy secret">copy</button><button class="hidebtn" title="hide / reveal secret">hide</button>';`,
		`var meta=document.createElement('div');meta.className='metarow';`,
		`meta.innerHTML='<span>source: '+esc(m.source)+'</span>'+(m.has_file?'<span>line <b>'+(m.line||'?')+'</b></span>':'')+'<a href="'+esc(m.url)+'" target="_blank">'+esc(m.url)+'</a>'+(m.has_file?'<a class="filelink" title="View full file - syntax highlighted, secret marked">&#128196; view full file</a>':'');`,
		`body.appendChild(strip);body.appendChild(meta);`,
		`var toks=(m.matches||[]).map(function(t){return '<div class="tok"><span class="v">'+esc(t)+'</span><button class="copy">copy</button></div>'}).join('');`,
		`if(toks)body.insertAdjacentHTML('beforeend','<div class="toks">'+toks+'</div>');`,
		`d.appendChild(head);d.appendChild(body);`,
		`head.onclick=function(){d.classList.toggle('open');};`,
		`var fl=meta.querySelector('.filelink');if(fl)fl.onclick=function(){viewFile(m.id,this);};`,
		`strip.querySelector('.copy').onclick=function(){copyVal(this);};`,
		`strip.querySelector('.hidebtn').onclick=function(){var v=strip.querySelector('.secval');var h=v.classList.toggle('hide');this.textContent=h?'show':'hide';};`,
		`var cbs=body.querySelectorAll('.tok .copy');for(var i=0;i<cbs.length;i++)(function(btn){btn.onclick=function(){copyText(btn.parentElement.querySelector('.v').textContent,btn);};})(cbs[i]);`,
		`return d;`,
		`}`,
		`function tokenBuild(t){`,
		`var d=document.createElement('div');d.className='tk '+(t.valid?'ok':'bad');`,
		`d.innerHTML='<span class="prov">'+esc(t.provider||'?')+'</span><span class="tokv">'+esc(t.token)+'</span><span class="status-tag '+(t.valid?'ok':'bad')+'">'+(t.valid?'VALID':'INVALID')+'</span>';`,
		`return d;`,
		`}`,
		`function logBuild(l){`,
		`var d=document.createElement('div');d.className='log-line';d.innerHTML=ansiHtml(l);`,
		`return d;`,
		`}`,
		`/* ---- ANSI -> HTML: renders the Logs tab exactly like the terminal ---- */`,
		`var ANSI16=['#000000','#cc0000','#4e9a06','#c4a000','#3465a4','#75507b','#06989a','#d3d7cf','#555753','#ef2929','#8ae234','#fce94f','#729fcf','#ad7fa8','#34e2e2','#eeeeec'];`,
		`var PAL256=ANSI16.slice();`,
		`(function(){var lv=[0,95,135,175,215,255];for(var r=0;r<6;r++)for(var g=0;g<6;g++)for(var b=0;b<6;b++){var rr=lv[r],gg=lv[g],bb=lv[b];PAL256.push('#'+hex2(rr)+hex2(gg)+hex2(bb));}for(var i=0;i<24;i++){var v=8+i*10;PAL256.push('#'+hex2(v)+hex2(v)+hex2(v));}})();`,
		`function hex2(n){n=n.toString(16);return n.length<2?'0'+n:n}`,
		`function sgrState(params){`,
		`var st={fg:'',bg:'',b:false,d:false,i:false,u:false,inv:false};`,
		`var ps=params.split(';'),k=0;`,
		`while(k<ps.length){`,
		`var c=parseInt(ps[k],10);`,
		`if(isNaN(c)){k++;continue}`,
		`if(c===0){st={fg:'',bg:'',b:false,d:false,i:false,u:false,inv:false};}`,
		`else if(c===1)st.b=true;`,
		`else if(c===2)st.d=true;`,
		`else if(c===3)st.i=true;`,
		`else if(c===4)st.u=true;`,
		`else if(c===7)st.inv=true;`,
		`else if(c===22){st.b=false;st.d=false}`,
		`else if(c===23)st.i=false;`,
		`else if(c===24)st.u=false;`,
		`else if(c===27)st.inv=false;`,
		`else if(c>=30&&c<=37)st.fg=ANSI16[c-30];`,
		`else if(c>=40&&c<=47)st.bg=ANSI16[c-40];`,
		`else if(c>=90&&c<=97)st.fg=ANSI16[c-90+8];`,
		`else if(c>=100&&c<=107)st.bg=ANSI16[c-100+8];`,
		`else if(c===39)st.fg='';`,
		`else if(c===49)st.bg='';`,
		`else if(c===38||c===48){`,
		`var mode=parseInt(ps[k+1],10);`,
		`if(mode===5){var n=parseInt(ps[k+2],10);if(c===38)st.fg=PAL256[n%256];else st.bg=PAL256[n%256];k+=2;}`,
		`else if(mode===2){var rr=parseInt(ps[k+2],10),gg=parseInt(ps[k+3],10),bb=parseInt(ps[k+4],10);var col='#'+hex2(rr)+hex2(gg)+hex2(bb);if(c===38)st.fg=col;else st.bg=col;k+=4;}`,
		`}`,
		`k++;`,
		`}`,
		`return st;`,
		`}`,
		`function styleCss(st){`,
		`var css='';`,
		`if(st.fg)css+='color:'+st.fg+';';`,
		`if(st.bg)css+='background-color:'+st.bg+';';`,
		`if(st.b)css+='font-weight:700;';`,
		`if(st.d)css+='opacity:.6;';`,
		`if(st.i)css+='font-style:italic;';`,
		`if(st.u)css+='text-decoration:underline;';`,
		`if(st.inv)css+='filter:invert(1);';`,
		`return css;`,
		`}`,
		`function ansiHtml(line){`,
		`line=String(line==null?'':line);`,
		`var out='',i=0,open=0;`,
		`function flush(){while(open>0){out+='</span>';open--;}}`,
		`function openSpan(css){if(css){out+='<span style="'+css+'">';open++;}}`,
		`while(i<line.length){`,
		`var c=line.charAt(i);`,
		`if(c==='\r'){i++;continue}`,
		`if(c==='\x1b'){`,
		`var j=i+1,ch=line.charAt(j);`,
		`if(ch===']'){`,
		`var end=line.indexOf('\x1b\\',j);`,
		`if(end<0){i=line.length;continue}`,
		`var payload=line.slice(j+1,end);`,
		`var parts=payload.split(';;');`,
		`var url=(parts[0]==='8'&&parts[1])?parts.slice(1).join(';;'):'';`,
		`var close=line.indexOf('\x1b]8;;\x1b\\',end+2);`,
		`var textEnd=(close>0)?close:line.length;`,
		`var inner=ansiHtml(line.slice(end+2,textEnd));`,
		`if(url){out+='<a href="'+esc(url)+'" target="_blank" rel="noopener">'+inner+'</a>';}`,
		`else{out+=inner;}`,
		`i=(close>0)?close+7:line.length;`,
		`continue;`,
		`}`,
		`if(ch==='['){`,
		`var m=line.indexOf('m',j);`,
		`if(m>0){`,
		`flush();`,
		`var nst=sgrState(line.slice(j+1,m));`,
		`openSpan(styleCss(nst));`,
		`i=m+1;`,
		`continue;`,
		`}`,
		`}`,
		`i=(ch==='['||ch===']')?j+2:j+1;`,
		`continue;`,
		`}`,
		`out+=esc(c);`,
		`i++;`,
		`}`,
		`flush();`,
		`return out;`,
		`}`,
		`/* Incremental list sync: only adds new / removes gone nodes, never rebuilds the whole list. */`,
		`function syncList(container,items,keyFn,buildFn){`,
		`var want={};for(var i=0;i<items.length;i++)want[keyFn(items[i])]=true;`,
		`var kids=Array.prototype.slice.call(container.children);`,
		`var byKey={};`,
		`for(var j=0;j<kids.length;j++){var k=kids[j];var kk=k.getAttribute('data-key');if(kk)byKey[kk]=k;}`,
		`/* remove nodes whose key is gone */`,
		`for(var j=0;j<kids.length;j++){var k=kids[j];var key=k.getAttribute('data-key');if(key&&!want[key]){k.classList.add('leaving');(function(n){setTimeout(function(){if(n.parentNode)n.parentNode.removeChild(n);},200);})(k);}}`,
		`/* add nodes that are new, in order */`,
		`var prev=null;`,
		`for(var i=0;i<items.length;i++){`,
		`var key=String(keyFn(items[i]));`,
		`var node=byKey[key];`,
		`if(!node){node=buildFn(items[i]);node.setAttribute('data-key',key);node.classList.add('appear');byKey[key]=node;}`,
		`if(prev){if(prev.nextSibling!==node)prev.parentNode.insertBefore(node,prev.nextSibling);}else{if(container.firstChild!==node)container.insertBefore(node,container.firstChild);}`,
		`prev=node;`,
		`}`,
		`}`,
		`function renderActivity(){`,
		`var ad=activityData||{fetching:[],scanning:[]};`,
		`var dlArr=ad.fetching||[], scArr=ad.scanning||[];`,
		`var inFlight=dlArr.length+scArr.length;`,
		`document.getElementById('cnt-activity').textContent=inFlight;`,
		`var ldot=document.getElementById('livedot');var ltxt=document.getElementById('livetext');`,
		`if(inFlight>0){ldot.classList.remove('idle');if(dlArr.length>0&&scArr.length>0){ltxt.textContent='fetching '+dlArr.length+' / scanning '+scArr.length;}else if(dlArr.length>0){ltxt.textContent='fetching '+dlArr.length+' repos';}else{ltxt.textContent='scanning '+scArr.length+' repos';}}else if(ad.total_fetched>0||ad.total_scanned>0){ldot.classList.add('idle');ltxt.textContent='idle';}else{ldot.classList.add('idle');ltxt.textContent='waiting for repos';}`,
		`document.getElementById('live-fetch-count').textContent=dlArr.length;`,
		`document.getElementById('live-scan-count').textContent=scArr.length;`,
		`var lf=document.getElementById('live-fetch');`,
		`if(dlArr.length===0){if(!lf.firstChild||lf.firstChild.className!=='live-empty')lf.innerHTML='<div class="live-empty">none</div>';}else{if(lf.firstChild&&lf.firstChild.className==='live-empty')lf.innerHTML='';syncList(lf,dlArr.slice(0,30),function(i){return i.url},liveBuild);}`,
		`var ls=document.getElementById('live-scan');`,
		`if(scArr.length===0){if(!ls.firstChild||ls.firstChild.className!=='live-empty')ls.innerHTML='<div class="live-empty">none</div>';}else{if(ls.firstChild&&ls.firstChild.className==='live-empty')ls.innerHTML='';syncList(ls,scArr.slice(0,30),function(i){return i.url},liveBuild);}`,
		`var el=document.getElementById('panel-activity');`,
		`if(inFlight===0&&(ad.total_cloned||0)===0){if(!el.firstChild||el.firstChild.className!=='empty')el.innerHTML='<div class="empty">No scan activity yet - waiting for repositories...</div>';return;}`,
		`if(el.firstChild&&el.firstChild.className==='empty')el.innerHTML='';`,
		`var html='<div class="act-grid">';`,
		`html+='<div class="act-stat"><div class="n">'+dlArr.length+'</div><div class="l">Fetching</div></div>';`,
		`html+='<div class="act-stat"><div class="n">'+scArr.length+'</div><div class="l">Scanning</div></div>';`,
		`html+='<div class="act-stat"><div class="n">'+(ad.total_cloned||0)+'</div><div class="l">Cloned</div></div>';`,
		`html+='<div class="act-stat"><div class="n">'+(ad.total_scanned||0)+'</div><div class="l">Scanned</div></div>';`,
		`html+='<div class="act-stat"><div class="n" style="color:var(--red)">'+(ad.total_failed||0)+'</div><div class="l">Failed</div></div>';`,
		`html+='<div class="act-stat"><div class="n" style="color:var(--orange)">'+(ad.rate_limited||0)+'</div><div class="l">Rate limited</div></div>';`,
		`html+='</div>';`,
		`el.innerHTML=html;`,
		`}`,
		`function liveBuild(i){`,
		`var d=document.createElement('div');d.className='live-item';`,
		`d.innerHTML='<span class="u" title="'+esc(i.url)+'">'+esc(shortRepo(i.url))+'</span><span class="s">'+i.since+'s</span>';`,
		`return d;`,
		`}`,
		`function shortRepo(u){u=String(u||'').replace(/\.git$/,'').replace(/^https?:\/\//,'').replace(/^www\./,'');var parts=u.split('/');if(parts.length>=3)return parts.slice(-2).join('/');return u}`,
		`function short(s,n){s=String(s||'');return s.length>n?s.slice(0,n-1)+'\u2026':s}`,
		`function copy(btn){var v=btn.parentElement.querySelector('.v').textContent;navigator.clipboard.writeText(v).then(function(){btn.textContent='copied';setTimeout(function(){btn.textContent='copy'},1200)})}`,
		`function copyText(v,btn){v=v==null?'':v;var o=btn.textContent;navigator.clipboard.writeText(v).then(function(){btn.textContent='copied';setTimeout(function(){btn.textContent=o},1200)})}`,
		`function copyVal(btn){copyText(btn.parentElement.querySelector('.secval').getAttribute('data-full'),btn)}`,
		`/* ---- file viewer modal ---- */`,
		`var modalState={detail:null,raw:false};`,
		`function detectLang(file){`,
		`var f=String(file||'').toLowerCase();var base=f.split('/').pop();`,
		`if(/^\.env(?:\.[a-z0-9]+)?$/.test(base))return 'env';`,
		`if(/\.(sh|bash|zsh|fish|ksh)$/.test(f))return 'shell';`,
		`if(/\.(json|json5|jsonc|geojson)$/.test(f)||base==='package.json'||base==='tsconfig.json'||base==='composer.json')return 'json';`,
		`if(/\.ya?ml$/.test(f))return 'yaml';`,
		`if(/\.(py|pyw|pyi)$/.test(f))return 'python';`,
		`if(/\.(js|mjs|cjs|jsx)$/.test(f))return 'javascript';`,
		`if(/\.(ts|tsx|mts|cts)$/.test(f))return 'typescript';`,
		`if(/\.go$/.test(f))return 'go';`,
		`if(/\.(java|kt|kts)$/.test(f))return 'java';`,
		`if(/\.(c|h|cc|cpp|cxx|hpp|hxx)$/.test(f))return 'cpp';`,
		`if(/\.cs$/.test(f))return 'csharp';`,
		`if(/\.(rb|rake|gemspec)$/.test(f))return 'ruby';`,
		`if(/\.php$/.test(f))return 'php';`,
		`if(/\.sql$/.test(f))return 'sql';`,
		`if(/\.(html?|vue|svelte)$/.test(f))return 'html';`,
		`if(/\.(css|scss|sass|less)$/.test(f))return 'css';`,
		`if(/\.(md|markdown)$/.test(f))return 'markdown';`,
		`if(/\.(ini|cfg|conf|toml|properties|editorconfig)$/.test(f)||base==='.gitconfig'||base==='.npmrc'||base==='.pypirc'||base==='.envrc')return 'ini';`,
		`if(base.indexOf('dockerfile')===0)return 'dockerfile';`,
		`if(/\.(xml|svg)$/.test(f))return 'xml';`,
		`return 'plain';`,
		`}`,
		`var LANG_RULES={`,
		`plain:[],`,
		`env:[[/^\s*#.*$/,'c'],[/^\s*\[[^\]]*\]\s*$/,'t'],[/^([A-Za-z_][A-Za-z0-9_.\-]*)(?=\s*=)/,'v'],[/"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'/,'s'],[/\b(?:true|false|null|yes|no|on|off)\b/,'b'],[/\b\d+(?:\.\d+)?\b/,'n']],`,
		`shell:[[/^\s*#.*$/,'c'],[/(\$(?:\([^)]*\)|\{[^}]*\}|[A-Za-z_][A-Za-z0-9_]*))/,'v'],[/"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'/,'s'],[/\b(?:if|then|else|elif|fi|for|while|do|done|case|esac|function|return|export|local|source|echo|printf|cd|ls|mkdir|rm|cp|mv|sudo|grep|sed|awk|curl|wget|git|docker|npm|yarn|pnpm|python|pip|go|make|tar|unzip|chmod|chown|cat|head|tail|find|xargs|env)\b/,'k'],[/\b\d+(?:\.\d+)?\b/,'n']],`,
		`json:[[/("(?:[^"\\\n]|\\.)*")(?=\s*:)/,'v'],[/"(?:[^"\\\n]|\\.)*"/,'s'],[/\b(?:true|false|null)\b/,'b'],[/-?\b\d+(?:\.\d+)?(?:[eE][+-]?\d+)?\b/,'n']],`,
		`yaml:[[/^\s*#.*$/,'c'],[/^(\s*(?:- )?[A-Za-z_][A-Za-z0-9_.\-]*(?=\s*:))/,'v'],[/"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'|[&*][A-Za-z0-9_]+/,'s'],[/\b(?:true|false|null|yes|no|on|off)\b/,'b'],[/-?\b\d+(?:\.\d+)?\b/,'n']],`,
		`python:[[/^\s*#.*$/,'c'],[/(?:"""[\s\S]*?"""|'''[\s\S]*?'''|"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*')/,'s'],[/\b(?:def|class|return|if|elif|else|for|while|import|from|as|with|try|except|finally|raise|lambda|pass|break|continue|global|nonlocal|yield|async|await|None|True|False|self|and|or|not|in|is|del|assert)\b/,'k'],[/\b\d+(?:\.\d+)?\b/,'n']],`,
		`javascript:[[/\/\/.*$|\/\*[\s\S]*?\*\//,'c'],[/"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'/,'s'],[/\b(?:var|let|const|function|return|if|else|for|while|do|switch|case|break|continue|new|class|extends|super|this|typeof|instanceof|in|of|try|catch|finally|throw|async|await|yield|import|from|export|default|delete|void|null|undefined|true|false|NaN|Infinity|require|module|exports)\b/,'k'],[/\b\d+(?:\.\d+)?\b/,'n']],`,
		`typescript:[[/\/\/.*$|\/\*[\s\S]*?\*\//,'c'],[/"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'/,'s'],[/\b(?:var|let|const|function|return|if|else|for|while|do|switch|case|break|continue|new|class|extends|implements|interface|type|enum|namespace|declare|abstract|readonly|public|private|protected|super|this|typeof|instanceof|in|of|try|catch|finally|throw|async|await|yield|import|from|export|default|delete|void|null|undefined|true|false|NaN|Infinity|as|require|module)\b/,'k'],[/\b\d+(?:\.\d+)?\b/,'n']],`,
		`go:[[/\/\/.*$|\/\*[\s\S]*?\*\//,'c'],[/"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'/,'s'],[/\b(?:func|package|import|var|const|type|struct|interface|map|chan|go|defer|return|if|else|for|range|switch|case|default|break|continue|fallthrough|select|make|new|len|cap|append|copy|delete|panic|recover|true|false|nil|string|int|int64|uint|uint64|float64|float32|bool|byte|rune|error)\b/,'k'],[/\b\d+(?:\.\d+)?\b/,'n']],`,
		`java:[[/\/\/.*$|\/\*[\s\S]*?\*\//,'c'],[/"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'/,'s'],[/\b(?:public|private|protected|class|interface|enum|extends|implements|static|final|void|int|long|double|float|boolean|char|byte|short|String|Object|new|return|if|else|for|while|do|switch|case|break|continue|try|catch|finally|throw|throws|import|package|this|super|true|false|null|abstract|synchronized|volatile|transient|default|record|var)\b/,'k'],[/\b\d+(?:\.\d+)?[lLfFdD]?\b/,'n']],`,
		`cpp:[[/\/\/.*$|\/\*[\s\S]*?\*\//,'c'],[/"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'/,'s'],[/\b(?:include|define|ifdef|ifndef|endif|pragma|using|namespace|class|struct|template|typename|public|private|protected|virtual|override|const|constexpr|static|inline|extern|void|int|long|short|unsigned|signed|float|double|char|bool|auto|new|delete|return|if|else|for|while|do|switch|case|break|continue|try|catch|throw|true|false|nullptr|this|std|string|vector|map|set|pair|shared_ptr|unique_ptr)\b/,'k'],[/\b\d+(?:\.\d+)?[uUlLfF]?\b/,'n']],`,
		`csharp:[[/\/\/.*$|\/\*[\s\S]*?\*\//,'c'],[/"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'/,'s'],[/\b(?:public|private|protected|internal|class|interface|enum|struct|namespace|using|static|readonly|const|void|int|long|double|float|decimal|bool|char|string|var|new|return|if|else|for|foreach|while|do|switch|case|break|continue|try|catch|finally|throw|async|await|task|this|base|true|false|null|override|virtual|abstract|sealed|partial|get|set|value)\b/,'k'],[/\b\d+(?:\.\d+)?[mMdDfFlL]?\b/,'n']],`,
		`ruby:[[/^\s*#.*$/,'c'],[/"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'/,'s'],[/\b(?:def|end|class|module|require|require_relative|include|extend|attr_reader|attr_writer|attr_accessor|return|if|elsif|else|unless|while|until|for|in|do|case|when|then|begin|rescue|ensure|raise|yield|lambda|proc|new|self|true|false|nil|and|or|not|puts|print|p)\b/,'k'],[/\b\d+(?:\.\d+)?\b/,'n']],`,
		`php:[[/\/\/.*$|#.*$|\/\*[\s\S]*?\*\//,'c'],[/"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'/,'s'],[/\b(?:function|class|interface|trait|namespace|use|return|if|else|elseif|foreach|for|while|do|switch|case|break|continue|new|echo|print|require|require_once|include|include_once|public|private|protected|static|final|const|var|true|false|null|this|self|parent|try|catch|finally|throw|extends|implements|abstract)\b/,'k'],[/\$[A-Za-z_][A-Za-z0-9_]*/,'v'],[/\b\d+(?:\.\d+)?\b/,'n']],`,
		`sql:[[/^\s*--.*$|\/\*[\s\S]*?\*\//,'c'],[/'(?:[^'\\\n]|\\.)*'|"(?:[^"\\\n]|\\.)*"/,'s'],[/\b(?:SELECT|INSERT|UPDATE|DELETE|FROM|WHERE|JOIN|LEFT|RIGHT|INNER|OUTER|FULL|CROSS|ON|GROUP|BY|ORDER|HAVING|LIMIT|OFFSET|AS|AND|OR|NOT|NULL|IN|EXISTS|BETWEEN|LIKE|ILIKE|CREATE|TABLE|ALTER|DROP|INDEX|VIEW|PRIMARY|KEY|FOREIGN|REFERENCES|UNIQUE|DEFAULT|CASE|WHEN|THEN|ELSE|END|UNION|ALL|DISTINCT|COUNT|SUM|AVG|MIN|MAX|VALUES|INTO|SET|GRANT|REVOKE|BEGIN|COMMIT|ROLLBACK|TRUNCATE|DESC|ASC|USING|WITH|RECURSIVE|DATABASE|SCHEMA|TRIGGER|FUNCTION|PROCEDURE)\b/i,'k'],[/\b\d+(?:\.\d+)?\b/,'n']],`,
		`html:[[/<!--[\s\S]*?-->/,'c'],[/<\/?[A-Za-z][A-Za-z0-9-]*/,'t'],[/[A-Za-z-]+(?==)/,'a'],[/"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'/,'s']],`,
		`css:[[/\/\*[\s\S]*?\*\//,'c'],[/^[^\/{};]+(?=\s*\{)/,'v'],[/[A-Za-z-]+(?=\s*:)/,'a'],[/"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'/,'s'],[/\b\d+(?:\.\d+)?(?:px|em|rem|%|vh|vw|s|ms)?\b|#[0-9a-fA-F]{3,8}\b/,'n']],`,
		`markdown:[[/^#{1,6}\s.*$/,'t'],[/\*\*[^*]+\*\*|__[^_]+__/,'k'],[/\x60[^\x60]+\x60/,'s'],[/\[[^\]]*\]\([^)]*\)/,'s'],[/^\s*[-*+]\s/,'p'],[/^\s*\d+\.\s/,'n']],`,
		`ini:[[/^\s*[#;].*$/,'c'],[/^\s*\[[^\]]*\]\s*$/,'t'],[/^([A-Za-z_][A-Za-z0-9_.\-]*)(?=\s*=)/,'v'],[/"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'/,'s'],[/\b\d+(?:\.\d+)?\b/,'n']],`,
		`dockerfile:[[/^\s*#.*$/,'c'],[/^(?:FROM|RUN|CMD|ENTRYPOINT|COPY|ADD|ENV|ARG|WORKDIR|EXPOSE|LABEL|MAINTAINER|USER|VOLUME|SHELL|HEALTHCHECK|STOPSIGNAL|ONBUILD)\b/,'k'],[/"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'/,'s'],[/--[A-Za-z-]+/,'a'],[/\b\d+(?:\.\d+)?\b/,'n']],`,
		`xml:[[/<!--[\s\S]*?-->/,'c'],[/<\/?[A-Za-z][A-Za-z0-9:-]*/,'t'],[/[A-Za-z-:]+(?==)/,'a'],[/"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'/,'s']]`,
		`};`,
		`var RULES_CACHE={};`,
		`function rulesFor(lang){`,
		`var r=RULES_CACHE[lang];if(r)return r;`,
		`var defs=LANG_RULES[lang]||LANG_RULES.plain;`,
		`r=[];`,
		`for(var i=0;i<defs.length;i++){var re=new RegExp(defs[i][0].source,defs[i][0].flags+'g');r.push({re:re,cls:defs[i][1]});}`,
		`RULES_CACHE[lang]=r;return r;`,
		`}`,
		`function tokenizeLine(line,rules){`,
		`if(!rules||!rules.length)return [{t:line,c:null}];`,
		`var segs=[];`,
		`for(var i=0;i<rules.length;i++){`,
		`var re=rules[i].re;re.lastIndex=0;var m;`,
		`while((m=re.exec(line))!==null){segs.push({s:m.index,e:m.index+m[0].length,c:rules[i].cls});if(m[0].length===0)re.lastIndex++;}`,
		`}`,
		`if(!segs.length)return [{t:line,c:null}];`,
		`segs.sort(function(a,b){return a.s-b.s||b.e-a.e});`,
		`var out=[],last=0;`,
		`for(var i=0;i<segs.length;i++){var sg=segs[i];if(sg.s<last)continue;if(sg.s>last)out.push({t:line.slice(last,sg.s),c:null});out.push({t:line.slice(sg.s,sg.e),c:sg.c});last=sg.e;}`,
		`if(last<line.length)out.push({t:line.slice(last),c:null});`,
		`return out;`,
		`}`,
		`function hlText(text,lang){`,
		`if(text==='')return '';`,
		`var toks=tokenizeLine(text,rulesFor(lang));var html='';`,
		`for(var i=0;i<toks.length;i++){var t=toks[i];if(t.c){html+='<span class="tok-'+t.c+'">'+esc(t.t)+'</span>';}else{html+=esc(t.t);}}`,
		`return html;`,
		`}`,
		`function buildCode(content,lang,secret,secLine){`,
		`var lines=String(content==null?'':content).split('\n');`,
		`var html='';`,
		`for(var i=0;i<lines.length;i++){`,
		`var no=i+1;var line=lines[i];`,
		`var isSec=!!(secret&&secLine===no);`,
		`var inner;`,
		`if(isSec){var idx=line.indexOf(secret);if(idx>=0){inner=hlText(line.slice(0,idx),lang)+'<span class="sec">'+esc(secret)+'</span>'+hlText(line.slice(idx+secret.length),lang);}else{inner=hlText(line,lang);}}else{inner=hlText(line,lang);}`,
		`html+='<div class="code-line'+(isSec?' hl-line':'')+'"><span class="ln">'+no+'</span><span class="ct">'+inner+'</span></div>';`,
		`}`,
		`return html;`,
		`}`,
		`function viewFile(id,btn){`,
		`if(btn)btn.disabled=true;`,
		`fetch('/api/file?id='+encodeURIComponent(id)).then(function(r){if(!r.ok)throw new Error('not found');return r.json();}).then(function(d){openModal(d);if(btn)btn.disabled=false;}).catch(function(){if(btn)btn.disabled=false;});`,
		`}`,
		`function modalEsc(e){if(e.key==='Escape')closeModal();}`,
		`function closeModal(){document.removeEventListener('keydown',modalEsc);var ovl=document.getElementById('file-modal');if(ovl)ovl.remove();modalState={detail:null,raw:false};}`,
		`function openModal(d){`,
		`closeModal();`,
		`var lang=detectLang(d.file);`,
		`modalState={detail:d,raw:lang==='env'};`,
		`var ovl=document.createElement('div');ovl.className='ovl';ovl.id='file-modal';`,
		`ovl.innerHTML='<div class="modal">'`,
		`+'<div class="modal-head"><a class="m-repo" href="'+esc(d.url)+'" target="_blank">'+esc(shortRepo(d.url))+'</a><span class="m-file" title="'+esc(d.file)+'">'+esc(d.file)+'</span>'+(d.secret?'<span class="m-sec" title="'+esc(d.secret)+'">'+esc(short(d.secret,120))+'</span>':'')+'<span class="spacer"></span><button class="close" title="Close (Esc)">&#10005;</button></div>'`,
		`+'<div class="modal-tools"><span class="m-lang">'+esc(lang)+'</span><button class="mbtn active" id="mb-hl">highlighted</button><button class="mbtn" id="mb-raw">raw</button><span class="spacer"></span><button class="copy" id="mb-copy">copy raw</button>'+(d.truncated?'<span class="trunc-note">file truncated at 128 KB</span>':'')+'</div>'`,
		`+'<div class="modal-body" id="mb-body"></div>'+'</div>';`,
		`document.body.appendChild(ovl);`,
		`document.getElementById('mb-hl').onclick=function(){modalState.raw=false;renderModal();};`,
		`document.getElementById('mb-raw').onclick=function(){modalState.raw=true;renderModal();};`,
		`document.getElementById('mb-copy').onclick=function(){var b=this;navigator.clipboard.writeText(d.content||'').then(function(){b.textContent='copied';setTimeout(function(){b.textContent='copy raw'},1200);});};`,
		`ovl.querySelector('.close').onclick=closeModal;`,
		`ovl.addEventListener('mousedown',function(e){if(e.target===ovl)closeModal();});`,
		`document.addEventListener('keydown',modalEsc);`,
		`renderModal();`,
		`}`,
		`function renderModal(){`,
		`var d=modalState.detail;if(!d)return;`,
		`var lang=detectLang(d.file);`,
		`var useLang=modalState.raw?'plain':lang;`,
		`document.getElementById('mb-hl').classList.toggle('active',!modalState.raw);`,
		`document.getElementById('mb-raw').classList.toggle('active',modalState.raw);`,
		`document.getElementById('mb-body').innerHTML='<div class="code">'+buildCode(d.content,useLang,d.secret,d.secret_line)+'</div>';`,
		`}`,
		`function applyStats(s){stats=s||{};document.getElementById('total').textContent=stats.total_matches||0;document.getElementById('crit').textContent=(stats.matches_by_priority&&stats.matches_by_priority[3])||0;document.getElementById('high').textContent=(stats.matches_by_priority&&stats.matches_by_priority[2])||0;document.getElementById('med').textContent=(stats.matches_by_priority&&stats.matches_by_priority[1])||0;document.getElementById('low').textContent=(stats.matches_by_priority&&stats.matches_by_priority[0])||0;document.getElementById('s-crit').textContent=(stats.matches_by_priority&&stats.matches_by_priority[3])||0;document.getElementById('s-high').textContent=(stats.matches_by_priority&&stats.matches_by_priority[2])||0;document.getElementById('s-med').textContent=(stats.matches_by_priority&&stats.matches_by_priority[1])||0;document.getElementById('s-low').textContent=(stats.matches_by_priority&&stats.matches_by_priority[0])||0;}`,
		`function handleEvent(ev){`,
		`if(ev.type==='match'){if(ev.match)matches.push(ev.match);if(ev.stats)applyStats(ev.stats);updateSigSelect();render();}`,
		`else if(ev.type==='token'){if(ev.token)tokens.push(ev.token);renderTokens();}`,
		`else if(ev.type==='log'){if(ev.log)logs.push(ev.log);renderLogs();}`,
		`else if(ev.type==='activity'){if(ev.activity)activityData=ev.activity;renderActivity();}`,
		`else if(ev.type==='snapshot'){if(ev.activity)activityData=ev.activity;renderActivity();}`,
		`}`,
		`function loadInitial(){`,
		`fetch('/api/ws').then(function(r){return r.json()}).then(function(d){matches=d.matches||[];applyStats(d.stats||{});updateSigSelect();render();});`,
		`fetch('/api/tokens').then(function(r){return r.json()}).then(function(d){tokens=d.tokens||[];renderTokens();});`,
		`fetch('/api/logs').then(function(r){return r.json()}).then(function(d){logs=d.logs||[];renderLogs();});`,
		`}`,
		`function updateSigSelect(){`,
		`var map={};matches.forEach(function(m){map[m.signature]=(map[m.signature]||0)+1});`,
		`var names=Object.keys(map).sort();`,
		`var sel=document.getElementById('sig');var cur=sel.value;`,
		`var sigHtml='<option value="">All</option>'+names.map(function(n){return '<option value="'+esc(n)+'">'+esc(n)+' ('+map[n]+')</option>'}).join('');`,
		`if(sel.getAttribute('data-html')!==sigHtml){sel.innerHTML=sigHtml;sel.setAttribute('data-html',sigHtml);}`,
		`sel.value=cur;`,
		`var srcs={};matches.forEach(function(m){srcs[m.source]=1});`,
		`var ss=document.getElementById('src');var sc=ss.value;`,
		`var srcHtml='<option value="">All</option>'+Object.keys(srcs).sort().map(function(n){return '<option>'+esc(n)+'</option>'}).join('');`,
		`if(ss.getAttribute('data-html')!==srcHtml){ss.innerHTML=srcHtml;ss.setAttribute('data-html',srcHtml);}`,
		`ss.value=sc;`,
		`var top=(stats.top_signatures||[]);`,
		`var sigsEl=document.getElementById('sigs');`,
		`if(top.length===0){if(!sigsEl.firstChild||sigsEl.firstChild.className!=='live-empty')sigsEl.innerHTML='<div class="live-empty">none</div>';}`,
		`else{if(sigsEl.firstChild&&sigsEl.firstChild.className==='live-empty')sigsEl.innerHTML='';`,
		`syncList(sigsEl,top,function(s){return s.name},function(s){var d=document.createElement('div');d.className='sig-item'+(state.sig===s.name?' active':'');d.innerHTML='<span>'+esc(s.name)+'</span><span class="c">'+s.count+'</span>';d.onclick=function(){state.sig=(state.sig===s.name?'':s.name);document.getElementById('sig').value=state.sig;render();};return d;});}`,
		`}`,
		`loadInitial();`,
		`var es=new EventSource('/api/events');`,
		`es.onmessage=function(e){try{handleEvent(JSON.parse(e.data));}catch(err){}};`,
		`es.onerror=function(){/* EventSource auto-reconnects */};`,
		`</script>`,
		`</body>`,
		`</html>`,
	}, "\n")
}
