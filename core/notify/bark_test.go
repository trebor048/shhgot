package notify

import (
	"encoding/json"
	"testing"
)

func TestBarkSoundAndURL(t *testing.T) {
	ev := Event{
		Signature: "OpenAI API Key",
		Severity:  SevCritical,
		File:      "config.py",
		Line:      12,
		URL:       "https://github.com/acme/app/blob/main/config.py#L12",
	}
	for _, tc := range []struct {
		severity Severity
		sound    string
	}{
		{SevCritical, "alarm"},
		{SevHigh, "alarm"},
		{SevMedium, "default"},
		{SevLow, "default"},
	} {
		ev.Severity = tc.severity
		p, err := (Bark{DeviceKey: "device123"}).Build(ev)
		if err != nil {
			t.Fatalf("Build(%s): %v", tc.severity, err)
		}
		if p.URL != "https://api.day.app/device123" {
			t.Fatalf("url = %q, want the default relay plus device key", p.URL)
		}
		var msg struct {
			Title string `json:"title"`
			Body  string `json:"body"`
			URL   string `json:"url"`
			Group string `json:"group"`
			Sound string `json:"sound"`
		}
		if err := json.Unmarshal(p.Body, &msg); err != nil {
			t.Fatalf("body is not valid JSON: %v", err)
		}
		if msg.Sound != tc.sound {
			t.Fatalf("severity %s: sound = %q, want %q", tc.severity, msg.Sound, tc.sound)
		}
		if msg.Group != "shhgit" {
			t.Fatalf("group = %q, want shhgit", msg.Group)
		}
		if msg.URL != ev.URL {
			t.Fatalf("url field = %q, want the finding url", msg.URL)
		}
	}
}

func TestBarkCustomBaseURL(t *testing.T) {
	p, err := (Bark{BaseURL: "https://bark.example/", DeviceKey: "k"}).Build(Event{Severity: SevLow})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.URL != "https://bark.example/k" {
		t.Fatalf("url = %q", p.URL)
	}
}

func TestBarkRequiresDeviceKey(t *testing.T) {
	if _, err := (Bark{}).Build(Event{Severity: SevHigh}); err == nil {
		t.Fatal("expected an error for an empty device key")
	}
}
