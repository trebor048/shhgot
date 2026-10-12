package notify

import (
	"strconv"
	"strings"
	"time"
)

// Policy decides whether a channel may deliver an event. It applies a global
// severity floor, the channel's own minimum, and quiet hours. Quiet hours
// suppress everything except critical findings.
type Policy struct {
	// QuietHours enables the quiet window.
	QuietHours bool
	// QuietStart and QuietEnd are "HH:MM" local times. A window whose start is
	// later than its end crosses midnight (for example 22:00-07:00).
	QuietStart string
	QuietEnd   string
	// MinSeverity is a global floor applied in addition to the channel's own.
	MinSeverity Severity
	// Now supplies the clock; nil means time.Now. Tests inject a fixed clock.
	Now func() time.Time
}

// now returns the policy clock's current time.
func (p Policy) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Allow reports whether ch may deliver ev. A finding below the effective
// severity floor is skipped. During quiet hours everything below critical is
// skipped; critical findings always pass.
func (p Policy) Allow(ch Channel, ev Event) bool {
	floor := ch.MinSeverity()
	if p.MinSeverity > floor {
		floor = p.MinSeverity
	}
	if ev.Severity < floor {
		return false
	}

	if p.QuietHours && p.inQuietHours(p.now()) && ev.Severity < SevCritical {
		return false
	}
	return true
}

// inQuietHours reports whether t falls inside the configured window. An
// unparseable configuration fails open (never suppresses): a malformed clock
// must not silently swallow alerts.
func (p Policy) inQuietHours(t time.Time) bool {
	start, ok := parseClock(p.QuietStart)
	if !ok {
		return false
	}
	end, ok := parseClock(p.QuietEnd)
	if !ok {
		return false
	}
	if start == end {
		return false
	}

	minute := t.Hour()*60 + t.Minute()
	if start < end {
		return minute >= start && minute < end
	}
	// Cross-midnight window.
	return minute >= start || minute < end
}

// parseClock turns "HH:MM" into minutes past midnight.
func parseClock(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, false
	}
	h, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || h < 0 || h > 23 {
		return 0, false
	}
	m, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}
