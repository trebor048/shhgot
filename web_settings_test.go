package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
		BaseURL:  strptr("https://api.deepseek.com/v1"),
		Model:    strptr("deepseek-reasoner"),
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
		BaseURL:  strptr("http://127.0.0.1:9/v1"),
		Model:    strptr("whatever"),
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
	got := mergeSettings(cur, settingsUpdate{BaseURL: strptr(cur.BaseURL), Model: strptr("deepseek-reasoner")})
	if got.Provider != "deepseek" || got.Model != "deepseek-reasoner" || got.APIKey != "k" {
		t.Fatalf("merge = %+v", got)
	}
}

// Rotating only the API key of a self-hosted gateway must not repoint the reviews
// at the vendor's public endpoint. An omitted field keeps what is stored; only an
// explicit empty string asks for the provider default, which is what the dashboard
// sends when the operator clears the box.
func TestSettingsUpdateDistinguishesOmittedFromEmpty(t *testing.T) {
	st := aiproviders.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	aiSettingsStore = st
	webBindIsLoopback = true
	t.Cleanup(func() { aiSettingsStore = nil })

	seed := aiproviders.Settings{
		Provider: "openai",
		APIKey:   "sk-first",
		BaseURL:  "https://llm.internal.example/v1",
		Model:    "internal-model-v3",
	}
	if err := st.Set(seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A key-only update: base_url and model are absent from the JSON entirely.
	rec := httptest.NewRecorder()
	settingsHandler(rec, localRequest(http.MethodPut, "/api/settings",
		[]byte(`{"provider":"openai","api_key":"sk-rotated"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	got, err := st.Get()
	if err != nil {
		t.Fatalf("store get: %v", err)
	}
	if got.BaseURL != seed.BaseURL {
		t.Errorf("base_url = %q after a key-only update, want the stored %q", got.BaseURL, seed.BaseURL)
	}
	if got.Model != seed.Model {
		t.Errorf("model = %q after a key-only update, want the stored %q", got.Model, seed.Model)
	}
	if got.APIKey != "sk-rotated" {
		t.Errorf("api key = %q, want the rotated one", got.APIKey)
	}

	// An explicit empty string still means "use the provider default", which is
	// what the dashboard sends when the field is cleared.
	rec = httptest.NewRecorder()
	settingsHandler(rec, localRequest(http.MethodPut, "/api/settings",
		[]byte(`{"provider":"openai","base_url":"","model":""}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	got, err = st.Get()
	if err != nil {
		t.Fatalf("store get: %v", err)
	}
	def := aiproviders.Defaults("openai")
	if got.BaseURL != def.BaseURL || got.Model != def.Model {
		t.Errorf("after an explicit blank: base_url=%q model=%q, want the openai defaults %q/%q",
			got.BaseURL, got.Model, def.BaseURL, def.Model)
	}
}

func strptr(s string) *string { return &s }

// An operator who supplies a key through the environment must not find it copied
// into ai_review/settings.json by an unrelated save. Besides putting a secret on
// disk that they deliberately kept out of it, that copy would then outrank the
// environment it came from, so changing the variable would appear to do nothing.
func TestSavingSettingsDoesNotPersistAnEnvironmentKey(t *testing.T) {
	t.Setenv("SHHGIT_AI_API_KEY", "env-only-key")

	st := aiproviders.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	aiSettingsStore = st
	webBindIsLoopback = true
	t.Cleanup(func() { aiSettingsStore = nil })

	// Nothing saved yet, so the environment supplies the key.
	eff, err := st.Get()
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if eff.APIKey != "env-only-key" {
		t.Fatalf("environment key not in effect: %q", eff.APIKey)
	}

	// An unrelated save: only the model changes.
	rec := httptest.NewRecorder()
	settingsHandler(rec, localRequest(http.MethodPut, "/api/settings", []byte(`{"provider":"deepseek","model":"deepseek-chat"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d: %s", rec.Code, rec.Body.String())
	}

	raw, err := os.ReadFile(st.Path())
	if err != nil {
		t.Fatalf("read the saved file: %v", err)
	}
	if strings.Contains(string(raw), "env-only-key") {
		t.Errorf("the environment-provided key was written to %s: %s", st.Path(), raw)
	}

	// The response must still report that a key is in effect, or the dashboard
	// would tell the operator their environment key had been lost.
	var view map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode the save response: %v", err)
	}
	saved, _ := view["settings"].(map[string]any)
	if saved == nil || saved["api_key_set"] != true {
		t.Errorf("save response = %s, want api_key_set true from the environment", rec.Body.String())
	}

	after, err := st.Get()
	if err != nil {
		t.Fatalf("get after the save: %v", err)
	}
	if after.APIKey != "env-only-key" {
		t.Errorf("key = %q, want the environment value to still apply", after.APIKey)
	}
	if after.Model != "deepseek-chat" {
		t.Errorf("model = %q, want the saved value", after.Model)
	}
}

// A key the operator did save must survive a later save that omits it.
func TestSavingSettingsKeepsAStoredKey(t *testing.T) {
	st := settingsEnv(t)

	rec := httptest.NewRecorder()
	settingsHandler(rec, localRequest(http.MethodPut, "/api/settings", []byte(`{"provider":"deepseek","model":"deepseek-reasoner"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d: %s", rec.Code, rec.Body.String())
	}

	after, err := st.Get()
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.APIKey != "sk-super-secret" {
		t.Errorf("key = %q, want the stored key kept", after.APIKey)
	}
	if after.Model != "deepseek-reasoner" {
		t.Errorf("model = %q, want the new value", after.Model)
	}
}

// GetStored backs every save, so it must report a corrupted file rather than
// treating it as empty and silently discarding what the operator had saved.
func TestGetStoredReportsACorruptedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := aiproviders.NewStore(path).GetStored(); err == nil {
		t.Error("a corrupted settings file must be reported, not ignored")
	}

	// A missing file means "nothing stored yet", not an error.
	if got, err := aiproviders.NewStore(filepath.Join(dir, "absent.json")).GetStored(); err != nil {
		t.Errorf("a missing file is not an error: %v", err)
	} else if got.APIKey != "" || got.Model != "" {
		t.Errorf("a missing file returned %+v, want empty settings", got)
	}

	// And it must not invent defaults the way Get does.
	if got, err := aiproviders.NewStore(filepath.Join(dir, "absent.json")).Get(); err != nil {
		t.Fatalf("get: %v", err)
	} else if got.Provider == "" {
		t.Error("Get should still apply the default provider")
	}
}
