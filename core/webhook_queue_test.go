package core

import (
	"net/http"
	"testing"
	"time"
)

// hashPayload used to type-assert embeds[0] and each field to a map without
// checking. A payload whose embed or field entry is a string or number (the
// payload is built from scanned, untrusted data) then panicked the queue
// worker. It must hash every shape without panicking.
func TestWebhookHashPayloadToleratesMalformedEmbeds(t *testing.T) {
	wq := &WebhookQueue{}
	payloads := []string{
		`{"embeds":["not-an-object"]}`,
		`{"embeds":[{"fields":["not-an-object"]}]}`,
		`{"embeds":[{"fields":[{"name":"n","value":"v"}]}]}`,
		`{"embeds":[{"title":"t","description":"d"}]}`,
		`{"content":"hello"}`,
		`not json at all`,
		`{}`,
	}
	for _, p := range payloads {
		got := wq.hashPayload("https://example.com/hook", p)
		if got == "" {
			t.Errorf("hashPayload(%q) returned an empty hash", p)
		}
	}
}

// A valid-JSON payload with no recognised field used to hash only the URL, so
// every generic (Slack-style {"text": ...}) message collapsed into one dedupe
// key and all but the first were dropped inside the dedupe window.
func TestWebhookHashPayloadDistinguishesGenericMessages(t *testing.T) {
	wq := &WebhookQueue{}
	url := "https://example.com/hook"
	a := wq.hashPayload(url, `{"text":"first finding"}`)
	b := wq.hashPayload(url, `{"text":"second finding"}`)
	if a == b {
		t.Fatalf("distinct generic payloads hashed identically: %s", a)
	}
	if a == wq.hashPayload(url, `{"embeds":[]}`) {
		t.Fatal("generic payload collided with an empty embeds payload")
	}
}

// minIntervalFor is the guard that actually paces delivery. Discord must be
// held to the safe floor even when rate_limit_ms asks for a faster pace, and a
// custom endpoint must honour its own configured value.
func TestWebhookMinIntervalEnforcesDiscordFloor(t *testing.T) {
	discord := "https://discord.com/api/webhooks/123/token"
	plain := "https://example.com/hook"

	wq := &WebhookQueue{rateLimitMS: 100}
	if got := wq.minIntervalFor(discord); got != time.Duration(discordWebhookFloorMS)*time.Millisecond {
		t.Errorf("Discord interval = %s, want floor %dms", got, discordWebhookFloorMS)
	}
	if got := wq.minIntervalFor(plain); got != 100*time.Millisecond {
		t.Errorf("non-Discord interval = %s, want 100ms", got)
	}

	// A value at or above the floor is respected as-is on Discord.
	wq.rateLimitMS = discordWebhookFloorMS + 250
	if got := wq.minIntervalFor(discord); got != (time.Duration(discordWebhookFloorMS)+250)*time.Millisecond {
		t.Errorf("Discord interval = %s, want %dms", got, discordWebhookFloorMS+250)
	}

	// Zero disables pacing for generic endpoints (still floored on Discord).
	wq.rateLimitMS = 0
	if got := wq.minIntervalFor(plain); got != 0 {
		t.Errorf("disabled interval = %s, want 0", got)
	}
	if got := wq.minIntervalFor(discord); got != time.Duration(discordWebhookFloorMS)*time.Millisecond {
		t.Errorf("disabled Discord interval = %s, want floor %dms", got, discordWebhookFloorMS)
	}
}

// reserveSendSlot must leave at least the configured gap between two sends to
// the same webhook, and track different webhooks independently. Before this
// existed the field was stored but never read, so both sends fired instantly.
func TestWebhookReserveSendSlotPacesDeliveries(t *testing.T) {
	wq := &WebhookQueue{
		rateLimitMS: 60,
		lastSend:    make(map[string]time.Time),
	}
	url := "https://example.com/hook"

	start := time.Now()
	wq.reserveSendSlot(url)
	wq.reserveSendSlot(url)
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond {
		t.Errorf("two sends were spaced %s apart, want >= 60ms", elapsed)
	}

	// A different endpoint has its own budget and must not wait.
	other := "https://example.com/other"
	start = time.Now()
	wq.reserveSendSlot(other)
	if elapsed := time.Since(start); elapsed > 40*time.Millisecond {
		t.Errorf("unrelated webhook was throttled: waited %s", elapsed)
	}
}

// parseRetryAfter accepts Discord's float seconds and the HTTP-date form, and
// returns 0 (so the caller backs off exponentially) for junk.
func TestWebhookParseRetryAfter(t *testing.T) {
	if d := parseRetryAfter("1.5"); d != 1500*time.Millisecond {
		t.Errorf("parseRetryAfter(\"1.5\") = %s, want 1.5s", d)
	}
	if d := parseRetryAfter("  3 "); d != 3*time.Second {
		t.Errorf("parseRetryAfter(\"  3 \") = %s, want 3s", d)
	}
	future := time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)
	if d := parseRetryAfter(future); d <= 0 || d > 2*time.Second {
		t.Errorf("parseRetryAfter(%q) = %s, want (0, 2s]", future, d)
	}
	for _, junk := range []string{"", "  ", "soon", "-5", "0"} {
		if d := parseRetryAfter(junk); d != 0 {
			t.Errorf("parseRetryAfter(%q) = %s, want 0", junk, d)
		}
	}
}

// ConfigureWebhookQueue must apply the operator's values and ignore zeroes so a
// partial config cannot silently disable pacing.
func TestConfigureWebhookQueue(t *testing.T) {
	original := webhookQueueConfig
	t.Cleanup(func() {
		webhookQueueConfigMutex.Lock()
		webhookQueueConfig = original
		webhookQueueConfigMutex.Unlock()
	})

	ConfigureWebhookQueue(WebhookQueueCfg{QueueSize: 42, RateLimitMS: 750, DedupeWindowSec: 90})
	if webhookQueueConfig.QueueSize != 42 || webhookQueueConfig.RateLimitMS != 750 || webhookQueueConfig.DedupeWindowSec != 90 {
		t.Fatalf("configured values not applied: %+v", webhookQueueConfig)
	}

	ConfigureWebhookQueue(WebhookQueueCfg{QueueSize: 0, RateLimitMS: -1, DedupeWindowSec: 0})
	if webhookQueueConfig.QueueSize != 42 || webhookQueueConfig.RateLimitMS != 750 || webhookQueueConfig.DedupeWindowSec != 90 {
		t.Errorf("zero/negative values overwrote defaults: %+v", webhookQueueConfig)
	}
}
