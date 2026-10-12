package core

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestExampleConfigRejectsLocalhostDSNs guards the shipped config against the
// false-positive class the operator reported: a connection string pointing at
// localhost / 127.0.0.1 / ::1 is not a leak. Every connection-string rule must
// carry a not_regex that discards those, while still matching a real remote DSN.
func TestExampleConfigRejectsLocalhostDSNs(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "config.yaml.example"))
	if err != nil {
		t.Skipf("example config not available: %v", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse config.yaml.example: %v", err)
	}

	localDSNs := []string{
		`DATABASE_URL=postgresql://app_rw:S3cr3tP4ss@localhost:5432/app`,
		`DATABASE_URL=postgresql://app_rw:S3cr3tP4ss@127.0.0.1:5432/app`,
		`db = "mysql://root:pw@[::1]:3306/db"`,
	}
	remoteDSN := `DATABASE_URL=postgresql://app_rw:S3cr3tP4ss@db.prod.internal:5432/app`

	checked := 0
	for _, s := range cfg.Signatures {
		if !strings.Contains(s.Name, "Connection String") {
			continue
		}
		re, err := regexp.Compile(s.Regex)
		if err != nil {
			t.Fatalf("%s: bad regex: %v", s.Name, err)
		}
		var notRe *regexp.Regexp
		if s.NotRegex != "" {
			if notRe, err = regexp.Compile(s.NotRegex); err != nil {
				t.Fatalf("%s: bad not_regex: %v", s.Name, err)
			}
		}
		for _, in := range localDSNs {
			m := re.FindString(in)
			if m == "" {
				continue
			}
			if notRe == nil || !notRe.MatchString(m) {
				t.Errorf("%s: matched a localhost DSN %q (matched %q); it must be rejected", s.Name, in, m)
			}
		}
		// The rule must still fire on a real remote DSN.
		if m := re.FindString(remoteDSN); m != "" {
			if notRe != nil && notRe.MatchString(m) {
				t.Errorf("%s: rejected a remote DSN %q (matched %q)", s.Name, remoteDSN, m)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no connection-string rule matched the remote DSN; the rules are over-broad")
	}
}

// TestExampleConfigHasNoAWSAccessKeyIDRule guards the operator's request to stop
// matching bare AWS access-key IDs (the rule fired on identifiers like
// AIDAQEBAQAAAAAAAAAAA, which are not secrets on their own).
func TestExampleConfigHasNoAWSAccessKeyIDRule(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "config.yaml.example"))
	if err != nil {
		t.Skipf("example config not available: %v", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse config.yaml.example: %v", err)
	}
	for _, s := range cfg.Signatures {
		if s.Name == "AWS Access Key ID" {
			t.Fatalf("rule %q must be removed; it matches bare access-key IDs", s.Name)
		}
		if s.Regex != "" && strings.Contains(s.Regex, "AIDA") && strings.Contains(s.Regex, "AKIA") {
			t.Fatalf("rule %q still matches bare AWS access-key IDs", s.Name)
		}
	}
}
