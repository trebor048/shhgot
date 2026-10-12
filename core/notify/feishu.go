package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Feishu posts an interactive card to a Feishu (Lark) custom-bot webhook. The
// card header is coloured by severity.
type Feishu struct {
	// WebhookURL is the custom-bot webhook endpoint.
	WebhookURL string
	// MinSev is the channel's built-in minimum severity.
	MinSev Severity
}

// Name implements Channel.
func (f Feishu) Name() string { return "feishu" }

// MinSeverity implements Channel.
func (f Feishu) MinSeverity() Severity { return f.MinSev }

type feishuText struct {
	Tag     string `json:"tag"`
	Content string `json:"content"`
}

type feishuHeader struct {
	Template string     `json:"template"`
	Title    feishuText `json:"title"`
}

type feishuElement struct {
	Tag  string     `json:"tag"`
	Text feishuText `json:"text"`
}

type feishuCard struct {
	Header   feishuHeader    `json:"header"`
	Elements []feishuElement `json:"elements"`
}

type feishuMessage struct {
	MsgType string     `json:"msg_type"`
	Card    feishuCard `json:"card"`
}

// feishuTemplate maps a severity to Feishu's card header colour.
func feishuTemplate(s Severity) string {
	switch s {
	case SevCritical:
		return "red"
	case SevHigh:
		return "orange"
	case SevMedium:
		return "yellow"
	default:
		return "green"
	}
}

// Build implements Channel.
func (f Feishu) Build(ev Event) (Payload, error) {
	if f.WebhookURL == "" {
		return Payload{}, errors.New("feishu: webhook URL is empty")
	}

	msg := feishuMessage{
		MsgType: "interactive",
		Card: feishuCard{
			Header: feishuHeader{
				Template: feishuTemplate(ev.Severity),
				Title:    feishuText{Tag: "plain_text", Content: truncate(eventSummary(ev), 256)},
			},
			Elements: []feishuElement{{
				Tag:  "div",
				Text: feishuText{Tag: "lark_md", Content: truncate(strings.Join(detailLines(ev), "\n"), 4096)},
			}},
		},
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return Payload{}, fmt.Errorf("feishu: marshal card: %w", err)
	}
	return Payload{
		Method:  http.MethodPost,
		URL:     f.WebhookURL,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
	}, nil
}
