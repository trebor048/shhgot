package notify

import (
	"errors"
	"testing"
	"time"
)

func noon() time.Time { return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC) }

// stubChannel is a channel with no transport of its own.
type stubChannel struct {
	name     string
	min      Severity
	buildErr error
}

func (s stubChannel) Name() string          { return s.name }
func (s stubChannel) MinSeverity() Severity { return s.min }
func (s stubChannel) Build(Event) (Payload, error) {
	if s.buildErr != nil {
		return Payload{}, s.buildErr
	}
	return Payload{Method: "POST", URL: "https://example.invalid/" + s.name, Body: []byte(s.name)}, nil
}

// failSender is a channel that owns its transport and always fails.
type failSender struct{ stubChannel }

func (f failSender) Send(Payload) error { return errors.New("send boom") }

// recordingDoer records the payload URLs it is asked to deliver.
type recordingDoer struct {
	calls []string
	err   error
}

func (d *recordingDoer) Do(p Payload) error {
	d.calls = append(d.calls, p.URL)
	return d.err
}

func TestDispatcherPerChannelMinSeverity(t *testing.T) {
	d := NewDispatcher(Policy{Now: noon})
	rec := &recordingDoer{}
	d.SetDoer(rec)

	d.Add(stubChannel{name: "high-only", min: SevLow}, SevHigh)
	d.Add(stubChannel{name: "everything", min: SevLow}, SevLow)

	results := d.Deliver(Event{Signature: "OpenAI API Key", Severity: SevMedium, File: "a.py"})
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if !results[0].Skipped {
		t.Fatalf("high-only channel must skip a medium finding: %+v", results[0])
	}
	if results[1].Skipped {
		t.Fatalf("low-floor channel must accept a medium finding: %+v", results[1])
	}
	if len(rec.calls) != 1 || rec.calls[0] != "https://example.invalid/everything" {
		t.Fatalf("unexpected deliveries: %v", rec.calls)
	}
}

func TestDispatcherAddCannotLowerChannelFloor(t *testing.T) {
	d := NewDispatcher(Policy{Now: noon})
	d.SetDoer(&recordingDoer{})
	// The channel declares a high floor; Add asks for a lower one, which must
	// not win.
	d.Add(stubChannel{name: "floor", min: SevHigh}, SevLow)

	results := d.Deliver(Event{Signature: "s", Severity: SevMedium})
	if len(results) != 1 || !results[0].Skipped {
		t.Fatalf("channel floor was lowered: %+v", results)
	}
}

func TestDispatcherSkipsFailingChannelWithoutAbortingOthers(t *testing.T) {
	d := NewDispatcher(Policy{Now: noon})
	rec := &recordingDoer{}
	d.SetDoer(rec)

	d.Add(stubChannel{name: "bad-build", min: SevLow, buildErr: errors.New("boom")}, SevLow)
	d.Add(failSender{stubChannel{name: "bad-send", min: SevLow}}, SevLow)
	d.Add(stubChannel{name: "good", min: SevLow}, SevLow)

	results := d.Deliver(Event{Signature: "OpenAI API Key", Severity: SevHigh})
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	if results[0].Err == nil {
		t.Fatal("build failure must be reported")
	}
	if results[1].Err == nil {
		t.Fatal("send failure must be reported")
	}
	if results[2].Err != nil {
		t.Fatalf("good channel must still deliver, got %v", results[2].Err)
	}
	if len(rec.calls) != 1 || rec.calls[0] != "https://example.invalid/good" {
		t.Fatalf("good channel was not delivered after failures: %v", rec.calls)
	}
}

func TestDispatcherTestDeliverBypassesPolicyAndReportsErrors(t *testing.T) {
	// Quiet hours are active, but validation must still attempt delivery.
	d := NewDispatcher(Policy{
		QuietHours: true,
		QuietStart: "22:00",
		QuietEnd:   "07:00",
		Now:        func() time.Time { return at(23, 30) },
	})
	rec := &recordingDoer{}
	d.SetDoer(rec)

	if err := d.TestDeliver(stubChannel{name: "c", min: SevLow}); err != nil {
		t.Fatalf("TestDeliver: %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("test delivery did not reach the transport: %v", rec.calls)
	}

	d.SetDoer(&recordingDoer{err: errors.New("nope")})
	if err := d.TestDeliver(stubChannel{name: "c", min: SevLow}); err == nil {
		t.Fatal("a failing test delivery must return an error")
	}

	if err := d.TestDeliver(nil); err == nil {
		t.Fatal("a nil channel must be rejected")
	}
}

func TestDispatcherNoDoerReportsTransportError(t *testing.T) {
	d := NewDispatcher(Policy{Now: noon})
	d.SetDoer(nil)
	d.Add(stubChannel{name: "no-transport", min: SevLow}, SevLow)
	results := d.Deliver(Event{Signature: "s", Severity: SevHigh})
	if len(results) != 1 || results[0].Err == nil {
		t.Fatalf("missing transport must be reported: %+v", results)
	}
}

// panicChannel panics from Build or Send so the dispatcher's isolation can be
// exercised.
type panicChannel struct {
	name       string
	panicBuild bool
	panicSend  bool
}

func (p panicChannel) Name() string          { return p.name }
func (p panicChannel) MinSeverity() Severity { return SevLow }
func (p panicChannel) Build(Event) (Payload, error) {
	if p.panicBuild {
		panic("build boom")
	}
	return Payload{Method: "POST", URL: "https://example.invalid/" + p.name, Body: []byte(p.name)}, nil
}
func (p panicChannel) Send(Payload) error {
	if p.panicSend {
		panic("send boom")
	}
	return nil
}

func TestDispatcherRecoversChannelPanicAndContinues(t *testing.T) {
	d := NewDispatcher(Policy{Now: noon})
	rec := &recordingDoer{}
	d.SetDoer(rec)

	d.Add(panicChannel{name: "panic-build", panicBuild: true}, SevLow)
	d.Add(panicChannel{name: "panic-send", panicSend: true}, SevLow)
	d.Add(stubChannel{name: "good", min: SevLow}, SevLow)

	results := d.Deliver(Event{Signature: "OpenAI API Key", Severity: SevHigh})
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	if results[0].Err == nil {
		t.Fatal("a Build panic must be reported per channel")
	}
	if results[1].Err == nil {
		t.Fatal("a Send panic must be reported per channel")
	}
	if results[2].Err != nil {
		t.Fatalf("a panic must not abort later channels: %v", results[2].Err)
	}
	if len(rec.calls) != 1 || rec.calls[0] != "https://example.invalid/good" {
		t.Fatalf("later channel was not delivered after panics: %v", rec.calls)
	}
}

func TestDispatcherTestDeliverRecoversPanic(t *testing.T) {
	d := NewDispatcher(Policy{Now: noon})
	d.SetDoer(&recordingDoer{})
	if err := d.TestDeliver(panicChannel{name: "boom", panicBuild: true}); err == nil {
		t.Fatal("a panic during TestDeliver must be returned as an error")
	}
}
