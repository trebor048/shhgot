package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Slack posts an incoming-webhook message carrying the finding and its
// severity, both in the top-level text and in a coloured attachment.
type Slack struct {
	// WebhookURL is the incoming-webhook endpoint.
	WebhookURL string
	// Channel overrides the webhook's default channel when set.
	Channel string
	// Username overrides the webhook's default display name when set.
	Username string
	// MinSev is the channel's built-in minimum severity.
	MinSev Severity
}

// Name implements Channel.
func (s Slack) Name() string { return "slack" }

// MinSeverity implements Channel.
func (s Slack) MinSeverity() Severity { return s.MinSev }

type slackField struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Short bool   `json:"short"`
}

type slackAttachment struct {
	Color  string       `json:"color,omitempty"`
	Title  string       `json:"title,omitempty"`
	Text   string       `json:"text"`
	Fields []slackField `json:"fields,omitempty"`
}

type slackMessage struct {
	Text        string            `json:"text"`
	Channel     string            `json:"channel,omitempty"`
	Username    string            `json:"username,omitempty"`
	Attachments []slackAttachment `json:"attachments,omitempty"`
}

// Build implements Channel.
func (s Slack) Build(ev Event) (Payload, error) {
	if s.WebhookURL == "" {
		return Payload{}, errors.New("slack: webhook URL is empty")
	}

	text := fmt.Sprintf("shhgit finding: %s (%s)", ev.Signature, ev.Severity)
	att := slackAttachment{
		Color: severityHex(ev.Severity),
		Title: truncate(ev.Signature, 256),
		Text:  truncate(eventSummary(ev), 4096),
	}
	if loc := location(ev); loc != "" {
		att.Fields = append(att.Fields, slackField{Title: "File", Value: truncate(loc, 1024), Short: true})
	}
	if ev.Repo != "" {
		att.Fields = append(att.Fields, slackField{Title: "Repository", Value: truncate(ev.Repo, 1024), Short: true})
	}
	if ev.Secret != "" {
		// maskSecret preserves length, and the secret is drawn from scanned
		// content, so bound it like every sibling field; Slack rejects an
		// over-long field and drops the whole alert.
		att.Fields = append(att.Fields, slackField{Title: "Secret", Value: "`" + truncate(maskSecret(ev.Secret), 1024) + "`", Short: false})
	}

	msg := slackMessage{
		Text:        text,
		Channel:     s.Channel,
		Username:    s.Username,
		Attachments: []slackAttachment{att},
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return Payload{}, fmt.Errorf("slack: marshal message: %w", err)
	}
	return Payload{
		Method:  http.MethodPost,
		URL:     s.WebhookURL,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
	}, nil
}
