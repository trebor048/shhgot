package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

// Match represents a detected secret match
type Match struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Source    string    `json:"source"`
	URL       string    `json:"url"`
	File      string    `json:"file"`
	Signature string    `json:"signature"`
	Matches   []string  `json:"matches"`
	Stars     int       `json:"stars"`
	Priority  int       `json:"priority"`
	Color     string    `json:"color"`
}

// Stats holds aggregated statistics
type Stats struct {
	TotalMatches      int                `json:"total_matches"`
	MatchesBySource   map[string]int     `json:"matches_by_source"`
	MatchesBySignature map[string]int    `json:"matches_by_signature"`
	MatchesByPriority map[int]int        `json:"matches_by_priority"`
	TopSignatures     []SignatureStat    `json:"top_signatures"`
	LastUpdated       time.Time          `json:"last_updated"`
}

// SignatureStat represents a signature with its match count
type SignatureStat struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// Hub manages WebSocket connections and match broadcasting
type Hub struct {
	clients      map[*Client]bool
	broadcast    chan *Match
	register     chan *Client
	unregister   chan *Client
	matches      []*Match
	stats        Stats
	matchesMutex sync.RWMutex
}

// Client represents a WebSocket connection
type Client struct {
	hub      *Hub
	conn     interface{} // Will be replaced with actual WebSocket conn in handler
	send     chan interface{}
}

var (
	port       = flag.String("port", "8000", "Port to run the server on")
	h          *Hub
	maxMatches = 10000 // Keep last N matches in memory
)

func newHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan *Match, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		matches:    make([]*Match, 0, maxMatches),
		stats: Stats{
			MatchesBySource:    make(map[string]int),
			MatchesBySignature: make(map[string]int),
			MatchesByPriority:  make(map[int]int),
		},
	}
}

func (h *Hub) run() {
	for {
		select {
		case client := <-h.register:
			h.clients[client] = true
			// Send existing matches to new client
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
			// Add to history
			h.matches = append(h.matches, match)
			if len(h.matches) > maxMatches {
				h.matches = h.matches[len(h.matches)-maxMatches:]
			}

			// Update stats
			h.stats.TotalMatches++
			h.stats.MatchesBySource[match.Source]++
			h.stats.MatchesBySignature[match.Signature]++
			h.stats.MatchesByPriority[match.Priority]++
			h.stats.LastUpdated = time.Now()

			// Recalculate top signatures
			h.updateTopSignatures()
			h.matchesMutex.Unlock()

			// Broadcast to all clients
			data := map[string]interface{}{
				"type":  "match",
				"match": match,
				"stats": h.stats,
			}
			for client := range h.clients {
				select {
				case client.send <- data:
				default:
					// Client's send channel is full, drop it
					go func(c *Client) {
						h.unregister <- c
					}(client)
				}
			}
		}
	}
}

func (h *Hub) updateTopSignatures() {
	type kv struct {
		Key   string
		Value int
	}
	var ss []kv
	for k, v := range h.stats.MatchesBySignature {
		ss = append(ss, kv{k, v})
	}
	// Simple sort by count (descending)
	for i := 0; i < len(ss); i++ {
		for j := i + 1; j < len(ss); j++ {
			if ss[j].Value > ss[i].Value {
				ss[i], ss[j] = ss[j], ss[i]
			}
		}
	}
	// Keep top 10
	limit := 10
	if len(ss) < limit {
		limit = len(ss)
	}
	h.stats.TopSignatures = make([]SignatureStat, limit)
	for i := 0; i < limit; i++ {
		h.stats.TopSignatures[i] = SignatureStat{Name: ss[i].Key, Count: ss[i].Value}
	}
}

// ReceiveMatch handles POST requests with match data from CLI
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

	h.broadcast <- &match
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// GetStats returns current statistics
func getStats(w http.ResponseWriter, r *http.Request) {
	h.matchesMutex.RLock()
	stats := h.stats
	h.matchesMutex.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// GetMatches returns all matches with optional filtering
func getMatches(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	signature := r.URL.Query().Get("signature")
	priority := r.URL.Query().Get("priority")

	h.matchesMutex.RLock()
	defer h.matchesMutex.RUnlock()

	filtered := make([]*Match, 0)
	for _, m := range h.matches {
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

// WebSocket handler
func wsHandler(w http.ResponseWriter, r *http.Request) {
	// For now, we'll skip actual WebSocket implementation and use polling
	// In production, use gorilla/websocket package
	w.Header().Set("Content-Type", "application/json")
	h.matchesMutex.RLock()
	json.NewEncoder(w).Encode(map[string]interface{}{
		"matches": h.matches,
		"stats":   h.stats,
	})
	h.matchesMutex.RUnlock()
}

// Health check endpoint
func health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
}

// CORS middleware
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

func main() {
	flag.Parse()

	h = newHub()
	go h.run()

	// API endpoints
	http.HandleFunc("/api/push", corsMiddleware(receiveMatch))
	http.HandleFunc("/api/stats", corsMiddleware(getStats))
	http.HandleFunc("/api/matches", corsMiddleware(getMatches))
	http.HandleFunc("/api/ws", corsMiddleware(wsHandler))
	http.HandleFunc("/health", corsMiddleware(health))

	// Serve static frontend
	http.Handle("/", http.FileServer(http.Dir("./frontend/dist")))

	addr := ":" + *port
	log.Printf("Starting shhgit server on %s", addr)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("Server error: %v", err)
		os.Exit(1)
	}
}
