// Package notify is a self-contained alerting toolkit for shhgit findings.
//
// It deliberately does not import core: a finding is described by the Event
// value and every channel renders it to a self-contained Payload. Severity
// routing, quiet hours and delivery can therefore be reasoned about and tested
// in isolation, with dialers and HTTP transports injected so no test touches
// the network.
package notify

import (
	"fmt"
	"net/url"
	"strings"
)

// Severity ranks a finding. The names mirror core's, but the numeric values
// differ (core has an extra SevInfo level), so never convert between them by
// casting ints: map through the wire names ("low"/"medium"/"high"/"critical").
type Severity int

const (
	// SevLow is the least urgent severity.
	SevLow Severity = iota
	// SevMedium is a valid but low-impact finding.
	SevMedium
	// SevHigh is a verified high-impact finding.
	SevHigh
	// SevCritical is a funded or top-tier verified finding.
	SevCritical
)

// String renders the severity for humans and payload bodies. Out-of-range
// values render as "unknown", never as a real level: the default must not
// demote a miscast severity below every MinSeverity floor.
func (s Severity) String() string {
	switch s {
	case SevCritical:
		return "critical"
	case SevHigh:
		return "high"
	case SevMedium:
		return "medium"
	case SevLow:
		return "low"
	default:
		return "unknown"
	}
}

// Event is the transport-neutral description of one finding. Every channel
// renders this one value; no channel reaches back into the scanner.
type Event struct {
	Signature string
	Secret    string
	URL       string
	Repo      string
	File      string
	Line      int
	Priority  int
	Severity  Severity
	Provider  string
	Tier      string
	Balance   string
	Identity  string
}

// Payload is one ready-to-send message. For plain HTTP channels the Dispatcher
// posts it; channels that own their transport (email, file, sound) consume it
// in their own Send method.
type Payload struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

// Channel renders an Event to a Payload. Build must be pure and must not touch
// the network: the Dispatcher owns delivery, either through its Doer or, when
// the channel implements Sender, through the channel itself.
type Channel interface {
	// Name identifies the channel in logs and delivery results.
	Name() string
	// MinSeverity is the channel's built-in floor.
	MinSeverity() Severity
	// Build renders the event. It returns an error only for an unusable
	// configuration (for example an empty webhook URL), never for I/O.
	Build(ev Event) (Payload, error)
}

// Sender is an optional capability for channels that are not plain HTTP. The
// Dispatcher prefers Send over its Doer when a channel implements it.
type Sender interface {
	Send(p Payload) error
}

// maskSecret keeps a secret recognisable without republishing it. Anything up
// to eight characters is fully masked. Longer secrets reveal at most their
// outer quarters, so at least half of the characters are always masked and a
// nine- to twelve-character secret never exposes eight of nine.
func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	keep := len(s) / 4
	if keep > 4 {
		keep = 4
	}
	return s[:keep] + strings.Repeat("*", len(s)-2*keep) + s[len(s)-keep:]
}

// truncate caps s to at most n bytes, appending an ellipsis when it cuts.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

// location renders "file:line" (or just the file when no line is known).
func location(ev Event) string {
	if ev.Line > 0 && ev.File != "" {
		return fmt.Sprintf("%s:%d", ev.File, ev.Line)
	}
	return ev.File
}

// checkBaseURL validates a channel's HTTP endpoint at Build time so a
// whitespace/garbage URL fails fast with a usable error instead of failing
// later at delivery. Only absolute http(s) URLs with a host are accepted.
func checkBaseURL(where, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("%s: invalid URL %q: %v", where, raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s: URL %q must use http or https", where, raw)
	}
	if u.Host == "" {
		return fmt.Errorf("%s: URL %q has no host", where, raw)
	}
	return nil
}

// eventSummary is the one-line human description shared by text channels.
func eventSummary(ev Event) string {
	parts := []string{fmt.Sprintf("[%s] %s", ev.Severity, ev.Signature)}
	if ev.Repo != "" {
		parts = append(parts, ev.Repo)
	}
	if loc := location(ev); loc != "" {
		parts = append(parts, loc)
	}
	if ev.URL != "" {
		parts = append(parts, ev.URL)
	}
	return strings.Join(parts, " ")
}

// detailLines is the ordered key/value body shared by the email and generic
// channels. Empty fields are omitted.
func detailLines(ev Event) []string {
	var lines []string
	add := func(label, value string) {
		if value != "" {
			lines = append(lines, label+": "+value)
		}
	}
	add("Signature", ev.Signature)
	add("Severity", ev.Severity.String())
	add("Rule priority", fmt.Sprintf("%d", ev.Priority))
	add("Repository", ev.Repo)
	add("File", location(ev))
	add("URL", ev.URL)
	add("Secret", maskSecret(ev.Secret))
	add("Provider", ev.Provider)
	add("Tier", ev.Tier)
	add("Balance", ev.Balance)
	add("Identity", ev.Identity)
	return lines
}

// discordColor is the decimal colour Discord wants in an embed. The palette is
// green/yellow/orange/red by severity.
func discordColor(s Severity) int {
	switch s {
	case SevCritical:
		return 15158332 // #E74C3C red
	case SevHigh:
		return 15105570 // #E67E22 orange
	case SevMedium:
		return 15844367 // #F1C40F yellow
	default:
		return 3066993 // #2ECC71 green
	}
}

// severityHex is the #RRGGBB colour Slack and the generic channels use.
func severityHex(s Severity) string {
	switch s {
	case SevCritical:
		return "#E74C3C"
	case SevHigh:
		return "#E67E22"
	case SevMedium:
		return "#F1C40F"
	default:
		return "#2ECC71"
	}
}
