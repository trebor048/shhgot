package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/trebor048/shhgot/aiproviders"
)

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
// a secret), an omitted base_url/model keeps the stored endpoint, and an explicit
// empty base_url/model means "use the provider default" rather than "blank it".
func mergeSettings(cur aiproviders.Settings, in settingsUpdate) aiproviders.Settings {
	next := cur
	if p := strings.ToLower(strings.TrimSpace(in.Provider)); p != "" {
		next.Provider = p
		// Switching provider must not inherit the previous provider's endpoint
		// or model; the provider default is the sane starting point.
		if p != strings.ToLower(cur.Provider) {
			d := aiproviders.Defaults(p)
			next.BaseURL = d.BaseURL
			next.Model = d.Model
		}
	}
	if k := strings.TrimSpace(in.APIKey); k != "" {
		next.APIKey = k
	}
	def := aiproviders.Defaults(next.Provider)
	if in.BaseURL != nil {
		if v := strings.TrimSpace(*in.BaseURL); v != "" {
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
	return next
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
		next := mergeSettings(cur, in)

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
	candidate := mergeSettings(cur, in)

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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	return nil
}
