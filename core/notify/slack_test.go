package notify

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSlackBodyCarriesSignatureAndSeverity(t *testing.T) {
	ch := Slack{WebhookURL: "https://hooks.slack.example/services/T000/B000", MinSev: SevLow}
	ev := Event{
		Signature: "OpenAI API Key",
		Severity:  SevCritical,
		Repo:      "acme/app",
		File:      "config.py",
		Line:      12,
		Secret:    "sk-abcdef0123456789",
		URL:       "https://github.com/acme/app/blob/main/config.py#L12",
	}

	p, err := ch.Build(ev)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Method != "POST" {
		t.Fatalf("method = %q, want POST", p.Method)
	}
	if got := p.Headers["Content-Type"]; got != "application/json" {
		t.Fatalf("content-type = %q", got)
	}

	body := string(p.Body)
	if !strings.Contains(body, "OpenAI API Key") {
		t.Fatalf("body does not carry the signature: %s", body)
	}
	if !strings.Contains(body, "critical") {
		t.Fatalf("body does not carry the severity: %s", body)
	}
	if !strings.Contains(body, "#E74C3C") {
		t.Fatalf("body does not carry the critical colour: %s", body)
	}
	if strings.Contains(body, "sk-abcdef0123456789") {
		t.Fatalf("body leaks the raw secret: %s", body)
	}

	var decoded map[string]any
	if err := json.Unmarshal(p.Body, &decoded); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if _, ok := decoded["text"]; !ok {
		t.Fatalf("body has no top-level text field: %s", body)
	}
}

func TestSlackRejectsEmptyWebhook(t *testing.T) {
	if _, err := (Slack{}).Build(Event{Severity: SevHigh}); err == nil {
		t.Fatal("expected an error for an empty webhook URL")
	}
}
