package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trebor048/shhgot/aiproviders"
)

// settingsEnv installs a settings store for one test.
func settingsEnv(t *testing.T) *aiproviders.Store {
	t.Helper()
	st := aiproviders.NewStore(t.TempDir() + "/settings.json")
	if err := st.Set(aiproviders.Settings{
		Provider: "deepseek",
		APIKey:   "sk-super-secret",
		BaseURL:  "https://api.deepseek.com/v1",
		Model:    "deepseek-chat",
	}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	aiSettingsStore = st
	webBindIsLoopback = true
	t.Cleanup(func() { aiSettingsStore = nil })
	return st
}

func TestSettingsGetNeverLeaksTheKey(t *testing.T) {
	settingsEnv(t)

	rec := httptest.NewRecorder()
	settingsHandler(rec, localRequest(http.MethodGet, "/api/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	body := rec.Body.String()
	if strings.Contains(body, "sk-super-secret") {
		t.Fatal("the settings response leaked the API key")
	}

	var got settingsView
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.APIKeySet {
		t.Error("api_key_set should be true when a key is stored")
	}
	if got.Provider != "deepseek" || got.Model != "deepseek-chat" {
		t.Errorf("provider/model = %q/%q", got.Provider, got.Model)
	}
	if len(got.Providers) != 4 {
		t.Errorf("providers = %v, want the four supported backends", got.Providers)
	}
	for _, p := range []string{"deepseek", "openai", "custom", "ollama"} {
		d, ok := got.Defaults[p]
		if !ok {
			t.Errorf("defaults are missing %q", p)
			continue
		}
		if d.APIKey != "" {
			t.Errorf("defaults for %q carry an api key", p)
		}
	}
	if got.Defaults["deepseek"].BaseURL != "https://api.deepseek.com/v1" {
		t.Errorf("deepseek default base url = %q", got.Defaults["deepseek"].BaseURL)
	}
	if got.Defaults["openai"].BaseURL != "https://api.openai.com/v1" {
		t.Errorf("openai default base url = %q", got.Defaults["openai"].BaseURL)
	}
	if got.Defaults["ollama"].BaseURL != "http://localhost:11434" {
		t.Errorf("ollama default base url = %q", got.Defaults["ollama"].BaseURL)
	}
}

func TestSettingsPutBlankKeyKeepsTheStoredOne(t *testing.T) {
	store := settingsEnv(t)

	body, _ := json.Marshal(settingsUpdate{
		Provider: "deepseek",
		APIKey:   "", // the dashboard cannot echo the key back, so blank means "keep"
		BaseURL:  "https://api.deepseek.com/v1",
		Model:    "deepseek-reasoner",
	})
	rec := httptest.NewRecorder()
	settingsHandler(rec, localRequest(http.MethodPut, "/api/settings", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-super-secret") {
		t.Fatal("the save response leaked the API key")
	}

	got, err := store.Get()
	if err != nil {
		t.Fatalf("store get: %v", err)
	}
	if got.APIKey != "sk-super-secret" {
		t.Fatalf("api key = %q, want the stored key to be preserved", got.APIKey)
	}
	if got.Model != "deepseek-reasoner" {
		t.Fatalf("model = %q, want the new model", got.Model)
	}
}

func TestSettingsPutRejectsUnknownProvider(t *testing.T) {
	store := settingsEnv(t)

	body, _ := json.Marshal(settingsUpdate{Provider: "not-a-provider"})
	rec := httptest.NewRecorder()
	settingsHandler(rec, localRequest(http.MethodPut, "/api/settings", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown provider", rec.Code)
	}

	// The stored settings must be untouched by a rejected save.
	got, _ := store.Get()
	if got.Provider != "deepseek" {
		t.Fatalf("provider = %q, want the previous value to survive", got.Provider)
	}
}

func TestSettingsPutCustomRequiresBaseURLAndModel(t *testing.T) {
	settingsEnv(t)

	body, _ := json.Marshal(settingsUpdate{Provider: "custom"})
	rec := httptest.NewRecorder()
	settingsHandler(rec, localRequest(http.MethodPut, "/api/settings", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 when custom has no base url", rec.Code)
	}
}

func TestSettingsSwitchingProviderResetsEndpointAndModel(t *testing.T) {
	store := settingsEnv(t)

	// The dashboard prefills the new provider's defaults; even if it sends
	// blanks, the previous provider's endpoint must not linger.
	body, _ := json.Marshal(settingsUpdate{Provider: "openai", APIKey: "sk-openai"})
	rec := httptest.NewRecorder()
	settingsHandler(rec, localRequest(http.MethodPut, "/api/settings", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	got, err := store.Get()
	if err != nil {
		t.Fatalf("store get: %v", err)
	}
	if got.Provider != "openai" {
		t.Fatalf("provider = %q", got.Provider)
	}
	if strings.Contains(got.BaseURL, "deepseek") {
		t.Fatalf("base url = %q, still the previous provider's endpoint", got.BaseURL)
	}
	if got.Model == "deepseek-chat" {
		t.Fatalf("model = %q, still the previous provider's model", got.Model)
	}
	if got.APIKey != "sk-openai" {
		t.Fatalf("api key = %q, want the newly supplied key", got.APIKey)
	}
}

func TestSettingsMethodNotAllowed(t *testing.T) {
	settingsEnv(t)

	rec := httptest.NewRecorder()
	settingsHandler(rec, localRequest(http.MethodDelete, "/api/settings", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow == "" {
		t.Error("405 responses should advertise Allow")
	}
}

func TestSettingsUnavailableBeforeInit(t *testing.T) {
	aiSettingsStore = nil
	t.Cleanup(func() { aiSettingsStore = nil })

	rec := httptest.NewRecorder()
	settingsHandler(rec, localRequest(http.MethodGet, "/api/settings", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when AI review is not initialised", rec.Code)
	}
}

func TestSettingsTestConnectionReportsFailureAsJSON(t *testing.T) {
	settingsEnv(t)

	// Point at a port that is closed, so the attempt fails fast and locally
	// without depending on the network or a real provider.
	body, _ := json.Marshal(settingsUpdate{
		Provider: "custom",
		BaseURL:  "http://127.0.0.1:9/v1",
		Model:    "whatever",
	})
	rec := httptest.NewRecorder()
	settingsTestHandler(rec, localRequest(http.MethodPost, "/api/settings/test", body))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with an ok:false body", rec.Code)
	}
	var got struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if got.OK {
		t.Fatalf("ok = true for an unreachable endpoint (%+v)", got)
	}
	if got.Error == "" {
		t.Error("a failed test should explain why")
	}
}

func TestSettingsTestRejectsGet(t *testing.T) {
	settingsEnv(t)

	rec := httptest.NewRecorder()
	settingsTestHandler(rec, localRequest(http.MethodGet, "/api/settings/test", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestMergeSettingsKeepsProviderOnRename(t *testing.T) {
	cur := aiproviders.Settings{Provider: "deepseek", APIKey: "k", BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat"}

	// An empty provider in the body must mean "keep the current one".
	got := mergeSettings(cur, settingsUpdate{BaseURL: cur.BaseURL, Model: "deepseek-reasoner"})
	if got.Provider != "deepseek" || got.Model != "deepseek-reasoner" || got.APIKey != "k" {
		t.Fatalf("merge = %+v", got)
	}
}
