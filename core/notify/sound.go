package notify

import (
	"fmt"
	"os"
	"runtime"
)

// Sound is the local attention channel. On Windows it rings the terminal bell;
// on every other platform it is a no-op, so a scan never depends on an audio
// device. Tests inject Beep to observe the call without making a sound.
type Sound struct {
	// Beep, when set, replaces the default audible signal.
	Beep func()
	// MinSev is the channel's built-in minimum severity.
	MinSev Severity
}

// Name implements Channel.
func (s Sound) Name() string { return "sound" }

// MinSeverity implements Channel.
func (s Sound) MinSeverity() Severity { return s.MinSev }

// Build implements Channel.
func (s Sound) Build(ev Event) (Payload, error) {
	return Payload{
		Method: "BEEP",
		Body:   []byte(fmt.Sprintf("%s %s %s", ev.Severity, ev.Signature, location(ev))),
	}, nil
}

// Send implements Sender.
func (s Sound) Send(_ Payload) error {
	if s.Beep != nil {
		s.Beep()
		return nil
	}
	if runtime.GOOS == "windows" {
		defaultBeep()
	}
	return nil
}

// defaultBeep writes the ASCII bell to stdout, which rings the terminal on
// Windows. It is intentionally the only platform that beeps.
func defaultBeep() {
	fmt.Fprint(os.Stdout, "\a")
}
