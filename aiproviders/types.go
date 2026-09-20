// Package aiproviders is a small, dependency-free client for the LLM backends
// shhgit can talk to.
//
// It exposes four providers behind one Client interface:
//
//   - "deepseek"  OpenAI-compatible, key required
//   - "openai"    OpenAI-compatible, key required
//   - "custom"    any OpenAI-compatible endpoint; BaseURL and Model required,
//     key optional (some self-hosted gateways need none)
//   - "ollama"    local Ollama; native /api/chat by default, or
//     /chat/completions when the configured base URL is a /v1 route
//
// The three OpenAI-compatible providers share a single implementation, so a fix
// or a header change lands in one place. Settings can be persisted with Store,
// which falls back per field to environment variables and then to the built-in
// provider defaults.
package aiproviders

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Role is the author of a Message.
type Role string

// The three roles every supported backend understands.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one turn of a conversation.
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

// Settings is the user-facing AI configuration. An empty BaseURL or Model means
// "use the provider default", which New fills in before any request is made.
type Settings struct {
	Provider string `json:"provider"` // "deepseek" | "openai" | "custom" | "ollama"
	APIKey   string `json:"api_key"`
	BaseURL  string `json:"base_url"` // optional override; "" means the provider default
	Model    string `json:"model"`    // optional override; "" means the provider default
}

// Client is one configured provider.
type Client interface {
	Name() string
	// Chat returns the complete assistant reply.
	Chat(ctx context.Context, msgs []Message) (string, error)
	// Stream calls onDelta for each incremental chunk and returns when the
	// stream is finished. onDelta may be nil. It returns promptly when ctx is
	// cancelled.
	Stream(ctx context.Context, msgs []Message, onDelta func(delta string) error) error
}

// Sentinels returned by New and Store.Set. Match them with errors.Is: the
// returned errors carry a human-readable explanation around these values.
var (
	ErrUnknownProvider = errors.New("unknown AI provider")
	ErrNoAPIKey        = errors.New("API key is required for this provider")
	ErrBadConfig       = errors.New("incomplete AI configuration")
)

// Provider ids, used verbatim in error messages, persisted settings and as the
// Client name.
const (
	ProviderDeepSeek = "deepseek"
	ProviderOpenAI   = "openai"
	ProviderCustom   = "custom"
	ProviderOllama   = "ollama"
)

// defaultProvider is used when nothing has configured a provider yet.
const defaultProvider = ProviderDeepSeek

// Timeouts.
//
// chatTimeout bounds a complete non-streaming Chat call, response body included,
// so it is applied as http.Client.Timeout.
//
// Streaming must NOT use it. http.Client.Timeout covers the whole exchange
// including reading the body, so a long generation would be killed mid-stream
// no matter how much time the caller's context allows. The streaming client
// below therefore sets no total timeout and is bounded instead by connection
// and header timeouts, with the caller's context as the real budget.
const (
	chatTimeout       = 60 * time.Second
	testConnTimeout   = 15 * time.Second
	streamDialTimeout = 10 * time.Second
	// streamHeaderTimeout leaves room for a cold model load before the first
	// byte, which a local Ollama can take a while over, while still failing a
	// genuinely dead endpoint instead of hanging.
	streamHeaderTimeout = 60 * time.Second
	maxErrorBodyBytes   = 4 << 10 // 4KB
	// maxStreamLine is the largest single SSE/NDJSON line the stream parsers
	// accept. Long delta lines must not abort a stream.
	maxStreamLine = 4 << 20 // 4MB
)

// streamHTTPClient is shared by every provider's streaming path. It
// deliberately has no Timeout: see the note above.
var streamHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   streamDialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   streamDialTimeout,
		ResponseHeaderTimeout: streamHeaderTimeout,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   4,
	},
}

// providerSpec describes the built-in defaults for one provider.
type providerSpec struct {
	id       string
	baseURL  string
	model    string
	needsKey bool
	custom   bool // true when the values above are not defaults but inputs
}

// providerSpecs returns the registry in display order. A fresh slice is
// returned each call so callers cannot mutate the table.
func providerSpecs() []providerSpec {
	return []providerSpec{
		{id: ProviderDeepSeek, baseURL: "https://api.deepseek.com/v1", model: "deepseek-chat", needsKey: true},
		{id: ProviderOpenAI, baseURL: "https://api.openai.com/v1", model: "gpt-4o-mini", needsKey: true},
		{id: ProviderCustom, custom: true},
		{id: ProviderOllama, baseURL: "http://localhost:11434", model: "llama3.1"},
	}
}

// KnownProviders lists the selectable provider ids in display order.
func KnownProviders() []string {
	specs := providerSpecs()
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		out = append(out, spec.id)
	}
	return out
}

// lookupProvider resolves a raw provider id, tolerating surrounding spaces and
// mixed case. It reports ErrUnknownProvider for an unrecognised id (including
// the empty string).
func lookupProvider(raw string) (providerSpec, error) {
	id := normalizeProvider(raw)
	for _, spec := range providerSpecs() {
		if spec.id == id {
			return spec, nil
		}
	}
	return providerSpec{}, fmt.Errorf("%w: %q", ErrUnknownProvider, strings.TrimSpace(raw))
}

func normalizeProvider(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// validateMessages rejects message shapes no backend can interpret.
func validateMessages(msgs []Message) error {
	for i, msg := range msgs {
		switch msg.Role {
		case RoleSystem, RoleUser, RoleAssistant:
		default:
			return fmt.Errorf("message %d: unsupported role %q", i, string(msg.Role))
		}
	}
	return nil
}

// resolve normalizes provider-independent input into an effective Settings: it
// trims the fields, canonicalises the provider id, rejects unusable
// configurations and fills in the provider defaults for BaseURL and Model.
//
// Custom endpoints have no built-in defaults, so a missing BaseURL or Model is
// ErrBadConfig there; a missing key is only fatal for providers that need one.
func resolve(in Settings) (Settings, error) {
	out, err := applyDefaults(in)
	if err != nil {
		return Settings{}, err
	}

	// applyDefaults resolved the provider, so this lookup cannot fail.
	spec, _ := lookupProvider(out.Provider)
	if spec.needsKey && out.APIKey == "" {
		return Settings{}, fmt.Errorf("%w: %s", ErrNoAPIKey, spec.id)
	}
	return out, nil
}

// validateBaseURL rejects an endpoint that is not an absolute http(s) URL.
//
// The stack this replaced validated the Ollama URL and the rewrite dropped it, so
// "localhost:11434" - which url.Parse reads as the scheme "localhost" - was stored
// happily and only surfaced on the first review as an opaque "unsupported protocol
// scheme" from the HTTP client. It is deliberately not restricted to loopback:
// Ollama on another machine is a normal setup (a GPU host, say), and the operator
// types the endpoint themselves.
func validateBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %q is not a valid URL: %v", ErrBadConfig, raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: %q must be an absolute http:// or https:// URL", ErrBadConfig, raw)
	}
	if u.Host == "" {
		return fmt.Errorf("%w: %q has no host", ErrBadConfig, raw)
	}
	return nil
}

// applyDefaults trims and canonicalises the settings and fills in the provider
// defaults for BaseURL and Model. Unlike resolve it does not judge whether the
// configuration is usable: Store.Get needs the effective endpoint and model
// even while the user has not supplied an API key yet.
//
// A missing BaseURL or Model is only an error for "custom", which has no
// built-in endpoint to fall back to.
func applyDefaults(in Settings) (Settings, error) {
	spec, err := lookupProvider(in.Provider)
	if err != nil {
		return Settings{}, err
	}

	out := Settings{
		Provider: spec.id,
		APIKey:   strings.TrimSpace(in.APIKey),
		BaseURL:  strings.TrimSpace(in.BaseURL),
		Model:    strings.TrimSpace(in.Model),
	}

	if out.BaseURL != "" {
		if err := validateBaseURL(out.BaseURL); err != nil {
			return Settings{}, err
		}
	}

	if spec.custom {
		if out.BaseURL == "" {
			return Settings{}, fmt.Errorf("%w: provider %q requires a base URL", ErrBadConfig, spec.id)
		}
		if out.Model == "" {
			return Settings{}, fmt.Errorf("%w: provider %q requires a model", ErrBadConfig, spec.id)
		}
		return out, nil
	}

	if out.BaseURL == "" {
		out.BaseURL = spec.baseURL
	}
	if out.Model == "" {
		out.Model = spec.model
	}
	return out, nil
}

// New validates settings, applies provider defaults and returns a ready client.
func New(s Settings) (Client, error) {
	eff, err := resolve(s)
	if err != nil {
		return nil, err
	}

	if eff.Provider == ProviderOllama && !isOpenAICompatibleBase(eff.BaseURL) {
		return newOllamaClient(eff), nil
	}
	return newOpenAICompatClient(eff), nil
}

// Defaults returns the built-in defaults for a provider: an empty Settings with
// BaseURL and Model filled in and Provider set to the canonical id.
//
// A "custom" endpoint has no built-in defaults (the address and model are
// supplied by the user), so for it only Provider comes back set. An unknown
// provider id yields a zero Settings; call it through New to get
// ErrUnknownProvider.
func Defaults(provider string) Settings {
	spec, err := lookupProvider(provider)
	if err != nil {
		return Settings{}
	}
	return Settings{Provider: spec.id, BaseURL: spec.baseURL, Model: spec.model}
}

// testPrompt keeps TestConnection's probe as close to one token as possible.
const testPrompt = "Reply with exactly one word."

// TestConnection performs one minimal live request and returns a short
// human-readable description of what responded.
//
// The probe asks the endpoint to cap the reply at a single token (max_tokens,
// or num_predict on a native Ollama endpoint) and is bounded by a short timeout.
func TestConnection(ctx context.Context, s Settings) (string, error) {
	eff, err := resolve(s)
	if err != nil {
		return "", err
	}
	client, err := New(eff)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, testConnTimeout)
	defer cancel()

	probe := []Message{
		{Role: RoleSystem, Content: testPrompt},
		{Role: RoleUser, Content: "ok"},
	}
	switch c := client.(type) {
	case *openAICompatClient:
		_, err = c.chat(ctx, probe, 1)
	case *ollamaClient:
		_, err = c.chat(ctx, probe, 1)
	default:
		_, err = client.Chat(ctx, probe)
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s: %s OK", eff.Provider, eff.Model), nil
}
