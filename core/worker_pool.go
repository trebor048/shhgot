package core

import (
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// ScanJob represents a single scanning job
type ScanJob struct {
	ID         string
	RepoURL    string
	FilePath   string
	Content    string
	Priority   int
	RetryCount int
	MaxRetries int
	CreatedAt  time.Time
	ResultChan chan *ScanResult
}

// ScanResult contains the result of scanning a job
type ScanResult struct {
	JobID      string
	RepoURL    string
	FilePath   string
	Matches    []*Match
	MatchCount int
	Duration   time.Duration
	Error      error
	Success    bool
	CacheHit   bool
}

// WorkerPool manages concurrent scanning workers
type WorkerPool struct {
	workers        int
	jobQueue       chan *ScanJob
	resultQueue    chan *ScanResult
	regexOptimizer *RegexOptimizer
	wg             sync.WaitGroup

	// Metrics
	jobsProcessed  int64
	jobsSuccessful int64
	jobsFailed     int64
	totalDuration  int64
	avgJobDuration time.Duration

	// Control
	done          chan struct{}
	paused        bool
	pauseMutex    sync.RWMutex
	activeJobs    int32
	maxActiveJobs int32
}

// GlobalWorkerPool is the singleton worker pool instance
var GlobalWorkerPool *WorkerPool

// InitGlobalWorkerPool initializes the global worker pool
func InitGlobalWorkerPool(numWorkers int, queueSize int, regexOpt *RegexOptimizer) {
	if GlobalWorkerPool == nil {
		GlobalWorkerPool = NewWorkerPool(numWorkers, queueSize, regexOpt)
		log.Printf("[POOL] Global worker pool initialized: %d workers, queue=%d", numWorkers, queueSize)
	}
}

// NewWorkerPool creates a new worker pool
func NewWorkerPool(numWorkers int, queueSize int, regexOpt *RegexOptimizer) *WorkerPool {
	return &WorkerPool{
		workers:        numWorkers,
		jobQueue:       make(chan *ScanJob, queueSize),
		resultQueue:    make(chan *ScanResult, queueSize),
		regexOptimizer: regexOpt,
		done:           make(chan struct{}),
		maxActiveJobs:  int32(numWorkers),
	}
}

// Start initializes and starts all workers
func (wp *WorkerPool) Start() {
	log.Printf("[POOL] Starting worker pool with %d workers", wp.workers)

	for i := 0; i < wp.workers; i++ {
		wp.wg.Add(1)
		go wp.worker(i)
	}

	log.Printf("[POOL] Worker pool started")
}

// Stop gracefully shuts down the worker pool
func (wp *WorkerPool) Stop() {
	log.Printf("[POOL] Stopping worker pool...")
	close(wp.done)
	wp.wg.Wait()
	close(wp.jobQueue)
	close(wp.resultQueue)
	log.Printf("[POOL] Worker pool stopped")
}

// Submit adds a job to the queue
func (wp *WorkerPool) Submit(job *ScanJob) bool {
	wp.pauseMutex.RLock()
	if wp.paused {
		wp.pauseMutex.RUnlock()
		return false
	}
	wp.pauseMutex.RUnlock()

	select {
	case wp.jobQueue <- job:
		return true
	case <-wp.done:
		return false
	default:
		return false // Queue full
	}
}

// Pause pauses job processing
func (wp *WorkerPool) Pause() {
	wp.pauseMutex.Lock()
	defer wp.pauseMutex.Unlock()
	wp.paused = true
	log.Printf("[POOL] Worker pool paused")
}

// Resume resumes job processing
func (wp *WorkerPool) Resume() {
	wp.pauseMutex.Lock()
	defer wp.pauseMutex.Unlock()
	wp.paused = false
	log.Printf("[POOL] Worker pool resumed")
}

// worker processes jobs from the queue
func (wp *WorkerPool) worker(id int) {
	defer wp.wg.Done()

	log.Printf("[WORKER-%d] Started", id)

	for {
		select {
		case <-wp.done:
			log.Printf("[WORKER-%d] Shutdown", id)
			return

		case job := <-wp.jobQueue:
			if job == nil {
				return
			}

			// Wait if paused
			wp.pauseMutex.RLock()
			if wp.paused {
				wp.pauseMutex.RUnlock()
				// Re-queue the job
				wp.jobQueue <- job
				time.Sleep(100 * time.Millisecond)
				continue
			}
			wp.pauseMutex.RUnlock()

			// Process the job
			result := wp.processJob(id, job)

			// Send result
			select {
			case wp.resultQueue <- result:
			case <-wp.done:
				return
			}

			// Update metrics
			atomic.AddInt64(&wp.jobsProcessed, 1)
			if result.Success {
				atomic.AddInt64(&wp.jobsSuccessful, 1)
			} else {
				atomic.AddInt64(&wp.jobsFailed, 1)
			}
			atomic.AddInt64(&wp.totalDuration, int64(result.Duration))
		}
	}
}

// processJob scans a single job
func (wp *WorkerPool) processJob(workerID int, job *ScanJob) *ScanResult {
	startTime := time.Now()
	atomic.AddInt32(&wp.activeJobs, 1)
	defer atomic.AddInt32(&wp.activeJobs, -1)

	result := &ScanResult{
		JobID:    job.ID,
		RepoURL:  job.RepoURL,
		FilePath: job.FilePath,
		Matches:  make([]*Match, 0),
		Success:  true,
	}

	// Update scan progress
	GlobalScanProgress.UpdateProgress(job.RepoURL, job.FilePath, 0)

	// Use pre-allocated patterns list (no rebuilding)
	patterns := wp.regexOptimizer.GetPatternsList()

	// Parallel matching with priority-based early exit
	matchResults := wp.regexOptimizer.ParallelMatch(patterns, job.Content)

	// Collect matches
	matchCount := 0
	for patternName, matched := range matchResults {
		if matched {
			matchCount++
			result.Matches = append(result.Matches, &Match{
				ID:        job.ID,
				Signature: patternName,
				File:      job.FilePath,
				URL:       job.RepoURL,
			})
		}
	}

	result.MatchCount = matchCount
	result.Duration = time.Since(startTime)

	// Update scan progress
	GlobalScanProgress.UpdateProgress(job.RepoURL, job.FilePath, matchCount)

	if matchCount > 0 {
		log.Printf("[WORKER-%d] Job %s completed in %v (%d matches)", workerID, job.ID, result.Duration, matchCount)
	}

	return result
}

// GetResults returns a channel to receive results
func (wp *WorkerPool) GetResults() <-chan *ScanResult {
	return wp.resultQueue
}

// Stats returns current pool statistics
func (wp *WorkerPool) Stats() map[string]interface{} {
	totalJobs := atomic.LoadInt64(&wp.jobsProcessed)
	totalDuration := time.Duration(atomic.LoadInt64(&wp.totalDuration))

	avgDuration := time.Duration(0)
	if totalJobs > 0 {
		avgDuration = totalDuration / time.Duration(totalJobs)
	}

	successRate := float64(0)
	if totalJobs > 0 {
		successRate = float64(atomic.LoadInt64(&wp.jobsSuccessful)) / float64(totalJobs) * 100
	}

	return map[string]interface{}{
		"workers":           wp.workers,
		"queue_size":        len(wp.jobQueue),
		"result_queue_size": len(wp.resultQueue),
		"active_jobs":       atomic.LoadInt32(&wp.activeJobs),
		"jobs_processed":    atomic.LoadInt64(&wp.jobsProcessed),
		"jobs_successful":   atomic.LoadInt64(&wp.jobsSuccessful),
		"jobs_failed":       atomic.LoadInt64(&wp.jobsFailed),
		"success_rate":      successRate,
		"avg_job_duration":  avgDuration.String(),
		"total_duration":    totalDuration.String(),
		"is_paused":         wp.paused,
	}
}

// QueueLength returns current queue length
func (wp *WorkerPool) QueueLength() int {
	return len(wp.jobQueue)
}

// ActiveJobs returns number of currently active jobs
func (wp *WorkerPool) ActiveJobs() int {
	return int(atomic.LoadInt32(&wp.activeJobs))
}

// IsBusy checks if pool is at capacity
func (wp *WorkerPool) IsBusy() bool {
	return int32(len(wp.jobQueue)) > wp.maxActiveJobs
}

// WaitForResults waits for and collects results
func (wp *WorkerPool) WaitForResults(count int, timeout time.Duration) []*ScanResult {
	results := make([]*ScanResult, 0, count)
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for i := 0; i < count; i++ {
		select {
		case result := <-wp.resultQueue:
			if result != nil {
				results = append(results, result)
			}
		case <-timer.C:
			log.Printf("[POOL] Timeout waiting for results. Got %d of %d", len(results), count)
			return results
		}
	}

	return results
}

// BatchProcess submits multiple jobs and waits for results
func (wp *WorkerPool) BatchProcess(jobs []*ScanJob, timeout time.Duration) []*ScanResult {
	// Submit all jobs
	submitted := 0
	for _, job := range jobs {
		if wp.Submit(job) {
			submitted++
		}
	}

	log.Printf("[POOL] Submitted %d of %d jobs", submitted, len(jobs))

	// Wait for results
	return wp.WaitForResults(submitted, timeout)
}

// Match represents a security match
type Match struct {
	ID        string
	Signature string
	File      string
	URL       string
	Line      int
	Secret    string
	Matches   []string
	Priority  int
	Timestamp time.Time
}
