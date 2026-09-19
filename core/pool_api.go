package core

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// PoolAPIHandler handles worker pool API requests
type PoolAPIHandler struct {
	pool *WorkerPool
}

// NewPoolAPIHandler creates a new pool API handler
func NewPoolAPIHandler(pool *WorkerPool) *PoolAPIHandler {
	return &PoolAPIHandler{pool: pool}
}

// GetStats returns pool statistics
func (h *PoolAPIHandler) GetStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	stats := h.pool.Stats()
	json.NewEncoder(w).Encode(stats)
}

// PausePool pauses the worker pool
func (h *PoolAPIHandler) Pause(w http.ResponseWriter, r *http.Request) {
	h.pool.Pause()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "paused"})
}

// ResumePool resumes the worker pool
func (h *PoolAPIHandler) Resume(w http.ResponseWriter, r *http.Request) {
	h.pool.Resume()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "resumed"})
}

// SubmitJob submits a single job to the pool
func (h *PoolAPIHandler) SubmitJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		RepoURL  string `json:"repo_url"`
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
		Priority int    `json:"priority"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	job := &ScanJob{
		ID:         generateJobID(),
		RepoURL:    req.RepoURL,
		FilePath:   req.FilePath,
		Content:    req.Content,
		Priority:   req.Priority,
		MaxRetries: 3,
	}

	if h.pool.Submit(job) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "submitted",
			"job_id": job.ID,
		})
	} else {
		http.Error(w, "Failed to submit job", http.StatusServiceUnavailable)
	}
}

// GetQueueStatus returns current queue status
func (h *PoolAPIHandler) GetQueueStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	status := map[string]interface{}{
		"queue_length": h.pool.QueueLength(),
		"active_jobs":  h.pool.ActiveJobs(),
		"is_busy":      h.pool.IsBusy(),
	}
	json.NewEncoder(w).Encode(status)
}

// ResizePool resizes the worker pool
func (h *PoolAPIHandler) Resize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sizeStr := r.URL.Query().Get("size")
	if sizeStr == "" {
		http.Error(w, "Missing size parameter", http.StatusBadRequest)
		return
	}

	size, err := strconv.Atoi(sizeStr)
	if err != nil || size < 1 || size > 256 {
		http.Error(w, "Invalid size", http.StatusBadRequest)
		return
	}

	// Note: Resizing requires stopping and restarting the pool
	// This is a simplified API - full implementation would handle graceful resize
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message":         "Resize would require pool restart",
		"current_workers": h.pool.workers,
		"requested_size":  size,
	})
}

// Helper function to generate job ID
func generateJobID() string {
	return "job_" + strconv.FormatInt(int64(time.Now().UnixNano()), 36)
}
