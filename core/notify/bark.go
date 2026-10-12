package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// defaultBarkBaseURL is the public Bark relay.
const defaultBarkBaseURL = "https://api.day.app"

// Bark pushes a notification to an iOS device through a Bark server.
type Bark struct {
	// BaseURL overrides the Bark relay; defaults to https://api.day.app.
	BaseURL string
	// DeviceKey is the target device's push key.
	DeviceKey string
	// MinSev is the channel's built-in minimum severity.
	MinSev Severity
}

// Name implements Channel.
func (b Bark) Name() string { return "bark" }

// MinSeverity implements Channel.
func (b Bark) MinSeverity() Severity { return b.MinSev }

type barkMessage struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url,omitempty"`
	Group string `json:"group"`
	Sound string `json:"sound"`
}

// barkSound picks the alarm tone for high and critical findings.
func barkSound(s Severity) string {
	if s >= SevHigh {
		return "alarm"
	}
	return "default"
}

// Build implements Channel.
func (b Bark) Build(ev Event) (Payload, error) {
	if b.DeviceKey == "" {
		return Payload{}, errors.New("bark: device key is empty")
	}

	base := b.BaseURL
	if base == "" {
		base = defaultBarkBaseURL
	}
	if err := checkBaseURL("bark", base); err != nil {
		return Payload{}, err
	}
	body, err := json.Marshal(barkMessage{
		Title: truncate(eventSummary(ev), 256),
		Body:  truncate(strings.Join(detailLines(ev), "\n"), 2048),
		URL:   ev.URL,
		Group: "shhgit",
		Sound: barkSound(ev.Severity),
	})
	if err != nil {
		return Payload{}, fmt.Errorf("bark: marshal message: %w", err)
	}
	return Payload{
		Method:  http.MethodPost,
		URL:     strings.TrimRight(base, "/") + "/" + b.DeviceKey,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
	}, nil
}
