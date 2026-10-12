package core

import (
	"path/filepath"
	"testing"
)

// A blacklisted extension that is a multi-dot suffix (".tar.gz") is never the
// result of filepath.Ext, so it used to be dead config; it must still skip.
func TestBlacklistedExtension(t *testing.T) {
	oldMap, oldSuffixes := blacklistExtMap, blacklistExtSuffixes
	blacklistExtMap = map[string]bool{".pem": true, ".tar.gz": true}
	blacklistExtSuffixes = []string{".tar.gz"}
	defer func() { blacklistExtMap, blacklistExtSuffixes = oldMap, oldSuffixes }()

	cases := []struct {
		path string
		want bool
	}{
		{"keys/server.pem", true},
		{"backup/app.tar.gz", true},
		{"src/main.go", false},
		// ".gz" alone is not ".tar.gz": the suffix must match in full.
		{"archive.gz", false},
	}
	for _, c := range cases {
		if got := blacklistedExtension(c.path); got != c.want {
			t.Errorf("blacklistedExtension(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// blacklisted_entropy_extensions may name a bare extension (".pem"), a
// multi-dot suffix (".env.local") or a whole filename ("id_ed25519").
// Comparing only filepath.Ext missed the latter two.
func TestCanCheckEntropySkipsNamedAndMultiDotEntries(t *testing.T) {
	old := session
	session = &Session{Config: &Config{
		BlacklistedEntropyExtensions: []string{".pem", ".env.local", "id_ed25519"},
	}}
	defer func() { session = old }()

	for _, name := range []string{"id_ed25519", ".env.local", "server.pem"} {
		f := MatchFile{Filename: name, Extension: filepath.Ext(name)}
		if f.CanCheckEntropy() {
			t.Errorf("%s: CanCheckEntropy() = true, want false", name)
		}
	}
	f := MatchFile{Filename: "app.go", Extension: ".go"}
	if !f.CanCheckEntropy() {
		t.Error("app.go: CanCheckEntropy() = false, want true")
	}
}
