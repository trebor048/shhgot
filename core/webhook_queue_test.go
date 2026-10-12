package core

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trebor048/shhgot/core/httpclient"
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

// TestWebhookParseRetryAfter pins the shared helper's behaviour on the webhook
// path: Discord's float seconds and the HTTP-date form are accepted, junk yields
// ok=false so the caller backs off exponentially.
func TestWebhookParseRetryAfter(t *testing.T) {
	parse := func(value string) time.Duration {
		d, _ := httpclient.ParseRetryAfter(value, time.Now())
		return d
	}
	if d := parse("1.5"); d != 1500*time.Millisecond {
		t.Errorf("ParseRetryAfter(\"1.5\") = %s, want 1.5s", d)
	}
	if d := parse("  3 "); d != 3*time.Second {
		t.Errorf("ParseRetryAfter(\"  3 \") = %s, want 3s", d)
	}
	future := time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)
	if d := parse(future); d <= 0 || d > 2*time.Second {
		t.Errorf("ParseRetryAfter(%q) = %s, want (0, 2s]", future, d)
	}
	for _, junk := range []string{"", "  ", "soon", "-5", "0"} {
		if d := parse(junk); d != 0 {
			t.Errorf("ParseRetryAfter(%q) = %s, want 0", junk, d)
		}
	}
}

// TestWebhookSingleAttemptPerQueueRetry proves the queue's delivery client does
// exactly one transport attempt per sendWithRetry iteration: the queue, not the
// client, owns retries. If the client also retried, the always-500 endpoint
// would see MaxRetries*(1+clientRetries) requests instead of MaxRetries.
func TestWebhookSingleAttemptPerQueueRetry(t *testing.T) {
	var count int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&count, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	wq := &WebhookQueue{
		rateLimitMS:  0,
		dedupeWindow: time.Second,
		lastSend:     make(map[string]time.Time),
		sent:         make(map[string]time.Time),
	}

	const queueRetries = 2
	start := time.Now()
	if err := wq.sendWithRetry(srv.URL, `{"text":"x"}`, queueRetries); err == nil {
		t.Fatal("sendWithRetry returned nil for an always-500 endpoint")
	}
	elapsed := time.Since(start)

	if got := atomic.LoadInt64(&count); got != queueRetries {
		t.Fatalf("server observed %d requests, want exactly %d (one transport attempt per queue retry)", got, queueRetries)
	}
	// The queue's own backoff here is 200ms. If the shared client also paced
	// (1200ms default), the second request could not arrive this quickly.
	if elapsed > time.Second {
		t.Fatalf("elapsed %s suggests client-side pacing; the queue must be the only pacer", elapsed)
	}
}

// A duplicate of an item that is still in flight must not be reported as
// delivered: the original alert has not gone out yet. Reporting true let
// publish() persist the cross-run dedupe fingerprint, so when the original
// later failed permanently the finding was suppressed for the dedupe window
// even though no alert was ever delivered. The duplicate must report false so a
// later run re-alerts it.
func TestWebhookEnqueueInflightDuplicateReportsFailure(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseFn := func() { releaseOnce.Do(func() { close(release) }) }
	started := make(chan struct{}, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer func() {
		releaseFn()
		srv.Close()
	}()

	wq := NewWebhookQueue(16, 0, 300, 1)

	first := make(chan bool, 1)
	wq.Enqueue(srv.URL+"/hook", `{"text":"dup"}`, func(ok bool) { first <- ok })

	// Wait until the first delivery is actually in flight: the handler is only
	// reached after Enqueue has claimed the hash.
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first request never reached the endpoint")
	}

	dup := make(chan bool, 1)
	wq.Enqueue(srv.URL+"/hook", `{"text":"dup"}`, func(ok bool) { dup <- ok })
	select {
	case ok := <-dup:
		if ok {
			t.Fatal("in-flight duplicate reported delivered before the alert went out")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the duplicate callback")
	}

	// Let the original fail permanently; its own callback must report failure.
	releaseFn()
	select {
	case ok := <-first:
		if ok {
			t.Fatal("permanently failed delivery reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the first callback")
	}
}

// Enqueue must report the terminal delivery outcome through OnDelivery: true
// when the endpoint accepts the payload, false after every retry fails. This is
// what lets publish() defer the cross-run dedupe fingerprint until the alert is
// actually confirmed.
func TestWebhookEnqueueReportsDeliveryOutcome(t *testing.T) {
	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okSrv.Close()
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer badSrv.Close()

	wq := NewWebhookQueue(16, 0, 300, 1)

	okResult := make(chan bool, 1)
	wq.Enqueue(okSrv.URL+"/hook", `{"text":"ok"}`, func(ok bool) { okResult <- ok })
	select {
	case ok := <-okResult:
		if !ok {
			t.Fatal("accepted delivery reported failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the success callback")
	}

	failResult := make(chan bool, 1)
	wq.Enqueue(badSrv.URL+"/hook", `{"text":"bad"}`, func(ok bool) { failResult <- ok })
	select {
	case ok := <-failResult:
		if ok {
			t.Fatal("permanently failed delivery reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the failure callback")
	}
}

// A full queue must report failure rather than silently dropping the alert, so
// the finding is re-delivered next run.
func TestWebhookEnqueueReportsDroppedItem(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
	}))
	defer func() {
		close(release)
		hang.Close()
	}()

	// Buffer of one: the consumer pins itself on the first (hanging) request,
	// the second item fills the buffer, and the third has nowhere to go.
	wq := NewWebhookQueue(1, 0, 300, 1)
	wq.Enqueue(hang.URL+"/a", `{"text":"a"}`, nil)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first request never reached the endpoint")
	}
	wq.Enqueue(hang.URL+"/b", `{"text":"b"}`, nil)

	dropped := make(chan bool, 1)
	wq.Enqueue(hang.URL+"/c", `{"text":"c"}`, func(ok bool) { dropped <- ok })
	select {
	case ok := <-dropped:
		if ok {
			t.Fatal("dropped item reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the drop callback")
	}
}

// Enqueue checked isDuplicate before the send and only marked the hash sent
// after a successful delivery, so concurrent identical findings all passed the
// check and were all delivered. The hash must be claimed atomically at enqueue.
func TestWebhookEnqueueDeduplicatesConcurrentIdentical(t *testing.T) {
	var count int64
	arrived := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseFn := func() { releaseOnce.Do(func() { close(release) }) }

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt64(&count, 1) == 1 {
			close(arrived)
			<-release
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer releaseFn()

	wq := NewWebhookQueue(64, 0, 300, 1)
	const n = 8

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			wq.Enqueue(srv.URL+"/hook", `{"text":"identical finding"}`, nil)
		}()
	}
	close(start)
	wg.Wait()

	// Every enqueue has returned while the first delivery is still blocked, so
	// with the check-then-act bug all of them saw an empty sent map.
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the first request never reached the endpoint")
	}
	releaseFn()

	// Wait until the request count stops changing, then assert it.
	var last int64 = -1
	stable := 0
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c := atomic.LoadInt64(&count)
		if c == last {
			stable++
			if stable >= 3 {
				break
			}
		} else {
			stable = 0
			last = c
		}
		time.Sleep(20 * time.Millisecond)
	}

	if got := atomic.LoadInt64(&count); got != 1 {
		t.Fatalf("endpoint received %d identical deliveries, want exactly 1", got)
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

// TestWebhookDrainDeliversQueuedItems proves Close/Drain empties the buffer:
// every queued alert fires its callback before Drain reports done, and an
// Enqueue after Close drops with a failure callback instead of panicking.
func TestWebhookDrainDeliversQueuedItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wq := NewWebhookQueue(16, 0, 300, 1)
	const n = 5
	got := make(chan bool, n)
	for i := 0; i < n; i++ {
		wq.Enqueue(srv.URL+"/hook", `{"text":"m`+string(rune('0'+i))+`"}`, func(ok bool) { got <- ok })
	}
	if !wq.Drain(10 * time.Second) {
		t.Fatal("Drain timed out with deliverable items queued")
	}
	for i := 0; i < n; i++ {
		select {
		case ok := <-got:
			if !ok {
				t.Fatal("queued delivery reported failure")
			}
		default:
			t.Fatalf("only %d of %d callbacks fired", i, n)
		}
	}

	after := make(chan bool, 1)
	wq.Enqueue(srv.URL+"/hook", `{"text":"late"}`, func(ok bool) { after <- ok })
	select {
	case ok := <-after:
		if ok {
			t.Fatal("post-Close enqueue reported success, want drop")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("post-Close enqueue never called back (or panicked)")
	}
}
