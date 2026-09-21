package core

import "testing"

// hashPayload used to type-assert embeds[0] and each field to a map without
// checking. A payload whose embed or field entry is a string or number (the
// payload is built from scanned, untrusted data) then panicked the queue
// worker. It must hash every shape without panicking.
func TestWebhookHashPayloadToleratesMalformedEmbeds(t *testing.T) {
	wq := &WebhookQueue{}
	payloads := []string{
		`{"embeds":["not-an-object"]}`,
		`{"embeds":[{"fields":["not-an-object"]}]}`,
		`{"embeds":[{"fields":[{"name":"n","value":"v"}]}]}`,
		`{"embeds":[{"title":"t","description":"d"}]}`,
		`{"content":"hello"}`,
		`not json at all`,
		`{}`,
	}
	for _, p := range payloads {
		got := wq.hashPayload("https://example.com/hook", p)
		if got == "" {
			t.Errorf("hashPayload(%q) returned an empty hash", p)
		}
	}
}

// A valid-JSON payload with no recognised field used to hash only the URL, so
// every generic (Slack-style {"text": ...}) message collapsed into one dedupe
// key and all but the first were dropped inside the dedupe window.
func TestWebhookHashPayloadDistinguishesGenericMessages(t *testing.T) {
	wq := &WebhookQueue{}
	url := "https://example.com/hook"
	a := wq.hashPayload(url, `{"text":"first finding"}`)
	b := wq.hashPayload(url, `{"text":"second finding"}`)
	if a == b {
		t.Fatalf("distinct generic payloads hashed identically: %s", a)
	}
	if a == wq.hashPayload(url, `{"embeds":[]}`) {
		t.Fatal("generic payload collided with an empty embeds payload")
	}
}
