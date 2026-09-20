package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fatih/color"
)

// captureStdout runs fn with stdout redirected into a pipe and colour disabled,
// then returns everything fn printed. This mirrors the way a CI transcript or a
// shell pipe sees the scanner: no ANSI colour and no OSC 8 hyperlinks.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	oldNoColor := color.NoColor
	os.Stdout = w
	color.NoColor = true
	t.Setenv("SHHGIT_NO_LINKS", "1")

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	os.Stdout = old
	color.NoColor = oldNoColor

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	_ = r.Close()
	return string(out)
}

func mustContain(t *testing.T, haystack, needle, label string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("%s: output does not contain %q\n--- output ---\n%s", label, needle, haystack)
	}
}

const (
	testRepoURL = "https://github.com/acme/widgets"
	testRepo    = "acme/widgets"
	testFile    = "src/config/secrets.go"
	testFileRef = "src/config/secrets.go:42"
	testLine    = 42
	testSig     = "OpenAI API Key"
	testSecret  = "sk-TESTONLY0123456789abcdef"
)

// Every preset must render every finding field for every match type: repo,
// file:line, signature name, the exact match, the star count and a link whose
// visible label carries file:line. The presets used to differ in which of these
// they dropped - minimal had no line, search had no signature or stars, and
// entropy/file matches hid the star count outside two presets.
func TestLogFormatPresetsPrintEveryFindingField(t *testing.T) {
	for _, preset := range formatOrder {
		t.Run(preset, func(t *testing.T) {
			setLogFormat(preset)

			t.Run("secret", func(t *testing.T) {
				out := captureStdout(t, func() {
					logSecret(1, testRepoURL, testSig, testFile, testSecret, "refs/heads/main", testLine, 7)
				})
				mustContain(t, out, testRepo, "repo")
				mustContain(t, out, testFileRef, "file:line")
				mustContain(t, out, testSig, "signature")
				mustContain(t, out, testSecret, "match")
				mustContain(t, out, "★ 7", "stars")
				mustContain(t, out, "📄 secrets.go:42", "link label")
			})

			t.Run("search", func(t *testing.T) {
				out := captureStdout(t, func() {
					logSearch(1, testRepoURL, testFile, testSecret, "refs/heads/main", testLine, 7)
				})
				mustContain(t, out, testRepo, "repo")
				mustContain(t, out, testFileRef, "file:line")
				mustContain(t, out, searchSignatureName, "signature")
				mustContain(t, out, testSecret, "match")
				mustContain(t, out, "★ 7", "stars")
				mustContain(t, out, "📄 secrets.go:42", "link label")
			})

			t.Run("file", func(t *testing.T) {
				out := captureStdout(t, func() {
					logFile(testRepoURL, "Environment Configuration File", "deploy/.env", "refs/heads/main", 5)
				})
				mustContain(t, out, testRepo, "repo")
				mustContain(t, out, "deploy/.env", "file")
				mustContain(t, out, "Environment Configuration File", "signature")
				mustContain(t, out, "★ 5", "stars")
				mustContain(t, out, "📄 .env", "link label")
			})

			t.Run("entropy", func(t *testing.T) {
				out := captureStdout(t, func() {
					logEntropy(testRepoURL, testFile, "AKIAIOSFODNN7EXAMPLE", "refs/heads/main", testLine, 3)
				})
				mustContain(t, out, testRepo, "repo")
				mustContain(t, out, testFileRef, "file:line")
				mustContain(t, out, entropySignatureName, "signature")
				mustContain(t, out, "AKIAIOSFODNN7EXAMPLE", "match")
				mustContain(t, out, "★ 3", "stars")
				mustContain(t, out, "📄 secrets.go:42", "link label")
			})
		})
	}
}

// The minimal preset is the one CI parses, so it must be exactly one line per
// finding: no separator banners, no multi-line <match> blocks. The other presets
// must not print the match twice either - the old code repeated it in a <match>
// block after already showing it on the detail line.
func TestLogMinimalIsOneLinePerFinding(t *testing.T) {
	setLogFormat(LogFormatMinimal)

	cases := map[string]func(){
		"secret": func() { logSecret(1, testRepoURL, testSig, testFile, testSecret, "main", testLine, 7) },
		"search": func() { logSearch(1, testRepoURL, testFile, testSecret, "main", testLine, 7) },
		"file":   func() { logFile(testRepoURL, testSig, "deploy/.env", "main", 5) },
		"entropy": func() {
			logEntropy(testRepoURL, testFile, "AKIAIOSFODNN7EXAMPLE", "main", testLine, 3)
		},
	}
	for name, fn := range cases {
		out := captureStdout(t, fn)
		if got := strings.Count(out, "\n"); got != 1 {
			t.Errorf("%s: minimal output has %d lines, want 1\n--- output ---\n%s", name, got, out)
		}
	}
}

// No preset should print a redundant <match> block once the match is already on
// its own labelled line.
func TestLogPresetsDoNotDuplicateMatchBlocks(t *testing.T) {
	for _, preset := range formatOrder {
		t.Run(preset, func(t *testing.T) {
			setLogFormat(preset)
			out := captureStdout(t, func() {
				logSecret(1, testRepoURL, testSig, testFile, testSecret, "main", testLine, 7)
			})
			if strings.Contains(out, "<match>") {
				t.Errorf("%s: still emits a <match> block:\n%s", preset, out)
			}
		})
	}
}

// The cosmic preset draws a bordered box around the finding. Its padding used to
// be measured in runes, so every emoji in the title pushed the closing border one
// column out. All three box lines must occupy the same number of terminal cells.
func TestLogCosmicBoxIsWidthSafe(t *testing.T) {
	setLogFormat(LogFormatEvenMoreFancy)
	out := captureStdout(t, func() {
		cosmicBox(cosmicBorder, cosmicTitle, "🔥  🚨 LEGENDARY SECRET BREACH  🔥")
	})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("cosmic box has %d lines, want 3:\n%s", len(lines), out)
	}
	want := displayWidth(lines[0])
	for i, ln := range lines {
		if got := displayWidth(ln); got != want {
			t.Errorf("box line %d width = %d, want %d: %q", i, got, want, ln)
		}
	}
}

// Long/PEM blobs are truncated, not redacted: the head of the value is shown so
// the finding stays identifiable, but everything past the cap is dropped.
func TestSecretMatchTruncatesPemAndLongBlobs(t *testing.T) {
	setLogFormat(LogFormatFancy)
	pem := "-----BEGIN RSA PRIVATE KEY-----\n" +
		strings.Repeat("MIIEowIBAAKCAQEA", 20) +
		"\n-----END RSA PRIVATE KEY-----"
	out := captureStdout(t, func() {
		logSecret(1, testRepoURL, "Private Key", testFile, pem, "main", testLine, 0)
	})
	mustContain(t, out, "[truncated,", "truncation notice")
	// The tail of the blob must not be echoed.
	if strings.Contains(out, "-----END RSA PRIVATE KEY-----") {
		t.Errorf("tail of the private key leaked into the terminal:\n%s", out)
	}
}

// A short match is rendered verbatim - truncation must not alter it.
func TestSecretMatchKeepsShortValuesIntact(t *testing.T) {
	setLogFormat(LogFormatFancy)
	out := captureStdout(t, func() {
		logSecret(1, testRepoURL, "Private Key", testFile, "sk-SHORT-VALUE", "main", testLine, 0)
	})
	mustContain(t, out, "sk-SHORT-VALUE", "short value")
	if strings.Contains(out, "[truncated,") {
		t.Errorf("short value was truncated:\n%s", out)
	}
}

// secretLineNum must resolve the line of the first match. Passing the joined
// summary of several matches (as the old code did) points the link at a line that
// may not exist.
func TestSecretLineNumUsesFirstMatch(t *testing.T) {
	contents := []byte("alpha\nbravo\nsk-SECRET-value\ncharlie\nsk-SECRET-value\n")

	if got := secretLineNum(contents, "sk-SECRET-value"); got != 3 {
		t.Errorf("secretLineNum = %d, want 3", got)
	}
	if got := secretLineNum(contents, "not-present"); got != 0 {
		t.Errorf("secretLineNum(absent) = %d, want 0", got)
	}
	if got := secretLineNum(contents, ""); got != 0 {
		t.Errorf("secretLineNum(empty) = %d, want 0", got)
	}
}

// The link helpers must produce a URL that points at the file and the exact line,
// not at the repository root, and must normalise the branch and path.
func TestLinkURLConstruction(t *testing.T) {
	fileCases := []struct {
		name     string
		branch   string
		file     string
		line     int
		expected string
	}{
		{"main branch with line", "main", "a/b.go", 42, "https://github.com/o/r/blob/main/a/b.go#L42"},
		{"refs/heads is stripped", "refs/heads/dev", "a/b.go", 7, "https://github.com/o/r/blob/dev/a/b.go#L7"},
		{"empty branch defaults to main", "", "a/b.go", 0, "https://github.com/o/r/blob/main/a/b.go"},
		{"backslashes become slashes", "main", `a\b.go`, 0, "https://github.com/o/r/blob/main/a/b.go"},
		{"leading slash is removed", "main", "/a/b.go", 0, "https://github.com/o/r/blob/main/a/b.go"},
	}
	for _, tc := range fileCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := githubFileURL("https://github.com/o/r", tc.file, tc.branch, tc.line); got != tc.expected {
				t.Errorf("githubFileURL = %q, want %q", got, tc.expected)
			}
		})
	}

	// A local finding must link to the file, not to the scanned directory, and
	// the path must be absolute, slash-separated and carry the line fragment.
	root := t.TempDir()
	rel := filepath.Join("sub", "x.go")
	got := localFileURL(root, rel, 9)

	abs, err := filepath.Abs(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("Abs: %v", err)
	}
	if !strings.HasPrefix(got, "file://") {
		t.Errorf("localFileURL = %q, want a file:// URL", got)
	}
	if !strings.Contains(got, filepath.ToSlash(abs)) {
		t.Errorf("localFileURL = %q, want it to contain %q", got, filepath.ToSlash(abs))
	}
	if !strings.HasSuffix(got, "#L9") {
		t.Errorf("localFileURL = %q, want an #L9 fragment", got)
	}
	if strings.Contains(got, "file:////") {
		t.Errorf("localFileURL = %q has a doubled slash after the scheme", got)
	}
	if withNoLine := localFileURL(root, rel, 0); strings.Contains(withNoLine, "#") {
		t.Errorf("localFileURL(line 0) = %q, want no fragment", withNoLine)
	}
}

// Links must degrade to their visible label when disabled by NO_COLOR or
// SHHGIT_NO_LINKS, and formatHyperlink must emit a well-formed OSC 8 sequence.
func TestLinkDegradationAndOSC8(t *testing.T) {
	t.Setenv("SHHGIT_NO_LINKS", "1")
	if linksEnabled() {
		t.Fatal("SHHGIT_NO_LINKS must disable links")
	}
	if got := createClickableLink("https://example.com/x", "label"); got != "label" {
		t.Errorf("disabled createClickableLink = %q, want %q", got, "label")
	}

	t.Setenv("NO_COLOR", "1")
	if linksEnabled() {
		t.Fatal("NO_COLOR must disable links")
	}

	// The escape sequence itself is independent of the terminal check.
	got := formatHyperlink("https://example.com/x", "label")
	want := "\033]8;;https://example.com/x\033\\label\033]8;;\033\\"
	if got != want {
		t.Errorf("formatHyperlink = %q, want %q", got, want)
	}
}

// A console whose virtual-terminal mode could not be enabled cannot render OSC 8,
// so links must fall back to their label even though stdout is a terminal.
func TestLinksDisabledWhenVTUnsupported(t *testing.T) {
	old := terminalVTSupported
	defer func() { terminalVTSupported = old }()

	terminalVTSupported = false
	if linksEnabled() {
		t.Fatal("links must be disabled when the console cannot render OSC 8")
	}
	if got := createClickableLink("https://example.com/x", "label"); got != "label" {
		t.Errorf("createClickableLink = %q, want the plain label", got)
	}

	terminalVTSupported = true
	t.Setenv("NO_COLOR", "1")
	if linksEnabled() {
		t.Fatal("NO_COLOR must still disable links when VT is supported")
	}
}

// A local (non-GitHub) scan must resolve the finding link to the file that holds
// the secret, not to the directory that was scanned.
func TestLocalScanLinkPointsAtFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join("sub", "x.go")

	userRepo, _, fileLink := getEnhancedLinkInfo(root, file, "", 9)

	if userRepo != root {
		t.Errorf("userRepo = %q, want the scanned root %q", userRepo, root)
	}
	if !strings.Contains(fileLink, "x.go:9") {
		t.Errorf("fileLink = %q, want it to carry the file and line", fileLink)
	}
	if strings.TrimSpace(fileLink) == root {
		t.Errorf("fileLink points at the scanned directory %q instead of the file", root)
	}
}

// A file/name match has no line, so its reference must be the path alone - never
// a bogus ":0".
func TestFileMatchLogsSignatureStarsAndLink(t *testing.T) {
	setLogFormat(LogFormatFancy)
	out := captureStdout(t, func() {
		logFile(testRepoURL, "Environment Configuration File", "deploy/.env", "refs/heads/main", 5)
	})
	mustContain(t, out, "Environment Configuration File", "signature")
	mustContain(t, out, "deploy/.env", "file")
	mustContain(t, out, "★ 5", "stars")
	mustContain(t, out, "📄 .env", "link label")
	if strings.Contains(out, ":0") {
		t.Errorf("file match printed a bogus line number:\n%s", out)
	}
}

// Entropy findings must report the pseudo-signature, the line number and the link
// like every other finding type.
func TestEntropyLogsSignatureLineAndLink(t *testing.T) {
	setLogFormat(LogFormatFancy)
	out := captureStdout(t, func() {
		logEntropy(testRepoURL, testFile, "AKIAIOSFODNN7EXAMPLE", "refs/heads/main", testLine, 3)
	})
	mustContain(t, out, entropySignatureName, "signature")
	mustContain(t, out, testFileRef, "file:line")
	mustContain(t, out, "AKIAIOSFODNN7EXAMPLE", "match")
	mustContain(t, out, "📄 secrets.go:42", "link label")
}

// A signature match must render the line it was given (which checkSignatures
// derives from matches[0]) so the link anchor is correct.
func TestSignatureMatchLogsLineNumber(t *testing.T) {
	setLogFormat(LogFormatFancy)
	out := captureStdout(t, func() {
		logSecret(2, testRepoURL, testSig, testFile, testSecret+", sk-SECOND", "main", testLine, 0)
	})
	mustContain(t, out, testFileRef, "file:line")
	mustContain(t, out, "📄 secrets.go:42", "link label")
}

// TestCarriageReturnLinesPinsEveryLineToColumnZero guards the regression where
// the standard logger's lines (regex optimizer, worker pool) were printed
// without a leading carriage return. On a console with
// DISABLE_NEWLINE_AUTO_RETURN set, "\n" does not return the cursor to column 0,
// so those lines started where the previous one ended and the output marched
// across the screen. Every line must now begin with "\r".
func TestCarriageReturnLinesPinsEveryLineToColumnZero(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"single line with newline", "hello\n", "\rhello\n"},
		{"single line without newline", "hello", "\rhello"},
		{"two lines", "a\nb\n", "\ra\n\rb\n"},
		{"trailing partial line", "a\nb", "\ra\n\rb"},
		{"blank line in the middle", "a\n\nb\n", "\ra\n\r\n\rb\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := carriageReturnLines(tc.in); got != tc.want {
				t.Errorf("carriageReturnLines(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestCarriageReturnLinesKeepsExistingPrefixes checks that the helper does not
// damage lines that already carry the core logger's "\r\033[K" prefix - a
// second carriage return is harmless.
func TestCarriageReturnLinesKeepsExistingPrefixes(t *testing.T) {
	got := carriageReturnLines("\r\x1b[Kalready prefixed\n")
	want := "\r\r\x1b[Kalready prefixed\n"
	if got != want {
		t.Errorf("carriageReturnLines() = %q, want %q", got, want)
	}
}
