package core

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
)

// webhookWarnOnce makes the queue report webhook trouble at most once per run.
// A successful delivery is never logged: one line per send would drown the
// match feed, which is what the operator is actually watching. A failure or a
// rate limit is worth surfacing, but only the first one.
var webhookWarnOnce sync.Once

// warnWebhookOnce prints a red one-line warning the first time it is called and
// stays silent afterwards.
func warnWebhookOnce(reason string) {
	webhookWarnOnce.Do(func() {
		red := color.New(color.FgHiRed, color.Bold).SprintFunc()
		Say("%s %s\n", red("[webhook]"), red(reason+" - further webhook errors will be suppressed"))
	})
}

// WebhookQueueItem represents a single webhook message to send
type WebhookQueueItem struct {
	URL        string
	Payload    string
	Timestamp  time.Time
	Hash       string // For deduplication
	RetryCount int
	MaxRetries int
}

// WebhookQueue manages webhook delivery with deduplication, rate limiting, and retries
type WebhookQueue struct {
	queue          chan *WebhookQueueItem
	sent           map[string]time.Time // hash -> last sent time
	sentMutex      sync.Mutex
	isRunning      bool
	ticker         *time.Ticker
	stopChan       chan bool
	rateLimitMS    int64         // milliseconds between sends per webhook
	dedupeWindow   time.Duration // how long to track duplicates
	outputFileChan chan string   // for splitting output to files
}

// NewWebhookQueue creates a new webhook queue with the specified config
func NewWebhookQueue(queueSize int, rateLimitMS int64, dedupeWindowSec int) *WebhookQueue {
	return &WebhookQueue{
		queue:        make(chan *WebhookQueueItem, queueSize),
		sent:         make(map[string]time.Time),
		rateLimitMS:  rateLimitMS,
		dedupeWindow: time.Duration(dedupeWindowSec) * time.Second,
		stopChan:     make(chan bool),
	}
}

// Start begins processing the webhook queue
func (wq *WebhookQueue) Start() {
	if wq.isRunning {
		return
	}
	wq.isRunning = true

	// Process queue items
	go func() {
		for {
			select {
			case item := <-wq.queue:
				if item != nil {
					wq.processItem(item)
				}
			case <-wq.stopChan:
				wq.isRunning = false
				return
			}
		}
	}()
}

// Stop stops processing the webhook queue
func (wq *WebhookQueue) Stop() {
	if wq.isRunning {
		wq.stopChan <- true
		time.Sleep(100 * time.Millisecond)
	}
}

// Enqueue adds a webhook message to the queue (non-blocking)
func (wq *WebhookQueue) Enqueue(url, payload string) {
	if !wq.isRunning {
		wq.Start()
	}

	// Calculate hash for deduplication
	hash := wq.hashPayload(url, payload)

	// Check if this is a duplicate
	if wq.isDuplicate(hash) {
		return
	}

	item := &WebhookQueueItem{
		URL:        url,
		Payload:    payload,
		Timestamp:  time.Now(),
		Hash:       hash,
		MaxRetries: 3,
	}

	// Non-blocking send to queue
	select {
	case wq.queue <- item:
		// Successfully queued
	default:
		// Queue full: warn once, then keep going (never block the scanner).
		warnWebhookOnce("delivery queue is full, dropping messages")
	}
}

// processItem handles sending a single webhook item with retries
func (wq *WebhookQueue) processItem(item *WebhookQueueItem) {
	for {
		err := wq.sendWebhook(item.URL, item.Payload)
		if err == nil {
			// Mark as sent. Success is deliberately not logged.
			wq.sentMutex.Lock()
			wq.sent[item.Hash] = time.Now()
			wq.sentMutex.Unlock()

			// Clean old entries from sent map (keep memory bounded)
			wq.cleanupSentMap()
			return
		}

		// Rate limiting is worth calling out on its own, once.
		if strings.Contains(err.Error(), "rate limited") {
			warnWebhookOnce("endpoint is rate limiting deliveries")
		}

		item.RetryCount++
		if item.RetryCount >= item.MaxRetries {
			warnWebhookOnce(fmt.Sprintf("delivery failed after %d attempts (%v)", item.MaxRetries, err))

			// Write to file instead if webhook fails permanently
			if wq.outputFileChan != nil {
				select {
				case wq.outputFileChan <- item.Payload:
				default:
				}
			}
			return
		}

		// Exponential backoff before the silent retry.
		backoff := time.Duration((1<<uint(item.RetryCount))*100) * time.Millisecond
		time.Sleep(backoff)
	}
}

// sendWebhook performs the actual HTTP POST
func (wq *WebhookQueue) sendWebhook(url, payload string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", strings.NewReader(payload))
	if err != nil {
		return fmt.Errorf("http error: %w", err)
	}
	defer resp.Body.Close()

	// Check for rate limiting
	if resp.StatusCode == http.StatusTooManyRequests {
		// Parse retry-after header if available
		retryAfter := resp.Header.Get("Retry-After")
		if retryAfter != "" {
			return fmt.Errorf("rate limited, retry after: %s", retryAfter)
		}
		return fmt.Errorf("rate limited (429)")
	}

	// Check for other non-2xx status codes
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// isDuplicate checks if a message hash was sent recently
func (wq *WebhookQueue) isDuplicate(hash string) bool {
	wq.sentMutex.Lock()
	defer wq.sentMutex.Unlock()

	lastSent, exists := wq.sent[hash]
	if !exists {
		return false
	}

	// Check if within deduplication window
	if time.Since(lastSent) < wq.dedupeWindow {
		return true
	}

	// Outside dedupeWindow, remove from tracking
	delete(wq.sent, hash)
	return false
}

// cleanupSentMap removes old entries to keep memory usage bounded
func (wq *WebhookQueue) cleanupSentMap() {
	wq.sentMutex.Lock()
	defer wq.sentMutex.Unlock()

	now := time.Now()
	for hash, lastSent := range wq.sent {
		if now.Sub(lastSent) > wq.dedupeWindow {
			delete(wq.sent, hash)
		}
	}
}

// hashPayload creates a hash of the payload for deduplication
// Uses URL + payload content (not timestamp)
func (wq *WebhookQueue) hashPayload(url, payload string) string {
	// Parse payload to extract unique content (ignore timestamps, IDs that might vary)
	hash := md5.New()
	io.WriteString(hash, url)

	// Try to extract meaningful content from JSON
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &data); err == nil {
		// For Discord embeds, hash the title, description, and fields (not timestamps)
		if embeds, ok := data["embeds"].([]interface{}); ok && len(embeds) > 0 {
			embed := embeds[0].(map[string]interface{})
			if title, ok := embed["title"]; ok {
				io.WriteString(hash, fmt.Sprintf("%v", title))
			}
			if desc, ok := embed["description"]; ok {
				io.WriteString(hash, fmt.Sprintf("%v", desc))
			}
			if fields, ok := embed["fields"].([]interface{}); ok {
				for _, field := range fields {
					f := field.(map[string]interface{})
					io.WriteString(hash, fmt.Sprintf("%v%v", f["name"], f["value"]))
				}
			}
		} else if content, ok := data["content"].(string); ok {
			// Generic webhook format
			io.WriteString(hash, content)
		}
	} else {
		// Fallback: hash the whole payload
		io.WriteString(hash, payload)
	}

	return fmt.Sprintf("%x", hash.Sum(nil))
}

// OutputFileQueue manages writing to files when webhooks fail
type OutputFileQueue struct {
	filePath  string
	queue     chan string
	mu        sync.Mutex
	isRunning bool
}

// NewOutputFileQueue creates a new file output queue
func NewOutputFileQueue(filePath string, bufferSize int) *OutputFileQueue {
	return &OutputFileQueue{
		filePath: filePath,
		queue:    make(chan string, bufferSize),
	}
}

// Start begins processing file outputs
func (oq *OutputFileQueue) Start() {
	if oq.isRunning {
		return
	}
	oq.isRunning = true

	go func() {
		for payload := range oq.queue {
			oq.writeToFile(payload)
		}
	}()
}

// Stop stops the file output processor
func (oq *OutputFileQueue) Stop() {
	if oq.isRunning {
		close(oq.queue)
		oq.isRunning = false
	}
}

// Enqueue adds a message to be written to file
func (oq *OutputFileQueue) Enqueue(payload string) {
	if !oq.isRunning {
		oq.Start()
	}

	select {
	case oq.queue <- payload:
		// Successfully queued
	default:
		// Queue full, write immediately (blocking)
		oq.writeToFile(payload)
	}
}

// writeToFile writes a payload to the file
// Splits into multiple files if needed (by date or size)
func (oq *OutputFileQueue) writeToFile(payload string) {
	oq.mu.Lock()
	defer oq.mu.Unlock()

	// Generate timestamped filename to split output
	timestamp := time.Now().Format("2006-01-02")
	filePath := strings.Replace(oq.filePath, ".jsonl", fmt.Sprintf("_%s.jsonl", timestamp), 1)

	// Try to write to file
	file, err := openFileAppend(filePath)
	if err != nil {
		Say("[output-file] Failed to open %s: %v\n", filePath, err)
		return
	}
	defer file.Close()

	// Write payload as JSONL (one JSON object per line). Success is not logged:
	// one line per write is the same noise as the old per-send webhook log.
	if _, err := io.WriteString(file, payload+"\n"); err != nil {
		Say("[output-file] Failed to write to %s: %v\n", filePath, err)
		return
	}
}

// openFileAppend opens a file for appending, creating it if needed
func openFileAppend(filePath string) (*os.File, error) {
	return os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
}
