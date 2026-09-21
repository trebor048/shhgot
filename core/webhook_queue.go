package core

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	queue        chan *WebhookQueueItem
	sent         map[string]time.Time // hash -> last sent time
	sentMutex    sync.Mutex
	startOnce    sync.Once
	stopChan     chan bool
	rateLimitMS  int64         // milliseconds between sends per webhook
	dedupeWindow time.Duration // how long to track duplicates
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

// Start begins processing the webhook queue. It is safe to call from any
// goroutine and any number of times.
//
// It used to test and set a plain isRunning bool. Enqueue is called from scanner
// workers, so two of them could both see false and each start a consumer, and the
// loop clearing the flag raced with those reads. sync.Once makes it idempotent.
func (wq *WebhookQueue) Start() {
	wq.startOnce.Do(func() {
		go func() {
			for {
				select {
				case item := <-wq.queue:
					if item != nil {
						wq.processItem(item)
					}
				case <-wq.stopChan:
					return
				}
			}
		}()
	})
}

// Enqueue adds a webhook message to the queue (non-blocking)
func (wq *WebhookQueue) Enqueue(url, payload string) {
	wq.Start()

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
			// There is no file fallback: the field that would have fed one was
			// never assigned by anything, so the block was dead.
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

	// Check for other non-2xx status codes. The body is capped: a misbehaving or
	// hostile endpoint could otherwise return an unbounded response, and the text
	// ends up in a log line.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
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
		hashed := false
		// For Discord embeds, hash the title, description, and fields (not timestamps).
		// Every assertion is checked: a payload whose embeds or fields hold a
		// non-object (a string, a number) would otherwise panic the queue worker.
		if embeds, ok := data["embeds"].([]interface{}); ok && len(embeds) > 0 {
			if embed, ok := embeds[0].(map[string]interface{}); ok {
				if title, ok := embed["title"]; ok {
					io.WriteString(hash, fmt.Sprintf("%v", title))
					hashed = true
				}
				if desc, ok := embed["description"]; ok {
					io.WriteString(hash, fmt.Sprintf("%v", desc))
					hashed = true
				}
				if fields, ok := embed["fields"].([]interface{}); ok {
					for _, field := range fields {
						f, ok := field.(map[string]interface{})
						if !ok {
							continue
						}
						io.WriteString(hash, fmt.Sprintf("%v%v", f["name"], f["value"]))
						hashed = true
					}
				}
			}
		}
		// Slack/Mattermost and the generic webhook_payload template use "text",
		// not "content".
		if !hashed {
			if content, ok := data["content"].(string); ok && content != "" {
				io.WriteString(hash, content)
				hashed = true
			}
		}
		if !hashed {
			if text, ok := data["text"].(string); ok && text != "" {
				io.WriteString(hash, text)
				hashed = true
			}
		}
		if !hashed {
			// Any shape we do not specifically understand must still be
			// distinguished by its content. Hashing only the URL collapsed every
			// such message into one key and deduplicated away all but the first.
			io.WriteString(hash, payload)
		}
	} else {
		// Fallback: hash the whole payload
		io.WriteString(hash, payload)
	}

	return fmt.Sprintf("%x", hash.Sum(nil))
}
