package aiproviders

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Environment variables consulted by Store.Get, in per-field precedence order.
// The generic names win over the provider-specific ones, matching how the rest
// of shhgit layers an explicit override on top of ambient configuration.
const (
	envProvider = "SHHGIT_AI_PROVIDER"
	envAPIKey   = "SHHGIT_AI_API_KEY"
	envBaseURL  = "SHHGIT_AI_BASE_URL"
	envModel    = "SHHGIT_AI_MODEL"

	envAPIKeyFmt  = "SHHGIT_AI_%s_API_KEY"
	envBaseURLFmt = "SHHGIT_AI_%s_BASE_URL"
	envModelFmt   = "SHHGIT_AI_%s_MODEL"
)

// wellKnownAPIKeys are the third-party variable names an operator is most
// likely to already have exported (shhgit itself documents DEEPSEEK_API_KEY).
// They are consulted only after the SHHGIT_AI_* names, so an explicit shhgit
// setting always wins over ambient tooling configuration.
var wellKnownAPIKeys = map[string][]string{
	ProviderDeepSeek: {"DEEPSEEK_API_KEY", "DEEPSEEK_KEY"},
	ProviderOpenAI:   {"OPENAI_API_KEY", "OPENAI_KEY"},
	ProviderOllama:   {"OLLAMA_API_KEY"},
	ProviderCustom:   {"CUSTOM_API_KEY", "OPENAI_COMPATIBLE_API_KEY"},
}

// Store persists AI settings as a JSON file. Writes go to a temporary file in
// the same directory and are then renamed into place, so a reader never
// observes a half-written file and a crash cannot truncate the real one.
type Store struct {
	path string
}

// NewStore returns a store backed by the JSON file at path.
func NewStore(path string) *Store { return &Store{path: path} }

// Path returns the file the store reads and writes.
func (s *Store) Path() string { return s.path }

// Get returns the saved settings, falling back per field to environment
// variables and then to the built-in provider defaults.
//
// A missing file is not an error: it means "use defaults". A file that exists
// but cannot be read or parsed is reported, because silently ignoring a
// corrupted config would silently change which provider is used.
func (s *Store) Get() (Settings, error) {
	var saved Settings
	raw, err := os.ReadFile(s.path)
	switch {
	case err == nil:
		if uerr := json.Unmarshal(raw, &saved); uerr != nil {
			return Settings{}, fmt.Errorf("parse %s: %w", s.path, uerr)
		}
	case errors.Is(err, fs.ErrNotExist):
		// No persisted settings yet: fall through to env + defaults.
	default:
		return Settings{}, fmt.Errorf("read %s: %w", s.path, err)
	}

	provider := normalizeProvider(saved.Provider)
	if provider == "" {
		provider = normalizeProvider(os.Getenv(envProvider))
	}
	if provider == "" {
		provider = defaultProvider
	}

	eff := Settings{
		Provider: provider,
		APIKey: firstNonEmpty(
			strings.TrimSpace(saved.APIKey),
			envFallback(append([]string{envAPIKey, providerEnvSuffix(envAPIKeyFmt, provider)}, wellKnownAPIKeys[provider]...)...),
		),
		BaseURL: firstNonEmpty(
			strings.TrimSpace(saved.BaseURL),
			envFallback(envBaseURL, providerEnvSuffix(envBaseURLFmt, provider)),
		),
		Model: firstNonEmpty(
			strings.TrimSpace(saved.Model),
			envFallback(envModel, providerEnvSuffix(envModelFmt, provider)),
		),
	}

	// Defaults are applied here rather than through resolve, because a not-yet
	// filled-in API key must still come back with the endpoint and model that
	// would be used, so the settings screen can show them. New enforces the key
	// requirement on top of whatever this returns. The only combination
	// applyDefaults rejects is "custom" with no base URL or model, and in that
	// case the raw values are more useful to the caller than an empty Settings.
	if resolved, rerr := applyDefaults(eff); rerr == nil {
		return resolved, nil
	}
	return eff, nil
}

// Set validates and atomically persists settings: the data is written to a
// temporary file in the same directory with mode 0600 and then renamed over the
// target, so a crash mid-write can never leave a truncated config behind.
//
// Provider defaults are applied before writing, so the stored file always names
// the endpoint and model actually in use.
func (s *Store) Set(v Settings) error {
	if strings.TrimSpace(s.path) == "" {
		return fmt.Errorf("%w: settings store has no path", ErrBadConfig)
	}

	eff, err := resolve(v)
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(eff, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(s.path)+".tmp*")
	if err != nil {
		return fmt.Errorf("create temporary settings file: %w", err)
	}
	tmpName := tmp.Name()
	// Best effort: after a successful rename this removes nothing.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	// The temp file is created 0600 already; this makes the intent explicit and
	// catches a permissive umask on Unix. Windows cannot express mode bits, so a
	// failure there is not fatal.
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}

	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace %s: %w", s.path, err)
	}
	return nil
}

// envFallback returns the first non-empty environment value among names.
func envFallback(names ...string) string {
	for _, name := range names {
		if name == "" {
			continue
		}
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// providerEnvSuffix expands a "<prefix>%s<suffix>" template for a provider id,
// e.g. SHHGIT_AI_%s_API_KEY + "deepseek" -> SHHGIT_AI_DEEPSEEK_API_KEY.
func providerEnvSuffix(format, provider string) string {
	name := strings.ToUpper(normalizeProvider(provider))
	if name == "" {
		return ""
	}
	return fmt.Sprintf(format, name)
}
