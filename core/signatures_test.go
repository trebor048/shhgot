package core

import (
	"testing"
)

// notRegexTestSession installs a minimal package session so signature
// matching (which reads session.Config.BlacklistedStrings) works without a
// full scanner session. It returns a restore function.
func notRegexTestSession(t *testing.T) func() {
	t.Helper()
	old := session
	session = &Session{Config: &Config{}, Log: &Logger{}}
	return func() { session = old }
}

func findSignature(t *testing.T, sigs []Signature, name string) Signature {
	t.Helper()
	for _, s := range sigs {
		if s.Name() == name {
			return s
		}
	}
	t.Fatalf("signature %q not loaded", name)
	return nil
}

func contentsFile(contents string) MatchFile {
	return MatchFile{
		Path:     "/tmp/nr-test/secret.env",
		Filename: "secret.env",
		Contents: []byte(contents),
		loaded:   true,
	}
}

// A not_regex guard must suppress placeholder values while letting real
// secrets through - the RE2-compatible replacement for a PCRE (?!...)
// negative lookahead.
func TestNotRegexSuppressesPlaceholders(t *testing.T) {
	defer notRegexTestSession(t)()
	session.Config.Signatures = []ConfigSignature{{
		Name:     "Hardcoded Password / Secret",
		Part:     PartContents,
		Regex:    `(?i)\b(?:password|passwd|pwd|secret|token)\s*[:=]\s*["'][^"'\s]{12,}["']`,
		NotRegex: `(?i)["'](?:test|example|changeme|password|secret|dummy|placeholder|x{4,}|redacted|\$\{)`,
		Priority: 2,
	}}
	sigs := GetSignatures(session)
	if len(sigs) != 1 {
		t.Fatalf("expected 1 signature, got %d", len(sigs))
	}
	sig := sigs[0]

	real := contentsFile(`password = "Xk9#mQ2$vL7pW4zR8tY6"`)
	if got := sig.GetContentsMatches(real); len(got) != 1 {
		t.Errorf("real secret: got %d matches, want 1 (%v)", len(got), got)
	}
	for _, placeholder := range []string{
		`password = "test1234567890"`,
		`password = "changeme-00-1234"`,
		`password = "xxxx-xxxx-xxxx"`,
	} {
		if got := sig.GetContentsMatches(contentsFile(placeholder)); len(got) != 0 {
			t.Errorf("placeholder %q: got %d matches, want 0 (%v)", placeholder, len(got), got)
		}
	}
}

// A path rule's not_regex is tested against the whole path string, mirroring
// a ^(?!.*(?:test|...)) whole-string guard.
func TestNotRegexOnPathRules(t *testing.T) {
	defer notRegexTestSession(t)()
	session.Config.Signatures = []ConfigSignature{{
		Name:     "SSH Private Key File (id_rsa)",
		Part:     PartPath,
		Regex:    `(?i).*\.ssh/id_rsa$`,
		NotRegex: `(?i)test|example|sample|demo`,
		Priority: 3,
	}}
	sig := findSignature(t, GetSignatures(session), "SSH Private Key File (id_rsa)")

	ok, _ := sig.Match(MatchFile{Path: "/home/deploy/.ssh/id_rsa"})
	if !ok {
		t.Errorf("clean key path did not match")
	}
	for _, p := range []string{
		"/home/deploy/test/.ssh/id_rsa",
		"/srv/example/.ssh/id_rsa",
	} {
		if ok, _ := sig.Match(MatchFile{Path: p}); ok {
			t.Errorf("fixture path %q matched, want suppressed", p)
		}
	}
}

// An uncompilable not_regex must skip that rule with a report, not a crash,
// and must not take the other rules down with it.
func TestBadNotRegexSkipsOnlyItsRule(t *testing.T) {
	defer notRegexTestSession(t)()
	session.Config.Signatures = []ConfigSignature{
		{
			Name:     "Good Rule",
			Part:     PartContents,
			Regex:    `AKIA[0-9A-Z]{16}`,
			Priority: 3,
		},
		{
			Name:     "Bad Guard Rule",
			Part:     PartContents,
			Regex:    `password\s*=\s*".+"`,
			NotRegex: `(?P<bad>`,
			Priority: 2,
		},
	}
	sigs := GetSignatures(session)
	if len(sigs) != 1 {
		t.Fatalf("expected 1 usable signature, got %d", len(sigs))
	}
	if sigs[0].Name() != "Good Rule" {
		t.Fatalf("surviving signature = %q, want %q", sigs[0].Name(), "Good Rule")
	}
}

// A signature entry with neither 'match' nor 'regex' is a typo. It must be
// skipped rather than compiled as an empty regex, which matches every string:
// as a contents rule it emitted an empty match at every position of every file,
// and as a path rule it matched every path.
func TestSignatureWithoutMatchOrRegexIsSkipped(t *testing.T) {
	defer notRegexTestSession(t)()
	session.Config.Signatures = []ConfigSignature{
		{
			Name:     "Good Rule",
			Part:     PartContents,
			Regex:    `AKIA[0-9A-Z]{16}`,
			Priority: 3,
		},
		{
			Name: "Typo Rule",
			Part: PartContents,
			// Neither Match nor Regex is set.
		},
	}
	sigs := GetSignatures(session)
	if len(sigs) != 1 {
		t.Fatalf("expected 1 usable signature, got %d", len(sigs))
	}
	if sigs[0].Name() != "Good Rule" {
		t.Fatalf("surviving signature = %q, want %q", sigs[0].Name(), "Good Rule")
	}
}

// Without not_regex nothing changes: every regex hit is returned.
func TestNoNotRegexKeepsOldBehavior(t *testing.T) {
	defer notRegexTestSession(t)()
	session.Config.Signatures = []ConfigSignature{{
		Name:     "Plain Rule",
		Part:     PartContents,
		Regex:    `password\s*=\s*".+"`,
		Priority: 2,
	}}
	sig := findSignature(t, GetSignatures(session), "Plain Rule")
	got := sig.GetContentsMatches(contentsFile(`password = "test1234567890"`))
	if len(got) != 1 {
		t.Errorf("got %d matches, want 1 (%v)", len(got), got)
	}
}

// A matched value that is an obvious template must be dropped even when the
// rule configures no not_regex of its own: .env.example files and CI workflows
// are full of "<password>", "${GH_TOKEN}" and "YOUR_API_KEY", and none of them
// is a leaked secret.
func TestPlaceholderValuesAreNotFindings(t *testing.T) {
	defer notRegexTestSession(t)()
	session.Config.Signatures = []ConfigSignature{{
		Name:     "Credentials in URL (user:pass@host)",
		Part:     PartContents,
		Regex:    `://[^:\s/]+:[^@\s/]{8,}@`,
		Priority: 3,
	}}
	sig := findSignature(t, GetSignatures(session), "Credentials in URL (user:pass@host)")

	for _, placeholder := range []string{
		`://x-access-token:${GH_TOKEN}@`, // CI workflow template
		`://user:{{ api_key }}@`,         // Helm / Jinja template
		`://user:YOUR_PASSWORD@`,         // documentation
		`://user:xxxxxxxxxxxx@`,          // redacted placeholder
	} {
		if got := sig.GetContentsMatches(contentsFile(placeholder)); len(got) != 0 {
			t.Errorf("placeholder %q: got %d matches, want 0 (%v)", placeholder, len(got), got)
		}
	}

	// A real credential in the same shape must still be reported.
	real := contentsFile(`://deployuser:hunter2secret@db.internal.corp.net`)
	if got := sig.GetContentsMatches(real); len(got) != 1 {
		t.Errorf("real credential: got %d matches, want 1 (%v)", len(got), got)
	}
}

// The placeholder guard must also reject values that are code, shell or deploy
// scaffolding rather than credentials. Every one of these was reported as a
// finding before the guard covered it.
func TestCodeAndEnvValuesAreNotFindings(t *testing.T) {
	defer notRegexTestSession(t)()
	session.Config.Signatures = []ConfigSignature{{
		Name:     "Hardcoded Password / Secret",
		Part:     PartContents,
		Regex:    `(?i)\b(?:password|passwd|pwd|secret|token)\s*[:=]\s*["'][^"'\s]{12,}["']`,
		Priority: 2,
	}}
	sig := findSignature(t, GetSignatures(session), "Hardcoded Password / Secret")

	for _, v := range []string{
		`token = "$($discovery.token)"`,                   // PowerShell interpolation
		`token = "$(vault_read_db_token)"`,                // shell command substitution
		`token = "REPLACE_WITH_URL_SAFE_RANDOM_PASSWORD"`, // deploy placeholder
		`secret = "%APP_SECRET_VALUE%"`,                   // Windows env var
		`password = "bp_dev_only_password"`,               // dev-only credential
	} {
		if got := sig.GetContentsMatches(contentsFile(v)); len(got) != 0 {
			t.Errorf("code/env value %q: got %d matches, want 0 (%v)", v, len(got), got)
		}
	}

	real := contentsFile(`password = "Xk9mQ2vL7pW4zR8tY6"`)
	if got := sig.GetContentsMatches(real); len(got) != 1 {
		t.Errorf("real secret: got %d matches, want 1 (%v)", len(got), got)
	}
}

// The PostgreSQL rule must not fire on the "<password>"/"<host>" placeholder
// that ships in almost every .env.example, but must keep real DSNs.
func TestConnectionStringPlaceholderIsNotAFinding(t *testing.T) {
	defer notRegexTestSession(t)()
	session.Config.Signatures = []ConfigSignature{{
		Name:     "PostgreSQL Connection String with Credentials",
		Part:     PartContents,
		Regex:    `(?i)postgres(?:ql)?://[^:\s"']+:[^@\s"']+@[^\s"']+`,
		Priority: 3,
	}}
	sig := findSignature(t, GetSignatures(session), "PostgreSQL Connection String with Credentials")

	placeholder := contentsFile(`DATABASE_URL=postgresql://postgres:<password>@<host>:5432/postgres`)
	if got := sig.GetContentsMatches(placeholder); len(got) != 0 {
		t.Errorf("placeholder DSN: got %d matches, want 0 (%v)", len(got), got)
	}

	real := contentsFile(`DATABASE_URL=postgresql://app_rw:S3cr3tP4ss@db.prod.internal:5432/app`)
	if got := sig.GetContentsMatches(real); len(got) != 1 {
		t.Errorf("real DSN: got %d matches, want 1 (%v)", len(got), got)
	}
}
