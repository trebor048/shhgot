package core

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestExampleConfigParsesAndHasNoSecrets guards config.yaml.example. It is
// committed, so a real credential checked into it is public. The test also
// proves the shipped template actually parses with the real Config shape.
func TestExampleConfigParsesAndHasNoSecrets(t *testing.T) {
	path := filepath.Join("..", "config.yaml.example")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("example config not available: %v", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("config.yaml.example does not parse as Config: %v", err)
	}
	if len(cfg.Signatures) == 0 {
		t.Fatal("config.yaml.example ships no signatures")
	}

	// Shapes that only occur in a live credential (not in a placeholder).
	real := regexp.MustCompile(
		`github_pat_11[A-Z0-9]{6}` + // fine-grained PAT prefix
			`|ghp_[A-Za-z0-9]{36}(?:$|[^A-Za-z0-9])` + // classic PAT
			`|sk-ant-api03-[A-Za-z0-9_-]{40,}` + // Anthropic key
			`|sk-[a-f0-9]{32}(?:$|[^a-f0-9])` + // OpenAI/DeepSeek-style key
			`|xox[aboprs]-[0-9]{6,}` + // Slack token
			`|discord\.com/api/webhooks/[0-9]{10,}/[A-Za-z0-9_-]{20,}`, // Discord webhook
	)
	if hits := real.FindAllString(string(data), -1); len(hits) > 0 {
		t.Errorf("config.yaml.example appears to contain real credentials: %q", hits)
	}
}
