package main

import (
	"testing"
	"time"
)

// TestHumanDurationDays verifies a whole-day duration is labelled in days, not
// hours (the digest window default is 24h, which previously printed "1h").
func TestHumanDurationDays(t *testing.T) {
	cases := map[time.Duration]string{
		24 * time.Hour:   "1d",
		48 * time.Hour:   "2d",
		2 * time.Hour:    "2h",
		90 * time.Minute: "90m",
	}
	for in, want := range cases {
		if got := humanDuration(in); got != want {
			t.Errorf("humanDuration(%s) = %q, want %q", in, got, want)
		}
	}
}

// TestContainsWord verifies short AI-provider names only match whole words, so a
// signature named "Example API Key" is not treated as an "exa" AI token.
func TestContainsWord(t *testing.T) {
	yes := []struct{ hay, needle string }{
		{"exa api key", "exa"},
		{"xai key", "xai"},
		{"openai api key", "openai"},
		{"cohere key", "cohere"},
		{"you.com api key", "you.com"},
	}
	for _, c := range yes {
		if !containsWord(c.hay, c.needle) {
			t.Errorf("containsWord(%q, %q) = false, want true", c.hay, c.needle)
		}
	}
	no := []struct{ hay, needle string }{
		{"example api key", "exa"},
		{"paying attention", "yi"},
		{"tokenkey", "key"},
	}
	for _, c := range no {
		if containsWord(c.hay, c.needle) {
			t.Errorf("containsWord(%q, %q) = true, want false", c.hay, c.needle)
		}
	}
}

// TestScanModeArgsHonoursTerminator verifies a mode flag after "--" is treated
// as a positional argument, matching Go's flag package.
func TestScanModeArgsHonoursTerminator(t *testing.T) {
	afterTerminator := &ModeConfig{Mode: ModeDefault}
	scanModeArgs([]string{"prog", "--", "--web"}, afterTerminator)
	if afterTerminator.Mode != ModeDefault {
		t.Fatalf("mode = %v, want ModeDefault: --web after -- must not select web", afterTerminator.Mode)
	}

	selected := &ModeConfig{Mode: ModeDefault}
	scanModeArgs([]string{"prog", "--web"}, selected)
	if selected.Mode != ModeWeb {
		t.Fatalf("mode = %v, want ModeWeb", selected.Mode)
	}
}
