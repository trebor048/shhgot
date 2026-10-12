package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
)

// pushPlusEndpoint is the PushPlus API root.
const pushPlusEndpoint = "https://www.pushplus.plus/send"

// PushPlus pushes an HTML message through PushPlus (推送加).
type PushPlus struct {
	// Token is the account's PushPlus token.
	Token string
	// MinSev is the channel's built-in minimum severity.
	MinSev Severity
}

// Name implements Channel.
func (p PushPlus) Name() string { return "pushplus" }

// MinSeverity implements Channel.
func (p PushPlus) MinSeverity() Severity { return p.MinSev }

type pushPlusMessage struct {
	Token    string `json:"token"`
	Title    string `json:"title"`
	Content  string `json:"content"`
	Template string `json:"template"`
}

// Build implements Channel.
func (p PushPlus) Build(ev Event) (Payload, error) {
	if p.Token == "" {
		return Payload{}, errors.New("pushplus: token is empty")
	}

	var lines []string
	for _, line := range detailLines(ev) {
		lines = append(lines, html.EscapeString(line))
	}

	body, err := json.Marshal(pushPlusMessage{
		Token:    p.Token,
		Title:    truncate(html.EscapeString(eventSummary(ev)), 256),
		Content:  truncate(strings.Join(lines, "<br/>"), 8192),
		Template: "html",
	})
	if err != nil {
		return Payload{}, fmt.Errorf("pushplus: marshal message: %w", err)
	}
	return Payload{
		Method:  http.MethodPost,
		URL:     pushPlusEndpoint,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
	}, nil
}
