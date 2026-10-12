package notify

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWxPusherBody(t *testing.T) {
	ch := WxPusher{AppToken: "AT_x", UIDs: []string{"UID_a", "UID_b"}}
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
	if p.URL != "https://wxpusher.zjiecode.com/api/send/message" {
		t.Fatalf("url = %q", p.URL)
	}

	var msg struct {
		AppToken    string   `json:"appToken"`
		Content     string   `json:"content"`
		ContentType int      `json:"contentType"`
		UIDs        []string `json:"uids"`
	}
	if err := json.Unmarshal(p.Body, &msg); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if msg.AppToken != "AT_x" {
		t.Fatalf("appToken = %q", msg.AppToken)
	}
	if msg.ContentType != 1 {
		t.Fatalf("contentType = %d, want 1", msg.ContentType)
	}
	if len(msg.UIDs) != 2 || msg.UIDs[0] != "UID_a" || msg.UIDs[1] != "UID_b" {
		t.Fatalf("uids = %v", msg.UIDs)
	}
	if !strings.Contains(msg.Content, "OpenAI API Key") {
		t.Fatalf("content missing signature: %q", msg.Content)
	}
	if strings.Contains(string(p.Body), "sk-abcdef0123456789") {
		t.Fatalf("body leaks the raw secret: %s", p.Body)
	}
}

func TestWxPusherConfigErrors(t *testing.T) {
	if _, err := (WxPusher{UIDs: []string{"a"}}).Build(Event{Severity: SevHigh}); err == nil {
		t.Fatal("expected an error for an empty app token")
	}
	if _, err := (WxPusher{AppToken: "AT_x"}).Build(Event{Severity: SevHigh}); err == nil {
		t.Fatal("expected an error for an empty UID list")
	}
}
