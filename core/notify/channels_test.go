package notify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscordEmbedColorBySeverity(t *testing.T) {
	ev := Event{Signature: "OpenAI API Key", Severity: SevCritical, Repo: "acme/app", File: "config.py", Line: 12}
	for _, tc := range []struct {
		severity Severity
		color    int
	}{
		{SevLow, 3066993},
		{SevMedium, 15844367},
		{SevHigh, 15105570},
		{SevCritical, 15158332},
	} {
		ev.Severity = tc.severity
		p, err := (Discord{WebhookURL: "https://discord.example/api/webhooks/1/x"}).Build(ev)
		if err != nil {
			t.Fatalf("Build(%s): %v", tc.severity, err)
		}
		var msg struct {
			Embeds []struct {
				Title  string `json:"title"`
				Color  int    `json:"color"`
				Fields []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"fields"`
			} `json:"embeds"`
		}
		if err := json.Unmarshal(p.Body, &msg); err != nil {
			t.Fatalf("body is not valid JSON: %v", err)
		}
		if len(msg.Embeds) != 1 {
			t.Fatalf("got %d embeds, want 1", len(msg.Embeds))
		}
		if msg.Embeds[0].Color != tc.color {
			t.Fatalf("severity %s: color = %d, want %d", tc.severity, msg.Embeds[0].Color, tc.color)
		}
		if msg.Embeds[0].Title != "OpenAI API Key" {
			t.Fatalf("title = %q", msg.Embeds[0].Title)
		}
	}
}

// Every Discord embed field value must fit the 1024-character limit. The File
// and Secret fields wrap their content in backticks, so truncating the content
// to 1024 and then adding the backticks produced 1026 characters and Discord
// rejected the whole embed, losing the alert.
func TestDiscordFieldValuesRespectLimit(t *testing.T) {
	longFile := strings.Repeat("p", 2000) + ".env"
	longSecret := "sk-" + strings.Repeat("s", 4000)
	ev := Event{
		Signature: "OpenAI API Key",
		Severity:  SevCritical,
		Repo:      "acme/app",
		File:      longFile,
		Line:      12,
		Secret:    longSecret,
	}
	p, err := (Discord{WebhookURL: "https://discord.example/api/webhooks/1/x"}).Build(ev)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	var msg struct {
		Embeds []struct {
			Fields []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"fields"`
		} `json:"embeds"`
	}
	if err := json.Unmarshal(p.Body, &msg); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if len(msg.Embeds) != 1 {
		t.Fatalf("got %d embeds, want 1", len(msg.Embeds))
	}
	if len(msg.Embeds[0].Fields) == 0 {
		t.Fatal("no fields rendered")
	}
	for _, f := range msg.Embeds[0].Fields {
		if len(f.Value) > 1024 {
			t.Errorf("field %q value is %d characters, want <= 1024", f.Name, len(f.Value))
		}
	}
}

func TestTelegramSendMessageURL(t *testing.T) {
	ch := Telegram{
		BotToken: "123456:ABC-DEF",
		ChatID:   "-100123",
		BaseURL:  "https://tg.example/",
	}
	p, err := ch.Build(Event{Signature: "OpenAI API Key", Severity: SevHigh, File: "a.py"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.URL != "https://tg.example/bot123456:ABC-DEF/sendMessage" {
		t.Fatalf("url = %q", p.URL)
	}
	var msg struct {
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	}
	_ = json.Unmarshal(p.Body, &msg)
	if msg.ChatID != "-100123" {
		t.Fatalf("chat_id = %q", msg.ChatID)
	}
	if !strings.Contains(msg.Text, "high") {
		t.Fatalf("text missing severity: %q", msg.Text)
	}
}

func TestGenericTemplateSubstitution(t *testing.T) {
	ev := Event{
		Signature: "OpenAI API Key",
		Severity:  SevCritical,
		Repo:      "acme/app",
		File:      "config.py",
		Line:      12,
		Secret:    "sk-abcdef0123456789",
		Priority:  9,
		Provider:  "OpenAI",
	}
	ch := Generic{
		URL:      "https://relay.example/hook",
		Template: `{"sig":"{signature}","sev":"{severity}","loc":"{file}:{line}","key":"{secret}","prio":{priority}}`,
	}
	p, err := ch.Build(ev)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	body := string(p.Body)
	for _, want := range []string{`"sig":"OpenAI API Key"`, `"sev":"critical"`, `"loc":"config.py:12"`, `"prio":9`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "sk-abcdef0123456789") {
		t.Fatalf("body leaks the raw secret: %s", body)
	}
}

func TestGenericDefaultBodyIsSafeJSON(t *testing.T) {
	ch := Generic{URL: "https://relay.example/hook"}
	p, err := ch.Build(Event{Signature: `quote " and newline` + "\n", Severity: SevHigh})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(p.Body, &decoded); err != nil {
		t.Fatalf("default body is not valid JSON: %v (%s)", err, p.Body)
	}
}

func TestGenericRequiresURL(t *testing.T) {
	if _, err := (Generic{}).Build(Event{Severity: SevHigh}); err == nil {
		t.Fatal("expected an error for an empty URL")
	}
}

func TestFileAppendsOneLinePerFinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "findings.log")
	ch := File{Path: path}
	ev := Event{Signature: "OpenAI API Key", Severity: SevCritical, File: "config.py", Line: 12, URL: "https://example.invalid/config.py"}

	for i := 0; i < 2; i++ {
		p, err := ch.Build(ev)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if err := ch.Send(p); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if got := strings.Count(string(data), "\n"); got != 2 {
		t.Fatalf("got %d lines, want 2: %q", got, string(data))
	}
}

func TestFileBuildStripsNewlines(t *testing.T) {
	ch := File{Path: "findings.log"}
	p, err := ch.Build(Event{
		Signature: "OpenAI API Key",
		Severity:  SevHigh,
		File:      "evil\ninjected.log",
		URL:       "https://example.invalid/a\r\nb",
		Secret:    "sk-abc\r\ndef",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := strings.Count(string(p.Body), "\n"); got != 1 {
		t.Fatalf("rendered line must contain exactly one newline, got %d: %q", got, p.Body)
	}
	if strings.ContainsRune(string(p.Body), '\r') {
		t.Fatalf("rendered line must not contain CR: %q", p.Body)
	}
}

func TestMaskSecretMasksAtLeastHalf(t *testing.T) {
	for _, s := range []string{"123456789", "1234567890", "12345678901", "123456789012"} {
		got := maskSecret(s)
		if len(got) != len(s) {
			t.Fatalf("maskSecret(%q) = %q, length changed", s, got)
		}
		if stars := strings.Count(got, "*"); stars*2 < len(s) {
			t.Fatalf("maskSecret(%q) = %q masks only %d of %d characters", s, got, stars, len(s))
		}
	}
}

func TestSoundUsesInjectedBeep(t *testing.T) {
	called := 0
	ch := Sound{Beep: func() { called++ }}
	p, err := ch.Build(Event{Signature: "OpenAI API Key", Severity: SevCritical, File: "a.py"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Method != "BEEP" {
		t.Fatalf("method = %q", p.Method)
	}
	if err := ch.Send(p); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if called != 1 {
		t.Fatalf("beep called %d times, want 1", called)
	}
}

// TestSeverityUnknownNeverDemotes proves an out-of-range severity renders as
// "unknown" rather than "low": the old default silently dropped such findings
// below every MinSeverity floor.
func TestSeverityUnknownNeverDemotes(t *testing.T) {
	if got := Severity(99).String(); got != "unknown" {
		t.Fatalf("Severity(99) = %q, want unknown", got)
	}
	for s, want := range map[Severity]string{SevLow: "low", SevMedium: "medium", SevHigh: "high", SevCritical: "critical"} {
		if got := s.String(); got != want {
			t.Fatalf("Severity(%d) = %q, want %q", int(s), got, want)
		}
	}
}
