package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/eth0izzle/shhgit/core"
)

// ChatMessage is one turn of the AI-review conversation. Role is "system",
// "user", or "assistant".
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

const (
	aiChatWorkspaceRoot  = "ai_review/workspaces"
	deepSeekCompletions  = "https://api.deepseek.com/chat/completions"
	defaultDeepSeekModel = "deepseek-chat"
	defaultOllamaURL     = "http://localhost:11434"
	defaultOllamaModel   = "hf.co/HauhauCS/Gemma-4-E4B-Uncensored-HauhauCS-Aggressive:Q5_K_M"

	// aiChatStreamTimeout bounds a single upstream completion so a hung
	// provider cannot pin a request forever. Client disconnects are cancelled
	// immediately via the request context, so this is only a safety net.
	aiChatStreamTimeout = 15 * time.Minute
)

// aiChatConfigFile is a var so tests can redirect the persisted runtime
// config to a temp file.
var aiChatConfigFile = "ai_review/chat_config.json"

// aiChatHTTPClient and aiChatDeepSeekURL are vars (not consts) so tests can
// point them at a local httptest server.
var (
	aiChatHTTPClient  = http.DefaultClient
	aiChatDeepSeekURL = deepSeekCompletions
)

// ---- runtime state -------------------------------------------------------

type aiChatRuntime struct {
	mu        sync.RWMutex
	config    core.AIReviewConfig
	store     *ChatStore
	streaming map[string]bool
}

var aiChat = &aiChatRuntime{store: newChatStore()}

func (r *aiChatRuntime) getConfig() core.AIReviewConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config
}

func (r *aiChatRuntime) setConfig(c core.AIReviewConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.config = c
}

// beginStream atomically marks a match id as streaming, returning false if a
// stream is already in flight for it (prevents two requests from interleaving
// turns into the same history). endStream releases the mark.
func (r *aiChatRuntime) beginStream(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.streaming == nil {
		r.streaming = make(map[string]bool)
	}
	if r.streaming[id] {
		return false
	}
	r.streaming[id] = true
	return true
}

func (r *aiChatRuntime) endStream(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.streaming, id)
}

// initAIChat seeds the runtime config from defaults, config.yaml, the
// DEEPSEEK_API_KEY env var, and any persisted chat_config.json (in that
// precedence order). Call once before the web server starts.
func initAIChat(cfg *core.Config) {
	effective := defaultAIChatConfig()
	if cfg != nil {
		mergeAIChatConfig(&effective, cfg.AIReview)
	}
	if key := os.Getenv("DEEPSEEK_API_KEY"); key != "" {
		effective.DeepseekKey = key
	}
	if persisted, err := loadChatConfig(aiChatConfigFile); err == nil {
		mergeAIChatConfig(&effective, persisted)
	}
	aiChat.setConfig(effective)
}

// ---- config --------------------------------------------------------------

func defaultSystemPrompt() string {
	return `You are a secrets-security analyst reviewing a detected credential in a code repository.
You will be given:
1. A secret value that was detected, and its signature (the type of credential).
2. The repository URL and the file path where it was found.
3. The full contents of that file.

Analyze and report, in clear markdown:
- What the secret is and which service or system it most likely authenticates to.
- What the file does and how the secret is used within it (its function).
- Every other secret, credential, key, token, or sensitive value present in the file.
- Any endpoints, hostnames, or URLs the file references.
- Whether the secret appears real and active, and the risk if it leaked.
- Concrete, specific remediation steps.

Be precise and reference specific lines. Do not fabricate details that are not present in the file.
When you finish, ask the user whether they want a deeper analysis of the surrounding repository (dependencies, related files, blast radius), and what they would like you to focus on.`
}

func defaultAIChatConfig() core.AIReviewConfig {
	return core.AIReviewConfig{
		Backend:       "deepseek",
		DeepseekModel: defaultDeepSeekModel,
		OllamaURL:     defaultOllamaURL,
		OllamaModel:   defaultOllamaModel,
		SystemPrompt:  defaultSystemPrompt(),
	}
}

// mergeAIChatConfig copies non-empty fields from src over dst. Empty fields
// in src leave dst untouched (so an empty API key never wipes an existing one).
func mergeAIChatConfig(dst *core.AIReviewConfig, src core.AIReviewConfig) {
	if src.Backend != "" {
		dst.Backend = src.Backend
	}
	if src.DeepseekKey != "" {
		dst.DeepseekKey = src.DeepseekKey
	}
	if src.DeepseekModel != "" {
		dst.DeepseekModel = src.DeepseekModel
	}
	if src.OllamaURL != "" {
		dst.OllamaURL = src.OllamaURL
	}
	if src.OllamaModel != "" {
		dst.OllamaModel = src.OllamaModel
	}
	if src.SystemPrompt != "" {
		dst.SystemPrompt = src.SystemPrompt
	}
}

func loadChatConfig(path string) (core.AIReviewConfig, error) {
	var cfg core.AIReviewConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func saveChatConfig(path string, cfg core.AIReviewConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// Write to a temp file then rename so a crash mid-write never corrupts
	// the persisted config. 0600 keeps the API key out of other users' reach.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// isLoopbackHost reports whether a host[:port] string resolves to a loopback
// address. The dashboard is local-only, and ollama_url must not be repointed at
// an arbitrary host (that would let a cross-origin page exfiltrate secrets).
func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// validateOllamaURL rejects any ollama_url that is not an http(s) loopback
// endpoint. Called both on config save and at stream time.
func validateOllamaURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid ollama_url %q", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("ollama_url must use http or https")
	}
	if !isLoopbackHost(u.Host) {
		return fmt.Errorf("ollama_url must point to a loopback host (localhost/127.0.0.1/::1)")
	}
	return nil
}

// ---- chat store ----------------------------------------------------------

// ChatStore keeps in-memory chat histories keyed by match id.
type ChatStore struct {
	mu       sync.Mutex
	sessions map[string][]ChatMessage
}

func newChatStore() *ChatStore {
	return &ChatStore{sessions: make(map[string][]ChatMessage)}
}

func (s *ChatStore) Get(id string) []ChatMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ChatMessage, len(s.sessions[id]))
	copy(out, s.sessions[id])
	return out
}

func (s *ChatStore) Append(id string, msg ChatMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = append(s.sessions[id], msg)
}

// EnsureContext appends msg as the first message for id only if the session is
// currently empty. Used to seed the initial analysis prompt atomically so
// concurrent requests cannot double-append it.
func (s *ChatStore) EnsureContext(id string, msg ChatMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions[id]) == 0 {
		s.sessions[id] = append(s.sessions[id], msg)
	}
}

// ---- providers -----------------------------------------------------------

func withSystem(system string, messages []ChatMessage) []ChatMessage {
	if system == "" {
		return messages
	}
	out := make([]ChatMessage, 0, len(messages)+1)
	out = append(out, ChatMessage{Role: "system", Content: system})
	out = append(out, messages...)
	return out
}

func buildDeepSeekPayload(cfg core.AIReviewConfig, messages []ChatMessage) (string, []byte, error) {
	body, err := json.Marshal(map[string]any{
		"model":    cfg.DeepseekModel,
		"stream":   true,
		"messages": withSystem(cfg.SystemPrompt, messages),
	})
	return aiChatDeepSeekURL, body, err
}

func buildOllamaPayload(cfg core.AIReviewConfig, messages []ChatMessage) (string, []byte, error) {
	url := strings.TrimRight(cfg.OllamaURL, "/") + "/api/chat"
	body, err := json.Marshal(map[string]any{
		"model":    cfg.OllamaModel,
		"stream":   true,
		"messages": withSystem(cfg.SystemPrompt, messages),
	})
	return url, body, err
}

type openAISSEChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

// parseOpenAISSE parses an OpenAI-style Server-Sent Events stream (DeepSeek),
// invoking onDelta for each content fragment. Stops at [DONE].
func parseOpenAISSE(r io.Reader, onDelta func(string) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return nil
		}
		if data == "" {
			continue
		}
		var chunk openAISSEChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return err
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				if err := onDelta(c.Delta.Content); err != nil {
					return err
				}
			}
		}
	}
	return sc.Err()
}

type ollamaChunk struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	Done bool `json:"done"`
}

// parseOllamaNDJSON parses an Ollama newline-delimited JSON stream, invoking
// onDelta for each content fragment.
func parseOllamaNDJSON(r io.Reader, onDelta func(string) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var chunk ollamaChunk
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			return err
		}
		if chunk.Message.Content != "" {
			if err := onDelta(chunk.Message.Content); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}

func streamDeepSeek(ctx context.Context, cfg core.AIReviewConfig, messages []ChatMessage, onDelta func(string) error) error {
	url, body, err := buildDeepSeekPayload(cfg, messages)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.DeepseekKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.DeepseekKey)
	}
	resp, err := aiChatHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("deepseek status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return parseOpenAISSE(resp.Body, onDelta)
}

func streamOllama(ctx context.Context, cfg core.AIReviewConfig, messages []ChatMessage, onDelta func(string) error) error {
	if err := validateOllamaURL(cfg.OllamaURL); err != nil {
		return err
	}
	url, body, err := buildOllamaPayload(cfg, messages)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := aiChatHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("ollama status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return parseOllamaNDJSON(resp.Body, onDelta)
}

// streamAIChat dispatches to the provider selected by cfg.Backend.
func streamAIChat(ctx context.Context, cfg core.AIReviewConfig, messages []ChatMessage, onDelta func(string) error) error {
	if cfg.Backend == "ollama" {
		return streamOllama(ctx, cfg, messages, onDelta)
	}
	return streamDeepSeek(ctx, cfg, messages, onDelta)
}

// ---- opencode workspace --------------------------------------------------

// slugFromURL turns a repository URL into a filesystem-safe directory slug.
func slugFromURL(u string) string {
	u = strings.TrimSpace(u)
	u = strings.TrimSuffix(u, "/")
	u = strings.TrimSuffix(u, ".git")
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.Index(u, "@"); i >= 0 {
		u = u[i+1:]
	}
	var b strings.Builder
	for _, r := range u {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.ToLower(strings.Trim(b.String(), "-"))
}

// safeWorkspaceDir resolves a repo URL to a directory under the workspace
// root, rejecting URLs that slugify to the empty string (which would collapse
// the path to the root itself).
func safeWorkspaceDir(repoURL string) (string, error) {
	slug := slugFromURL(repoURL)
	if slug == "" {
		return "", fmt.Errorf("invalid repository URL")
	}
	return filepath.Join(aiChatWorkspaceRoot, slug), nil
}

func buildOpencodeArgs(cloneDir string) []string {
	return []string{"-d", cloneDir, "opencode"}
}

// ensureClone returns the local clone of repoURL, cloning (depth 1) if needed.
func ensureClone(dir, repoURL string) error {
	if info, err := os.Stat(filepath.Join(dir, ".git")); err == nil && info.IsDir() {
		return nil // already cloned
	}
	// Defense in depth: never RemoveAll the workspace root (or anything
	// outside it). The handler validates the slug first, but a bug elsewhere
	// must not be able to delete arbitrary paths.
	if abs, err := filepath.Abs(dir); err == nil {
		if root, rerr := filepath.Abs(aiChatWorkspaceRoot); rerr == nil {
			if abs == root || !strings.HasPrefix(abs, root+string(filepath.Separator)) {
				return fmt.Errorf("refusing to clone into %q", dir)
			}
		}
	}
	_ = os.RemoveAll(dir) // clear any stale/partial clone
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	return realClone(dir, repoURL)
}

// launchOpencode opens a Windows Terminal tab running opencode in cloneDir.
func launchOpencode(dir string) error {
	if _, err := exec.LookPath("wt.exe"); err != nil {
		return fmt.Errorf("wt.exe not found in PATH: %w", err)
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		return fmt.Errorf("opencode not found in PATH: %w", err)
	}
	// Absolutize so wt.exe's -d resolves correctly regardless of its own cwd.
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve workspace dir: %w", err)
	}
	return exec.Command("wt.exe", buildOpencodeArgs(abs)...).Start()
}

// ---- HTTP handlers -------------------------------------------------------

func registerAIChatRoutes() {
	http.HandleFunc("/api/ai/review/config", corsMiddleware(aiLocalGuard(aiReviewConfig)))
	http.HandleFunc("/api/ai/review/history", corsMiddleware(aiLocalGuard(aiReviewHistory)))
	http.HandleFunc("/api/ai/review/stream", corsMiddleware(aiLocalGuard(aiReviewStream)))
	http.HandleFunc("/api/ai/review/opencode", corsMiddleware(aiLocalGuard(aiReviewOpencode)))
}

// aiLocalGuard rejects requests whose Host is not loopback (DNS-rebinding
// defense) and cross-origin browser requests (CSRF defense). The dashboard is a
// local-only tool; non-browser clients such as curl send no Origin header and
// pass as long as their Host is loopback.
func aiLocalGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackHost(r.Host) {
			writeErr(w, http.StatusForbidden, "forbidden")
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			u, err := url.Parse(o)
			if err != nil || !strings.EqualFold(u.Host, r.Host) {
				writeErr(w, http.StatusForbidden, "cross-origin request blocked")
				return
			}
		}
		next(w, r)
	}
}

// GET/POST /api/ai/review/config
func aiReviewConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := aiChat.getConfig()
		out := cfg
		out.DeepseekKey = "" // never echo the key; has_key reports presence
		writeJSON(w, http.StatusOK, map[string]any{
			"config":  out,
			"has_key": cfg.DeepseekKey != "",
		})
	case http.MethodPost:
		var in core.AIReviewConfig
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
			return
		}
		cur := aiChat.getConfig()
		mergeAIChatConfig(&cur, in) // empty fields (incl. key) preserve current
		if err := validateOllamaURL(cur.OllamaURL); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := saveChatConfig(aiChatConfigFile, cur); err != nil {
			writeErr(w, http.StatusInternalServerError, "save failed: "+err.Error())
			return
		}
		aiChat.setConfig(cur)
		out := cur
		out.DeepseekKey = "" // never echo the key
		writeJSON(w, http.StatusOK, map[string]any{
			"config":  out,
			"has_key": cur.DeepseekKey != "",
		})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "GET or POST required")
	}
}

// GET /api/ai/review/history?match_id=...
func aiReviewHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	id := r.URL.Query().Get("match_id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "missing match_id")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": aiChat.store.Get(id)})
}

type aiStreamRequest struct {
	MatchID     string        `json:"match_id"`
	URL         string        `json:"url"`
	File        string        `json:"file"`
	Secret      string        `json:"secret"`
	Signature   string        `json:"signature"`
	UserMessage string        `json:"user_message"`
	Messages    []ChatMessage `json:"messages"`
}

type sseMsg struct {
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
	Message string `json:"message,omitempty"`
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, msg sseMsg) {
	data, _ := json.Marshal(msg)
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

// buildContextMessage assembles the initial user message describing the match.
func buildContextMessage(req aiStreamRequest, content string) ChatMessage {
	var b strings.Builder
	b.WriteString("A secret was detected in a repository. Please analyze it.\n\n")
	if req.Signature != "" {
		b.WriteString("Secret type: " + req.Signature + "\n")
	}
	if req.Secret != "" {
		b.WriteString("Secret value: " + req.Secret + "\n")
	}
	if req.URL != "" {
		b.WriteString("Repository: " + req.URL + "\n")
	}
	if req.File != "" {
		b.WriteString("File path: " + req.File + "\n")
	}
	if content != "" {
		b.WriteString("\nFull file contents:\n```\n" + content + "\n```\n")
	} else {
		b.WriteString("\n(Full file contents were not available for this match.)\n")
	}
	return ChatMessage{Role: "user", Content: b.String()}
}

// POST /api/ai/review/stream — streams the AI reply as SSE.
func aiReviewStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req aiStreamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	cfg := aiChat.getConfig()
	if cfg.Backend == "deepseek" && cfg.DeepseekKey == "" {
		writeErr(w, http.StatusBadRequest, "DeepSeek API key is not set — open settings to add it")
		return
	}
	if req.MatchID == "" {
		writeErr(w, http.StatusBadRequest, "missing match_id")
		return
	}
	if !aiChat.beginStream(req.MatchID) {
		writeErr(w, http.StatusConflict, "a review is already streaming for this match")
		return
	}
	defer aiChat.endStream(req.MatchID)

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Resolve the full file content captured at scan time, if available.
	content := ""
	fileMu.Lock()
	if d, ok := fileDetails[req.MatchID]; ok {
		content = d.Content
	}
	fileMu.Unlock()

	// Build (and persist) the message history.
	store := aiChat.store
	store.EnsureContext(req.MatchID, buildContextMessage(req, content))
	if um := strings.TrimSpace(req.UserMessage); um != "" {
		store.Append(req.MatchID, ChatMessage{Role: "user", Content: um})
	}
	history := store.Get(req.MatchID)

	// Cancelled when the client disconnects (r.Context) or when the timeout
	// elapses; both stop the upstream request from being drained pointlessly.
	ctx, cancel := context.WithTimeout(r.Context(), aiChatStreamTimeout)
	defer cancel()

	var buf strings.Builder
	err := streamAIChat(ctx, cfg, history, func(delta string) error {
		buf.WriteString(delta)
		writeSSE(w, flusher, sseMsg{Type: "delta", Content: delta})
		return nil
	})

	if err != nil {
		// Only notify a still-connected client; a disconnect shows up as
		// r.Context().Err() != nil and needs no (unwritable) error event.
		if r.Context().Err() == nil {
			writeSSE(w, flusher, sseMsg{Type: "error", Message: err.Error()})
		}
		if buf.Len() > 0 {
			store.Append(req.MatchID, ChatMessage{Role: "assistant", Content: buf.String()})
		}
		return
	}

	store.Append(req.MatchID, ChatMessage{Role: "assistant", Content: buf.String()})
	writeSSE(w, flusher, sseMsg{Type: "done"})
}

// POST /api/ai/review/opencode — clones (if needed) and opens an opencode
// workspace for the match's repository in Windows Terminal.
func aiReviewOpencode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	if body.URL == "" {
		writeErr(w, http.StatusBadRequest, "missing url")
		return
	}
	dir, err := safeWorkspaceDir(body.URL)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := ensureClone(dir, body.URL); err != nil {
		writeErr(w, http.StatusInternalServerError, "clone failed: "+err.Error())
		return
	}
	if err := launchOpencode(dir); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "true", "dir": dir})
}
