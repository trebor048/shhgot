package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/trebor048/shhgot/aiproviders"
	"github.com/trebor048/shhgot/core"
	"github.com/trebor048/shhgot/reviewstore"
)

// reviewTimeout bounds one review or follow-up call. Without it a hung provider
// would leave a review stuck in "running" forever.
const reviewTimeout = 5 * time.Minute

const defaultReviewSystemPrompt = `You are a senior application security engineer triaging a credential that a secret scanner just found in a code repository.

You will be given the detected credential, the file it was found in, and the surrounding file content. Analyse it and reply in Markdown using exactly these sections:

## What it is
The credential type, what it grants access to, and how confident you are in that identification.

## Impact if abused
What an attacker gains. Name the concrete blast radius - data, money, infrastructure or lateral movement. If the value looks like a placeholder, an example, or a test fixture, say so plainly and explain why you think that.

## Exploitability
How reachable this really is: does the repository have to be public, does the credential still look live, what would an attacker have to do next? Do not overstate it.

## Remediation
Numbered, specific steps in the right order: rotate or revoke, purge from history, then prevent recurrence (pre-commit hooks, a secret manager, tighter scoping). Be concrete about the provider's console where you are confident.

## Confidence and unknowns
What you cannot determine from the material provided, and what the operator should check next.

Rules:
- Be specific to the evidence. Never invent file contents, repository history, or provider behaviour you cannot infer.
- The value may be truncated, so say so if that limits your answer.
- Never repeat the credential back in your answer. Refer to it as "the detected value".
- Prefer short paragraphs and lists over long prose.`

var (
	reviewStore  *reviewstore.Store
	reviewPrompt = defaultReviewSystemPrompt

	// reviewJobs holds the in-flight reviews so a browser that connects late
	// still receives everything streamed so far.
	reviewJobsMu sync.Mutex
	reviewJobs   = map[string]*reviewJob{}
)

// reconcileInterruptedReviews marks reviews that a previous process left
// queued or running as failed, and returns how many it changed.
//
// A review is flipped to "running" before its goroutine starts, and only the
// process that started it can finish it: the in-flight job lives in an
// in-memory map that does not survive a restart. So at startup any review still
// marked queued or running is orphaned and can never make progress. Left alone
// it would show a spinner in the dashboard for good, and because it is not in a
// terminal state it cannot be retried either - the operator could only delete
// it. Marking it failed says what actually happened and returns it to a state
// the UI understands.
func reconcileInterruptedReviews(store *reviewstore.Store) int {
	if store == nil {
		return 0
	}

	reconciled := 0
	for _, rev := range store.List() {
		if rev.Status != reviewstore.StatusQueued && rev.Status != reviewstore.StatusRunning {
			continue
		}
		rev.Status = reviewstore.StatusFailed
		rev.Error = "interrupted: shhgit stopped while this review was in progress, so it never finished"
		if err := store.Update(rev); err != nil {
			// A review that cannot be rewritten is reported by the caller's
			// normal error handling on the next access; keep going so one bad
			// file does not block the rest.
			log.Printf("[ai-review] could not mark interrupted review %s as failed: %v", rev.ID, err)
			continue
		}
		reconciled++
	}
	return reconciled
}

// initAIReview prepares the review store and the AI settings store. It is
// called once at startup; a failure is reported but must not stop the scanner,
// because AI review is an optional feature.
func initAIReview(cfg *core.Config) error {
	dir := "ai_review"
	var ai core.AIReviewConfig
	if cfg != nil {
		ai = cfg.AIReview
		if s := strings.TrimSpace(ai.ReviewDir); s != "" {
			dir = s
		}
	}

	reviewsDir := filepath.Join(dir, "reviews")
	if err := os.MkdirAll(reviewsDir, 0o700); err != nil {
		return fmt.Errorf("create review directory: %w", err)
	}

	st, err := reviewstore.NewStore(reviewsDir)
	if err != nil {
		return fmt.Errorf("open review store: %w", err)
	}
	reviewStore = st
	if n := reconcileInterruptedReviews(st); n > 0 {
		log.Printf("[ai-review] marked %d interrupted review(s) as failed", n)
	}

	if p := strings.TrimSpace(ai.SystemPrompt); p != "" {
		reviewPrompt = p
	}

	store := aiproviders.NewStore(filepath.Join(dir, "settings.json"))
	aiSettingsStore = store

	// config.yaml seeds the settings file once. After that the dashboard owns
	// them, so an operator who switches provider in the UI is not overridden on
	// the next restart.
	if _, err := os.Stat(store.Path()); errors.Is(err, os.ErrNotExist) {
		seed := configSettings(ai)

		// Fill anything config.yaml left blank from the single-backend chat
		// config this release replaced, so an upgrade keeps the provider, key
		// and prompt that were already working.
		legacy, legacyPrompt, hasLegacy := legacyChatSettings(dir)
		if hasLegacy {
			if seed.Provider == "" {
				seed.Provider = legacy.Provider
			}
			if seed.APIKey == "" {
				seed.APIKey = legacy.APIKey
			}
			if seed.BaseURL == "" {
				seed.BaseURL = legacy.BaseURL
			}
			if seed.Model == "" {
				seed.Model = legacy.Model
			}
		}

		if seed.Provider != "" || seed.APIKey != "" || seed.BaseURL != "" || seed.Model != "" {
			// Seeding is best effort. An unusable seed - an unknown provider in
			// config.yaml, say - must not disable AI review altogether: without a
			// settings file the store falls back to the environment and then to
			// the built-in defaults, which is a working configuration.
			if _, err := aiproviders.New(seed); err != nil {
				log.Printf("[ai-review] ignoring unusable AI settings from config.yaml: %v", err)
			} else if err := store.Set(seed); err != nil {
				return fmt.Errorf("seed AI settings: %w", err)
			}
		}
		if hasLegacy && legacyPrompt != "" && strings.TrimSpace(ai.SystemPrompt) == "" {
			reviewPrompt = legacyPrompt
		}
	}

	return nil
}

// legacyChatSettings reads ai_review/chat_config.json, the configuration used by
// the single-backend AI chat that this release replaced. It exists so an upgrade
// does not silently discard a provider, API key or system prompt the operator
// had already set up. ok is false when there is no usable file.
func legacyChatSettings(dir string) (aiproviders.Settings, string, bool) {
	data, err := os.ReadFile(filepath.Join(dir, "chat_config.json"))
	if err != nil {
		return aiproviders.Settings{}, "", false
	}

	var legacy struct {
		Backend       string `json:"backend"`
		DeepseekKey   string `json:"deepseek_api_key"`
		DeepseekModel string `json:"deepseek_model"`
		OllamaURL     string `json:"ollama_url"`
		OllamaModel   string `json:"ollama_model"`
		SystemPrompt  string `json:"system_prompt"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return aiproviders.Settings{}, "", false
	}

	s := aiproviders.Settings{Provider: strings.ToLower(strings.TrimSpace(legacy.Backend))}
	switch s.Provider {
	case "ollama":
		s.BaseURL = strings.TrimSpace(legacy.OllamaURL)
		s.Model = strings.TrimSpace(legacy.OllamaModel)
	default:
		s.APIKey = strings.TrimSpace(legacy.DeepseekKey)
		s.Model = strings.TrimSpace(legacy.DeepseekModel)
		// The old chat defaulted to DeepSeek, and an empty or unrecognised
		// backend lands in this branch anyway. Naming it explicitly matters:
		// a provider-less seed is rejected by the settings store, which used to
		// fail the whole AI setup on an upgrade.
		if s.Provider == "" {
			s.Provider = "deepseek"
		}
	}
	if s.Provider == "" && s.APIKey == "" && s.Model == "" {
		return aiproviders.Settings{}, "", false
	}
	return s, strings.TrimSpace(legacy.SystemPrompt), true
}

// configSettings maps the config.yaml AI section, including the legacy
// single-backend keys, onto provider settings.
func configSettings(ai core.AIReviewConfig) aiproviders.Settings {
	s := aiproviders.Settings{
		Provider: strings.ToLower(strings.TrimSpace(ai.Provider)),
		APIKey:   strings.TrimSpace(ai.APIKey),
		BaseURL:  strings.TrimSpace(ai.BaseURL),
		Model:    strings.TrimSpace(ai.Model),
	}
	if s.Provider == "" {
		s.Provider = strings.ToLower(strings.TrimSpace(ai.Backend))
	}
	switch s.Provider {
	case "ollama":
		if s.BaseURL == "" {
			s.BaseURL = strings.TrimSpace(ai.OllamaURL)
		}
		if s.Model == "" {
			s.Model = strings.TrimSpace(ai.OllamaModel)
		}
	case "deepseek", "":
		if s.APIKey == "" {
			s.APIKey = strings.TrimSpace(ai.DeepseekKey)
		}
		if s.Model == "" {
			s.Model = strings.TrimSpace(ai.DeepseekModel)
		}
	}
	return s
}

// reviewClient builds a provider client from the saved settings.
func reviewClient() (aiproviders.Client, aiproviders.Settings, error) {
	if aiSettingsStore == nil {
		return nil, aiproviders.Settings{}, errors.New("AI settings are not initialised")
	}
	s, err := aiSettingsStore.Get()
	if err != nil {
		return nil, aiproviders.Settings{}, err
	}
	c, err := aiproviders.New(s)
	if err != nil {
		return nil, s, err
	}
	return c, s, nil
}

// effectiveModel is what the dashboard labels a review with.
func effectiveModel(s aiproviders.Settings) string {
	if s.Model != "" {
		return s.Model
	}
	return aiproviders.Defaults(s.Provider).Model
}

// --- request/response shapes -------------------------------------------------

// reviewRequest is the body of POST /api/review.
type reviewRequest struct {
	MatchID   string `json:"match_id"`
	Signature string `json:"signature"`
	Secret    string `json:"secret"`
	File      string `json:"file"`
	URL       string `json:"url"`
	Repo      string `json:"repo"`
	Context   string `json:"context"`
}

// reviewView is what the dashboard receives for a review.
//
// Secret and Assessment are omitted unless the view is full. The list endpoint
// omits them deliberately: the dashboard polls it every few seconds and renders
// only metadata from it (id, status, signature, file, repo, provider, model), so
// sending the credential and the whole assessment on every poll would put live
// secrets on a hot path for nothing. The detail endpoint carries them.
type reviewView struct {
	ID         string                `json:"id"`
	CreatedAt  time.Time             `json:"created_at"`
	UpdatedAt  time.Time             `json:"updated_at"`
	Signature  string                `json:"signature"`
	Secret     string                `json:"secret,omitempty"`
	File       string                `json:"file"`
	URL        string                `json:"url"`
	Repo       string                `json:"repo"`
	Provider   string                `json:"provider"`
	Model      string                `json:"model"`
	Status     reviewstore.Status    `json:"status"`
	Assessment string                `json:"assessment,omitempty"`
	Error      string                `json:"error,omitempty"`
	Messages   []reviewstore.Message `json:"messages,omitempty"`
}

// viewOf builds the JSON view of a review. full includes the credential, the
// assessment and the chat history; without it only list metadata is returned.
func viewOf(r *reviewstore.Review, full bool) reviewView {
	v := reviewView{
		ID: r.ID, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		Signature: r.Signature, File: r.File, URL: r.URL, Repo: r.Repo,
		Provider: r.Provider, Model: r.Model, Status: r.Status,
		Error: r.Error,
	}
	if full {
		v.Secret = r.Secret
		v.Assessment = r.Assessment
		v.Messages = r.Messages
	}
	return v
}

// --- routing -----------------------------------------------------------------

func registerReviewRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/review", corsMiddleware(localGuard(reviewCollectionHandler)))
	mux.HandleFunc("/api/review/", corsMiddleware(localGuard(reviewItemHandler)))
}

// reviewCollectionHandler serves GET (list) and POST (create + start).
func reviewCollectionHandler(w http.ResponseWriter, r *http.Request) {
	if reviewStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "AI review is not initialised")
		return
	}

	switch r.Method {
	case http.MethodGet:
		all := reviewStore.List()
		out := make([]reviewView, 0, len(all))
		for _, rev := range all {
			out = append(out, viewOf(rev, false))
		}
		writeJSON(w, http.StatusOK, map[string]any{"reviews": out})

	case http.MethodPost:
		var in reviewRequest
		if err := decodeJSON(w, r, &in); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if strings.TrimSpace(in.Signature) == "" && strings.TrimSpace(in.Secret) == "" {
			writeJSONError(w, http.StatusBadRequest, "a review needs at least a signature or a secret")
			return
		}

		// If the browser told us which match this is, pull the stored file body
		// from the dashboard's file cache: the scan already captured it, and
		// cloned repositories are deleted afterwards. Without this the model
		// would only see metadata and could not judge the surrounding code.
		if in.MatchID != "" {
			fileMu.Lock()
			d, ok := fileDetails[in.MatchID]
			fileMu.Unlock()
			if ok {
				if in.File == "" {
					in.File = d.File
				}
				if in.URL == "" {
					in.URL = d.URL
				}
				if d.Content != "" {
					notes := strings.TrimSpace(in.Context)
					in.Context = d.Content
					if notes != "" {
						// The browser only sends a short scanner summary; keep it,
						// clearly labelled, rather than mistaking it for the file.
						in.Context += "\n\n" + scannerNotesMarker + "\n" + notes
					}
				} else if notes := strings.TrimSpace(in.Context); notes != "" {
					// The cache had no body for this match: keep the summary but
					// record that it is not file content, so the prompt builder
					// does not present it as such.
					in.Context = noFileBodyMarker + "\n" + notes
				}
			}
		}

		client, settings, err := reviewClient()
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		rev := &reviewstore.Review{
			Signature: strings.TrimSpace(in.Signature),
			Secret:    strings.TrimSpace(in.Secret),
			File:      strings.TrimSpace(in.File),
			URL:       strings.TrimSpace(in.URL),
			Repo:      strings.TrimSpace(in.Repo),
			Context:   in.Context,
			Provider:  settings.Provider,
			Model:     effectiveModel(settings),
			Status:    reviewstore.StatusQueued,
		}
		if rev.Repo == "" {
			rev.Repo = repoFromURL(rev.URL)
		}
		if err := reviewStore.Create(rev); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}

		// Capture what the response needs before handing the review to the
		// background worker; from here on the worker owns that struct.
		id := rev.ID
		startReview(rev, client)
		writeJSON(w, http.StatusCreated, map[string]any{
			"id":     id,
			"status": string(reviewstore.StatusRunning),
		})

	default:
		w.Header().Set("Allow", "GET, POST")
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// reviewItemHandler serves /api/review/<id>, /stream and /chat.
func reviewItemHandler(w http.ResponseWriter, r *http.Request) {
	if reviewStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "AI review is not initialised")
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, "/api/review/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	if id == "" {
		writeJSONError(w, http.StatusNotFound, "no such review")
		return
	}

	switch action {
	case "":
		switch r.Method {
		case http.MethodGet:
			rev, ok := reviewStore.Get(id)
			if !ok {
				writeJSONError(w, http.StatusNotFound, "no such review")
				return
			}
			writeJSON(w, http.StatusOK, viewOf(rev, true))
		case http.MethodDelete:
			// Only tear down the job once the review is known to exist. Dropping it
			// first meant a 404 for an unknown id could still have destroyed a
			// running job, so a stream request arriving in between found no job,
			// lost the live deltas and was told the review was not running.
			if _, exists := reviewStore.Get(id); !exists {
				writeJSONError(w, http.StatusNotFound, "no such review")
				return
			}
			dropJob(id)
			if err := reviewStore.Delete(id); err != nil {
				// The store reports a missing review and an invalid id with the
				// same wrapped os.ErrNotExist, so errors.Is is the way to tell
				// "no such review" apart from a real filesystem failure.
				if errors.Is(err, os.ErrNotExist) {
					writeJSONError(w, http.StatusNotFound, "no such review")
					return
				}
				writeJSONError(w, http.StatusInternalServerError, "could not delete the review: "+err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		default:
			w.Header().Set("Allow", "GET, DELETE")
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		}

	case "stream":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		reviewStreamHandler(w, r, id)

	case "chat":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		reviewChatHandler(w, r, id)

	default:
		writeJSONError(w, http.StatusNotFound, "no such review endpoint")
	}
}

// --- the review engine -------------------------------------------------------

// startReview runs the provider call in the background so a review completes
// even if the operator closes the dashboard tab.
//
// It takes ownership of rev: the caller must not touch that struct afterwards.
// The goroutine below works on a private copy for the same reason, so no field
// of a live review is ever written from two goroutines at once.
func startReview(rev *reviewstore.Review, client aiproviders.Client) {
	store := reviewStore
	if store == nil || client == nil {
		return
	}
	// Read the package-level prompt and store once, here, rather than from the
	// goroutine: both are mutable and a stale read would be a data race.
	prompt := reviewPrompt

	local := *rev

	job := newReviewJob()
	reviewJobsMu.Lock()
	reviewJobs[local.ID] = job
	reviewJobsMu.Unlock()

	local.Status = reviewstore.StatusRunning
	local.Error = ""
	// SetResult rather than Update: a chat turn can be appended while the review
	// runs, and writing this whole struct back would discard it.
	if err := store.SetResult(local.ID, reviewstore.StatusRunning, local.Assessment, ""); err != nil {
		log.Printf("[ai-review] could not mark review %s as running: %v", local.ID, err)
	}
	broadcastReview(&local)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), reviewTimeout)
		defer cancel()

		err := client.Stream(ctx, buildReviewMessages(&local, prompt), func(delta string) error {
			job.publish(delta)
			return nil
		})

		status := reviewstore.StatusDone
		assessment := job.full()
		errMsg := ""
		if err != nil {
			status = reviewstore.StatusFailed
			assessment = ""
			errMsg = err.Error()
		}
		job.finish(status, errMsg)

		// Record the outcome without rewriting the record: chat turns appended
		// while the review ran must survive it finishing.
		if uerr := store.SetResult(local.ID, status, assessment, errMsg); uerr != nil {
			log.Printf("[ai-review] could not save the result of review %s: %v", local.ID, uerr)
		}

		// Broadcast the stored record so subscribers see the transcript as it
		// actually is, not the pre-run snapshot.
		if fresh, ok := store.Get(local.ID); ok {
			broadcastReview(fresh)
		}
		dropJob(local.ID)
	}()
}

// buildReviewMessages assembles the analysis prompt for a fresh review.
func buildReviewMessages(rev *reviewstore.Review, system string) []aiproviders.Message {
	var b strings.Builder
	fmt.Fprintf(&b, "Repository: %s\n", orUnknown(rev.Repo))
	fmt.Fprintf(&b, "File: %s\n", orUnknown(rev.File))
	fmt.Fprintf(&b, "Source URL: %s\n", orUnknown(rev.URL))
	fmt.Fprintf(&b, "Detected signature: %s\n", orUnknown(rev.Signature))
	fmt.Fprintf(&b, "Detected value: %s\n", orUnknown(rev.Secret))
	if rev.Context != "" {
		b.WriteString(contextHeading(rev.Context))
		b.WriteString(rev.Context)
		if !strings.HasSuffix(rev.Context, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("--- end of supplied context ---")
	} else {
		b.WriteString("\nNo file content was captured for this finding; base your answer on the metadata above and say what you could not determine.\n")
	}

	return []aiproviders.Message{
		{Role: aiproviders.RoleSystem, Content: system},
		{Role: aiproviders.RoleUser, Content: b.String()},
	}
}

// Markers carried inside a stored review's Context, so the prompt can state what
// the model is actually being shown. A review holds the captured file body, or
// only the browser's one-line scanner summary when the file cache had already
// evicted the entry (it keeps the most recent 300) or when the review predates a
// restart, since that cache lives in memory.
const (
	// scannerNotesMarker is appended when the scanner's summary is kept
	// alongside a captured body, so its presence means the body is there.
	scannerNotesMarker = "--- scanner notes, not part of the file ---"
	// noFileBodyMarker is written when only the summary exists.
	noFileBodyMarker = "--- no file content was captured for this finding ---"
)

// contextHeading introduces a review's stored context in the prompt.
//
// Describing a one-line summary as "file content" invited the model to reason as
// though it had read the file, which is worse than saying nothing. Records
// written before these markers existed cannot be told apart, so they get a
// heading that is true either way rather than a guess.
func contextHeading(context string) string {
	switch {
	case strings.Contains(context, scannerNotesMarker):
		return "\n--- file content, for context (may be truncated) ---\n"
	case strings.Contains(context, noFileBodyMarker):
		return "\n--- no file content was captured for this finding; the scanner's summary follows ---\n"
	default:
		return "\n--- context supplied with this finding (the captured file content if it was available, otherwise the scanner's summary) ---\n"
	}
}

// buildChatMessages replays the review so a follow-up question has the same
// context the assessment was written from.
func buildChatMessages(rev *reviewstore.Review, system string) []aiproviders.Message {
	msgs := buildReviewMessages(rev, system)
	if rev.Assessment != "" {
		msgs = append(msgs, aiproviders.Message{Role: aiproviders.RoleAssistant, Content: rev.Assessment})
	}
	for _, m := range rev.Messages {
		role := aiproviders.RoleUser
		if strings.EqualFold(m.Role, "assistant") {
			role = aiproviders.RoleAssistant
		}
		msgs = append(msgs, aiproviders.Message{Role: role, Content: m.Content})
	}
	return msgs
}

// reviewStreamHandler streams a review to the dashboard over SSE.
func reviewStreamHandler(w http.ResponseWriter, r *http.Request, id string) {
	rev, ok := reviewStore.Get(id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "no such review")
		return
	}

	job := lookupJob(id)
	flusher := sseHeaders(w)

	if job == nil {
		// No in-flight job: either it finished (or the server restarted) and the
		// result is already persisted, or it never started.
		switch rev.Status {
		case reviewstore.StatusDone:
			_ = sseSend(w, flusher, map[string]any{"type": "done", "assessment": rev.Assessment})
		case reviewstore.StatusFailed:
			_ = sseSend(w, flusher, map[string]any{"type": "error", "error": rev.Error})
		default:
			_ = sseSend(w, flusher, map[string]any{"type": "error", "error": "review is not running"})
		}
		return
	}

	history, ch, _ := job.subscribe()
	// Every return below leaves the fan-out set, so a client that goes away
	// mid-review cannot slow down the deltas sent to the ones that stayed.
	defer job.unsubscribe(ch)
	if history != "" {
		// reset tells the client this frame is the whole assessment so far, not
		// another delta. Without it a client that reconnects (EventSource does
		// that on its own after a dropped connection) would append the history a
		// second time and show the assessment twice.
		if err := sseSend(w, flusher, map[string]any{"type": "delta", "text": history, "reset": true}); err != nil {
			return
		}
	}

	// A review can take minutes and may produce no output for long stretches, and
	// an idle SSE connection is exactly what an intermediary drops. The match feed
	// already keeps itself alive this way; without it a proxied dashboard - the
	// Cloudflare Tunnel profile, for one - would lose the stream mid-review and the
	// operator would wait on a review that had already finished.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case delta, open := <-ch:
			if !open {
				text, status, errMsg := job.result()
				if status == reviewstore.StatusFailed {
					_ = sseSend(w, flusher, map[string]any{"type": "error", "error": errMsg})
				} else {
					_ = sseSend(w, flusher, map[string]any{"type": "done", "assessment": text})
				}
				return
			}
			if err := sseSend(w, flusher, map[string]any{"type": "delta", "text": delta}); err != nil {
				return
			}

		case <-heartbeat.C:
			if err := sseSend(w, flusher, map[string]any{"type": "ping"}); err != nil {
				return
			}

		case <-r.Context().Done():
			return
		}
	}
}

// reviewChatHandler answers a follow-up question, streaming the reply.
func reviewChatHandler(w http.ResponseWriter, r *http.Request, id string) {
	rev, ok := reviewStore.Get(id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "no such review")
		return
	}

	var in struct {
		Message string `json:"message"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	question := strings.TrimSpace(in.Message)
	if question == "" {
		writeJSONError(w, http.StatusBadRequest, "message is required")
		return
	}

	client, _, err := reviewClient()
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	now := time.Now().UTC()
	if err := reviewStore.AppendMessage(id, reviewstore.Message{Role: "user", Content: question, CreatedAt: now}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Re-read so the new question is part of the replayed conversation.
	rev, ok = reviewStore.Get(id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "no such review")
		return
	}

	flusher := sseHeaders(w)
	ctx, cancel := context.WithTimeout(r.Context(), reviewTimeout)
	defer cancel()

	var reply strings.Builder
	err = client.Stream(ctx, buildChatMessages(rev, reviewPrompt), func(delta string) error {
		reply.WriteString(delta)
		return sseSend(w, flusher, map[string]any{"type": "delta", "text": delta})
	})
	if err != nil {
		_ = sseSend(w, flusher, map[string]any{"type": "error", "error": err.Error()})
		return
	}

	answer := reply.String()
	if strings.TrimSpace(answer) == "" {
		_ = sseSend(w, flusher, map[string]any{"type": "error", "error": "the provider returned an empty reply"})
		return
	}
	if err := reviewStore.AppendMessage(id, reviewstore.Message{Role: "assistant", Content: answer, CreatedAt: time.Now().UTC()}); err != nil {
		_ = sseSend(w, flusher, map[string]any{"type": "error", "error": "could not save the reply: " + err.Error()})
		return
	}
	_ = sseSend(w, flusher, map[string]any{"type": "done", "assessment": answer})
}

// --- in-flight review broker -------------------------------------------------

// reviewJob fans one in-flight review out to any number of SSE subscribers.
type reviewJob struct {
	mu     sync.Mutex
	text   strings.Builder
	subs   map[chan string]struct{}
	done   bool
	status reviewstore.Status
	errMsg string
}

func newReviewJob() *reviewJob {
	return &reviewJob{subs: map[chan string]struct{}{}, status: reviewstore.StatusRunning}
}

// publish appends a delta and forwards it to every subscriber.
//
// The send happens while holding the mutex on purpose: finish closes the
// subscriber channels under the same mutex, so a send can never race with a
// close. Sends are non-blocking, so one stalled browser cannot hold up a
// review; a dropped delta is acceptable because the terminal "done" event
// carries the complete text.
func (j *reviewJob) publish(delta string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	// Ignore output that arrives after the job finished. Both providers call back
	// synchronously inside Stream today, so this cannot happen yet - but a provider
	// that streamed from another goroutine would otherwise keep appending after the
	// terminal event, and the text a late subscriber is handed as history, and the
	// assessment full() reports, would include output no terminal event carried.
	if j.done {
		return
	}
	j.text.WriteString(delta)
	for c := range j.subs {
		select {
		case c <- delta:
		default:
		}
	}
}

func (j *reviewJob) finish(status reviewstore.Status, errMsg string) {
	j.mu.Lock()
	j.done = true
	j.status = status
	j.errMsg = errMsg
	for c := range j.subs {
		close(c)
	}
	j.subs = map[chan string]struct{}{}
	j.mu.Unlock()
}

func (j *reviewJob) full() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.text.String()
}

func (j *reviewJob) result() (string, reviewstore.Status, string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.text.String(), j.status, j.errMsg
}

// subscribe returns everything streamed so far plus a channel of later deltas.
// The channel is closed when the job finishes; finished reports whether it had
// already finished at subscription time.
func (j *reviewJob) subscribe() (history string, ch chan string, finished bool) {
	ch = make(chan string, 256)
	j.mu.Lock()
	defer j.mu.Unlock()
	history = j.text.String()
	if j.done {
		close(ch)
		return history, ch, true
	}
	j.subs[ch] = struct{}{}
	return history, ch, false
}

// unsubscribe removes a subscriber that has gone away.
//
// Without it a browser that disconnects or reconnects mid-review would leave its
// channel in the fan-out set until the review finished, and publish - which walks
// every subscriber under the job mutex on each delta - would keep paying for
// stale entries. Entry and removal both happen under j.mu, the same mutex publish
// sends under, so a close can never race a send. Calling it on a channel that was
// never registered (the already-finished case) is a no-op.
func (j *reviewJob) unsubscribe(ch chan string) {
	if ch == nil {
		return
	}
	j.mu.Lock()
	if _, ok := j.subs[ch]; ok {
		delete(j.subs, ch)
		close(ch)
	}
	j.mu.Unlock()
}

func lookupJob(id string) *reviewJob {
	reviewJobsMu.Lock()
	defer reviewJobsMu.Unlock()
	return reviewJobs[id]
}

func dropJob(id string) {
	reviewJobsMu.Lock()
	delete(reviewJobs, id)
	reviewJobsMu.Unlock()
}

func broadcastReview(rev *reviewstore.Review) {
	broadcastFeed(FeedEvent{Type: "review", Review: &ReviewEvent{
		ID:       rev.ID,
		Status:   string(rev.Status),
		Provider: rev.Provider,
		Model:    rev.Model,
		Error:    rev.Error,
	}})
}

// --- SSE helpers -------------------------------------------------------------

func sseHeaders(w http.ResponseWriter) http.Flusher {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f, _ := w.(http.Flusher)
	if f != nil {
		f.Flush()
	}
	return f
}

func sseSend(w io.Writer, f http.Flusher, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
		return err
	}
	if f != nil {
		f.Flush()
	}
	return nil
}

// --- small helpers -----------------------------------------------------------

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(unknown)"
	}
	return s
}

// repoFromURL derives "owner/name" from a repository or blob URL, for labelling
// only. It understands https, ssh and scp-style git remotes on any host.
func repoFromURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimPrefix(s, "git@")
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "ssh://")

	// git@github.com:owner/name uses a colon where a URL uses a slash.
	if i := strings.Index(s, ":"); i != -1 && !strings.Contains(s[:i], "/") {
		s = s[:i] + "/" + s[i+1:]
	}

	parts := make([]string, 0, 4)
	for _, p := range strings.Split(s, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	// Drop a leading hostname (anything containing a dot), so the owner and
	// repository survive regardless of which forge this came from.
	if len(parts) > 1 && strings.Contains(parts[0], ".") {
		parts = parts[1:]
	}
	if len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}
