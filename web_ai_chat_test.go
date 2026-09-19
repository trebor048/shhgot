package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eth0izzle/shhgit/core"
)

func TestDefaultAIChatConfig(t *testing.T) {
	cfg := defaultAIChatConfig()

	if cfg.Backend != "deepseek" {
		t.Fatalf("backend = %q, want deepseek", cfg.Backend)
	}
	if cfg.DeepseekModel != "deepseek-chat" {
		t.Fatalf("deepseek_model = %q, want deepseek-chat", cfg.DeepseekModel)
	}
	if cfg.OllamaURL != "http://localhost:11434" {
		t.Fatalf("ollama_url = %q, want http://localhost:11434", cfg.OllamaURL)
	}
	if cfg.OllamaModel == "" {
		t.Fatal("ollama_model should default to a non-empty model")
	}
	if cfg.DeepseekKey != "" {
		t.Fatalf("deepseek_key should default empty, got %q", cfg.DeepseekKey)
	}
	if !strings.Contains(cfg.SystemPrompt, "secret") || !strings.Contains(cfg.SystemPrompt, "file") {
		t.Fatal("system_prompt should instruct the model about the secret and the file")
	}
}

func TestMergeAIChatConfigNonEmptyWins(t *testing.T) {
	dst := defaultAIChatConfig()
	src := core.AIReviewConfig{
		Backend:       "ollama",
		DeepseekModel: "deepseek-reasoner",
		OllamaModel:   "llama3.1",
		SystemPrompt:  "custom prompt",
	}

	mergeAIChatConfig(&dst, src)

	if dst.Backend != "ollama" {
		t.Fatalf("backend = %q, want ollama", dst.Backend)
	}
	if dst.DeepseekModel != "deepseek-reasoner" {
		t.Fatalf("deepseek_model = %q, want deepseek-reasoner", dst.DeepseekModel)
	}
	if dst.OllamaModel != "llama3.1" {
		t.Fatalf("ollama_model = %q, want llama3.1", dst.OllamaModel)
	}
	if dst.SystemPrompt != "custom prompt" {
		t.Fatalf("system_prompt = %q, want custom prompt", dst.SystemPrompt)
	}
}

func TestMergeAIChatConfigEmptyPreserves(t *testing.T) {
	dst := defaultAIChatConfig()
	dst.DeepseekKey = "sk-secret"
	before := dst

	mergeAIChatConfig(&dst, core.AIReviewConfig{}) // all-empty source

	if dst.DeepseekKey != "sk-secret" {
		t.Fatalf("empty source wiped key: got %q", dst.DeepseekKey)
	}
	if dst.Backend != before.Backend || dst.DeepseekModel != before.DeepseekModel || dst.OllamaURL != before.OllamaURL {
		t.Fatal("empty source mutated non-empty destination fields")
	}
}

func TestSaveLoadChatConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat_config.json")
	want := defaultAIChatConfig()
	want.Backend = "ollama"
	want.DeepseekKey = "sk-test-123"
	want.SystemPrompt = "be brief"

	if err := saveChatConfig(path, want); err != nil {
		t.Fatalf("saveChatConfig: %v", err)
	}
	got, err := loadChatConfig(path)
	if err != nil {
		t.Fatalf("loadChatConfig: %v", err)
	}
	if got != want {
		t.Fatalf("round trip mismatch:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestLoadChatConfigMissingFile(t *testing.T) {
	_, err := loadChatConfig(filepath.Join(t.TempDir(), "nope.json"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}
}

func TestChatStoreAppendAndGet(t *testing.T) {
	s := newChatStore()

	if got := s.Get("missing"); len(got) != 0 {
		t.Fatalf("expected empty history for unknown id, got %v", got)
	}

	s.Append("m1", ChatMessage{Role: "user", Content: "hi"})
	s.Append("m1", ChatMessage{Role: "assistant", Content: "hello"})

	got := s.Get("m1")
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	if got[0].Role != "user" || got[0].Content != "hi" {
		t.Fatalf("first message wrong: %+v", got[0])
	}
	if got[1].Role != "assistant" || got[1].Content != "hello" {
		t.Fatalf("second message wrong: %+v", got[1])
	}
}

func TestChatStoreIsolation(t *testing.T) {
	s := newChatStore()
	s.Append("a", ChatMessage{Role: "user", Content: "one"})
	s.Append("b", ChatMessage{Role: "user", Content: "two"})

	if len(s.Get("a")) != 1 || len(s.Get("b")) != 1 {
		t.Fatal("sessions are not isolated by id")
	}
}

func TestChatStoreEnsureContextNoDuplicate(t *testing.T) {
	s := newChatStore()
	first := ChatMessage{Role: "user", Content: "context-1"}
	second := ChatMessage{Role: "user", Content: "context-2"}

	s.EnsureContext("m", first)
	s.EnsureContext("m", second)

	got := s.Get("m")
	if len(got) != 1 {
		t.Fatalf("expected exactly 1 context message, got %d: %+v", len(got), got)
	}
	if got[0].Content != "context-1" {
		t.Fatalf("context = %q, want the first one", got[0].Content)
	}
}

func TestBuildDeepSeekPayload(t *testing.T) {
	cfg := defaultAIChatConfig()
	cfg.SystemPrompt = "you are a helper"
	cfg.DeepseekModel = "deepseek-chat"

	url, body, err := buildDeepSeekPayload(cfg, []ChatMessage{{Role: "user", Content: "analyze"}})
	if err != nil {
		t.Fatalf("buildDeepSeekPayload: %v", err)
	}
	if url == "" {
		t.Fatal("empty url")
	}

	var payload struct {
		Model    string        `json:"model"`
		Stream   bool          `json:"stream"`
		Messages []ChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if payload.Model != "deepseek-chat" {
		t.Fatalf("model = %q", payload.Model)
	}
	if !payload.Stream {
		t.Fatal("stream should be true")
	}
	if len(payload.Messages) != 2 {
		t.Fatalf("expected system + user messages, got %d", len(payload.Messages))
	}
	if payload.Messages[0].Role != "system" || payload.Messages[0].Content != "you are a helper" {
		t.Fatalf("system message wrong: %+v", payload.Messages[0])
	}
	if payload.Messages[1].Role != "user" || payload.Messages[1].Content != "analyze" {
		t.Fatalf("user message wrong: %+v", payload.Messages[1])
	}
}

func TestBuildOllamaPayload(t *testing.T) {
	cfg := defaultAIChatConfig()
	cfg.OllamaURL = "http://localhost:11434"
	cfg.OllamaModel = "llama3.1"
	cfg.SystemPrompt = "sys"

	url, body, err := buildOllamaPayload(cfg, []ChatMessage{{Role: "user", Content: "x"}})
	if err != nil {
		t.Fatalf("buildOllamaPayload: %v", err)
	}
	if !strings.HasSuffix(url, "/api/chat") {
		t.Fatalf("url = %q, want suffix /api/chat", url)
	}

	var payload struct {
		Model    string        `json:"model"`
		Stream   bool          `json:"stream"`
		Messages []ChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if payload.Model != "llama3.1" || !payload.Stream {
		t.Fatalf("payload wrong: model=%q stream=%v", payload.Model, payload.Stream)
	}
	if len(payload.Messages) != 2 || payload.Messages[0].Role != "system" {
		t.Fatalf("expected system message prepended, got %+v", payload.Messages)
	}
}

func TestParseOpenAISSE(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n" +
		"data: [DONE]\n\n"

	var got strings.Builder
	err := parseOpenAISSE(strings.NewReader(body), func(d string) error {
		got.WriteString(d)
		return nil
	})
	if err != nil {
		t.Fatalf("parseOpenAISSE: %v", err)
	}
	if got.String() != "Hello" {
		t.Fatalf("deltas = %q, want Hello", got.String())
	}
}

func TestParseOpenAISSEIgnoresNonData(t *testing.T) {
	body := ": keep-alive\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"
	var got strings.Builder
	if err := parseOpenAISSE(strings.NewReader(body), func(d string) error {
		got.WriteString(d)
		return nil
	}); err != nil {
		t.Fatalf("parseOpenAISSE: %v", err)
	}
	if got.String() != "ok" {
		t.Fatalf("deltas = %q, want ok", got.String())
	}
}

func TestParseOllamaNDJSON(t *testing.T) {
	body := "{\"message\":{\"content\":\"Hel\"},\"done\":false}\n" +
		"{\"message\":{\"content\":\"lo\"},\"done\":false}\n" +
		"{\"message\":{\"content\":\"\"},\"done\":true}\n"

	var got strings.Builder
	err := parseOllamaNDJSON(strings.NewReader(body), func(d string) error {
		got.WriteString(d)
		return nil
	})
	if err != nil {
		t.Fatalf("parseOllamaNDJSON: %v", err)
	}
	if got.String() != "Hello" {
		t.Fatalf("deltas = %q, want Hello", got.String())
	}
}

func TestStreamDeepSeek(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"!\"}}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	oldURL := aiChatDeepSeekURL
	oldClient := aiChatHTTPClient
	aiChatDeepSeekURL = srv.URL
	aiChatHTTPClient = srv.Client()
	defer func() {
		aiChatDeepSeekURL = oldURL
		aiChatHTTPClient = oldClient
	}()

	cfg := defaultAIChatConfig()
	cfg.DeepseekKey = "sk-test"

	var got strings.Builder
	err := streamDeepSeek(context.Background(), cfg, []ChatMessage{{Role: "user", Content: "hi"}}, func(d string) error {
		got.WriteString(d)
		return nil
	})
	if err != nil {
		t.Fatalf("streamDeepSeek: %v", err)
	}
	if got.String() != "Hi!" {
		t.Fatalf("streamed = %q, want Hi!", got.String())
	}
}

func TestStreamOllama(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		io.WriteString(w, "{\"message\":{\"content\":\"Yo\"},\"done\":false}\n")
		io.WriteString(w, "{\"message\":{\"content\":\"!\"},\"done\":false}\n")
		io.WriteString(w, "{\"message\":{\"content\":\"\"},\"done\":true}\n")
	}))
	defer srv.Close()

	oldClient := aiChatHTTPClient
	aiChatHTTPClient = srv.Client()
	defer func() { aiChatHTTPClient = oldClient }()

	cfg := defaultAIChatConfig()
	cfg.Backend = "ollama"
	cfg.OllamaURL = srv.URL

	var got strings.Builder
	err := streamOllama(context.Background(), cfg, []ChatMessage{{Role: "user", Content: "hi"}}, func(d string) error {
		got.WriteString(d)
		return nil
	})
	if err != nil {
		t.Fatalf("streamOllama: %v", err)
	}
	if got.String() != "Yo!" {
		t.Fatalf("streamed = %q, want Yo!", got.String())
	}
}

func TestStreamAIChatRoutesToOllama(t *testing.T) {
	var ollamaHit bool
	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ollamaHit = true
		io.WriteString(w, "{\"message\":{\"content\":\"ok\"},\"done\":false}\n")
		io.WriteString(w, "{\"message\":{\"content\":\"\"},\"done\":true}\n")
	}))
	defer ollamaSrv.Close()

	deepseekSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("deepseek server should not be hit when backend=ollama")
	}))
	defer deepseekSrv.Close()

	oldURL := aiChatDeepSeekURL
	oldClient := aiChatHTTPClient
	aiChatDeepSeekURL = deepseekSrv.URL
	aiChatHTTPClient = ollamaSrv.Client()
	defer func() {
		aiChatDeepSeekURL = oldURL
		aiChatHTTPClient = oldClient
	}()

	cfg := defaultAIChatConfig()
	cfg.Backend = "ollama"
	cfg.OllamaURL = ollamaSrv.URL

	var got strings.Builder
	if err := streamAIChat(context.Background(), cfg, []ChatMessage{{Role: "user", Content: "hi"}}, func(d string) error {
		got.WriteString(d)
		return nil
	}); err != nil {
		t.Fatalf("streamAIChat: %v", err)
	}
	if !ollamaHit {
		t.Fatal("ollama server was not hit")
	}
	if got.String() != "ok" {
		t.Fatalf("streamed = %q, want ok", got.String())
	}
}

func TestBuildOpencodeArgs(t *testing.T) {
	args := buildOpencodeArgs("C:\\tmp\\repo")
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %v", args)
	}
	if args[0] != "-d" || args[1] != "C:\\tmp\\repo" || args[2] != "opencode" {
		t.Fatalf("args = %v", args)
	}
}

func TestSlugFromURLEdgeCases(t *testing.T) {
	for _, u := range []string{"", "/", "://", "git@"} {
		if got := slugFromURL(u); got != "" {
			t.Errorf("slugFromURL(%q) = %q, want empty", u, got)
		}
	}
}

func TestSafeWorkspaceDirRejectsEmptySlug(t *testing.T) {
	if _, err := safeWorkspaceDir("://"); err == nil {
		t.Fatal("expected error for empty slug")
	}
	dir, err := safeWorkspaceDir("https://github.com/octocat/hello-world.git")
	if err != nil {
		t.Fatalf("valid URL should resolve: %v", err)
	}
	if !strings.Contains(dir, "octocat-hello-world") {
		t.Fatalf("workspace dir %q should slugify owner-repo", dir)
	}
}

func TestDashboardEmbedsAIChat(t *testing.T) {
	html := getEmbeddedDashboard()

	for _, marker := range []string{
		"chat-win",
		".aibtn",
		"openChat",
		"streamReply",
		"aisettings-ovl",
		"ai-backend-deepseek",
		"ai-backend-ollama",
		"api/ai/review/stream",
		"api/ai/review/opencode",
		"margin-bottom:6px",
		"makeParticles",
		"i<48",
		"244,114,182",
	} {
		if !strings.Contains(html, marker) {
			t.Errorf("dashboard HTML missing %q", marker)
		}
	}

	// Validate the inline <script> syntax when node is available.
	start := strings.Index(html, "<script>")
	end := strings.Index(html, "</script>")
	if start < 0 || end < 0 || end <= start {
		t.Fatal("could not locate <script> block in dashboard HTML")
	}
	js := html[start+len("<script>") : end]

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available; skipping JS syntax check")
	}
	jsFile := filepath.Join(t.TempDir(), "dashboard.js")
	if err := os.WriteFile(jsFile, []byte(js), 0o644); err != nil {
		t.Fatalf("write dashboard.js: %v", err)
	}
	if out, err := exec.Command(node, "--check", jsFile).CombinedOutput(); err != nil {
		t.Fatalf("dashboard JS syntax invalid: %v\n%s", err, out)
	}
}

func TestAIReviewConfigGetDefaults(t *testing.T) {
	aiChat.setConfig(defaultAIChatConfig())

	rec := httptest.NewRecorder()
	aiReviewConfig(rec, httptest.NewRequest(http.MethodGet, "/api/ai/review/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var body struct {
		Config core.AIReviewConfig `json:"config"`
		HasKey bool                `json:"has_key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Config.Backend != "deepseek" {
		t.Fatalf("backend = %q, want deepseek", body.Config.Backend)
	}
	if body.Config.DeepseekKey != "" {
		t.Fatalf("key should be empty in GET response, got %q", body.Config.DeepseekKey)
	}
	if body.HasKey {
		t.Fatal("has_key should be false for default config")
	}
}

func TestAIReviewConfigPostPersistsAndOmitsKey(t *testing.T) {
	oldPath := aiChatConfigFile
	aiChatConfigFile = filepath.Join(t.TempDir(), "chat_config.json")
	defer func() { aiChatConfigFile = oldPath }()

	aiChat.setConfig(defaultAIChatConfig())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/ai/review/config",
		strings.NewReader(`{"backend":"ollama","deepseek_api_key":"sk-abc123456789","system_prompt":"be brief"}`))
	aiReviewConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Config core.AIReviewConfig `json:"config"`
		HasKey bool                `json:"has_key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Config.Backend != "ollama" {
		t.Fatalf("backend = %q, want ollama", resp.Config.Backend)
	}
	if !resp.HasKey {
		t.Fatal("has_key should be true after setting a key")
	}
	if resp.Config.DeepseekKey != "" {
		t.Fatalf("response must not echo the API key, got %q", resp.Config.DeepseekKey)
	}

	loaded, err := loadChatConfig(aiChatConfigFile)
	if err != nil {
		t.Fatalf("load persisted config: %v", err)
	}
	if loaded.Backend != "ollama" || loaded.DeepseekKey != "sk-abc123456789" || loaded.SystemPrompt != "be brief" {
		t.Fatalf("persisted config wrong: %+v", loaded)
	}
}

func TestAIReviewHistoryEmpty(t *testing.T) {
	rec := httptest.NewRecorder()
	aiReviewHistory(rec, httptest.NewRequest(http.MethodGet, "/api/ai/review/history?match_id=does-not-exist", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Messages []ChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Messages) != 0 {
		t.Fatalf("expected empty history, got %d messages", len(body.Messages))
	}
}

func TestAIReviewStreamSSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	oldURL := aiChatDeepSeekURL
	oldClient := aiChatHTTPClient
	aiChatDeepSeekURL = srv.URL
	aiChatHTTPClient = srv.Client()
	defer func() {
		aiChatDeepSeekURL = oldURL
		aiChatHTTPClient = oldClient
	}()

	cfg := defaultAIChatConfig()
	cfg.DeepseekKey = "sk-test"
	aiChat.setConfig(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/ai/review/stream",
		strings.NewReader(`{"match_id":"sse-test-1","url":"https://github.com/a/b","file":"x.js","secret":"sk-secret","signature":"OpenAI"}`))
	rec := httptest.NewRecorder()
	aiReviewStream(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"delta"`) {
		t.Fatalf("SSE missing delta event: %q", body)
	}
	if !strings.Contains(body, `"content":"Hel"`) || !strings.Contains(body, `"content":"lo"`) {
		t.Fatalf("SSE missing streamed content: %q", body)
	}
	if !strings.Contains(body, `"type":"done"`) {
		t.Fatalf("SSE missing done event: %q", body)
	}

	// The conversation should now be persisted for reopen.
	msgs := aiChat.store.Get("sse-test-1")
	if len(msgs) != 2 {
		t.Fatalf("expected 2 persisted messages, got %d", len(msgs))
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "Hello" {
		t.Fatalf("assistant message wrong: %+v", msgs[1])
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, h := range []string{"localhost", "localhost:11434", "127.0.0.1", "127.0.0.1:11434", "[::1]:11434", "::1"} {
		if !isLoopbackHost(h) {
			t.Errorf("isLoopbackHost(%q) = false, want true", h)
		}
	}
	for _, h := range []string{"evil.com", "evil.com:8080", "192.168.1.5", "10.0.0.1", "8.8.8.8"} {
		if isLoopbackHost(h) {
			t.Errorf("isLoopbackHost(%q) = true, want false", h)
		}
	}
}

func TestValidateOllamaURL(t *testing.T) {
	if err := validateOllamaURL("http://localhost:11434"); err != nil {
		t.Errorf("localhost should pass: %v", err)
	}
	if err := validateOllamaURL("http://127.0.0.1:11434"); err != nil {
		t.Errorf("127.0.0.1 should pass: %v", err)
	}
	if err := validateOllamaURL("http://evil.com:11434"); err == nil {
		t.Error("non-loopback host should fail")
	}
	if err := validateOllamaURL("ftp://localhost"); err == nil {
		t.Error("non-http scheme should fail")
	}
	if err := validateOllamaURL(""); err == nil {
		t.Error("empty url should fail")
	}
}

func TestAILocalGuard(t *testing.T) {
	hits := 0
	handler := aiLocalGuard(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	})

	// loopback host, no Origin (non-browser) -> allowed
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/ai/review/config", nil)
	req.Host = "127.0.0.1:8080"
	handler(rec, req)
	if rec.Code != http.StatusOK || hits != 1 {
		t.Fatalf("loopback+no-origin should pass: code=%d hits=%d", rec.Code, hits)
	}

	// non-loopback Host -> rejected (DNS-rebinding defense)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/ai/review/config", nil)
	req.Host = "evil.com"
	handler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback host should be forbidden, got %d", rec.Code)
	}

	// cross-origin browser request -> rejected
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/ai/review/config", nil)
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "http://evil.com")
	handler(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin should be forbidden, got %d", rec.Code)
	}

	// same-origin browser request -> allowed
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/ai/review/config", nil)
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Origin", "http://127.0.0.1:8080")
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("same-origin should pass, got %d", rec.Code)
	}
}

func TestAIReviewConfigPostRejectsNonLoopbackOllama(t *testing.T) {
	oldPath := aiChatConfigFile
	aiChatConfigFile = filepath.Join(t.TempDir(), "chat_config.json")
	defer func() { aiChatConfigFile = oldPath }()

	aiChat.setConfig(defaultAIChatConfig())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/ai/review/config",
		strings.NewReader(`{"backend":"ollama","ollama_url":"http://evil.com:11434"}`))
	aiReviewConfig(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if _, err := os.Stat(aiChatConfigFile); !os.IsNotExist(err) {
		t.Fatalf("config must not be persisted on invalid ollama_url, stat err=%v", err)
	}
}

func TestBeginEndStream(t *testing.T) {
	aiChat.endStream("guard-test") // reset any prior state
	if !aiChat.beginStream("guard-test") {
		t.Fatal("first beginStream should succeed")
	}
	if aiChat.beginStream("guard-test") {
		t.Fatal("concurrent beginStream should fail")
	}
	aiChat.endStream("guard-test")
	if !aiChat.beginStream("guard-test") {
		t.Fatal("beginStream after endStream should succeed")
	}
	aiChat.endStream("guard-test")
}

func TestAIReviewStreamConflict(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer mock.Close()

	oldClient := aiChatHTTPClient
	oldURL := aiChatDeepSeekURL
	aiChatHTTPClient = mock.Client()
	aiChatDeepSeekURL = mock.URL
	defer func() { aiChatHTTPClient = oldClient; aiChatDeepSeekURL = oldURL }()

	aiChat.setConfig(core.AIReviewConfig{Backend: "deepseek", DeepseekKey: "sk-test", DeepseekModel: "deepseek-chat", SystemPrompt: defaultSystemPrompt()})
	defer aiChat.setConfig(defaultAIChatConfig())

	body := `{"match_id":"conflict-1","url":"https://github.com/a/b","file":"x","secret":"s","signature":"t"}`

	done := make(chan struct{})
	go func() {
		defer close(done)
		rec := httptest.NewRecorder()
		aiReviewStream(rec, httptest.NewRequest(http.MethodPost, "/api/ai/review/stream", strings.NewReader(body)))
	}()

	<-entered // the first stream has begun and reached the provider

	rec2 := httptest.NewRecorder()
	aiReviewStream(rec2, httptest.NewRequest(http.MethodPost, "/api/ai/review/stream", strings.NewReader(body)))
	if rec2.Code != http.StatusConflict {
		t.Fatalf("expected 409 for concurrent stream, got %d", rec2.Code)
	}

	close(release)
	<-done
}
