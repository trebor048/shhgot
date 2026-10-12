package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Discord posts a rich embed to a Discord webhook, coloured by severity.
type Discord struct {
	// WebhookURL is the full Discord webhook endpoint.
	WebhookURL string
	// Username overrides the webhook's default display name when set.
	Username string
	// MinSev is the channel's built-in minimum severity.
	MinSev Severity
}

// Name implements Channel.
func (d Discord) Name() string { return "discord" }

// MinSeverity implements Channel.
func (d Discord) MinSeverity() Severity { return d.MinSev }

type discordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

// codeValue wraps s in backticks and caps the completed value at max bytes.
// Truncating s first and then adding the backticks let a value reach max+2,
// past Discord's 1024-character field limit, which made Discord reject the
// whole embed and lose the alert.
func codeValue(s string, max int) string {
	return truncate("`"+s+"`", max)
}

type discordEmbed struct {
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	Color       int            `json:"color"`
	Fields      []discordField `json:"fields,omitempty"`
}

type discordMessage struct {
	Username string         `json:"username,omitempty"`
	Embeds   []discordEmbed `json:"embeds"`
}

// embedSize approximates the character count Discord measures for an embed:
// the description plus every field name and value.
func embedSize(description string, fields []discordField) int {
	total := len(description)
	for _, f := range fields {
		total += len(f.Name) + len(f.Value)
	}
	return total
}

// Build implements Channel.
func (d Discord) Build(ev Event) (Payload, error) {
	if d.WebhookURL == "" {
		return Payload{}, errors.New("discord: webhook URL is empty")
	}

	fields := []discordField{
		{Name: "Severity", Value: ev.Severity.String(), Inline: true},
	}
	if ev.Repo != "" {
		fields = append(fields, discordField{Name: "Repository", Value: truncate(ev.Repo, 1024), Inline: true})
	}
	if loc := location(ev); loc != "" {
		fields = append(fields, discordField{Name: "File", Value: codeValue(loc, 1024), Inline: true})
	}
	if ev.Provider != "" {
		fields = append(fields, discordField{Name: "Provider", Value: truncate(ev.Provider, 1024), Inline: true})
	}
	if ev.Tier != "" {
		fields = append(fields, discordField{Name: "Tier", Value: truncate(ev.Tier, 1024), Inline: true})
	}
	if ev.Balance != "" {
		fields = append(fields, discordField{Name: "Balance", Value: truncate(ev.Balance, 1024), Inline: true})
	}
	if ev.Identity != "" {
		fields = append(fields, discordField{Name: "Identity", Value: truncate(ev.Identity, 1024), Inline: true})
	}
	if ev.Secret != "" {
		fields = append(fields, discordField{Name: "Secret", Value: codeValue(maskSecret(ev.Secret), 1024), Inline: false})
	}

	description := ""
	if ev.URL != "" {
		description = ev.URL
	}

	// Discord rejects an embed whose summed field+description characters reach
	// 6000, losing the alert entirely. Shrink the description first, then halve
	// the longest field values, rather than letting Discord drop the message.
	const embedCharBudget = 5900
	if embedSize(description, fields) > embedCharBudget {
		if over := embedSize(description, fields) - embedCharBudget; over < len(description) {
			description = truncate(description, len(description)-over)
		} else {
			description = ""
		}
		for embedSize(description, fields) > embedCharBudget {
			shrunk := false
			for i := range fields {
				if len(fields[i].Value) > 128 {
					fields[i].Value = truncate(fields[i].Value, len(fields[i].Value)/2)
					shrunk = true
				}
			}
			if !shrunk {
				break
			}
		}
	}

	msg := discordMessage{
		Username: d.Username,
		Embeds: []discordEmbed{{
			Title:       truncate(ev.Signature, 256),
			Description: truncate(description, 4096),
			Color:       discordColor(ev.Severity),
			Fields:      fields,
		}},
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return Payload{}, fmt.Errorf("discord: marshal embed: %w", err)
	}
	return Payload{
		Method:  http.MethodPost,
		URL:     d.WebhookURL,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
	}, nil
}
