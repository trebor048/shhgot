package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/trebor048/shhgot/aiproviders"
)

// errNoTrailingData is returned when a JSON body holds data past the first
// value.
var errNoTrailingData = errors.New("unexpected data after JSON value")

// maxJSONBody caps request bodies for the settings and review APIs. The
// dashboard sends small JSON objects; anything larger is a mistake or abuse.
const maxJSONBody = 1 << 20 // 1 MiB

// aiSettingsStore is created once at startup by initAIReview.
var aiSettingsStore *aiproviders.Store

// settingsView is the dashboard-facing view of the AI configuration.
//
// It deliberately cannot carry the API key: the browser is told only whether a
// key is stored (api_key_set), so a key can never leak back out through the
// settings API, a screenshot, or a cached response.
type settingsView struct {
	Provider  string                          `json:"provider"`
	BaseURL   string                          `json:"base_url"`
	Model     string                          `json:"model"`
	APIKeySet bool                            `json:"api_key_set"`
	Providers []string                        `json:"providers"`
	Defaults  map[string]aiproviders.Settings `json:"defaults"`
}

// settingsUpdate is the request body accepted by PUT /api/settings and
// POST /api/settings/test.
//
// BaseURL and Model are pointers so that "absent" and "empty" can mean different
// things: an omitted field keeps what is stored, while an explicit empty string
// asks for the provider default (which is what the dashboard sends when the
// operator clears the box). Without that distinction a client rotating only the
// API key of a self-hosted OpenAI-compatible gateway had its custom endpoint and
// model replaced by the vendor defaults - silently sending the next review, and
// the finding in it, to the wrong host.
type settingsUpdate struct {
	Provider string  `json:"provider"`
	APIKey   string  `json:"api_key"`
	BaseURL  *string `json:"base_url"`
	Model    *string `json:"model"`
}

func newSettingsView(s aiproviders.Settings) settingsView {
	providers := aiproviders.KnownProviders()
	defaults := make(map[string]aiproviders.Settings, len(providers))
	for _, p := range providers {
		d := aiproviders.Defaults(p)
		d.APIKey = "" // provider defaults never carry a secret, but be explicit
		defaults[p] = d
	}
	return settingsView{
		Provider:  s.Provider,
		BaseURL:   s.BaseURL,
		Model:     s.Model,
		APIKeySet: s.APIKey != "",
		Providers: providers,
		Defaults:  defaults,
	}
}

// mergeSettings applies a browser-supplied update to the stored settings.
//
// Rules: an empty api_key keeps the stored key (so the UI never has to round trip
// a secret) unless the provider is switched, an omitted base_url/model keeps the
// stored endpoint, and an explicit empty base_url/model means "use the provider
// default" rather than "blank it".
func mergeSettings(cur aiproviders.Settings, in settingsUpdate) (aiproviders.Settings, error) {
	next := cur
	if p := strings.ToLower(strings.TrimSpace(in.Provider)); p != "" {
		next.Provider = p
		// Switching provider must not inherit the previous provider's endpoint,
		// model or key; all three are provider-scoped, and a key silently sent to
		// a different vendor is a credential disclosure. A key supplied in this
		// same request is applied below.
		if p != strings.ToLower(cur.Provider) {
			d := aiproviders.Defaults(p)
			next.BaseURL = d.BaseURL
			next.Model = d.Model
			next.APIKey = ""
		}
	}
	if k := strings.TrimSpace(in.APIKey); k != "" {
		next.APIKey = k
	}
	def := aiproviders.Defaults(next.Provider)
	if in.BaseURL != nil {
		if v := strings.TrimSpace(*in.BaseURL); v != "" {
			// The endpoint is operator-supplied and this API is bound to
			// loopback, so a private/LAN host (Ollama on a GPU box) is a normal
			// setup, not SSRF. aiproviders.New/Set validate it as an absolute
			// http(s) URL with a host before it is stored.
			next.BaseURL = v
		} else {
			next.BaseURL = def.BaseURL
		}
	}
	if in.Model != nil {
		if v := strings.TrimSpace(*in.Model); v != "" {
			next.Model = v
		} else {
			next.Model = def.Model
		}
	}
	return next, nil
}

func registerSettingsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/settings", corsMiddleware(localGuard(settingsHandler)))
	mux.HandleFunc("/api/settings/test", corsMiddleware(localGuard(settingsTestHandler)))
}

// settingsHandler serves GET (read, key masked) and PUT/POST (save).
func settingsHandler(w http.ResponseWriter, r *http.Request) {
	if aiSettingsStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "AI settings are not initialised")
		return
	}

	switch r.Method {
	case http.MethodGet:
		cur, err := aiSettingsStore.Get()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, newSettingsView(cur))

	case http.MethodPut, http.MethodPost:
		var in settingsUpdate
		if err := decodeJSON(w, r, &in); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		// Merge from what the file holds, not from Get: Get falls back to the
		// environment, and saving that back would persist an environment-provided
		// API key into the file, where it would then outrank the environment.
		cur, err := aiSettingsStore.GetStored()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		next, err := mergeSettings(cur, in)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		// Judge the configuration the operator will actually get: the values being
		// saved plus their per-field environment fallbacks. Validating `next` by
		// itself would reject a working setup whose key comes from the
		// environment. `next` is still what gets written, so no environment
		// secret is persisted.
		effective := aiproviders.WithEnvFallback(next)
		if _, err := aiproviders.New(effective); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := aiSettingsStore.Set(next); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Report the effective settings, so the dashboard shows that a key is in
		// effect rather than claiming none is set.
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":       true,
			"settings": newSettingsView(effective),
		})

	default:
		w.Header().Set("Allow", "GET, PUT, POST")
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// settingsTestHandler performs one live request against the configured provider
// so the Settings tab can offer a real "Test Connection" button.
//
// It reports failure with HTTP 200 and {"ok":false,...} so the browser can show
// the provider's own error text instead of a generic HTTP error page.
func settingsTestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if aiSettingsStore == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "AI settings are not initialised")
		return
	}

	var in settingsUpdate
	if err := decodeJSON(w, r, &in); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Test exactly what saving would produce: the UI can verify a new key or
	// endpoint before committing it.
	cur, err := aiSettingsStore.Get()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	candidate, err := mergeSettings(cur, in)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	msg, err := aiproviders.TestConnection(ctx, candidate)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg})
}

// --- small JSON helpers shared by the review and settings APIs ---

func writeJSON(w http.ResponseWriter, code int, v any) {
	// Marshal before writing the header so a JSON failure can become a clean 500
	// instead of a truncated 200 body.
	b, err := json.Marshal(v)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "marshal failed"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(b)
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return decodeJSONInner(w, r, v, true)
}

// decodeJSONPermissive is decodeJSON without unknown-field rejection, for
// routes that accept payloads from external tools (/api/push takes a Match
// from any producer): a newer producer's extra field must not become a 400.
// The body cap and trailing-data rejection still apply.
func decodeJSONPermissive(w http.ResponseWriter, r *http.Request, v any) error {
	return decodeJSONInner(w, r, v, false)
}

func decodeJSONInner(w http.ResponseWriter, r *http.Request, v any, strict bool) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		return err
	}
	// Reject trailing garbage after the first JSON value: without this a
	// body of `<valid object><junk>` decodes as the object and the junk is
	// silently ignored.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return errNoTrailingData
	}
	return nil
}
