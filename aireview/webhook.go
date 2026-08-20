package aireview

import (
	"bytes"
	"encoding/json"
	"net/http"
)

// postJSON is injectable for tests; defaults to a real HTTP POST.
var postJSON = func(url string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// SendDiscordEmbed posts a Discord embed to the configured webhook.
// Failures are intentionally ignored (notifications are best-effort).
func (s *Service) SendDiscordEmbed(title, description string, color int) {
	if s.Webhook == "" {
		return
	}
	payload := map[string]any{
		"embeds": []map[string]any{{
			"title":       title,
			"description": description,
			"color":       color,
		}},
	}
	_ = postJSON(s.Webhook, payload)
}
