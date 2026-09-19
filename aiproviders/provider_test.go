package aiproviders_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trebor048/shhgot/aiproviders"
)

const (
	// testKey is long enough for the redaction pass to be meaningful.
	testKey = "sk-test-0123456789abcdef"
)

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// clearAIEnv hides every variable Store.Get consults so a test starts from a
// known state regardless of the ambient environment.
func clearAIEnv(t *testing.T) {
	t.Helper()
	for _, name := range aiEnvVars() {
		t.Setenv(name, "")
	}
}

// aiEnvVars lists the names clearAIEnv blanks, including the third-party
// aliases Store.Get falls back to.
func aiEnvVars() []string {
	names := []string{"SHHGIT_AI_PROVIDER"}
	for _, suffix := range []string{"_API_KEY", "_BASE_URL", "_MODEL"} {
		names = append(names, "SHHGIT_AI"+suffix)
		for _, provider := range aiproviders.KnownProviders() {
			names = append(names, "SHHGIT_AI_"+strings.ToUpper(provider)+suffix)
		}
	}
	return append(names, "DEEPSEEK_API_KEY", "DEEPSEEK_KEY", "OPENAI_API_KEY", "OPENAI_KEY", "OLLAMA_KEY", "OLLAMA_API_KEY", "OLLAMA_HOST")
}

// capture records the request a fake provider server received.
type capture struct {
	Method string
	Path   string
	Auth   string
	Header http.Header
	Body   []byte
}

// writeJSON emits one non-streaming chat-completions style completion.
func writeJSON(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, content)
}

// ---- Chat across all four providers --------------------------------------

func TestChatAllProviders(t *testing.T) {
	msgs := []aiproviders.Message{
		{Role: aiproviders.RoleSystem, Content: "be brief"},
		{Role: aiproviders.RoleUser, Content: "hello"},
	}

	tests := []struct {
		name           string
		settings       func(base string) aiproviders.Settings
		wantPath       string
		wantAuth       string
		wantMaxTokens  bool
		wantNumPredict int // >0 for native ollama
	}{
		{
			name: "deepseek",
			settings: func(base string) aiproviders.Settings {
				return aiproviders.Settings{Provider: "deepseek", APIKey: testKey, BaseURL: base}
			},
			wantPath: "/chat/completions",
			wantAuth: "Bearer " + testKey,
		},
		{
			name: "openai",
			settings: func(base string) aiproviders.Settings {
				return aiproviders.Settings{Provider: "openai", APIKey: testKey, BaseURL: base}
			},
			wantPath: "/chat/completions",
			wantAuth: "Bearer " + testKey,
		},
		{
			name: "custom with key",
			settings: func(base string) aiproviders.Settings {
				return aiproviders.Settings{Provider: "custom", APIKey: testKey, BaseURL: base, Model: "my-model"}
			},
			wantPath: "/chat/completions",
			wantAuth: "Bearer " + testKey,
		},
		{
			name: "custom without key",
			settings: func(base string) aiproviders.Settings {
				return aiproviders.Settings{Provider: "custom", BaseURL: base, Model: "my-model"}
			},
			wantPath: "/chat/completions",
			wantAuth: "",
		},
		{
			name: "ollama native",
			settings: func(base string) aiproviders.Settings {
				return aiproviders.Settings{Provider: "ollama", BaseURL: base}
			},
			wantPath: "/api/chat",
			wantAuth: "",
		},
		{
			name: "ollama via /v1 is OpenAI compatible",
			settings: func(base string) aiproviders.Settings {
				return aiproviders.Settings{Provider: "ollama", BaseURL: base + "/v1"}
			},
			wantPath: "/v1/chat/completions",
			wantAuth: "",
		},
		{
			name: "trailing slash on base is tolerated",
			settings: func(base string) aiproviders.Settings {
				return aiproviders.Settings{Provider: "deepseek", APIKey: testKey, BaseURL: base + "/"}
			},
			wantPath: "/chat/completions",
			wantAuth: "Bearer " + testKey,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu   sync.Mutex
				got  capture
				hits int
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request body: %v", err)
				}
				mu.Lock()
				got = capture{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Header: r.Header.Clone(), Body: raw}
				hits++
				mu.Unlock()

				if r.URL.Path == "/api/chat" {
					w.Header().Set("Content-Type", "application/x-ndjson")
					fmt.Fprint(w, `{"model":"llama3.1","message":{"role":"assistant","content":"native reply"},"done":true}`+"\n")
					return
				}
				writeJSON(w, "hello there")
			}))
			defer srv.Close()

			client, err := aiproviders.New(tc.settings(srv.URL))
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			reply, err := client.Chat(ctxT(t), msgs)
			if err != nil {
				t.Fatalf("Chat: %v", err)
			}

			mu.Lock()
			defer mu.Unlock()

			if hits != 1 {
				t.Fatalf("server hits = %d, want 1", hits)
			}
			if got.Method != http.MethodPost {
				t.Errorf("method = %q, want POST", got.Method)
			}
			if got.Path != tc.wantPath {
				t.Errorf("path = %q, want %q", got.Path, tc.wantPath)
			}
			if got.Auth != tc.wantAuth {
				t.Errorf("Authorization = %q, want %q", got.Auth, tc.wantAuth)
			}
			if ct := got.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			// Body shape: every provider sends model, messages and stream.
			var body struct {
				Model    string `json:"model"`
				Messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"messages"`
				Stream  bool `json:"stream"`
				Options *struct {
					NumPredict *int `json:"num_predict"`
				} `json:"options"`
				MaxTokens *int `json:"max_tokens"`
			}
			if err := json.Unmarshal(got.Body, &body); err != nil {
				t.Fatalf("decode request body %s: %v", got.Body, err)
			}
			if body.Model == "" {
				t.Errorf("request body has no model: %s", got.Body)
			}
			if body.Stream {
				t.Errorf("non-streaming Chat sent stream=true: %s", got.Body)
			}
			if len(body.Messages) != len(msgs) {
				t.Fatalf("messages = %d, want %d (%s)", len(body.Messages), len(msgs), got.Body)
			}
			for i, want := range msgs {
				if body.Messages[i].Role != string(want.Role) || body.Messages[i].Content != want.Content {
					t.Errorf("messages[%d] = %+v, want %+v", i, body.Messages[i], want)
				}
			}
			if body.MaxTokens != nil {
				t.Errorf("Chat must not send max_tokens: %s", got.Body)
			}

			// Expected model per provider, proving defaults were applied.
			switch tc.name {
			case "deepseek", "trailing slash on base is tolerated":
				if body.Model != "deepseek-chat" {
					t.Errorf("model = %q, want deepseek-chat", body.Model)
				}
			case "openai":
				if body.Model != "gpt-4o-mini" {
					t.Errorf("model = %q, want gpt-4o-mini", body.Model)
				}
			case "custom with key", "custom without key":
				if body.Model != "my-model" {
					t.Errorf("model = %q, want my-model", body.Model)
				}
			case "ollama native", "ollama via /v1 is OpenAI compatible":
				if body.Model != "llama3.1" {
					t.Errorf("model = %q, want llama3.1", body.Model)
				}
			}

			switch tc.wantPath {
			case "/api/chat":
				if body.Options != nil {
					t.Errorf("native ollama Chat must not send options: %s", got.Body)
				}
				if reply != "native reply" {
					t.Errorf("reply = %q, want %q", reply, "native reply")
				}
			default:
				if reply != "hello there" {
					t.Errorf("reply = %q, want %q", reply, "hello there")
				}
			}
		})
	}
}

// ---- streaming ------------------------------------------------------------

func TestStreamOpenAICompatibleSplitEventsAndEscapes(t *testing.T) {
	// The full payload carries real newlines, a quote and multi-byte runes. json
	// Marshal percent-encodes none of that (SetEscapeHTML(false)) and produces
	// the two-character \n escape on the wire.
	full := "Hello \nworld\n\"quoted\" éé\nend"
	event, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"delta": map[string]any{"content": full}}},
	})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if !strings.Contains(string(event), `\n`) {
		t.Fatalf("fixture does not contain an escaped newline: %s", event)
	}
	// Split the single event across two writes inside the escaped newline (the
	// wire bytes are `\` then `n`), so neither write holds a complete event and
	// the second write begins with the escape's tail.
	whole := "data: " + string(event) + "\n\n"
	split := strings.Index(whole, `\n`) + 1
	if split <= 0 || split >= len(whole) {
		t.Fatalf("cannot split fixture %q", whole)
	}
	part1 := whole[:split]
	part2 := whole[split:]

	var mu sync.Mutex
	var requestBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		requestBody = raw
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("fake server does not support flushing")
			return
		}
		// A keep-alive comment and a blank line before the first event.
		fmt.Fprint(w, ": ping\n\n")
		fmt.Fprint(w, part1)
		flusher.Flush()
		fmt.Fprint(w, part2)
		flusher.Flush()
		fmt.Fprint(w, "[DONE]")
		flusher.Flush()
	}))
	defer srv.Close()

	client, err := aiproviders.New(aiproviders.Settings{Provider: "openai", APIKey: testKey, BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var deltas []string
	err = client.Stream(ctxT(t), []aiproviders.Message{{Role: aiproviders.RoleUser, Content: "hi"}}, func(delta string) error {
		deltas = append(deltas, delta)
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if len(deltas) != 1 {
		t.Fatalf("deltas = %q (%d), want 1 chunk", deltas, len(deltas))
	}
	if deltas[0] != full {
		t.Errorf("delta = %q, want %q", deltas[0], full)
	}

	mu.Lock()
	defer mu.Unlock()
	var body struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(requestBody, &body); err != nil {
		t.Fatalf("decode request body %s: %v", requestBody, err)
	}
	if !body.Stream {
		t.Errorf("Stream sent stream=false: %s", requestBody)
	}
}

func TestStreamOpenAIMultipleChunksAndDataPrefixTolerance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		// A role-only delta (no content) must be skipped, not emitted as "".
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
		fmt.Fprint(w, "data:{\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n") // no space after data:
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"b\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client, err := aiproviders.New(aiproviders.Settings{Provider: "custom", BaseURL: srv.URL, Model: "m"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var got []string
	if err := client.Stream(ctxT(t), []aiproviders.Message{{Role: aiproviders.RoleUser, Content: "hi"}}, func(d string) error {
		got = append(got, d)
		return nil
	}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if strings.Join(got, "") != "ab" {
		t.Errorf("deltas = %q, want [a b]", got)
	}
}

func TestStreamOpenAIStopsOnCallbackError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, c := range []string{"one", "two", "three"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client, err := aiproviders.New(aiproviders.Settings{Provider: "openai", APIKey: testKey, BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	stop := errors.New("stop now")
	var seen []string
	err = client.Stream(ctxT(t), []aiproviders.Message{{Role: aiproviders.RoleUser, Content: "hi"}}, func(d string) error {
		seen = append(seen, d)
		if len(seen) == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("Stream error = %v, want %v", err, stop)
	}
	if len(seen) != 2 {
		t.Errorf("callback ran %d times, want 2", len(seen))
	}
}

func TestStreamHonoursContextCancellation(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
		w.(http.Flusher).Flush()
		// Hold the connection open until the test is done with it.
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	client, err := aiproviders.New(aiproviders.Settings{Provider: "openai", APIKey: testKey, BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var got []string
	done := make(chan error, 1)
	go func() {
		done <- client.Stream(ctx, []aiproviders.Message{{Role: aiproviders.RoleUser, Content: "hi"}}, func(d string) error {
			got = append(got, d)
			cancel() // cancel from inside the stream
			return nil
		})
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Stream error = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stream did not return promptly after cancellation")
	}
	if len(got) != 1 || got[0] != "first" {
		t.Errorf("deltas = %q, want [first]", got)
	}
}

func TestStreamNativeOllama(t *testing.T) {
	var mu sync.Mutex
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %q, want /api/chat", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("native ollama must not send Authorization, got %q", auth)
		}
		var decoded map[string]any
		_ = json.NewDecoder(r.Body).Decode(&decoded)
		mu.Lock()
		body = decoded
		mu.Unlock()

		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, `{"model":"llama3.1","message":{"role":"assistant","content":"Hel"},"done":false}`+"\n")
		flusher.Flush()
		// Split a line in the middle of a compressed-looking JSON object.
		fmt.Fprint(w, `{"model":"llama3.1","message":{"role":"assistant",`)
		flusher.Flush()
		fmt.Fprint(w, `"content":"lo\nworld"},"done":false}`+"\n")
		flusher.Flush()
		fmt.Fprint(w, `{"model":"llama3.1","message":{"role":"assistant","content":""},"done":true,"total_duration":1}`+"\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client, err := aiproviders.New(aiproviders.Settings{Provider: "ollama", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var deltas []string
	if err := client.Stream(ctxT(t), []aiproviders.Message{{Role: aiproviders.RoleUser, Content: "hi"}}, func(d string) error {
		deltas = append(deltas, d)
		return nil
	}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if len(deltas) != 2 || deltas[0] != "Hel" || deltas[1] != "lo\nworld" {
		t.Fatalf("deltas = %q, want [Hel, lo\\nworld]", deltas)
	}

	mu.Lock()
	defer mu.Unlock()
	if body["stream"] != true {
		t.Errorf("stream = %v, want true", body["stream"])
	}
	if body["model"] != "llama3.1" {
		t.Errorf("model = %v, want llama3.1", body["model"])
	}
}

func TestStreamLineLongerThanDefaultScannerBuffer(t *testing.T) {
	// 200KB of payload in one SSE event: larger than bufio's 64KB default.
	big := strings.Repeat("x", 200*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", big)
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	client, err := aiproviders.New(aiproviders.Settings{Provider: "openai", APIKey: testKey, BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var total int
	if err := client.Stream(ctxT(t), []aiproviders.Message{{Role: aiproviders.RoleUser, Content: "hi"}}, func(d string) error {
		total += len(d)
		return nil
	}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if total != len(big) {
		t.Errorf("received %d bytes, want %d", total, len(big))
	}
}

// ---- error paths ----------------------------------------------------------

func TestNon2xxErrorsNeverLeakKey(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		path     string
		status   int
		body     string
		key      string
	}{
		{
			name:     "openai 401 echoes the key",
			provider: "openai",
			path:     "/chat/completions",
			status:   http.StatusUnauthorized,
			body:     `{"error":{"message":"Incorrect API key provided: ` + testKey + `","type":"invalid_request_error"}}`,
			key:      testKey,
		},
		{
			name:     "deepseek 500",
			provider: "deepseek",
			path:     "/chat/completions",
			status:   http.StatusInternalServerError,
			body:     "internal server error " + testKey,
			key:      testKey,
		},
		{
			name:     "custom 401 with header echo",
			provider: "custom",
			path:     "/chat/completions",
			status:   http.StatusUnauthorized,
			body:     "bad Authorization: Bearer " + testKey,
			key:      testKey,
		},
		{
			name:     "native ollama 500",
			provider: "ollama",
			path:     "/api/chat",
			status:   http.StatusInternalServerError,
			body:     `{"error":"model not found"}`,
			key:      "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Errorf("path = %q, want %q", r.URL.Path, tc.path)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()

			settings := aiproviders.Settings{Provider: tc.provider, APIKey: tc.key, BaseURL: srv.URL}
			if tc.provider == "custom" {
				settings.Model = "my-model"
			}
			client, err := aiproviders.New(settings)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if client.Name() != tc.provider {
				t.Errorf("Name() = %q, want %q", client.Name(), tc.provider)
			}

			msgs := []aiproviders.Message{{Role: aiproviders.RoleUser, Content: "hi"}}

			_, err = client.Chat(ctxT(t), msgs)
			if err == nil {
				t.Fatal("Chat succeeded on a non-2xx response")
			}
			wantPrefix := fmt.Sprintf("%s status %d:", tc.provider, tc.status)
			if !strings.HasPrefix(err.Error(), wantPrefix) {
				t.Errorf("Chat error = %q, want prefix %q", err, wantPrefix)
			}
			if tc.key != "" && strings.Contains(err.Error(), tc.key) {
				t.Errorf("Chat error leaked the API key: %q", err)
			}

			err = client.Stream(ctxT(t), msgs, func(string) error { return nil })
			if err == nil {
				t.Fatal("Stream succeeded on a non-2xx response")
			}
			if !strings.HasPrefix(err.Error(), wantPrefix) {
				t.Errorf("Stream error = %q, want prefix %q", err, wantPrefix)
			}
			if tc.key != "" && strings.Contains(err.Error(), tc.key) {
				t.Errorf("Stream error leaked the API key: %q", err)
			}
		})
	}
}

func TestChatEmptySuccessIsAnError(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		path     string
		response string
	}{
		{
			name:     "openai null content",
			provider: "openai",
			path:     "/chat/completions",
			response: `{"choices":[{"message":{"role":"assistant","content":null},"finish_reason":"stop"}]}`,
		},
		{
			name:     "openai empty string content",
			provider: "openai",
			path:     "/chat/completions",
			response: `{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"length"}]}`,
		},
		{
			name:     "openai no choices",
			provider: "openai",
			path:     "/chat/completions",
			response: `{"id":"c1","choices":[]}`,
		},
		{
			name:     "openai missing choices field",
			provider: "openai",
			path:     "/chat/completions",
			response: `{"id":"c1","object":"chat.completion"}`,
		},
		{
			name:     "custom empty content",
			provider: "custom",
			path:     "/chat/completions",
			response: `{"choices":[{"message":{"content":"   "},"finish_reason":"stop"}]}`,
		},
		{
			name:     "ollama empty content",
			provider: "ollama",
			path:     "/api/chat",
			response: `{"model":"llama3.1","message":{"role":"assistant","content":""},"done":true}`,
		},
		{
			name:     "ollama missing message",
			provider: "ollama",
			path:     "/api/chat",
			response: `{"model":"llama3.1","done":true}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.response)
			}))
			defer srv.Close()

			settings := aiproviders.Settings{Provider: tc.provider, APIKey: testKey, BaseURL: srv.URL}
			if tc.provider == "custom" {
				settings.Model = "my-model"
			}
			client, err := aiproviders.New(settings)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			reply, err := client.Chat(ctxT(t), []aiproviders.Message{{Role: aiproviders.RoleUser, Content: "hi"}})
			if err == nil {
				t.Fatalf("Chat returned %q with no error, want an error", reply)
			}
			if reply != "" {
				t.Errorf("Chat returned %q alongside %v, want empty", reply, err)
			}
		})
	}
}

func TestChatMalformedJSONIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices": [ this is not json`)
	}))
	defer srv.Close()

	client, err := aiproviders.New(aiproviders.Settings{Provider: "openai", APIKey: testKey, BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Chat(ctxT(t), []aiproviders.Message{{Role: aiproviders.RoleUser, Content: "hi"}}); err == nil {
		t.Fatal("Chat accepted a malformed response")
	}
}

func TestChatRejectsUnknownRole(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		writeJSON(w, "unexpected")
	}))
	defer srv.Close()

	client, err := aiproviders.New(aiproviders.Settings{Provider: "openai", APIKey: testKey, BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Chat(ctxT(t), []aiproviders.Message{{Role: "tool", Content: "x"}}); err == nil {
		t.Fatal("Chat accepted an unsupported role")
	}
	if hits != 0 {
		t.Errorf("server was called %d times for an invalid message", hits)
	}
}

func TestStreamStatusCodeErrorBeforeBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "upstream down")
	}))
	defer srv.Close()

	client, err := aiproviders.New(aiproviders.Settings{Provider: "openai", APIKey: testKey, BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = client.Stream(ctxT(t), []aiproviders.Message{{Role: aiproviders.RoleUser, Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("Stream succeeded on 503")
	}
	if !strings.Contains(err.Error(), "status 503") || !strings.Contains(err.Error(), "upstream down") {
		t.Errorf("Stream error = %q, want status and body snippet", err)
	}
}

func TestStreamWithNilCallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ignored\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()

	client, err := aiproviders.New(aiproviders.Settings{Provider: "openai", APIKey: testKey, BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Stream(ctxT(t), []aiproviders.Message{{Role: aiproviders.RoleUser, Content: "hi"}}, nil); err != nil {
		t.Fatalf("Stream with nil callback: %v", err)
	}
}

func TestStreamServerClosedWithoutDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		w.(http.Flusher).Flush()
		// Return without sending [DONE]: EOF is a clean end of stream.
	}))
	defer srv.Close()

	client, err := aiproviders.New(aiproviders.Settings{Provider: "openai", APIKey: testKey, BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var got string
	if err := client.Stream(ctxT(t), []aiproviders.Message{{Role: aiproviders.RoleUser, Content: "hi"}}, func(d string) error {
		got += d
		return nil
	}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got != "partial" {
		t.Errorf("got %q, want partial", got)
	}
}

// ---- New / Defaults / KnownProviders --------------------------------------

func TestNewValidation(t *testing.T) {
	tests := []struct {
		name    string
		in      aiproviders.Settings
		wantErr error
	}{
		{name: "unknown provider", in: aiproviders.Settings{Provider: "gemini"}, wantErr: aiproviders.ErrUnknownProvider},
		{name: "empty provider", in: aiproviders.Settings{}, wantErr: aiproviders.ErrUnknownProvider},
		{name: "whitespace provider", in: aiproviders.Settings{Provider: "   "}, wantErr: aiproviders.ErrUnknownProvider},
		{name: "deepseek without key", in: aiproviders.Settings{Provider: "deepseek"}, wantErr: aiproviders.ErrNoAPIKey},
		{name: "openai without key", in: aiproviders.Settings{Provider: "openai"}, wantErr: aiproviders.ErrNoAPIKey},
		{name: "deepseek blank key", in: aiproviders.Settings{Provider: "deepseek", APIKey: "  "}, wantErr: aiproviders.ErrNoAPIKey},
		{name: "custom without base url", in: aiproviders.Settings{Provider: "custom", Model: "m"}, wantErr: aiproviders.ErrBadConfig},
		{name: "custom without model", in: aiproviders.Settings{Provider: "custom", BaseURL: "http://localhost:1234/v1"}, wantErr: aiproviders.ErrBadConfig},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, err := aiproviders.New(tc.in)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("New error = %v, want %v", err, tc.wantErr)
			}
			if client != nil {
				t.Errorf("New returned a client alongside %v", err)
			}
		})
	}
}

func TestNewLenientProviderInput(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"deepseek", "deepseek"},
		{"  DeepSeek  ", "deepseek"},
		{"OPENAI", "openai"},
		{" OpenAI\t", "openai"},
		{"Ollama", "ollama"},
		{"CUSTOM", "custom"},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			in := aiproviders.Settings{Provider: tc.raw, APIKey: "k"}
			if tc.want == "custom" {
				in.BaseURL = "http://localhost:1234/v1"
				in.Model = "m"
			}
			client, err := aiproviders.New(in)
			if err != nil {
				t.Fatalf("New(%q): %v", tc.raw, err)
			}
			if client.Name() != tc.want {
				t.Errorf("Name() = %q, want %q", client.Name(), tc.want)
			}
		})
	}
}

func TestNewOllamaNeedsNoKey(t *testing.T) {
	client, err := aiproviders.New(aiproviders.Settings{Provider: "ollama"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if client.Name() != "ollama" {
		t.Errorf("Name() = %q, want ollama", client.Name())
	}
}

func TestNewTrimsSettings(t *testing.T) {
	client, err := aiproviders.New(aiproviders.Settings{
		Provider: " custom ",
		APIKey:   "  key-1234  ",
		BaseURL:  " http://example.invalid/v1 ",
		Model:    " my-model ",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if client.Name() != "custom" {
		t.Errorf("Name() = %q, want custom", client.Name())
	}
}

func TestDefaults(t *testing.T) {
	tests := []struct {
		provider string
		baseURL  string
		model    string
	}{
		{"deepseek", "https://api.deepseek.com/v1", "deepseek-chat"},
		{"openai", "https://api.openai.com/v1", "gpt-4o-mini"},
		{"ollama", "http://localhost:11434", "llama3.1"},
		{"custom", "", ""}, // no built-in endpoint or model
	}
	for _, tc := range tests {
		t.Run(tc.provider, func(t *testing.T) {
			got := aiproviders.Defaults(tc.provider)
			if got.Provider != tc.provider {
				t.Errorf("Provider = %q, want %q", got.Provider, tc.provider)
			}
			if got.BaseURL != tc.baseURL {
				t.Errorf("BaseURL = %q, want %q", got.BaseURL, tc.baseURL)
			}
			if got.Model != tc.model {
				t.Errorf("Model = %q, want %q", got.Model, tc.model)
			}
			if got.APIKey != "" {
				t.Errorf("APIKey = %q, want empty", got.APIKey)
			}
		})
	}
}

func TestKnownProviders(t *testing.T) {
	got := aiproviders.KnownProviders()
	want := []string{"deepseek", "openai", "custom", "ollama"}
	if len(got) != len(want) {
		t.Fatalf("KnownProviders() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("KnownProviders() = %v, want %v", got, want)
		}
	}

	// The returned slice is a copy: mutating it must not affect the next call.
	got[0] = "mutated"
	if again := aiproviders.KnownProviders(); again[0] != "deepseek" {
		t.Fatalf("KnownProviders() returned shared state: %v", again)
	}
}

func TestSentinelsAreDistinct(t *testing.T) {
	for _, err := range []error{aiproviders.ErrUnknownProvider, aiproviders.ErrNoAPIKey, aiproviders.ErrBadConfig} {
		if err == nil || err.Error() == "" {
			t.Fatalf("sentinel %v is not a usable error", err)
		}
	}
	if aiproviders.ErrUnknownProvider == aiproviders.ErrNoAPIKey ||
		aiproviders.ErrNoAPIKey == aiproviders.ErrBadConfig ||
		aiproviders.ErrUnknownProvider == aiproviders.ErrBadConfig {
		t.Fatal("sentinel errors are not distinct")
	}
	if aiproviders.ErrUnknownProvider.Error() != "unknown AI provider" {
		t.Errorf("ErrUnknownProvider = %q", aiproviders.ErrUnknownProvider)
	}
	if aiproviders.ErrNoAPIKey.Error() != "API key is required for this provider" {
		t.Errorf("ErrNoAPIKey = %q", aiproviders.ErrNoAPIKey)
	}
	if aiproviders.ErrBadConfig.Error() != "incomplete AI configuration" {
		t.Errorf("ErrBadConfig = %q", aiproviders.ErrBadConfig)
	}
}

func TestRoleConstants(t *testing.T) {
	if aiproviders.RoleSystem != "system" || aiproviders.RoleUser != "user" || aiproviders.RoleAssistant != "assistant" {
		t.Fatalf("roles = %q %q %q", aiproviders.RoleSystem, aiproviders.RoleUser, aiproviders.RoleAssistant)
	}
}

func TestMessageJSONShape(t *testing.T) {
	raw, err := json.Marshal(aiproviders.Message{Role: aiproviders.RoleUser, Content: "hi"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != `{"role":"user","content":"hi"}` {
		t.Errorf("Message JSON = %s", raw)
	}
}

// ---- TestConnection -------------------------------------------------------

func TestTestConnectionSuccess(t *testing.T) {
	var mu sync.Mutex
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer "+testKey {
			t.Errorf("Authorization = %q", auth)
		}
		var decoded map[string]any
		_ = json.NewDecoder(r.Body).Decode(&decoded)
		mu.Lock()
		body = decoded
		mu.Unlock()
		writeJSON(w, "OK")
	}))
	defer srv.Close()

	desc, err := aiproviders.TestConnection(ctxT(t), aiproviders.Settings{Provider: "openai", APIKey: testKey, BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	if desc != "openai: gpt-4o-mini OK" {
		t.Errorf("description = %q, want %q", desc, "openai: gpt-4o-mini OK")
	}

	mu.Lock()
	defer mu.Unlock()
	mt, ok := body["max_tokens"]
	if !ok {
		t.Fatalf("probe did not send max_tokens: %v", body)
	}
	if n, _ := mt.(float64); n != 1 {
		t.Errorf("max_tokens = %v, want 1", mt)
	}
	if body["stream"] != false {
		t.Errorf("probe stream = %v, want false", body["stream"])
	}
}

func TestTestConnectionNativeOllamaUsesNumPredict(t *testing.T) {
	var mu sync.Mutex
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var decoded map[string]any
		_ = json.NewDecoder(r.Body).Decode(&decoded)
		mu.Lock()
		body = decoded
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprint(w, `{"message":{"role":"assistant","content":"ok"},"done":true}`+"\n")
	}))
	defer srv.Close()

	desc, err := aiproviders.TestConnection(ctxT(t), aiproviders.Settings{Provider: "ollama", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	if desc != "ollama: llama3.1 OK" {
		t.Errorf("description = %q, want %q", desc, "ollama: llama3.1 OK")
	}

	mu.Lock()
	defer mu.Unlock()
	opts, ok := body["options"].(map[string]any)
	if !ok {
		t.Fatalf("probe sent no options: %v", body)
	}
	if n, _ := opts["num_predict"].(float64); n != 1 {
		t.Errorf("num_predict = %v, want 1", opts["num_predict"])
	}
}

func TestTestConnectionFailure(t *testing.T) {
	t.Run("http error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"error":{"message":"bad key %s"}}`, testKey)
		}))
		defer srv.Close()

		desc, err := aiproviders.TestConnection(ctxT(t), aiproviders.Settings{Provider: "deepseek", APIKey: testKey, BaseURL: srv.URL})
		if err == nil {
			t.Fatalf("TestConnection returned %q and no error", desc)
		}
		if desc != "" {
			t.Errorf("description = %q, want empty on failure", desc)
		}
		if !strings.Contains(err.Error(), "status 401") {
			t.Errorf("error = %q, want a status 401 message", err)
		}
		if strings.Contains(err.Error(), testKey) {
			t.Errorf("error leaked the API key: %q", err)
		}
	})

	t.Run("unreachable endpoint", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := srv.URL
		srv.Close() // nothing is listening any more

		_, err := aiproviders.TestConnection(ctxT(t), aiproviders.Settings{Provider: "openai", APIKey: testKey, BaseURL: url})
		if err == nil {
			t.Fatal("TestConnection succeeded against a dead endpoint")
		}
		if strings.Contains(err.Error(), testKey) {
			t.Errorf("error leaked the API key: %q", err)
		}
	})

	t.Run("invalid settings", func(t *testing.T) {
		if _, err := aiproviders.TestConnection(ctxT(t), aiproviders.Settings{Provider: "nope"}); !errors.Is(err, aiproviders.ErrUnknownProvider) {
			t.Fatalf("error = %v, want ErrUnknownProvider", err)
		}
	})
}

// ---- Store ----------------------------------------------------------------

func TestStoreRoundTrip(t *testing.T) {
	clearAIEnv(t)
	path := filepath.Join(t.TempDir(), "nested", "ai_settings.json")
	store := aiproviders.NewStore(path)

	in := aiproviders.Settings{Provider: "openai", APIKey: testKey, Model: "gpt-4o"}
	if err := store.Set(in); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := store.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// BaseURL was empty on input, so the provider default is persisted.
	want := aiproviders.Settings{
		Provider: "openai",
		APIKey:   testKey,
		BaseURL:  "https://api.openai.com/v1",
		Model:    "gpt-4o",
	}
	if got != want {
		t.Errorf("Get() = %+v, want %+v", got, want)
	}

	// The file must be valid JSON with the documented keys.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings file: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("settings file is not valid JSON: %v (%s)", err, raw)
	}
	for _, key := range []string{"provider", "api_key", "base_url", "model"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("settings file is missing %q: %s", key, raw)
		}
	}

	// Set again: the rename must replace the existing file cleanly.
	if err := store.Set(aiproviders.Settings{Provider: "deepseek", APIKey: "second-key-9876"}); err != nil {
		t.Fatalf("Set (replace): %v", err)
	}
	got, err = store.Get()
	if err != nil {
		t.Fatalf("Get after replace: %v", err)
	}
	if got.Provider != "deepseek" || got.Model != "deepseek-chat" || got.APIKey != "second-key-9876" {
		t.Errorf("Get() after replace = %+v", got)
	}

	// No temporary litter left behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory contains %v, want only %s", names, filepath.Base(path))
	}
}

func TestStoreSetValidates(t *testing.T) {
	clearAIEnv(t)
	dir := t.TempDir()

	tests := []struct {
		name    string
		in      aiproviders.Settings
		wantErr error
	}{
		{name: "unknown provider", in: aiproviders.Settings{Provider: "bogus"}, wantErr: aiproviders.ErrUnknownProvider},
		{name: "missing key", in: aiproviders.Settings{Provider: "openai"}, wantErr: aiproviders.ErrNoAPIKey},
		{name: "custom missing base", in: aiproviders.Settings{Provider: "custom", Model: "m"}, wantErr: aiproviders.ErrBadConfig},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".json")
			err := aiproviders.NewStore(path).Set(tc.in)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Set error = %v, want %v", err, tc.wantErr)
			}
			if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("Set wrote %s despite the error (stat err = %v)", path, statErr)
			}
		})
	}
}

func TestStoreGetMissingFileReturnsDefaults(t *testing.T) {
	clearAIEnv(t)
	store := aiproviders.NewStore(filepath.Join(t.TempDir(), "does-not-exist.json"))

	got, err := store.Get()
	if err != nil {
		t.Fatalf("Get on a missing file returned an error: %v", err)
	}
	want := aiproviders.Settings{
		Provider: "deepseek",
		BaseURL:  "https://api.deepseek.com/v1",
		Model:    "deepseek-chat",
	}
	if got != want {
		t.Errorf("Get() = %+v, want %+v", got, want)
	}
}

func TestStoreGetEnvironmentFallbacks(t *testing.T) {
	t.Run("provider and key from the environment", func(t *testing.T) {
		clearAIEnv(t)
		t.Setenv("SHHGIT_AI_PROVIDER", "openai")
		t.Setenv("SHHGIT_AI_OPENAI_API_KEY", testKey) // provider-specific name

		got, err := aiproviders.NewStore(filepath.Join(t.TempDir(), "missing.json")).Get()
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		want := aiproviders.Settings{
			Provider: "openai",
			APIKey:   testKey,
			BaseURL:  "https://api.openai.com/v1",
			Model:    "gpt-4o-mini",
		}
		if got != want {
			t.Errorf("Get() = %+v, want %+v", got, want)
		}
	})

	t.Run("well-known vendor variables are aliases", func(t *testing.T) {
		for _, tc := range []struct {
			provider string
			alias    string
		}{
			{provider: "deepseek", alias: "DEEPSEEK_API_KEY"},
			{provider: "openai", alias: "OPENAI_API_KEY"},
		} {
			t.Run(tc.alias, func(t *testing.T) {
				clearAIEnv(t)
				t.Setenv("SHHGIT_AI_PROVIDER", tc.provider)
				t.Setenv(tc.alias, testKey)

				got, err := aiproviders.NewStore(filepath.Join(t.TempDir(), "missing.json")).Get()
				if err != nil {
					t.Fatalf("Get: %v", err)
				}
				if got.APIKey != testKey {
					t.Errorf("APIKey = %q, want the %s alias value", got.APIKey, tc.alias)
				}
			})
		}
	})

	t.Run("explicit shhgit names beat vendor aliases", func(t *testing.T) {
		clearAIEnv(t)
		t.Setenv("SHHGIT_AI_PROVIDER", "openai")
		t.Setenv("SHHGIT_AI_OPENAI_API_KEY", "explicit-key-0000")
		t.Setenv("OPENAI_API_KEY", "ambient-key-9999")

		got, err := aiproviders.NewStore(filepath.Join(t.TempDir(), "missing.json")).Get()
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.APIKey != "explicit-key-0000" {
			t.Errorf("APIKey = %q, want explicit-key-0000", got.APIKey)
		}
	})

	t.Run("generic variables win over provider specific ones", func(t *testing.T) {
		clearAIEnv(t)
		t.Setenv("SHHGIT_AI_PROVIDER", "deepseek")
		t.Setenv("SHHGIT_AI_API_KEY", "generic-key-1111")
		t.Setenv("SHHGIT_AI_DEEPSEEK_API_KEY", "specific-key-2222")
		t.Setenv("SHHGIT_AI_MODEL", "generic-model")
		t.Setenv("SHHGIT_AI_DEEPSEEK_MODEL", "specific-model")
		t.Setenv("SHHGIT_AI_BASE_URL", "https://generic.example/v1")
		t.Setenv("SHHGIT_AI_DEEPSEEK_BASE_URL", "https://specific.example/v1")

		got, err := aiproviders.NewStore(filepath.Join(t.TempDir(), "missing.json")).Get()
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.APIKey != "generic-key-1111" {
			t.Errorf("APIKey = %q, want generic-key-1111", got.APIKey)
		}
		if got.Model != "generic-model" {
			t.Errorf("Model = %q, want generic-model", got.Model)
		}
		if got.BaseURL != "https://generic.example/v1" {
			t.Errorf("BaseURL = %q, want https://generic.example/v1", got.BaseURL)
		}
	})

	t.Run("provider specific variables used when no generic one is set", func(t *testing.T) {
		clearAIEnv(t)
		t.Setenv("SHHGIT_AI_PROVIDER", "ollama")
		t.Setenv("SHHGIT_AI_OLLAMA_BASE_URL", "http://127.0.0.1:12345/v1")
		t.Setenv("SHHGIT_AI_OLLAMA_MODEL", "qwen2.5")

		got, err := aiproviders.NewStore(filepath.Join(t.TempDir(), "missing.json")).Get()
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		want := aiproviders.Settings{
			Provider: "ollama",
			BaseURL:  "http://127.0.0.1:12345/v1",
			Model:    "qwen2.5",
		}
		if got != want {
			t.Errorf("Get() = %+v, want %+v", got, want)
		}
	})

	t.Run("file values win over the environment", func(t *testing.T) {
		clearAIEnv(t)
		t.Setenv("SHHGIT_AI_API_KEY", "env-key-3333")
		t.Setenv("SHHGIT_AI_MODEL", "env-model")
		t.Setenv("SHHGIT_AI_BASE_URL", "https://env.example/v1")

		path := filepath.Join(t.TempDir(), "saved.json")
		store := aiproviders.NewStore(path)
		if err := store.Set(aiproviders.Settings{Provider: "deepseek", APIKey: "file-key-4444", Model: "file-model"}); err != nil {
			t.Fatalf("Set: %v", err)
		}

		got, err := store.Get()
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.APIKey != "file-key-4444" || got.Model != "file-model" || got.BaseURL != "https://api.deepseek.com/v1" {
			t.Errorf("Get() = %+v, want the persisted values", got)
		}
	})

	t.Run("environment fills only the empty fields", func(t *testing.T) {
		clearAIEnv(t)
		t.Setenv("SHHGIT_AI_MODEL", "env-only-model")

		path := filepath.Join(t.TempDir(), "partial.json")
		if err := os.WriteFile(path, []byte(`{"provider":"custom","base_url":"http://localhost:9999/v1","api_key":""}`), 0o600); err != nil {
			t.Fatalf("seed settings file: %v", err)
		}
		t.Setenv("SHHGIT_AI_CUSTOM_API_KEY", "custom-env-key-5555")

		got, err := aiproviders.NewStore(path).Get()
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		want := aiproviders.Settings{
			Provider: "custom",
			APIKey:   "custom-env-key-5555",
			BaseURL:  "http://localhost:9999/v1",
			Model:    "env-only-model",
		}
		if got != want {
			t.Errorf("Get() = %+v, want %+v", got, want)
		}
	})

	t.Run("unknown provider in the file does not error", func(t *testing.T) {
		clearAIEnv(t)
		path := filepath.Join(t.TempDir(), "typo.json")
		if err := os.WriteFile(path, []byte(`{"provider":"opemai","api_key":"k-1234"}`), 0o600); err != nil {
			t.Fatalf("seed settings file: %v", err)
		}
		got, err := aiproviders.NewStore(path).Get()
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Provider != "opemai" {
			t.Errorf("Provider = %q, want the raw stored value", got.Provider)
		}
	})
}

func TestStoreGetCorruptFileReportsError(t *testing.T) {
	clearAIEnv(t)
	path := filepath.Join(t.TempDir(), "corrupt.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed settings file: %v", err)
	}
	if _, err := aiproviders.NewStore(path).Get(); err == nil {
		t.Fatal("Get accepted a corrupt settings file")
	}
}

func TestStoreFileMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not honour Unix mode bits, so 0600 cannot be asserted here")
	}
	clearAIEnv(t)
	path := filepath.Join(t.TempDir(), "ai.json")
	store := aiproviders.NewStore(path)
	if err := store.Set(aiproviders.Settings{Provider: "openai", APIKey: testKey}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}
}

func TestStoreEmptyPathFails(t *testing.T) {
	if err := aiproviders.NewStore("").Set(aiproviders.Settings{Provider: "ollama"}); !errors.Is(err, aiproviders.ErrBadConfig) {
		t.Fatalf("Set error = %v, want ErrBadConfig", err)
	}
}

func TestStoreConcurrentAccess(t *testing.T) {
	clearAIEnv(t)
	path := filepath.Join(t.TempDir(), "concurrent.json")
	store := aiproviders.NewStore(path)

	if err := store.Set(aiproviders.Settings{Provider: "openai", APIKey: testKey}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.Get(); err != nil {
				t.Errorf("concurrent Get: %v", err)
			}
		}()
	}
	wg.Wait()

	got, err := store.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.APIKey != testKey {
		t.Errorf("APIKey = %q, want %q", got.APIKey, testKey)
	}
}

// ---- helpers sanity -------------------------------------------------------

func TestJoinURLThroughPublicBehaviour(t *testing.T) {
	// Exercised indirectly, but assert the two documented shapes once so a
	// regression in URL building shows up as a routing failure, not a mystery.
	var paths []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		writeJSON(w, "ok")
	}))
	defer srv.Close()

	for _, base := range []string{srv.URL, srv.URL + "/", srv.URL + "/v1", srv.URL + "/v1/"} {
		client, err := aiproviders.New(aiproviders.Settings{Provider: "openai", APIKey: testKey, BaseURL: base})
		if err != nil {
			t.Fatalf("New(%q): %v", base, err)
		}
		if _, err := client.Chat(ctxT(t), []aiproviders.Message{{Role: aiproviders.RoleUser, Content: "hi"}}); err != nil {
			t.Fatalf("Chat with base %q: %v", base, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"/chat/completions", "/chat/completions", "/v1/chat/completions", "/v1/chat/completions"}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("paths[%d] = %q, want %q", i, paths[i], want[i])
		}
	}
}

// TestSSEParserToleratesChunkBoundaries drives the streaming parser through a
// raw connection so the read boundaries are under the test's control.
func TestSSEParserToleratesChunkBoundaries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		bw := bufio.NewWriter(w)
		fmt.Fprint(bw, "data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}\n")
		bw.Flush()
		fmt.Fprint(bw, "\n") // blank line arrives in a separate write
		bw.Flush()
		fmt.Fprint(bw, "data: {\"choices\":[{\"delta\":{\"content\":\"two\"}}]}\n\ndata: [DONE]\n\n")
		bw.Flush()
	}))
	defer srv.Close()

	client, err := aiproviders.New(aiproviders.Settings{Provider: "openai", APIKey: testKey, BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var got []string
	if err := client.Stream(ctxT(t), []aiproviders.Message{{Role: aiproviders.RoleUser, Content: "hi"}}, func(d string) error {
		got = append(got, d)
		return nil
	}); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if strings.Join(got, "|") != "one|two" {
		t.Errorf("deltas = %q, want [one two]", got)
	}
}
