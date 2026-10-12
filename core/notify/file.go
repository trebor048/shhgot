package notify

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// sanitizeField replaces CR and LF with spaces so a finding field cannot forge
// extra lines in the rendered log.
func sanitizeField(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

// File appends one line per finding to a log file. It is intended for a
// long-lived local audit trail when no remote endpoint is configured.
type File struct {
	// Path is the destination file; it is created 0600 when absent.
	Path string
	// MinSev is the channel's built-in minimum severity.
	MinSev Severity
}

// Name implements Channel.
func (f File) Name() string { return "file" }

// MinSeverity implements Channel.
func (f File) MinSeverity() Severity { return f.MinSev }

// Build implements Channel. The line is deterministic: timestamping is left to
// the operator's log tooling so identical findings format identically.
func (f File) Build(ev Event) (Payload, error) {
	if f.Path == "" {
		return Payload{}, errors.New("file: path is empty")
	}
	line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\n",
		ev.Severity, sanitizeField(ev.Signature), sanitizeField(location(ev)),
		sanitizeField(ev.URL), sanitizeField(maskSecret(ev.Secret)))
	return Payload{
		Method: "FILE",
		URL:    f.Path,
		Body:   []byte(line),
	}, nil
}

// Send implements Sender: it appends p.Body to the destination file.
func (f File) Send(p Payload) error {
	if p.URL == "" {
		return errors.New("file: empty destination")
	}
	fh, err := os.OpenFile(p.URL, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("file: open %s: %w", p.URL, err)
	}
	defer fh.Close()
	if _, err := fh.Write(p.Body); err != nil {
		return fmt.Errorf("file: write %s: %w", p.URL, err)
	}
	return nil
}
