package core

import (
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fatih/color"
	"github.com/trebor048/shhgot/core/httpclient"
)

// discordWebhookFloorMS is the slowest gap we ever allow on a single Discord
// webhook. Discord buckets a webhook at 5 requests per 2 seconds; a 500 ms gap
// is 4 per 2 s, leaving one request of headroom for the validation POST and
// clock skew. No configuration can push this endpoint below the floor, because
// hitting the bucket is what gets the whole webhook throttled.
const discordWebhookFloorMS int64 = 500

// Severity embed colours, matching the palette the terminal UI uses so a
// critical finding looks the same in Discord, the dashboard and the TUI.
const (
	severityColorCritical = "#ED4245" // red
	severityColorHigh     = "#E67E22" // orange
	severityColorMedium   = "#FEE75C" // yellow
	severityColorLow      = "#57F287" // green
)

// SeverityColorHex maps a finding severity to the hex colour used for a Discord
// embed, replacing the signature colour as the embed accent. It returns the
// empty string for an unknown or empty severity so the caller can keep the
// signature colour (records written before Slice 6 have no severity).
func SeverityColorHex(severity string) string {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical":
		return severityColorCritical
	case "high":
		return severityColorHigh
	case "medium":
		return severityColorMedium
	case "low":
		return severityColorLow
	default:
		return ""
	}
}

// EmbedColor resolves the decimal Discord embed colour for a finding: the
// severity colour when the severity is known, otherwise the signature colour.
// It is the single place the severity -> colour decision is made, so the
// webhook cannot drift from the dashboard/TUI palette.
func EmbedColor(severity, signatureColor string) int {
	if hex := SeverityColorHex(severity); hex != "" {
		return parseColorHex(hex)
	}
	return parseColorHex(signatureColor)
}

// webhookHTTPClient is deliberately configured with no client-side retries and
// no per-host pacing. sendWithRetry and reserveSendSlot are the sole retrier and
// pacer on the webhook path: a second layer would multiply deliveries and fight
// the queue's own spacing.
var webhookHTTPClient = httpclient.New(httpclient.Config{
	Retries: 0,
	Timeout: 10 * time.Second,
})

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
	// OnDelivery, when non-nil, is invoked exactly once with the final outcome
	// of this item's delivery: true when the endpoint accepted the message (or
	// it was already delivered inside the dedupe window), false when the item
	// was dropped or every retry failed. It is how a caller such as publish()
	// learns whether it may record a cross-run dedupe fingerprint. It runs on
	// the queue's consumer goroutine, so callers should keep it short (a
	// mutex-guarded store append, not an unbounded send).
	OnDelivery func(ok bool)
}

// WebhookQueue manages webhook delivery with deduplication, rate limiting, and retries
type WebhookQueue struct {
	queue        chan *WebhookQueueItem
	sent         map[string]time.Time // hash -> last sent time
	inflight     map[string]bool      // hash -> claimed but not yet delivered
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

	// done closes when the consumer goroutine exits. closeOnce makes Close
	// idempotent; closed reports whether Close ran (Enqueue after Close drops
	// instead of panicking on a send to the closed channel); closeMu
	// serialises Close against the Enqueue send.
	done      chan struct{}
	closeOnce sync.Once
	closed    atomic.Bool
	closeMu   sync.Mutex
}

// NewWebhookQueue creates a new webhook queue with the specified config
func NewWebhookQueue(queueSize int, rateLimitMS int64, dedupeWindowSec int, batchSize int) *WebhookQueue {
	if batchSize < 1 {
		batchSize = 1
	}
	return &WebhookQueue{
		queue:        make(chan *WebhookQueueItem, queueSize),
		sent:         make(map[string]time.Time),
		inflight:     make(map[string]bool),
		lastSend:     make(map[string]time.Time),
		rateLimitMS:  rateLimitMS,
		dedupeWindow: time.Duration(dedupeWindowSec) * time.Second,
		batchSize:    batchSize,
		done:         make(chan struct{}),
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
			defer close(wq.done)
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
				drained := false
				for len(batch) < wq.batchSize {
					select {
					case nxt := <-wq.queue:
						if nxt == nil {
							// A receive from a closed channel is always ready and
							// yields nil, so `continue` would spin at 100% CPU.
							// Close: stop coalescing but still deliver the batch
							// already accumulated - returning here dropped up to
							// batchSize-1 buffered alerts (and their persist
							// callbacks) on shutdown.
							drained = true
						} else if nxt.URL == item.URL {
							batch = append(batch, nxt)
							continue
						} else {
							held = nxt
						}
					default:
					}
					break
				}

				wq.processBatch(batch)
				if drained && held == nil {
					return
				}
			}
		}()
	})
}

// Enqueue adds a webhook message to the queue (non-blocking). onDelivery, when
// non-nil, is invoked exactly once when the item's fate is known: true on an
// accepted delivery, false on a drop or a permanent failure. A duplicate that
// was already delivered inside the dedupe window reports true, because the
// alert did go out.
func (wq *WebhookQueue) Enqueue(url, payload string, onDelivery func(ok bool)) {
	wq.Start()

	// Calculate hash for deduplication
	hash := wq.hashPayload(url, payload)

	// Claim the hash atomically before sending. Checking for a duplicate and
	// only recording it after a successful send let concurrent identical
	// findings all pass the check and deliver.
	switch wq.claimHash(hash) {
	case claimAlreadySent:
		// The alert already went out inside the dedupe window, so this
		// duplicate is genuinely delivered.
		notifyDelivery(onDelivery, true)
		return
	case claimInflight:
		// An identical payload is queued or mid-delivery; its alert has not
		// gone out yet. Report failure rather than success: if the original
		// ultimately fails, the finding must be re-alerted by a later run
		// instead of being suppressed by a dedupe fingerprint recorded from an
		// alert that was never delivered.
		notifyDelivery(onDelivery, false)
		return
	}

	item := &WebhookQueueItem{
		URL:        url,
		Payload:    payload,
		Hash:       hash,
		MaxRetries: 3,
		OnDelivery: onDelivery,
	}

	// Non-blocking send to queue. Serialised against Close: sending (even in
	// a select with default) on a closed channel panics.
	wq.closeMu.Lock()
	defer wq.closeMu.Unlock()
	if wq.closed.Load() {
		wq.releaseHash(hash)
		warnWebhookOnce("delivery queue is closed, dropping messages")
		notifyDelivery(onDelivery, false)
		return
	}
	select {
	case wq.queue <- item:
		// Successfully queued
	default:
		// Queue full: release the claim so a later enqueue can retry, warn once,
		// then keep going (never block the scanner).
		wq.releaseHash(hash)
		warnWebhookOnce("delivery queue is full, dropping messages")
		notifyDelivery(onDelivery, false)
	}
}

// Close stops the queue: no further item is accepted and the consumer exits
// once the buffered items are delivered. It is idempotent and safe to call
// before Start.
func (wq *WebhookQueue) Close() {
	wq.closeOnce.Do(func() {
		wq.closeMu.Lock()
		defer wq.closeMu.Unlock()
		wq.closed.Store(true)
		close(wq.queue)
	})
}

// Drain closes the queue and waits up to timeout for the buffered items to be
// delivered (each delivery still fires its OnDelivery callback, so findings
// are persisted once their alert goes out). It reports whether the queue
// emptied in time; on timeout the undelivered remainder stays queued and the
// caller must decide whether to wait longer or exit without it.
func (wq *WebhookQueue) Drain(timeout time.Duration) bool {
	wq.Close()
	if timeout <= 0 {
		select {
		case <-wq.done:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-wq.done:
		return true
	case <-timer.C:
		return false
	}
}

// notifyDelivery fires an item's completion callback if one is set.
func notifyDelivery(onDelivery func(ok bool), ok bool) {
	if onDelivery != nil {
		onDelivery(ok)
	}
}

// maxDiscordEmbeds is Discord's documented cap on embeds per message.
const maxDiscordEmbeds = 10

// maxDiscordContent is Discord's documented cap on message content characters.
const maxDiscordContent = 2000

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
			err := wq.sendWithRetry(batch[0].URL, merged, batch[0].MaxRetries)
			if err == nil {
				hashes := make([]string, 0, len(batch))
				for _, it := range batch {
					hashes = append(hashes, it.Hash)
				}
				wq.markSent(hashes)
			} else {
				// Permanent failure: release the claims so a later run retries.
				for _, it := range batch {
					wq.releaseHash(it.Hash)
				}
			}
			for _, it := range batch {
				notifyDelivery(it.OnDelivery, err == nil)
			}
			return
		}
	}
	for _, it := range batch {
		err := wq.sendWithRetry(it.URL, it.Payload, it.MaxRetries)
		if err == nil {
			wq.markSent([]string{it.Hash})
		} else {
			wq.releaseHash(it.Hash)
		}
		notifyDelivery(it.OnDelivery, err == nil)
	}
}

// markSent records delivered payloads so a duplicate inside the dedupe window is
// not sent again, and keeps the sent map bounded.
func (wq *WebhookQueue) markSent(hashes []string) {
	now := time.Now()
	wq.sentMutex.Lock()
	for _, h := range hashes {
		wq.sent[h] = now
		if wq.inflight != nil {
			delete(wq.inflight, h)
		}
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
		// Every item's content (usually a Discord ping) must survive the
		// merge: keeping only the first silently drops the rest. Distinct
		// contents are concatenated within Discord's 2000-character message
		// cap; anything larger refuses the merge and is sent separately.
		if doc.Content != "" && !strings.Contains(content, doc.Content) {
			if content != "" {
				content += "\n"
			}
			content += doc.Content
			if len(content) > maxDiscordContent {
				return "", false
			}
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

// sendWebhook performs the actual HTTP POST through the queue's own client,
// which has client-side retries and pacing disabled.
func (wq *WebhookQueue) sendWebhook(url, payload string) error {
	// SSRF guard: validate the target URL before any request is made.
	if err := httpclient.SafeURL(url); err != nil {
		return fmt.Errorf("webhook URL rejected by SSRF guard: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(payload))
	if err != nil {
		return fmt.Errorf("request error: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := webhookHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("http error: %w", err)
	}
	defer resp.Body.Close()

	// Check for rate limiting. Discord exposes the wait via Retry-After, and
	// X-RateLimit-Reset-After on some endpoints; prefer the former.
	if resp.StatusCode == http.StatusTooManyRequests {
		now := time.Now()
		retryAfter, ok := httpclient.ParseRetryAfter(resp.Header.Get("Retry-After"), now)
		if !ok {
			retryAfter, _ = httpclient.ParseRetryAfter(resp.Header.Get("X-RateLimit-Reset-After"), now)
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

// claimState is the outcome of claiming a payload hash for delivery.
type claimState int

const (
	// claimNew means the caller won the claim and now owns delivery of the hash.
	claimNew claimState = iota
	// claimInflight means an identical payload is already queued or being
	// delivered. Its alert has not necessarily gone out yet.
	claimInflight
	// claimAlreadySent means the hash was delivered within the dedupe window.
	claimAlreadySent
)

// claimHash atomically claims hash for delivery and reports the resulting
// state. A new claim is recorded as in-flight, not sent: only markSent records
// a real delivery, so an in-flight duplicate is never mistaken for a delivered
// alert. The claim is taken at enqueue time, not after delivery, which is what
// closes the check-then-act race in Enqueue.
func (wq *WebhookQueue) claimHash(hash string) claimState {
	wq.sentMutex.Lock()
	defer wq.sentMutex.Unlock()

	if wq.inflight == nil {
		wq.inflight = make(map[string]bool)
	}
	if wq.inflight[hash] {
		return claimInflight
	}
	if last, ok := wq.sent[hash]; ok && time.Since(last) < wq.dedupeWindow {
		return claimAlreadySent
	}
	wq.inflight[hash] = true
	return claimNew
}

// releaseHash drops an in-flight claim so a delivery that never happened - a
// full queue or every retry failing - is retried by a later run instead of
// being suppressed for the rest of the dedupe window.
func (wq *WebhookQueue) releaseHash(hash string) {
	wq.sentMutex.Lock()
	delete(wq.sent, hash)
	if wq.inflight != nil {
		delete(wq.inflight, hash)
	}
	wq.sentMutex.Unlock()
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
