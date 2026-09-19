package aiproviders

import (
	"net/http"
	"testing"
)

// TestStreamingClientsHaveNoWholeResponseTimeout pins a regression that would
// silently truncate long reviews.
//
// http.Client.Timeout covers the entire exchange, including reading the response
// body. Reusing the non-streaming Chat client (chatTimeout, 60s) for Stream meant
// a long generation was cut off mid-answer no matter how much time the caller's
// context allowed. A slow local Ollama hits that easily, and the web review
// handler budgets five minutes, so the streaming path must carry no total
// timeout and rely on the caller's context instead.
func TestStreamingClientsHaveNoWholeResponseTimeout(t *testing.T) {
	oc := newOpenAICompatClient(Settings{
		Provider: ProviderOpenAI,
		APIKey:   "k",
		BaseURL:  "http://example.invalid",
		Model:    "m",
	})
	if oc.streamHTTP == nil {
		t.Fatal("openai-compatible client has no streaming http client")
	}
	if oc.streamHTTP.Timeout != 0 {
		t.Errorf("streaming client Timeout = %v, want 0: the caller's context must be the budget",
			oc.streamHTTP.Timeout)
	}
	if oc.streamHTTP == oc.http {
		t.Error("streaming must not share the non-streaming client")
	}
	if oc.http.Timeout != chatTimeout {
		t.Errorf("non-streaming client Timeout = %v, want %v", oc.http.Timeout, chatTimeout)
	}

	ol := newOllamaClient(Settings{
		Provider: ProviderOllama,
		BaseURL:  "http://example.invalid",
		Model:    "m",
	})
	if ol.streamHTTP == nil {
		t.Fatal("ollama client has no streaming http client")
	}
	if ol.streamHTTP.Timeout != 0 {
		t.Errorf("ollama streaming client Timeout = %v, want 0", ol.streamHTTP.Timeout)
	}
	if ol.streamHTTP == ol.http {
		t.Error("ollama streaming must not share the non-streaming client")
	}
}

// TestStreamingTransportStillFailsFast guards the other direction: removing the
// whole-response timeout must not leave the streaming path unbounded, or a dead
// endpoint would hang a review until its five-minute budget expired.
func TestStreamingTransportStillFailsFast(t *testing.T) {
	if streamHTTPClient.Timeout != 0 {
		t.Errorf("streamHTTPClient.Timeout = %v, want 0", streamHTTPClient.Timeout)
	}

	tr, ok := streamHTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("streaming transport = %T, want *http.Transport", streamHTTPClient.Transport)
	}
	if tr.DialContext == nil {
		t.Error("DialContext must be set so a connection attempt is bounded")
	}
	if tr.TLSHandshakeTimeout <= 0 {
		t.Error("TLSHandshakeTimeout must be set")
	}
	if tr.ResponseHeaderTimeout <= 0 {
		t.Error("ResponseHeaderTimeout must be set so a silent endpoint fails rather than hangs")
	}
	if tr.ResponseHeaderTimeout > 2*chatTimeout {
		t.Errorf("ResponseHeaderTimeout = %v, unexpectedly long", tr.ResponseHeaderTimeout)
	}
}
