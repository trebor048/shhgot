package notify

import (
	"net/url"
	"strings"
	"testing"
)

func TestServerChanFormBody(t *testing.T) {
	ch := ServerChan{SendKey: "SCT123"}
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
	if p.URL != "https://sctapi.ftqq.com/SCT123.send" {
		t.Fatalf("url = %q", p.URL)
	}
	if got := p.Headers["Content-Type"]; got != "application/x-www-form-urlencoded" {
		t.Fatalf("content-type = %q", got)
	}
	form, err := url.ParseQuery(string(p.Body))
	if err != nil {
		t.Fatalf("body is not valid form encoding: %v", err)
	}
	if !strings.Contains(form.Get("title"), "OpenAI API Key") {
		t.Fatalf("title = %q", form.Get("title"))
	}
	if form.Get("desp") == "" {
		t.Fatal("desp is empty")
	}
	if strings.Contains(string(p.Body), "sk-abcdef0123456789") {
		t.Fatalf("body leaks the raw secret: %s", p.Body)
	}
}

func TestServerChanRequiresSendKey(t *testing.T) {
	if _, err := (ServerChan{}).Build(Event{Severity: SevHigh}); err == nil {
		t.Fatal("expected an error for an empty send key")
	}
}
