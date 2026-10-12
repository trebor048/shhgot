package aiproviders_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trebor048/shhgot/aiproviders"
)

// Set validated the environment-backed form but persisted the raw one, so a
// custom provider whose base URL and model came only from SHHGIT_AI_BASE_URL /
// SHHGIT_AI_MODEL passed validation and then failed the save with "requires a
// base URL". The non-secret fields must be filled from the same environment
// fallback before defaults are applied.
func TestStoreSetPersistsEnvBackedCustomProvider(t *testing.T) {
	clearAIEnv(t)
	t.Setenv("SHHGIT_AI_BASE_URL", "http://localhost:12345/v1")
	t.Setenv("SHHGIT_AI_MODEL", "env-model")
	t.Setenv("SHHGIT_AI_CUSTOM_API_KEY", "env-secret-key")

	path := filepath.Join(t.TempDir(), "custom.json")
	store := aiproviders.NewStore(path)

	// The form names the custom provider but carries no endpoint of its own.
	if err := store.Set(aiproviders.Settings{Provider: "custom"}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := store.GetStored()
	if err != nil {
		t.Fatalf("GetStored: %v", err)
	}
	want := aiproviders.Settings{
		Provider: "custom",
		BaseURL:  "http://localhost:12345/v1",
		Model:    "env-model",
	}
	if got != want {
		t.Errorf("GetStored() = %+v, want %+v", got, want)
	}

	// The environment API key is judged for validation but must not be written.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if strings.Contains(string(raw), "env-secret-key") {
		t.Errorf("environment API key was persisted: %s", raw)
	}
}
