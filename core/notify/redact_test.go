package notify

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// failTransport makes client.Do return a *url.Error carrying the full request
// URL, which is the shape net/http uses for a transport failure.
type failTransport struct{ err error }

func (f failTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

func TestRedactURLKeepsOnlySchemeAndHost(t *testing.T) {
	if got := redactURL("https://api.telegram.org/bot123456:SECRET-TOKEN/sendMessage"); got != "https://api.telegram.org" {
		t.Fatalf("redactURL = %q, want scheme+host only", got)
	}
	if got := redactURL("https://hooks.slack.com/services/T000/B000/SECRET"); got != "https://hooks.slack.com" {
		t.Fatalf("redactURL = %q, want scheme+host only", got)
	}
	if got := redactURL("not a url"); got != "<redacted>" {
		t.Fatalf("redactURL(unparseable) = %q, want <redacted>", got)
	}
}

// A failed Telegram delivery must not leak the bot token through the error that
// reaches log.Warn / the /api/logs ring.
func TestTelegramDeliveryErrorRedactsBotToken(t *testing.T) {
	const token = "123456:SECRET-BOT-TOKEN"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ch := Telegram{BotToken: token, ChatID: "1", BaseURL: srv.URL}
	p, err := ch.Build(Event{Signature: "OpenAI API Key", Severity: SevHigh})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := (HTTPDoer{}).Do(p); err == nil {
		t.Fatal("expected the 500 response to fail delivery")
	} else if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "123456") {
		t.Fatalf("delivery error leaks the bot token: %q", err.Error())
	}
}

func TestTelegramTransportErrorRedactsBotToken(t *testing.T) {
	const token = "123456:SECRET-BOT-TOKEN"

	ch := Telegram{BotToken: token, ChatID: "1", BaseURL: "https://api.telegram.org"}
	p, err := ch.Build(Event{Signature: "OpenAI API Key", Severity: SevHigh})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	doer := HTTPDoer{Client: &http.Client{Transport: failTransport{err: errors.New("connection refused")}}}
	err = doer.Do(p)
	if err == nil {
		t.Fatal("expected the transport failure to be reported")
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "123456") {
		t.Fatalf("transport error leaks the bot token: %q", err.Error())
	}
}

func TestSlackDeliveryErrorRedactsWebhookSecret(t *testing.T) {
	const path = "/services/T000/B000/SUPER-SECRET-PART"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ch := Slack{WebhookURL: srv.URL + path}
	p, err := ch.Build(Event{Signature: "OpenAI API Key", Severity: SevHigh})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := (HTTPDoer{}).Do(p); err == nil {
		t.Fatal("expected the 500 response to fail delivery")
	} else if strings.Contains(err.Error(), "SUPER-SECRET-PART") || strings.Contains(err.Error(), "T000") {
		t.Fatalf("delivery error leaks the webhook secret: %q", err.Error())
	}
}

func TestRedactURLErrorStripsURLFromURLError(t *testing.T) {
	// http.Client wraps a transport error in *url.Error, whose Error() prints
	// the full URL when the URL field is left intact.
	doer := HTTPDoer{Client: &http.Client{Transport: failTransport{err: errors.New("boom")}}}
	err := doer.Do(Payload{Method: http.MethodPost, URL: "https://hooks.slack.com/services/T000/B000/LEAK-ME"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "LEAK-ME") {
		t.Fatalf("wrapped url.Error leaked the secret path: %q", err.Error())
	}
}
