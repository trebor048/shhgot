package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/trebor048/shhgot/core"
)

// WebServerConfig holds configuration for the web server
type WebServerConfig struct {
	Host            string
	Port            string
	EnableTLS       bool
	CertFile        string
	KeyFile         string
	EnableAuth      bool
	RateLimitPerSec float64
	EnableTunnel    bool
	TunnelName      string
}

// MatchPage represents a paginated response of matches
type MatchPage struct {
	Matches    []*Match `json:"matches"`
	Pagination struct {
		Page       int  `json:"page"`
		Limit      int  `json:"limit"`
		Total      int  `json:"total"`
		TotalPages int  `json:"total_pages"`
		HasNext    bool `json:"has_next"`
		HasPrev    bool `json:"has_prev"`
	} `json:"pagination"`
	Filters map[string]interface{} `json:"filters"`
}

// WebSocketUpgrader handles WebSocket connections
var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for now (restrict in production)
	},
}

// Prometheus metrics
var (
	matchesCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "shhgit_matches_total",
			Help: "Total number of matches detected",
		},
		[]string{"source", "priority"},
	)

	apiRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "shhgit_api_request_duration_seconds",
			Help:    "API request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"endpoint", "method", "status"},
	)

	apiRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "shhgit_api_requests_total",
			Help: "Total API requests",
		},
		[]string{"endpoint", "method", "status"},
	)
)

func init() {
	prometheus.MustRegister(matchesCounter)
	prometheus.MustRegister(apiRequestDuration)
	prometheus.MustRegister(apiRequestsTotal)
}

// MetricsMiddleware wraps handlers to record metrics
func MetricsMiddleware(endpoint string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Create a response writer wrapper to capture status code
		wrapped := &responseWriterWrapper{ResponseWriter: w, statusCode: 200}

		next(wrapped, r)

		duration := time.Since(start).Seconds()
		status := strconv.Itoa(wrapped.statusCode)

		apiRequestDuration.WithLabelValues(endpoint, r.Method, status).Observe(duration)
		apiRequestsTotal.WithLabelValues(endpoint, r.Method, status).Inc()
	}
}

// responseWriterWrapper captures the status code
type responseWriterWrapper struct {
	http.ResponseWriter
	statusCode int
}

func (w *responseWriterWrapper) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

// GetMatchesPaginated returns paginated matches with optional filtering
func GetMatchesPaginated(w http.ResponseWriter, r *http.Request) {
	// Parse query parameters
	pageStr := r.URL.Query().Get("page")
	if pageStr == "" {
		pageStr = "1"
	}
	page, _ := strconv.Atoi(pageStr)
	if page < 1 {
		page = 1
	}

	limitStr := r.URL.Query().Get("limit")
	if limitStr == "" {
		limitStr = "50"
	}
	limit, _ := strconv.Atoi(limitStr)
	if limit < 1 || limit > 500 {
		limit = 50
	}

	// Get filters
	source := r.URL.Query().Get("source")
	signature := r.URL.Query().Get("signature")
	priorityStr := r.URL.Query().Get("priority")
	sortBy := r.URL.Query().Get("sort")
	order := r.URL.Query().Get("order")
	if order == "" {
		order = "desc"
	}

	// Acquire read lock
	hub := ensureWebHub()
	hub.matchesMutex.RLock()
	defer hub.matchesMutex.RUnlock()

	// Filter matches
	filtered := make([]*Match, 0)
	for _, m := range hub.matches {
		if source != "" && m.Source != source {
			continue
		}
		if signature != "" && m.Signature != signature {
			continue
		}
		if priorityStr != "" {
			priority, _ := strconv.Atoi(priorityStr)
			if m.Priority != priority {
				continue
			}
		}
		filtered = append(filtered, m)
	}

	// Sort matches
	switch sortBy {
	case "timestamp":
		if order == "asc" {
			sort.Slice(filtered, func(i, j int) bool {
				return filtered[i].Timestamp.Before(filtered[j].Timestamp)
			})
		} else {
			sort.Slice(filtered, func(i, j int) bool {
				return filtered[i].Timestamp.After(filtered[j].Timestamp)
			})
		}
	case "priority":
		if order == "asc" {
			sort.Slice(filtered, func(i, j int) bool {
				return filtered[i].Priority < filtered[j].Priority
			})
		} else {
			sort.Slice(filtered, func(i, j int) bool {
				return filtered[i].Priority > filtered[j].Priority
			})
		}
	default:
		// Default: sort by timestamp descending
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].Timestamp.After(filtered[j].Timestamp)
		})
	}

	// Paginate
	total := len(filtered)
	totalPages := (total + limit - 1) / limit
	offset := (page - 1) * limit
	if offset >= total {
		offset = 0
		page = 1
	}

	end := offset + limit
	if end > total {
		end = total
	}

	paginated := filtered[offset:end]

	// Build response
	resp := MatchPage{
		Matches: paginated,
	}
	resp.Pagination.Page = page
	resp.Pagination.Limit = limit
	resp.Pagination.Total = total
	resp.Pagination.TotalPages = totalPages
	resp.Pagination.HasNext = page < totalPages
	resp.Pagination.HasPrev = page > 1

	resp.Filters = map[string]interface{}{
		"source":    source,
		"signature": signature,
		"priority":  priorityStr,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// GetMatchByID returns a specific match by ID
func GetMatchByID(w http.ResponseWriter, r *http.Request) {
	matchID := strings.TrimPrefix(r.URL.Path, "/api/matches/")
	if matchID == "" {
		http.Error(w, `{"error": "match ID required"}`, http.StatusBadRequest)
		return
	}

	hub := ensureWebHub()
	hub.matchesMutex.RLock()
	defer hub.matchesMutex.RUnlock()

	for _, m := range hub.matches {
		if m.ID == matchID {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(m)
			return
		}
	}

	http.Error(w, `{"error": "match not found"}`, http.StatusNotFound)
}

// HandleWebSocket handles WebSocket connections for real-time updates
func HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}
	defer conn.Close()

	hub := ensureWebHub()

	// Send initial state
	hub.matchesMutex.RLock()
	data := map[string]interface{}{
		"type":    "snapshot",
		"matches": hub.matches,
		"stats":   hub.stats,
	}
	hub.matchesMutex.RUnlock()

	conn.WriteJSON(data)

	// Keep connection alive and listen for closes
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			break
		}
	}
}

// HealthCheck returns server health status
func HealthCheck(w http.ResponseWriter, r *http.Request) {
	hub := ensureWebHub()
	hub.matchesMutex.RLock()
	matchCount := len(hub.matches)
	hub.matchesMutex.RUnlock()

	health := map[string]interface{}{
		"status":    "ok",
		"timestamp": time.Now(),
		"matches":   matchCount,
		"uptime":    time.Since(startTime).Seconds(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(health)
}

// ExportMatches exports filtered matches as CSV or JSON
func ExportMatches(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}

	// Get filters from query params
	source := r.URL.Query().Get("source")
	signature := r.URL.Query().Get("signature")
	priorityStr := r.URL.Query().Get("priority")

	hub := ensureWebHub()
	hub.matchesMutex.RLock()
	defer hub.matchesMutex.RUnlock()

	// Filter matches
	filtered := make([]*Match, 0)
	for _, m := range hub.matches {
		if source != "" && m.Source != source {
			continue
		}
		if signature != "" && m.Signature != signature {
			continue
		}
		if priorityStr != "" {
			priority, _ := strconv.Atoi(priorityStr)
			if m.Priority != priority {
				continue
			}
		}
		filtered = append(filtered, m)
	}

	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=shhgit-matches-%d.csv", time.Now().Unix()))

		// Write CSV header
		fmt.Fprint(w, "ID,Timestamp,Source,URL,File,Signature,Priority,Matches\n")

		// Write rows
		for _, m := range filtered {
			matchesStr := strings.Join(m.Matches, ";")
			fmt.Fprintf(w, "%s,%s,%s,%s,%s,%s,%d,%s\n",
				m.ID, m.Timestamp.Format(time.RFC3339), m.Source, m.URL, m.File, m.Signature, m.Priority, matchesStr)
		}
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=shhgit-matches-%d.json", time.Now().Unix()))
		json.NewEncoder(w).Encode(filtered)
	}
}

// StartWebServerV2 starts the enhanced web server
func StartWebServerV2(config WebServerConfig) error {
	mux := http.NewServeMux()

	// Initialize rate limiter
	rateLimiter := core.NewRateLimiter(config.RateLimitPerSec, 10)

	// API endpoints
	mux.HandleFunc("/health", HealthCheck)
	mux.HandleFunc("/metrics", promhttp.Handler().ServeHTTP)

	// Protected endpoints (with rate limiting and optional auth)
	mux.HandleFunc("/api/matches", func(w http.ResponseWriter, r *http.Request) {
		core.AuthMiddleware(rateLimiter.Middleware(GetMatchesPaginated), config.EnableAuth)(w, r)
	})

	mux.HandleFunc("/api/matches/export", func(w http.ResponseWriter, r *http.Request) {
		core.AuthMiddleware(rateLimiter.Middleware(ExportMatches), config.EnableAuth)(w, r)
	})

	mux.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
		core.AuthMiddleware(HandleWebSocket, config.EnableAuth)(w, r)
	})

	mux.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
		core.AuthMiddleware(rateLimiter.Middleware(getStats), config.EnableAuth)(w, r)
	})

	mux.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		core.AuthMiddleware(rateLimiter.Middleware(getLogs), config.EnableAuth)(w, r)
	})

	mux.HandleFunc("/api/tokens", func(w http.ResponseWriter, r *http.Request) {
		core.AuthMiddleware(rateLimiter.Middleware(getTokens), config.EnableAuth)(w, r)
	})

	mux.HandleFunc("/api/signatures", func(w http.ResponseWriter, r *http.Request) {
		core.AuthMiddleware(rateLimiter.Middleware(getSignatures), config.EnableAuth)(w, r)
	})

	mux.HandleFunc("/api/activity", func(w http.ResponseWriter, r *http.Request) {
		core.AuthMiddleware(rateLimiter.Middleware(getActivity), config.EnableAuth)(w, r)
	})

	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		core.AuthMiddleware(eventsHandler, config.EnableAuth)(w, r)
	})

	mux.HandleFunc("/api/matches/", func(w http.ResponseWriter, r *http.Request) {
		core.AuthMiddleware(GetMatchByID, config.EnableAuth)(w, r)
	})

	// Regex optimizer stats
	mux.HandleFunc("/api/stats/regex", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		stats := core.GlobalRegexOptimizer.Stats()
		json.NewEncoder(w).Encode(stats)
	})

	// Scan progress
	mux.HandleFunc("/api/scan/progress", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		snapshot := core.GlobalScanProgress.GetSnapshot()
		json.NewEncoder(w).Encode(snapshot)
	})

	// Worker pool management endpoints
	if core.GlobalWorkerPool != nil {
		poolHandler := core.NewPoolAPIHandler(core.GlobalWorkerPool)
		mux.HandleFunc("/api/pool/stats", poolHandler.GetStats)
		mux.HandleFunc("/api/pool/pause", poolHandler.Pause)
		mux.HandleFunc("/api/pool/resume", poolHandler.Resume)
		mux.HandleFunc("/api/pool/queue", poolHandler.GetQueueStatus)
		mux.HandleFunc("/api/pool/resize", poolHandler.Resize)
		mux.HandleFunc("/api/pool/job", poolHandler.SubmitJob)
	}

	// Serve embedded web UI
	mux.HandleFunc("/", serveEmbeddedWeb)

	// CORS middleware
	corsHandler := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, DELETE, PUT")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
			w.Header().Set("Access-Control-Max-Age", "3600")

			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next(w, r)
		}
	}

	// Wrap all handlers with CORS
	finalHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		corsHandler(mux.ServeHTTP)(w, r)
	})

	// Configure server
	addr := fmt.Sprintf("%s:%s", config.Host, config.Port)
	server := &http.Server{
		Addr:         addr,
		Handler:      finalHandler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("[SERVER] Starting web server on %s", addr)

	// Start Cloudflare Tunnel if enabled
	if config.EnableTunnel {
		core.PrintTunnelSetupInstructions(config.TunnelName)
		go func() {
			tunnelConfig := &core.TunnelConfig{
				Enabled:    true,
				TunnelName: config.TunnelName,
				LocalURL:   fmt.Sprintf("http://localhost:%s", config.Port),
			}
			if err := core.StartCloudflaredTunnel(tunnelConfig); err != nil {
				log.Printf("[TUNNEL] Error: %v", err)
			}
		}()
	}

	// Start TLS or plain HTTP
	if config.EnableTLS && config.CertFile != "" && config.KeyFile != "" {
		log.Printf("[SERVER] Starting with TLS")
		return server.ListenAndServeTLS(config.CertFile, config.KeyFile)
	} else {
		log.Printf("[SERVER] Starting without TLS (HTTP only)")
		return server.ListenAndServe()
	}
}

var startTime = time.Now()
