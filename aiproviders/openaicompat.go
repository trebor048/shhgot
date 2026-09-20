package aiproviders

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// openAICompatClient implements the OpenAI chat-completions protocol. All three
// OpenAI-compatible providers (deepseek, openai, custom) use exactly this type;
// only the effective base URL, model and key differ.
type openAICompatClient struct {
	name    string
	apiKey  string
	baseURL string
	model   string
	http    *http.Client
	// streamHTTP has no total timeout; a long generation must be bounded by the
	// caller's context, not by http.Client.Timeout.
	streamHTTP *http.Client
}

func newOpenAICompatClient(s Settings) *openAICompatClient {
	return &openAICompatClient{
		name:       s.Provider,
		apiKey:     s.APIKey,
		baseURL:    s.BaseURL,
		model:      s.Model,
		http:       &http.Client{Timeout: chatTimeout},
		streamHTTP: streamHTTPClient,
	}
}

// Name returns the provider id: "deepseek", "openai" or "custom". An Ollama
// endpoint configured as an OpenAI-compatible route keeps the "ollama" id,
// because that is what the user selected and what error messages must name.
func (c *openAICompatClient) Name() string { return c.name }

// chatRequest is the request body sent to {base}/chat/completions. Field order
// matches the documented wire shape: model, messages, stream.
type chatRequest struct {
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	Stream    bool      `json:"stream"`
	MaxTokens int       `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content *string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

type chatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content *string `json:"content"`
		} `json:"delta"`
		// FinishReason is set by OpenAI-compatible endpoints on the final chunk
		// ("stop", "length", ...) and is a valid end of stream even when the
		// endpoint does not send the "[DONE]" sentinel.
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// Chat performs one non-streaming completion.
func (c *openAICompatClient) Chat(ctx context.Context, msgs []Message) (string, error) {
	return c.chat(ctx, msgs, 0)
}

// chat performs one non-streaming completion, asking the endpoint to cap the
// reply at maxTokens when that is greater than zero.
func (c *openAICompatClient) chat(ctx context.Context, msgs []Message, maxTokens int) (string, error) {
	if err := validateMessages(msgs); err != nil {
		return "", err
	}

	body, err := json.Marshal(chatRequest{Model: c.model, Messages: msgs, Stream: false, MaxTokens: maxTokens})
	if err != nil {
		return "", fmt.Errorf("%s: encode request: %w", c.name, err)
	}

	resp, err := c.post(ctx, "chat/completions", body, c.http)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if err := c.checkStatus(resp); err != nil {
		return "", err
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxStreamLine))
	if err != nil {
		return "", fmt.Errorf("%s: read response: %w", c.name, err)
	}

	var out chatResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("%s: decode response: %w (body: %s)", c.name, err, snippet(data))
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("%s: response contained no choices", c.name)
	}

	choice := out.Choices[0]
	if choice.Message.Content == nil || strings.TrimSpace(*choice.Message.Content) == "" {
		// A 200 with an empty completion is not a usable answer.
		return "", fmt.Errorf("%s: response contained no content (finish_reason %q)", c.name, choice.FinishReason)
	}
	return *choice.Message.Content, nil
}

// Stream performs one streaming completion, invoking onDelta for every chunk of
// assistant text. It returns when the server signals completion, and promptly
// (with ctx's error) when ctx is cancelled.
func (c *openAICompatClient) Stream(ctx context.Context, msgs []Message, onDelta func(delta string) error) error {
	if err := validateMessages(msgs); err != nil {
		return err
	}

	body, err := json.Marshal(chatRequest{Model: c.model, Messages: msgs, Stream: true})
	if err != nil {
		return fmt.Errorf("%s: encode request: %w", c.name, err)
	}

	resp, err := c.post(ctx, "chat/completions", body, c.streamHTTP)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := c.checkStatus(resp); err != nil {
		return err
	}

	// A long single SSE line must not abort the stream, so the scanner buffer is
	// far larger than bufio's 64KB default.
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxStreamLine)

	sawDelta := false
	sawCompletion := false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}

		line := strings.TrimRight(scanner.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue // keep-alive or the blank line between events
		}
		if !strings.HasPrefix(trimmed, "data:") {
			continue // comments, and any other SSE field we do not consume
		}

		payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			sawCompletion = true
			return nil
		}

		var chunk chatStreamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return fmt.Errorf("%s: decode stream chunk: %w (data: %s)", c.name, err, snippet([]byte(payload)))
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].FinishReason != "" {
			// A non-empty finish reason ("stop", "length", ...) is a valid end of
			// stream for endpoints that do not send the "[DONE]" sentinel.
			sawCompletion = true
		}
		if len(chunk.Choices) == 0 || chunk.Choices[0].Delta.Content == nil {
			continue
		}
		delta := *chunk.Choices[0].Delta.Content
		if delta == "" {
			continue
		}
		sawDelta = true
		if onDelta != nil {
			if err := onDelta(delta); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return fmt.Errorf("%s: read stream: %w", c.name, err)
	}
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	// The stream ended on its own. Without a completion marker the answer we just
	// handed the caller is a fragment - an HTML error page from a proxy carries no
	// text at all, a dropped connection carries half a sentence - and reporting that
	// as a finished review is what let a truncated assessment look complete.
	return streamVerdict(c.name, sawDelta, sawCompletion)
}

// post sends one JSON request to {base}/{path}. The Authorization header is
// only added when a key is set, which is what lets keyless custom endpoints and
// Ollama work. hc selects the timeout policy: c.http for a bounded Chat call,
// c.streamHTTP for a stream whose budget is the caller's context.
func (c *openAICompatClient) post(ctx context.Context, path string, body []byte, hc *http.Client) (*http.Response, error) {
	url := joinURL(c.baseURL, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", c.name, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := hc.Do(req)
	if err != nil {
		// Never surface the API key, even if the transport echoed the request.
		return nil, fmt.Errorf("%s: request failed: %w", c.name, redactError(err, c.apiKey))
	}
	return resp, nil
}

// checkStatus converts a non-2xx response into "<provider> status <code>:
// <body snippet>" after draining a bounded amount of the body.
func (c *openAICompatClient) checkStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return errorFromResponse(c.name, c.apiKey, resp)
}

// errorFromResponse reads at most maxErrorBodyBytes and formats the provider
// error. The API key is redacted so an upstream echo of the request can never
// leak it into logs.
func errorFromResponse(name, apiKey string, resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	msg := snippet(raw)
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return fmt.Errorf("%s status %d: %s", name, resp.StatusCode, redactString(msg, apiKey))
}

// joinURL appends path to base, leaving exactly one separator.
func joinURL(base, path string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

// isOpenAICompatibleBase reports whether a base URL points at an
// OpenAI-compatible route rather than Ollama's native API. Ollama is reached
// natively at http://localhost:11434, but fronted by an OpenAI-compatible proxy
// (or served under /v1) it speaks /chat/completions.
func isOpenAICompatibleBase(base string) bool {
	trimmed := strings.TrimRight(strings.TrimSpace(base), "/")
	lower := strings.ToLower(trimmed)
	return strings.HasSuffix(lower, "/v1") || strings.Contains(lower, "/v1/")
}

// snippet collapses a body to one bounded, printable line.
func snippet(body []byte) string {
	const limit = 400
	text := strings.TrimSpace(string(body))
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit]) + "..."
	}
	return text
}

// redactError removes a key from an error message.
func redactError(err error, apiKey string) error {
	if err == nil {
		return nil
	}
	masked := redactString(err.Error(), apiKey)
	if masked == err.Error() {
		return err
	}
	return fmt.Errorf("%s", masked)
}

// bearerToken matches an Authorization value wherever an upstream echoes one
// back. "Bearer" is unambiguous, so this is redacted whatever the key's length.
var bearerToken = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=@-]+`)

// redactString removes the API key and any echoed Authorization value from a
// provider error.
//
// A key of four characters or more is replaced wherever it appears. A shorter one
// is replaced only where it stands alone as a token: replacing it blindly would
// corrupt unrelated words, but ignoring it entirely - as this used to - lets a
// short credential through in text that is stored with the review as its error
// and broadcast on the live feed.
func redactString(s, apiKey string) string {
	if s == "" {
		return s
	}

	out := s
	switch {
	case len(apiKey) >= 4:
		out = strings.ReplaceAll(out, apiKey, "[redacted]")
	case apiKey != "":
		// Require a non-alphanumeric boundary on both sides, so a short key is
		// not matched inside a longer unrelated word.
		standalone := regexp.MustCompile(`(^|[^A-Za-z0-9])` + regexp.QuoteMeta(apiKey) + `([^A-Za-z0-9]|$)`)
		out = standalone.ReplaceAllString(out, "${1}[redacted]${2}")
	}
	return bearerToken.ReplaceAllString(out, "Bearer [redacted]")
}
