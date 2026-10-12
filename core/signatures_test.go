package core

import (
	"strings"
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

// The name-keyed rules are built as "<provider>[\w.-]{0,20}\s*[:=]\s*<value>".
// A provider's own name is an ordinary word, so without a guard every one of
// these was reported - and none of them is a credential.
func TestProviderNameAssignmentsAreNotFindings(t *testing.T) {
	for _, v := range []string{
		`accessKey: credential.refresh_token`,
		`accessToken = tokenJson.access_token`,
		`client_id: this.policy.bucket_name`,
		`lambda: _build_engine_and_rank`,
		`public_key = tls_private_key.RSA-KEY.public_key_openssh`,
		`highlighted = highlightedMessageId`,
		`firebase:firebase-crashlytics`,
		`highlightAlerts = HighlightAlertRuntimeBridge`,
		`accessToken = leaseObject.requiredString`,
		`accessToken = lease.accessToken.trim`,
		`accessToken = accessLease.accessToken`,
		`NEXUS_BASE_BRANCH=handoff-continuite-20260920`,
		`api_key = aCamelCase2IdentifierName`,
		`client_secret = config2ValueWithDigits`,
		`client_key = oauth2ClientIdentifierValue`,
		`access_token = a_lowercase_snake_identifier_name`,
		`auth_token = someidentifiername`,
	} {
		if !looksLikeCodeReference(v) {
			t.Errorf("code reference %q not recognised", v)
		}
	}
}

// A real credential must survive the guard: opaque strings, JWTs, connection
// strings, PEM blocks and bare token formats all carry a signal it must not
// reject.
func TestCredentialValuesAreStillFindings(t *testing.T) {
	for _, v := range []string{
		`LAMBDA_API_KEY=AbCd1234EfGh5678IjKl9012`,
		`DATABASE_URL=postgresql://app_rw:S3cr3tP4ss@db.prod.internal:5432/app`,
		`accessToken = "Xk9mQ2vL7pW4zR8tY6ab"`,
		`password = "Xk9#mQ2$vL7pW4zR8tY6"`,
		`client_secret = "AbCd1234EfGh5678IjKl9012"`,
		`AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`,
		`firebase_api_key = AbCd1234EfGh5678IjKl9012`,
		`db = "postgres://user:pass@host/db"`,
		`Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig`,
		`-----BEGIN RSA PRIVATE KEY-----`,
		// Split so GitHub Push Protection does not read this synthetic fixture as
		// a live Stripe key; the value the test observes is unchanged.
		"sk_" + "live_51H8xkQAbCdEfGhIjKlMnOpQrStUvWx",
		`https://hooks.slack.com/services/T00000000/B00000000/XXXXXXXXXXXXXXXX`,
		`o123.ingest.sentry.io`,
		`api_key = "Zq7Wm2Rk9Yt4Pv6Ld3Hs8Cx1Nf5Jb0Gz"`,
		`token = Xk9mQ2vL7pW4zR8tY6ab`,
		`secret_key = AbCd9876EfGh5432IjKl2109MnOpQrSt`,
		`api_key = 9f86d081884c7d659a2feaa0c55ad015`,
	} {
		if looksLikeCodeReference(v) {
			t.Errorf("credential %q wrongly classified as code", v)
		}
	}
}

// End to end: the broad name-keyed rules that produced the false positives must
// return nothing for code, while still reporting a real credential.
func TestBroadNameKeyedRulesIgnoreCode(t *testing.T) {
	defer notRegexTestSession(t)()
	session.Config.Signatures = []ConfigSignature{
		{
			Name:     "Generic Access Key / Access Token / Auth Token",
			Part:     PartContents,
			Regex:    `(?i)\b(?:access[_-]?key|accessKey|access[_-]?token|accessToken|auth[_-]?token|authToken)\s*[:=]\s*["']?[A-Za-z0-9_.\-]{20,}`,
			Priority: 2,
		},
		{
			Name:     "Lambda Labs API Key",
			Part:     PartContents,
			Regex:    `(?i)\blambda[\w.-]{0,20}\s*[:=]\s*["']?[a-zA-Z0-9_-]{20,}\b`,
			Priority: 3,
		},
		{
			Name:     "Highlight.io API Key",
			Part:     PartContents,
			Regex:    `(?i)\bhighlight[\w.-]{0,20}\s*[:=]\s*["']?[A-Za-z0-9_-]{20,}\b`,
			Priority: 2,
		},
	}
	sigs := GetSignatures(session)

	code := "accessToken = leaseObject.requiredString\n" +
		"lambda: _build_engine_and_rank\n" +
		"highlighted = highlightedMessageId\n"
	for _, s := range sigs {
		if got := s.GetContentsMatches(contentsFile(code)); len(got) != 0 {
			t.Errorf("%s: code produced %d matches, want 0 (%v)", s.Name(), len(got), got)
		}
	}

	real := "accessToken = \"Xk9mQ2vL7pW4zR8tY6ab\"\n" +
		"lambda_api_key = AbCd1234EfGh5678IjKl9012\n" +
		"highlight_api_key = ZzYy1234XxWw5678VvUu9012\n"
	for _, s := range sigs {
		if got := s.GetContentsMatches(contentsFile(real)); len(got) != 1 {
			t.Errorf("%s: real credential produced %d matches, want 1 (%v)", s.Name(), len(got), got)
		}
	}
}

// A connection string pointing at loopback, a private range or a
// documentation host is a dev-example placeholder, not a production leak. The
// rule under test carries no not_regex of its own, so this proves the global
// code guard in signatures.go - which also covers a drifted live config that
// never received the per-rule guards.
func TestNonRoutableHostsAreNotFindings(t *testing.T) {
	defer notRegexTestSession(t)()
	session.Config.Signatures = []ConfigSignature{{
		Name:     "Database Connection String with Credentials",
		Part:     PartContents,
		Regex:    `(postgres(ql)?|mysql|mongodb(\+srv)?)://[^\s:@/]+:[^\s:@/]+@[^\s"'\),;]+`,
		Priority: 3,
	}}
	sig := findSignature(t, GetSignatures(session), "Database Connection String with Credentials")

	for _, placeholder := range []string{
		`DATABASE_URL=postgresql://postgres:password@localhost:5432/app`,
		`DATABASE_URL=postgresql://app:S3cr3tP4ss@LOCALHOST:5432/app`,
		`DATABASE_URL=postgresql://app:S3cr3tP4ss@127.0.0.1:5432/app`,
		`DATABASE_URL=mysql://root:Adm1nStr0ng!@0.0.0.0:3306/app`,
		`DATABASE_URL=postgresql://app:S3cr3tP4ss@10.0.0.5:5432/app`,
		`DATABASE_URL=postgresql://app:S3cr3tP4ss@172.16.0.5:5432/app`,
		`DATABASE_URL=postgresql://app:S3cr3tP4ss@172.31.255.1:5432/app`,
		`DATABASE_URL=postgresql://app:S3cr3tP4ss@192.168.1.5:5432/app`,
		`DATABASE_URL=postgresql://app:S3cr3tP4ss@[::1]:5432/app`,
		`DATABASE_URL=postgresql://app:S3cr3tP4ss@db.local:5432/app`,
		`DATABASE_URL=postgresql://app:S3cr3tP4ss@example.com:5432/app`,
		`DATABASE_URL=postgresql://app:S3cr3tP4ss@host.docker.internal:5432/app`,
	} {
		if got := sig.GetContentsMatches(contentsFile(placeholder)); len(got) != 0 {
			t.Errorf("non-routable DSN %q: got %d matches, want 0 (%v)", placeholder, len(got), got)
		}
	}

	for _, real := range []string{
		`DATABASE_URL=postgresql://app_rw:S3cr3tP4ss@db.prod.internal:5432/app`,
		`DATABASE_URL=postgresql://app_rw:S3cr3tP4ss@pg-1.eu-west-1.rds.amazonaws.com:5432/app`,
	} {
		if got := sig.GetContentsMatches(contentsFile(real)); len(got) != 1 {
			t.Errorf("routable DSN %q: got %d matches, want 1 (%v)", real, len(got), got)
		}
	}
}

// A password-named assignment holding a vendor default
// (`db_password = "password123"`) documents nothing worth stealing. Only the
// whole value counts, and only for password-named targets.
func TestWeakPasswordValuesAreNotFindings(t *testing.T) {
	defer notRegexTestSession(t)()
	session.Config.Signatures = []ConfigSignature{{
		Name:     "Database Password in Quotes",
		Part:     PartContents,
		Regex:    `(db_password|database_password)\s*[:=]\s*["'][^"']{8,}["']`,
		Priority: 3,
	}}
	sig := findSignature(t, GetSignatures(session), "Database Password in Quotes")

	for _, weak := range []string{
		`db_password = "password"`,
		`db_password = "Password123"`,
		`database_password: "12345678"`,
		`db_password = "administrator"`,
	} {
		if got := sig.GetContentsMatches(contentsFile(weak)); len(got) != 0 {
			t.Errorf("weak password %q: got %d matches, want 0 (%v)", weak, len(got), got)
		}
	}

	// A near-miss is still a finding: only the entire value counts.
	for _, real := range []string{
		`db_password = "Xk9#mQ2$vL7pW4zR8tY6"`,
		`db_password = "password12345"`,
		`db_password = "my-password-123"`,
	} {
		if got := sig.GetContentsMatches(contentsFile(real)); len(got) != 1 {
			t.Errorf("real password %q: got %d matches, want 1 (%v)", real, len(got), got)
		}
	}

	// The guard is scoped to password-named targets: the same value under a
	// token name is unaffected.
	if !isWeakPasswordValue(`db_password = "admin"`) {
		t.Errorf(`isWeakPasswordValue("db_password = \"admin\"") = false, want true`)
	}
	if isWeakPasswordValue(`api_token = "password123"`) {
		t.Errorf(`isWeakPasswordValue("api_token = \"password123\"") = true, want false`)
	}
	if isWeakPasswordValue(`db_password = "password12345"`) {
		t.Errorf(`isWeakPasswordValue("db_password = \"password12345\"") = true, want false (whole value only)`)
	}
}

// Bare placeholder words in a value (fake, mock, sample, example, todo,
// dummy) are documentation, not leaks. They live in the code-level
// placeholder guard because blacklisted_strings must hold complete values
// only - a bare word there would suppress any match merely containing it.
func TestPlaceholderWordsAreNotFindings(t *testing.T) {
	defer notRegexTestSession(t)()
	session.Config.Signatures = []ConfigSignature{{
		Name:     "Hardcoded Token",
		Part:     PartContents,
		Regex:    `(?i)\btoken\s*[:=]\s*["'][^"'\s]{12,}["']`,
		Priority: 2,
	}}
	sig := findSignature(t, GetSignatures(session), "Hardcoded Token")

	for _, placeholder := range []string{
		`token = "fake-key-1234567890ab"`,
		`token = "mock-secret-abcdef123456"`,
		`token = "sample-token-1234567890ab"`,
		`token = "example-key-1234567890ab"`,
		`token = "todo-replace-this-key-123"`,
		`token = "dummy-value-1234567890ab"`,
	} {
		if got := sig.GetContentsMatches(contentsFile(placeholder)); len(got) != 0 {
			t.Errorf("placeholder word %q: got %d matches, want 0 (%v)", placeholder, len(got), got)
		}
	}

	real := contentsFile(`token = "Xk9mQ2vL7pW4zR8tY6ab"`)
	if got := sig.GetContentsMatches(real); len(got) != 1 {
		t.Errorf("real token: got %d matches, want 1 (%v)", len(got), got)
	}
}

// Every shipped GitHub token signature name must pass IsTokenType, otherwise
// its matches never reach AddGitHubToken (no validation, no pooling, no
// priority3.log entry). The fine-grained PAT rule ("GitHub Fine-Grained PAT")
// has no "token" in its name and used to fail the gate.
func TestIsTokenTypeCoversAllGitHubTokenNames(t *testing.T) {
	defer notRegexTestSession(t)()
	session.Config.Signatures = []ConfigSignature{
		{Name: "GitHub Fine-Grained PAT", Part: PartContents, Regex: `github_pat_[0-9a-zA-Z_]{10}`},
		{Name: "GitHub OAuth Token", Part: PartContents, Regex: `gho_[0-9a-zA-Z]{10}`},
		{Name: "GitHub Personal Access Token", Part: PartContents, Regex: `ghp_[A-Za-z0-9]{10}`},
		{Name: "GitHub Refresh Token", Part: PartContents, Regex: `ghr_[0-9a-zA-Z]{10}`},
		{Name: "GitHub Server-to-Server Token", Part: PartContents, Regex: `ghs_[0-9a-zA-Z]{10}`},
		{Name: "GitHub User-to-Server Token", Part: PartContents, Regex: `ghu_[0-9a-zA-Z]{10}`},
		{Name: "GitLab Runner Registration Token", Part: PartContents, Regex: `glrt-[A-Za-z0-9_-]{10}`},
	}
	sigs := GetSignatures(session)
	if len(sigs) != 7 {
		t.Fatalf("expected 7 signatures, got %d", len(sigs))
	}
	for _, s := range sigs {
		want := s.Name() != "GitLab Runner Registration Token"
		if got := s.IsTokenType(); got != want {
			t.Errorf("%s: IsTokenType() = %v, want %v", s.Name(), got, want)
		}
	}
}

// The credential-name guard must not discard a match merely because the
// variable is named after the format it holds. A card number, an IBAN, a SWIFT
// code, a routing/account/sort identifier or a certificate assignment is a
// credential, not a code reference. Regression: the name whitelist omitted
// these nouns, so the shipped signatures for them matched and were then
// silently dropped by the global guard.
func TestFormatKeywordsAreNotCodeReferences(t *testing.T) {
	for _, v := range []string{
		`iban = "DE89370400440532013000"`,
		`swift_code = "DEUTDEFF"`,
		`bic = "DEUTDEFF500"`,
		`card_number: 4111111111111111`,
		`credit_card = "5500005555555559"`,
		`cvv: 123`,
		`pan: "378282246310005"`,
		`routing_number = 021000021`,
		`account_number = "000123456789"`,
		`acct_no = 12345678`,
		`sort_code = "20-00-00"`,
		`bsb: 062000`,
		`ifsc = HDFC0001234`,
		`clabe: 032180000118359719`,
		// A trailing "z" keeps the 32-char run out of [0-9a-f], so GitHub's
		// push-protection Twilio rule (AC[0-9a-f]{32}) does not flag this
		// fixture; the digit density still makes it a credential, not a word.
		`twilio_sid = "AC0123456789abcdef0123456789abcdez"`,
		`DefaultEndpointsProtocol=https;AccountName=x;AccountKey=AbCd1234567890==`,
		`certificate = "-----BEGIN CERTIFICATE-----"`,
		`jwt: "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig"`,
		`username: "admin" password: "Xk9mQ2vL7pW4zR8tY6"`,
	} {
		if looksLikeCodeReference(v) {
			t.Errorf("format keyword %q wrongly classified as code", v)
		}
	}
}

// TestInvalidAITokenMatchIsStillReported guards the verifier gate. A live
// AI-token signature whose provider rejects the key must still yield the match:
// the credential is a leaked secret whether or not it is still live, and
// dropping it here made GetContentsMatches return nothing, so the token never
// reached the terminal, the dashboard, the TUI or the CSVs - the operator saw
// only the "token is INVALID" validation line. The verdict must instead travel
// as the verifier outcome (severity low, verified=false).
func TestInvalidAITokenMatchIsStillReported(t *testing.T) {
	defer notRegexTestSession(t)()
	tv := NewTokenValidator(nil)
	// Every provider endpoint answers 401, so the key is definitively invalid.
	tv.httpClient = &countingDoer{status: 401, body: `{"error":{"message":"authentication failed"}}`}
	session.TokenValidator = tv
	session.Config.Signatures = []ConfigSignature{{
		Name:     "OpenAI API Key",
		Part:     PartContents,
		Regex:    `\bsk-proj-[A-Za-z0-9_-]{40,}\b`,
		Verifier: "openai",
		Priority: 3,
	}}
	sigs := GetSignatures(session)
	if len(sigs) != 1 {
		t.Fatalf("expected 1 signature, got %d", len(sigs))
	}

	token := "sk-proj-" + strings.Repeat("a", 48)
	got := sigs[0].GetContentsMatches(contentsFile("OPENAI_API_KEY=" + token))
	if len(got) != 1 || got[0] != token {
		t.Fatalf("invalid AI token was dropped: got %v, want the match kept", got)
	}

	// And the rejection must still be recorded for severity classification.
	tv.mutex.Lock()
	res, ok := tv.testedTokens[token]
	tv.mutex.Unlock()
	if !ok || res.valid {
		t.Fatalf("verdict not recorded or not invalid: ok=%v valid=%v", ok, res.valid)
	}
}
