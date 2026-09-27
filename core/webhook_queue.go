package core

import (
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
)

// discordWebhookFloorMS is the slowest gap we ever allow on a single Discord
// webhook. Discord buckets a webhook at 5 requests per 2 seconds; a 500 ms gap
// is 4 per 2 s, leaving one request of headroom for the validation POST and
// clock skew. No configuration can push this endpoint below the floor, because
// hitting the bucket is what gets the whole webhook throttled.
const discordWebhookFloorMS int64 = 500

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
	Hash       string // For deduplication
	MaxRetries int
}

// WebhookQueue manages webhook delivery with deduplication, rate limiting, and retries
type WebhookQueue struct {
	queue        chan *WebhookQueueItem
	sent         map[string]time.Time // hash -> last sent time
	sentMutex    sync.Mutex
	startOnce    sync.Once
	rateLimitMS  int64         // milliseconds between sends per webhook
	dedupeWindow time.Duration // how long to track duplicates
	// batchSize coalesces up to this many same-endpoint items into one request.
	// 1 disables batching.
	batchSize int

	// sendMutex guards lastSend and serialises the spacing decision so two
	// senders can never both see "enough time has passed" and fire together.
	sendMutex sync.Mutex
	lastSend  map[string]time.Time // webhook URL -> last delivery time
}

// NewWebhookQueue creates a new webhook queue with the specified config
func NewWebhookQueue(queueSize int, rateLimitMS int64, dedupeWindowSec int, batchSize int) *WebhookQueue {
	if batchSize < 1 {
		batchSize = 1
	}
	return &WebhookQueue{
		queue:        make(chan *WebhookQueueItem, queueSize),
		sent:         make(map[string]time.Time),
		lastSend:     make(map[string]time.Time),
		rateLimitMS:  rateLimitMS,
		dedupeWindow: time.Duration(dedupeWindowSec) * time.Second,
		batchSize:    batchSize,
	}
}

// minIntervalFor returns the minimum spacing to enforce between two sends to
// url. Discord's documented bucket is far tighter than a generic endpoint's, so
// it gets a hard floor regardless of what rate_limit_ms says.
func (wq *WebhookQueue) minIntervalFor(url string) time.Duration {
	ms := wq.rateLimitMS
	if strings.Contains(url, "discord.com/api/webhooks") && ms < discordWebhookFloorMS {
		ms = discordWebhookFloorMS
	}
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

// reserveSendSlot blocks until url may be delivered to again, then records the
// slot as taken. Without this the queue drained as fast as the HTTP round-trips
// completed and burst straight through Discord's bucket.
//
// The timestamp is stamped before the request is attempted, not after, so a
// slow or hung request cannot be overlapped by the next one.
func (wq *WebhookQueue) reserveSendSlot(url string) {
	interval := wq.minIntervalFor(url)

	wq.sendMutex.Lock()
	defer wq.sendMutex.Unlock()

	if interval > 0 {
		if last, ok := wq.lastSend[url]; ok {
			if wait := interval - time.Since(last); wait > 0 {
				time.Sleep(wait)
			}
		}
	}
	wq.lastSend[url] = time.Now()
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
			var held *WebhookQueueItem
			for {
				item := held
				held = nil
				if item == nil {
					item = <-wq.queue
					if item == nil {
						return
					}
				}

				// Coalesce the immediately-following items for the same endpoint.
				// An item for a different endpoint is held for the next round, so
				// ordering between endpoints is preserved.
				batch := []*WebhookQueueItem{item}
				for len(batch) < wq.batchSize {
					select {
					case nxt := <-wq.queue:
						if nxt == nil {
							continue
						}
						if nxt.URL == item.URL {
							batch = append(batch, nxt)
							continue
						}
						held = nxt
					default:
					}
					break
				}

				wq.processBatch(batch)
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

// maxDiscordEmbeds is Discord's documented cap on embeds per message.
const maxDiscordEmbeds = 10

// processBatch delivers one batch of queued items. When batching is enabled and
// every item targets the same endpoint with a Discord embed payload they are
// merged into one message (Discord accepts up to ten embeds per message), which
// cuts the request count by the batch size. Anything else is sent one at a time.
func (wq *WebhookQueue) processBatch(batch []*WebhookQueueItem) {
	if len(batch) == 0 {
		return
	}
	if len(batch) > 1 {
		if merged, ok := mergeDiscordPayloads(batch); ok {
			if wq.sendWithRetry(batch[0].URL, merged, batch[0].MaxRetries) == nil {
				hashes := make([]string, 0, len(batch))
				for _, it := range batch {
					hashes = append(hashes, it.Hash)
				}
				wq.markSent(hashes)
			}
			return
		}
	}
	for _, it := range batch {
		if wq.sendWithRetry(it.URL, it.Payload, it.MaxRetries) == nil {
			wq.markSent([]string{it.Hash})
		}
	}
}

// markSent records delivered payloads so a duplicate inside the dedupe window is
// not sent again, and keeps the sent map bounded.
func (wq *WebhookQueue) markSent(hashes []string) {
	now := time.Now()
	wq.sentMutex.Lock()
	for _, h := range hashes {
		wq.sent[h] = now
	}
	wq.sentMutex.Unlock()
	wq.cleanupSentMap()
}

// sendWithRetry delivers one payload, honouring a Retry-After on 429 and backing
// off otherwise. It returns nil once the endpoint accepts the payload, or a
// non-nil error after maxRetries attempts.
func (wq *WebhookQueue) sendWithRetry(url, payload string, maxRetries int) error {
	if maxRetries < 1 {
		maxRetries = 1
	}
	var lastErr error
	for attempt := 1; ; attempt++ {
		// Pace deliveries before every attempt, successful or not, so retries
		// cannot themselves hammer the endpoint.
		wq.reserveSendSlot(url)

		err := wq.sendWebhook(url, payload)
		if err == nil {
			return nil
		}
		lastErr = err

		if attempt >= maxRetries {
			break
		}

		// A 429 means the server told us exactly how long to wait; honour that
		// instead of guessing with exponential backoff. This is what keeps a
		// transient throttle from turning into a permanent one.
		var rle *rateLimitError
		if errors.As(err, &rle) {
			warnWebhookOnce("endpoint is rate limiting deliveries")
			if rle.retryAfter > 0 {
				time.Sleep(rle.retryAfter)
				continue
			}
		}
		// No Retry-After (or a non-429 failure): exponential backoff.
		time.Sleep(time.Duration((1<<uint(attempt))*100) * time.Millisecond)
	}
	warnWebhookOnce(fmt.Sprintf("delivery failed after %d attempts (%v)", maxRetries, lastErr))
	return lastErr
}

// mergeDiscordPayloads combines several queued items into one Discord message by
// concatenating their embeds. ok is false when a payload is not a Discord embed
// payload or the total would exceed Discord's per-message cap, in which case the
// caller sends them one at a time.
func mergeDiscordPayloads(batch []*WebhookQueueItem) (string, bool) {
	if len(batch) < 2 {
		return "", false
	}
	var (
		embeds  []json.RawMessage
		content string
	)
	for _, it := range batch {
		var doc struct {
			Content string            `json:"content"`
			Embeds  []json.RawMessage `json:"embeds"`
		}
		if err := json.Unmarshal([]byte(it.Payload), &doc); err != nil || len(doc.Embeds) == 0 {
			return "", false
		}
		if content == "" {
			content = doc.Content
		}
		embeds = append(embeds, doc.Embeds...)
		if len(embeds) > maxDiscordEmbeds {
			return "", false
		}
	}
	out := map[string]any{"embeds": embeds}
	if content != "" {
		out["content"] = content
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// rateLimitError reports a 429 and, when the server supplied one, how long to
// wait before retrying.
type rateLimitError struct {
	retryAfter time.Duration
	detail     string
}

func (e *rateLimitError) Error() string {
	if e.retryAfter > 0 {
		return fmt.Sprintf("rate limited, retry after %s", e.retryAfter)
	}
	return "rate limited: " + e.detail
}

// parseRetryAfter reads a Retry-After style header. Discord sends seconds as a
// float (e.g. "1.5"); the HTTP spec also allows an absolute date, so both are
// accepted. An unparseable or non-positive value yields 0, letting the caller
// fall back to exponential backoff.
func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(value, 64); err == nil && secs > 0 {
		return time.Duration(secs * float64(time.Second))
	}
	if t, err := http.ParseTime(value); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// sendWebhook performs the actual HTTP POST
func (wq *WebhookQueue) sendWebhook(url, payload string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", strings.NewReader(payload))
	if err != nil {
		return fmt.Errorf("http error: %w", err)
	}
	defer resp.Body.Close()

	// Check for rate limiting. Discord exposes the wait via Retry-After, and
	// X-RateLimit-Reset-After on some endpoints; prefer the former.
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		if retryAfter <= 0 {
			retryAfter = parseRetryAfter(resp.Header.Get("X-RateLimit-Reset-After"))
		}
		return &rateLimitError{retryAfter: retryAfter, detail: "429 Too Many Requests"}
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
