package aiproviders

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ollamaClient talks to a local Ollama daemon over its native API.
//
// Ollama exposes POST /api/chat with newline-delimited JSON responses; a base
// URL that already carries /v1 (an OpenAI-compatible proxy in front of Ollama)
// is routed to /chat/completions instead by New, which hands those settings to
// openAICompatClient.
type ollamaClient struct {
	name    string
	baseURL string
	model   string
	http    *http.Client
	// streamHTTP has no total timeout; a local model generating a long
	// assessment must be bounded by the caller's context, not by the 60s
	// whole-response limit that would otherwise cut it off mid-stream.
	streamHTTP *http.Client
}

func newOllamaClient(s Settings) *ollamaClient {
	return &ollamaClient{
		name:       s.Provider,
		baseURL:    s.BaseURL,
		model:      s.Model,
		http:       &http.Client{Timeout: chatTimeout},
		streamHTTP: streamHTTPClient,
	}
}

func (c *ollamaClient) Name() string { return c.name }

// ollamaChatRequest is the native request body. Ollama takes no API key, and
// num_predict is its equivalent of max_tokens (used by TestConnection).
type ollamaChatRequest struct {
	Model    string         `json:"model"`
	Messages []Message      `json:"messages"`
	Stream   bool           `json:"stream"`
	Options  *ollamaOptions `json:"options,omitempty"`
}

type ollamaOptions struct {
	NumPredict int `json:"num_predict"`
}

type ollamaChatResponse struct {
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	Done  bool   `json:"done"`
	Error string `json:"error"`
}

// Chat performs one non-streaming completion via /api/chat.
func (c *ollamaClient) Chat(ctx context.Context, msgs []Message) (string, error) {
	return c.chat(ctx, msgs, 0)
}

// chat performs one non-streaming completion, capping the reply with
// num_predict when maxTokens is greater than zero.
func (c *ollamaClient) chat(ctx context.Context, msgs []Message, maxTokens int) (string, error) {
	if err := validateMessages(msgs); err != nil {
		return "", err
	}

	req := ollamaChatRequest{Model: c.model, Messages: msgs, Stream: false}
	if maxTokens > 0 {
		req.Options = &ollamaOptions{NumPredict: maxTokens}
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("%s: encode request: %w", c.name, err)
	}

	resp, err := c.post(ctx, "api/chat", body, c.http)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", errorFromResponse(c.name, "", resp)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxStreamLine))
	if err != nil {
		return "", fmt.Errorf("%s: read response: %w", c.name, err)
	}

	var out ollamaChatResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("%s: decode response: %w (body: %s)", c.name, err, snippet(data))
	}
	if out.Error != "" {
		return "", fmt.Errorf("%s: %s", c.name, out.Error)
	}
	if strings.TrimSpace(out.Message.Content) == "" {
		return "", fmt.Errorf("%s: response contained no content", c.name)
	}
	return out.Message.Content, nil
}

// Stream performs a streaming completion via /api/chat, calling onDelta for
// each object's message.content. The final object carries "done":true.
func (c *ollamaClient) Stream(ctx context.Context, msgs []Message, onDelta func(delta string) error) error {
	if err := validateMessages(msgs); err != nil {
		return err
	}

	body, err := json.Marshal(ollamaChatRequest{Model: c.model, Messages: msgs, Stream: true})
	if err != nil {
		return fmt.Errorf("%s: encode request: %w", c.name, err)
	}

	resp, err := c.post(ctx, "api/chat", body, c.streamHTTP)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errorFromResponse(c.name, "", resp)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxStreamLine)

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var chunk ollamaChatResponse
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			return fmt.Errorf("%s: decode stream chunk: %w (data: %s)", c.name, err, snippet([]byte(line)))
		}
		if chunk.Error != "" {
			return fmt.Errorf("%s: %s", c.name, chunk.Error)
		}
		if chunk.Message.Content != "" && onDelta != nil {
			if err := onDelta(chunk.Message.Content); err != nil {
				return err
			}
		}
		if chunk.Done {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return fmt.Errorf("%s: read stream: %w", c.name, err)
	}
	return ctx.Err()
}

// post sends one JSON request to {base}/{path}. Ollama's native API takes no
// credentials, so no Authorization header is ever sent. hc selects the timeout
// policy: c.http for a bounded Chat call, c.streamHTTP for a stream whose
// budget is the caller's context.
func (c *ollamaClient) post(ctx context.Context, path string, body []byte, hc *http.Client) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(c.baseURL, path), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", c.name, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/x-ndjson, application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: request failed: %w", c.name, err)
	}
	return resp, nil
}
