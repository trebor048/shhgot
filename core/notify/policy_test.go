package notify

import (
	"testing"
	"time"
)

func at(h, m int) time.Time {
	return time.Date(2026, 9, 29, h, m, 0, 0, time.UTC)
}

func TestPolicyQuietHoursCrossMidnight(t *testing.T) {
	ch := stubChannel{name: "all", min: SevLow}
	p := Policy{
		QuietHours: true,
		QuietStart: "22:00",
		QuietEnd:   "07:00",
		Now:        func() time.Time { return at(23, 30) },
	}

	if p.Allow(ch, Event{Severity: SevMedium}) {
		t.Fatal("medium finding must be suppressed during quiet hours")
	}
	if p.Allow(ch, Event{Severity: SevHigh}) {
		t.Fatal("high finding must be suppressed during quiet hours")
	}
	if !p.Allow(ch, Event{Severity: SevCritical}) {
		t.Fatal("critical finding must bypass quiet hours")
	}

	// 03:00 is still inside the cross-midnight window.
	p.Now = func() time.Time { return at(3, 0) }
	if p.Allow(ch, Event{Severity: SevMedium}) {
		t.Fatal("medium finding must be suppressed after midnight")
	}

	// 07:00 is the exclusive end.
	p.Now = func() time.Time { return at(7, 0) }
	if !p.Allow(ch, Event{Severity: SevMedium}) {
		t.Fatal("finding must pass once the window ends")
	}

	// Midday is outside the window entirely.
	p.Now = func() time.Time { return at(12, 0) }
	if !p.Allow(ch, Event{Severity: SevMedium}) {
		t.Fatal("finding must pass outside quiet hours")
	}
}

func TestPolicyQuietHoursSameDay(t *testing.T) {
	ch := stubChannel{name: "all", min: SevLow}
	p := Policy{
		QuietHours: true,
		QuietStart: "01:00",
		QuietEnd:   "05:00",
		Now:        func() time.Time { return at(2, 0) },
	}
	if p.Allow(ch, Event{Severity: SevMedium}) {
		t.Fatal("medium finding must be suppressed inside a same-day window")
	}
	p.Now = func() time.Time { return at(6, 0) }
	if !p.Allow(ch, Event{Severity: SevMedium}) {
		t.Fatal("finding must pass outside a same-day window")
	}
}

func TestPolicySeverityFloor(t *testing.T) {
	medium := Event{Severity: SevMedium}
	high := Event{Severity: SevHigh}

	// The global floor skips a medium finding.
	global := Policy{MinSeverity: SevHigh, Now: func() time.Time { return at(12, 0) }}
	if global.Allow(stubChannel{name: "c", min: SevLow}, medium) {
		t.Fatal("global MinSeverity must suppress a medium finding")
	}
	if !global.Allow(stubChannel{name: "c", min: SevLow}, high) {
		t.Fatal("global MinSeverity must allow a high finding")
	}

	// The channel floor is independent of the global floor.
	channel := Policy{Now: func() time.Time { return at(12, 0) }}
	if channel.Allow(stubChannel{name: "c", min: SevHigh}, medium) {
		t.Fatal("channel MinSeverity must suppress a medium finding")
	}
	if !channel.Allow(stubChannel{name: "c", min: SevHigh}, high) {
		t.Fatal("channel MinSeverity must allow a high finding")
	}
}

func TestPolicyInvalidClockFailsOpen(t *testing.T) {
	p := Policy{
		QuietHours: true,
		QuietStart: "not-a-time",
		QuietEnd:   "07:00",
		Now:        func() time.Time { return at(23, 30) },
	}
	if !p.Allow(stubChannel{name: "all", min: SevLow}, Event{Severity: SevMedium}) {
		t.Fatal("an unparseable quiet window must not suppress findings")
	}
}
