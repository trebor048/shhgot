package notify

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPushPlusBody(t *testing.T) {
	ch := PushPlus{Token: "pp-token"}
	p, err := ch.Build(Event{
		Signature: "OpenAI API Key",
		Severity:  SevCritical,
		File:      "config.py",
		Line:      12,
		Secret:    "sk-abcdef0123456789",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.URL != "https://www.pushplus.plus/send" {
		t.Fatalf("url = %q", p.URL)
	}

	var msg struct {
		Token    string `json:"token"`
		Title    string `json:"title"`
		Content  string `json:"content"`
		Template string `json:"template"`
	}
	if err := json.Unmarshal(p.Body, &msg); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if msg.Token != "pp-token" {
		t.Fatalf("token = %q", msg.Token)
	}
	if msg.Template != "html" {
		t.Fatalf("template = %q, want html", msg.Template)
	}
	if !strings.Contains(msg.Content, "OpenAI API Key") {
		t.Fatalf("content missing signature: %q", msg.Content)
	}
	if strings.Contains(string(p.Body), "sk-abcdef0123456789") {
		t.Fatalf("body leaks the raw secret: %s", p.Body)
	}
}

func TestPushPlusRequiresToken(t *testing.T) {
	if _, err := (PushPlus{}).Build(Event{Severity: SevHigh}); err == nil {
		t.Fatal("expected an error for an empty token")
	}
}
