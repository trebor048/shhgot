package notify

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFeishuCardShapeBySeverity(t *testing.T) {
	ev := Event{
		Signature: "OpenAI API Key",
		Severity:  SevCritical,
		Repo:      "acme/app",
		File:      "config.py",
		Line:      12,
		Secret:    "sk-abcdef0123456789",
	}
	for _, tc := range []struct {
		severity Severity
		template string
	}{
		{SevCritical, "red"},
		{SevHigh, "orange"},
		{SevMedium, "yellow"},
		{SevLow, "green"},
	} {
		ev.Severity = tc.severity
		p, err := (Feishu{WebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/abc"}).Build(ev)
		if err != nil {
			t.Fatalf("Build(%s): %v", tc.severity, err)
		}
		if p.Method != "POST" {
			t.Fatalf("method = %q, want POST", p.Method)
		}

		var msg struct {
			MsgType string `json:"msg_type"`
			Card    struct {
				Header struct {
					Template string `json:"template"`
					Title    struct {
						Tag     string `json:"tag"`
						Content string `json:"content"`
					} `json:"title"`
				} `json:"header"`
				Elements []struct {
					Tag  string `json:"tag"`
					Text struct {
						Tag     string `json:"tag"`
						Content string `json:"content"`
					} `json:"text"`
				} `json:"elements"`
			} `json:"card"`
		}
		if err := json.Unmarshal(p.Body, &msg); err != nil {
			t.Fatalf("body is not valid JSON: %v", err)
		}
		if msg.MsgType != "interactive" {
			t.Fatalf("msg_type = %q, want interactive", msg.MsgType)
		}
		if msg.Card.Header.Template != tc.template {
			t.Fatalf("severity %s: template = %q, want %q", tc.severity, msg.Card.Header.Template, tc.template)
		}
		if msg.Card.Header.Title.Tag != "plain_text" {
			t.Fatalf("header title tag = %q, want plain_text", msg.Card.Header.Title.Tag)
		}
		if len(msg.Card.Elements) != 1 {
			t.Fatalf("got %d elements, want 1", len(msg.Card.Elements))
		}
		if msg.Card.Elements[0].Tag != "div" || msg.Card.Elements[0].Text.Tag != "lark_md" {
			t.Fatalf("element tags = %q/%q, want div/lark_md", msg.Card.Elements[0].Tag, msg.Card.Elements[0].Text.Tag)
		}
		if !strings.Contains(msg.Card.Elements[0].Text.Content, "OpenAI API Key") {
			t.Fatalf("card text missing signature: %q", msg.Card.Elements[0].Text.Content)
		}
		if strings.Contains(string(p.Body), "sk-abcdef0123456789") {
			t.Fatalf("body leaks the raw secret: %s", p.Body)
		}
	}
}

func TestFeishuRequiresWebhook(t *testing.T) {
	if _, err := (Feishu{}).Build(Event{Severity: SevHigh}); err == nil {
		t.Fatal("expected an error for an empty webhook URL")
	}
}
