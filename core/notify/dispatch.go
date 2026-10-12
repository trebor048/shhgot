package notify

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Doer performs one payload against a transport. The default is HTTPDoer;
// tests inject a recorder.
type Doer interface {
	Do(p Payload) error
}

// HTTPDoer posts a Payload with net/http.
type HTTPDoer struct {
	// Client is the underlying client; nil means a 10 s client.
	Client *http.Client
}

// Do implements Doer.
func (d HTTPDoer) Do(p Payload) error {
	client := d.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	method := p.Method
	if method == "" {
		method = http.MethodPost
	}
	var body io.Reader
	if len(p.Body) > 0 {
		body = bytes.NewReader(p.Body)
	}
	req, err := http.NewRequest(method, p.URL, body)
	if err != nil {
		// p.URL carries the bot token / webhook secret in its path; only the
		// scheme and host may reach a log line or the /api/logs ring.
		return fmt.Errorf("notify: build request to %s: %w", redactURL(p.URL), redactURLError(err))
	}
	for k, v := range p.Headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("notify: send to %s: %w", redactURL(p.URL), redactURLError(err))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notify: %s %s returned HTTP %d", method, redactURL(p.URL), resp.StatusCode)
	}
	return nil
}

// redactURL returns only the scheme and host of raw, so an error can name the
// endpoint without leaking a webhook secret or Telegram bot token held in the
// path, query or userinfo. A URL that does not parse is replaced entirely
// rather than echoed back.
func redactURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "<redacted>"
	}
	return u.Scheme + "://" + u.Host
}

// redactURLError strips the secret-bearing URL from a wrapped *url.Error, which
// net/http includes verbatim in its message (for example
// `Post "https://api.telegram.org/bot<TOKEN>/sendMessage": ...`). Other error
// types are returned unchanged.
func redactURLError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		cp := *uerr
		cp.URL = redactURL(cp.URL)
		return &cp
	}
	return err
}

// DeliveryResult is the outcome for one channel in a Deliver call.
type DeliveryResult struct {
	// Channel is the channel's Name.
	Channel string
	// Skipped is true when the policy suppressed the event.
	Skipped bool
	// Err is non-nil when Build or transport failed. A failure on one channel
	// never prevents the others.
	Err error
}

// entry is one registered channel plus its effective severity floor.
type entry struct {
	ch  Channel
	min Severity
}

// Dispatcher delivers events to registered channels, applying the Policy and
// never letting one channel's failure abort the rest.
type Dispatcher struct {
	mu      sync.Mutex
	policy  Policy
	entries []entry
	doer    Doer
}

// NewDispatcher returns a Dispatcher using p and the default HTTPDoer.
func NewDispatcher(p Policy) *Dispatcher {
	return &Dispatcher{policy: p, doer: HTTPDoer{}}
}

// SetDoer replaces the transport used for channels that do not implement
// Sender. It is meant to be called before Add/Deliver.
func (d *Dispatcher) SetDoer(doer Doer) {
	d.mu.Lock()
	d.doer = doer
	d.mu.Unlock()
}

// Add registers a channel. min can only raise the channel's built-in
// MinSeverity, never lower it. Adding the same channel twice registers it
// twice; callers own deduplication.
func (d *Dispatcher) Add(ch Channel, min Severity) {
	if ch == nil {
		return
	}
	if chMin := ch.MinSeverity(); chMin > min {
		min = chMin
	}
	d.mu.Lock()
	d.entries = append(d.entries, entry{ch: ch, min: min})
	d.mu.Unlock()
}

// minOverride lets Policy.Allow use a per-channel floor without changing the
// Channel interface. It is only ever passed to Allow.
type minOverride struct {
	Channel
	min Severity
}

// MinSeverity implements Channel.
func (m minOverride) MinSeverity() Severity { return m.min }

// Deliver builds and sends ev to every registered channel. Channels the policy
// skips are reported with Skipped set. A build or transport error is captured
// per channel and never aborts the remaining channels.
func (d *Dispatcher) Deliver(ev Event) []DeliveryResult {
	d.mu.Lock()
	entries := make([]entry, len(d.entries))
	copy(entries, d.entries)
	doer := d.doer
	d.mu.Unlock()

	results := make([]DeliveryResult, 0, len(entries))
	for _, e := range entries {
		res := DeliveryResult{Channel: e.ch.Name()}

		effective := minOverride{Channel: e.ch, min: e.min}
		if !d.policy.Allow(effective, ev) {
			res.Skipped = true
			results = append(results, res)
			continue
		}

		res.Err = deliverToChannel(doer, res.Channel, e.ch, ev)
		results = append(results, res)
	}
	return results
}

// deliverToChannel builds and sends ev to ch, recovering a panic from either
// step so a misbehaving channel is reported as a per-channel error instead of
// aborting the remaining channels and propagating into the scanner. name is the
// channel name already resolved by the caller.
func deliverToChannel(doer Doer, name string, ch Channel, ev Event) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("notify: channel %q panicked: %v", name, r)
		}
	}()
	payload, err := ch.Build(ev)
	if err != nil {
		return err
	}
	return send(doer, ch, payload)
}

// TestDeliver builds and sends a synthetic critical event to ch, bypassing the
// policy. It backs the opt-in startup validation: a non-nil error names a
// misconfigured channel.
func (d *Dispatcher) TestDeliver(ch Channel) error {
	if ch == nil {
		return errors.New("notify: nil channel")
	}
	d.mu.Lock()
	doer := d.doer
	d.mu.Unlock()

	ev := Event{
		Signature: "shhgit test notification",
		Repo:      "shhgit",
		File:      "notify",
		Severity:  SevCritical,
	}
	return deliverToChannel(doer, ch.Name(), ch, ev)
}

// send routes a payload to the channel's own transport when it has one, or to
// the dispatcher's Doer otherwise.
func send(doer Doer, ch Channel, p Payload) error {
	if s, ok := ch.(Sender); ok {
		return s.Send(p)
	}
	if doer == nil {
		return errors.New("notify: no transport configured")
	}
	return doer.Do(p)
}
