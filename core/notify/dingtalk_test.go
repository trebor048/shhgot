package notify

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDingTalkSignedURLMatchesVector(t *testing.T) {
	// Independently computed: base64url(HMAC-SHA256("SECtestsecret",
	// "1700000000000\nSECtestsecret")).
	const wantSign = "7LVwF0dAF3_-MRRulbpE4y72Ogzykc6bS2nG4I99T4s"

	ch := DingTalk{
		WebhookURL: "https://oapi.dingtalk.com/robot/send?access_token=abc",
		Secret:     "SECtestsecret",
		Now:        func() time.Time { return time.UnixMilli(1700000000000) },
	}
	p, err := ch.Build(Event{Signature: "OpenAI API Key", Severity: SevCritical, File: "a.py"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	wantURL := "https://oapi.dingtalk.com/robot/send?access_token=abc&timestamp=1700000000000&sign=" + wantSign
	if p.URL != wantURL {
		t.Fatalf("url = %q, want %q", p.URL, wantURL)
	}

	var msg struct {
		MsgType  string `json:"msgtype"`
		Markdown struct {
			Title string `json:"title"`
			Text  string `json:"text"`
		} `json:"markdown"`
	}
	if err := json.Unmarshal(p.Body, &msg); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if msg.MsgType != "markdown" {
		t.Fatalf("msgtype = %q, want markdown", msg.MsgType)
	}
	if msg.Markdown.Title == "" || msg.Markdown.Text == "" {
		t.Fatalf("markdown title/text must be populated: %+v", msg.Markdown)
	}
}

func TestDingTalkWithoutSecretLeavesURLUnsigned(t *testing.T) {
	const raw = "https://oapi.dingtalk.com/robot/send?access_token=abc"
	p, err := (DingTalk{WebhookURL: raw}).Build(Event{Signature: "s", Severity: SevHigh})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.URL != raw {
		t.Fatalf("url = %q, want the unsigned webhook %q", p.URL, raw)
	}
}

func TestDingTalkAppendsWithAmpersandWithoutQuery(t *testing.T) {
	ch := DingTalk{
		WebhookURL: "https://oapi.dingtalk.com/robot/send",
		Secret:     "SECtestsecret",
		Now:        func() time.Time { return time.UnixMilli(1700000000000) },
	}
	p, err := ch.Build(Event{Signature: "s", Severity: SevHigh})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if want := "https://oapi.dingtalk.com/robot/send?timestamp=1700000000000&sign="; len(p.URL) <= len(want) || p.URL[:len(want)] != want {
		t.Fatalf("url = %q, want it to start with %q", p.URL, want)
	}
}

func TestDingTalkRequiresWebhook(t *testing.T) {
	if _, err := (DingTalk{}).Build(Event{Severity: SevHigh}); err == nil {
		t.Fatal("expected an error for an empty webhook URL")
	}
}
