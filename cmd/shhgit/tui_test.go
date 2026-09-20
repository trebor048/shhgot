package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/trebor048/shhgot/reviewstore"
)

// ─── Test harness ───────────────────────────────────────────────────────────
//
// The render path is split so that a frame is a pure function of a tuiView plus
// a width and a height. These helpers build those views from an isolated buffer
// (never the process-wide one, except where a test is explicitly about the
// public API) and drive the real key decoder + key handler.

type tuiHarness struct {
	t     *testing.T
	b     *tuiBuffer
	ui    *tuiUI
	dec   *tuiKeyDecoder
	v     *tuiView
	w, h  int
	color bool
}

func newTUIHarness(t *testing.T, w, h int) *tuiHarness {
	t.Helper()
	hx := &tuiHarness{
		t:   t,
		b:   newTUIBuffer(),
		ui:  &tuiUI{started: time.Now()},
		dec: &tuiKeyDecoder{},
		w:   w,
		h:   h,
	}
	hx.snapshot()
	return hx
}

func (h *tuiHarness) snapshot() {
	h.v = h.ui.view(h.b, h.w, h.h, h.color)
}

func (h *tuiHarness) frame() string { return renderTUIView(h.v) }

// send feeds bytes through the real key decoder and handler, then re-snapshots
// the view the way the render loop would.
func (h *tuiHarness) send(seq string) {
	h.t.Helper()
	h.feed(seq)
	// A bare ESC (or a truncated sequence) only resolves on the timer flush.
	if h.apply(h.dec.flush()) {
		h.t.Fatalf("input %q unexpectedly asked the UI to quit", seq)
	}
	h.snapshot()
}

// feed applies bytes without resolving a pending sequence, which is what a test
// needs when it feeds a multi-byte rune one byte at a time.
func (h *tuiHarness) feed(seq string) {
	h.t.Helper()
	for i := 0; i < len(seq); i++ {
		if h.apply(h.dec.feed(seq[i])) {
			h.t.Fatalf("input %q unexpectedly asked the UI to quit", seq)
		}
	}
}

// quit reports whether seq makes the UI quit.
func (h *tuiHarness) quit(seq string) bool {
	h.t.Helper()
	for i := 0; i < len(seq); i++ {
		if h.apply(h.dec.feed(seq[i])) {
			return true
		}
	}
	return h.apply(h.dec.flush())
}

func (h *tuiHarness) apply(keys []tuiKey) bool {
	for _, k := range keys {
		if h.ui.handleKey(k, h.v) {
			return true
		}
	}
	return false
}

func (h *tuiHarness) push(m *TUIMatch) {
	h.b.addMatch(m)
	h.snapshot()
}

func tuiTestMatch(id, repo, file string, line int, secret, sig string, prio int, ts time.Time) *TUIMatch {
	return &TUIMatch{
		ID:        id,
		Signature: sig,
		Source:    "GitHub",
		URL:       "https://github.com/" + repo,
		Repo:      repo,
		File:      file,
		Link:      "https://github.com/" + repo + "/blob/main/" + file + fmt.Sprintf("#L%d", line),
		Secret:    secret,
		Matches:   []string{secret},
		Line:      line,
		Priority:  prio,
		Stars:     12,
		Timestamp: ts,
	}
}

// frameLines splits a frame into its rendered lines with the trailing
// erase-to-end-of-line removed. Requires a cursor-home prefix, which is also
// what makes the frame flicker-free (no full-screen clear per frame).
func tuiFrameLines(t *testing.T, frame string) []string {
	t.Helper()
	if frame == "" {
		return nil
	}
	if !strings.HasPrefix(frame, tuiCursorHome) {
		t.Fatalf("frame does not start with cursor-home: %q", tuiClampBytes(frame, 40))
	}
	parts := strings.Split(strings.TrimPrefix(frame, tuiCursorHome), "\r\n")
	for i, p := range parts {
		parts[i] = strings.TrimSuffix(p, tuiEraseLine)
	}
	return parts
}

// assertFrameWidths checks the invariants every frame must hold: valid UTF-8, no
// replacement characters (a split rune would produce one), the expected row
// count, and no line wider than the terminal - an over-wide line wraps and
// shears the layout.
func assertFrameWidths(t *testing.T, frame string, width, height int) {
	t.Helper()
	if !utf8.ValidString(frame) {
		t.Fatalf("frame is not valid UTF-8")
	}
	if strings.ContainsRune(frame, utf8.RuneError) {
		t.Errorf("frame contains U+FFFD: a multi-byte rune was split during truncation")
	}
	lines := tuiFrameLines(t, frame)
	if len(lines) != height {
		t.Errorf("frame has %d lines, want %d", len(lines), height)
	}
	for i, ln := range lines {
		if got := tuiDisplayWidth(ln); got > width {
			t.Errorf("line %d is %d cells wide, want at most %d: %q", i, got, width, ln)
		}
	}
}

// ─── Frame rendering ────────────────────────────────────────────────────────

func TestTUIViewRendersPushedMatch(t *testing.T) {
	h := newTUIHarness(t, 120, 30)
	h.push(tuiTestMatch("m1", "acme/webapp", "internal/config/db.go", 42,
		"ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ012345", "GitHub Personal Access Token", 3, time.Now()))

	frame := h.frame()
	for _, want := range []string{"acme/webapp", "db.go:42", "GitHub Personal Access Token", "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ012345", "CRIT"} {
		if !strings.Contains(frame, want) {
			t.Errorf("frame is missing %q\n%s", want, tuiFrameLines(t, frame)[4])
		}
	}
	assertFrameWidths(t, frame, 120, 30)
}

func TestTUIViewHeaderCountersAndStatus(t *testing.T) {
	h := newTUIHarness(t, 160, 30)
	now := time.Now()
	h.push(tuiTestMatch("a", "o/r", "a.go", 1, "s", "sig-a", 3, now))
	h.push(tuiTestMatch("b", "o/r", "b.go", 2, "s", "sig-b", 3, now))
	h.push(tuiTestMatch("c", "o/r", "c.go", 3, "s", "sig-c", 2, now))
	h.push(tuiTestMatch("d", "o/r", "d.go", 4, "s", "sig-d", 1, now))
	h.push(tuiTestMatch("e", "o/r", "e.go", 5, "s", "sig-e", 0, now))
	h.b.setStatus(TUIStatus{
		Message:           "scanning acme/webapp",
		ReposScanned:      17,
		RequestsRemaining: 4821,
		RateLimitReset:    time.Now().Add(12 * time.Minute),
	})
	h.snapshot()

	frame := h.frame()
	for _, want := range []string{
		"shhgit", "findings 5",
		"crit 2", "high 1", "med 1", "low 1",
		"repos 17", "api 4821",
		"scanning acme/webapp",
		"rate limit resets in",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("header is missing %q", want)
		}
	}
}

func TestTUIViewHeadingsAreChangedByTabSwitching(t *testing.T) {
	h := newTUIHarness(t, 120, 30)
	h.push(tuiTestMatch("m1", "o/r", "a.go", 7, "tok", "sig", 2, time.Now()))
	h.b.addToken("ghp_deadbeefcafe", true, "github")
	h.b.addLog("cloning acme/webapp")
	h.b.setReview("m1", "This token is live.", false)
	h.snapshot()

	if f := h.frame(); !strings.Contains(f, "FINDINGS") {
		t.Fatalf("findings tab is not the default tab:\n%s", f)
	}

	h.send("2")
	if f := h.frame(); !strings.Contains(f, "TOKENS") || strings.Contains(f, "FINDINGS  1 of 1") {
		t.Errorf("tab 2 did not switch to the tokens body:\n%s", f)
	}

	h.send("3")
	if f := h.frame(); !strings.Contains(f, "LOGS") || !strings.Contains(f, "cloning acme/webapp") {
		t.Errorf("tab 3 did not switch to the logs body:\n%s", f)
	}

	h.send("4")
	f := h.frame()
	if !strings.Contains(f, "1-4 or Tab switch tabs") || !strings.Contains(f, "KEYS ON THE FINDINGS TAB") {
		t.Errorf("tab 4 did not switch to the help body:\n%s", f)
	}
	if strings.Contains(f, "cloning acme/webapp") {
		t.Errorf("help tab still shows log content")
	}

	// Tab wraps from Help back to Findings.
	h.send("\t")
	if f := h.frame(); !strings.Contains(f, "FINDINGS") {
		t.Errorf("Tab did not wrap back to the findings tab")
	}
	// ? toggles back to the previous tab.
	h.send("?")
	if f := h.frame(); !strings.Contains(f, "KEYS ON THE FINDINGS TAB") {
		t.Errorf("? did not open help")
	}
	h.send("?")
	if f := h.frame(); !strings.Contains(f, "DETAIL") {
		t.Errorf("? did not toggle back out of help")
	}
}

func TestTUIViewDetailShowsLinkAsHyperlinkWithPlainFallback(t *testing.T) {
	m := tuiTestMatch("m1", "o/r", "a.go", 7, "tok", "sig", 1, time.Now())
	h := newTUIHarness(t, 120, 30)
	h.push(m)

	plain := h.frame()
	if !strings.Contains(plain, m.Link) {
		t.Errorf("colourless frame must show the link as plain text")
	}
	if strings.Contains(plain, "\033]8;;") {
		t.Errorf("colourless frame must not emit OSC-8 hyperlinks")
	}

	h.color = true
	h.snapshot()
	coloured := h.frame()
	if !strings.Contains(coloured, "\033]8;;"+m.Link+"\033\\") {
		t.Errorf("coloured frame must wrap the link in an OSC-8 hyperlink")
	}
	if !strings.Contains(coloured, m.Link) {
		t.Errorf("OSC-8 hyperlink must still show the URL as its text")
	}
	// A URL carrying control bytes must not be able to terminate the sequence early.
	h.b.addMatch(&TUIMatch{ID: "evil", Signature: "sig", Repo: "o/r", File: "a.go",
		Link: "https://evil.example/\x1b]8;;\x07", Secret: "s", Priority: 1, Timestamp: time.Now()})
	h.ui.sel = 0
	h.snapshot()
	if f := h.frame(); strings.Contains(f, "evil.example/\x1b]8;;\x07") {
		t.Errorf("control bytes leaked from a link into the frame")
	}
}

func TestTUIViewRedactsPrivateKeySecrets(t *testing.T) {
	pem := "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQ\n-----END OPENSSH PRIVATE KEY-----"
	h := newTUIHarness(t, 120, 30)
	h.push(tuiTestMatch("m1", "o/r", "id_rsa", 1, pem, "Private Key", 3, time.Now()))

	frame := h.frame()
	if strings.Contains(frame, "-----BEGIN") || strings.Contains(frame, "b3BlbnNzaC1rZXk") {
		t.Errorf("private key body was printed to the terminal")
	}
	if !strings.Contains(frame, "REDACTED") {
		t.Errorf("redacted private key should be marked as REDACTED:\n%s", frame)
	}

	// The same rule applies to an oversized blob secret.
	long := strings.Repeat("A1b2C3d4", 40)
	h.push(tuiTestMatch("m2", "o/r", "long.go", 3, long, "Generic API Key", 2, time.Now()))
	frame = h.frame()
	if strings.Contains(frame, long) {
		t.Errorf("a secret longer than 200 characters was printed")
	}
	if !strings.Contains(frame, "REDACTED") {
		t.Errorf("oversized secret should be marked as REDACTED")
	}
}

func TestTUIViewFilterNarrowsTheList(t *testing.T) {
	h := newTUIHarness(t, 120, 30)
	now := time.Now()
	h.push(tuiTestMatch("m1", "acme/alpha", "alpha.go", 1, "tok-alpha", "AWS Access Key", 2, now))
	h.push(tuiTestMatch("m2", "acme/beta", "beta.go", 2, "tok-beta", "Slack Token", 2, now))
	h.push(tuiTestMatch("m3", "acme/gamma", "gamma.go", 3, "tok-gamma", "Stripe Key", 2, now))

	if f := h.frame(); !strings.Contains(f, "3 of 3") {
		t.Fatalf("expected all three findings unfiltered:\n%s", f)
	}

	// Live preview while typing.
	h.send("/beta")
	if f := h.frame(); !strings.Contains(f, "filter: beta") || !strings.Contains(f, "1 of 3") {
		t.Errorf("typing a filter must narrow the list live:\n%s", f)
	}
	if f := h.frame(); strings.Contains(f, "alpha.go") || strings.Contains(f, "gamma.go") {
		t.Errorf("filtered out findings are still listed")
	}
	if got := len(h.v.matches); got != 1 {
		t.Fatalf("filtered list has %d entries, want 1", got)
	}

	// Enter commits it.
	h.send("\r")
	if h.ui.filtering {
		t.Errorf("Enter must leave filter input mode")
	}
	if f := h.frame(); !strings.Contains(f, "beta.go") {
		t.Errorf("committed filter lost the matching finding:\n%s", f)
	}

	// c clears it.
	h.send("c")
	if f := h.frame(); !strings.Contains(f, "3 of 3") || !strings.Contains(f, "gamma.go") {
		t.Errorf("c must clear the filter and restore the list:\n%s", f)
	}

	// Esc cancels an in-progress edit and restores the committed filter.
	h.send("/gamma\r")
	h.send("/zzz")
	if !strings.Contains(h.frame(), "0 of 3") {
		t.Errorf("live preview of a non-matching filter should show nothing")
	}
	h.send("\x1b")
	if h.ui.filtering || h.ui.input != h.ui.filter {
		t.Errorf("Esc must cancel the filter edit (filtering=%v input=%q filter=%q)", h.ui.filtering, h.ui.input, h.ui.filter)
	}
	if f := h.frame(); !strings.Contains(f, "gamma.go") {
		t.Errorf("Esc must restore the previously applied filter:\n%s", f)
	}

	// A filter is case-insensitive and matches on the secret text too.
	h.send("c")
	h.send("/TOK-ALPHA\r")
	if f := h.frame(); !strings.Contains(f, "alpha.go") || strings.Contains(f, "beta.go") {
		t.Errorf("filter should be case-insensitive and match the secret:\n%s", f)
	}
}

func TestTUIViewSelectionClampsAtBothEnds(t *testing.T) {
	h := newTUIHarness(t, 120, 30)
	base := time.Now()
	// Newest first: newest.go is added last, so it must be selected first.
	h.push(tuiTestMatch("m1", "o/r", "oldest.go", 1, "s1", "sig-1", 1, base))
	h.push(tuiTestMatch("m2", "o/r", "middle.go", 2, "s2", "sig-2", 1, base.Add(time.Second)))
	h.push(tuiTestMatch("m3", "o/r", "newest.go", 3, "s3", "sig-3", 1, base.Add(2*time.Second)))

	if f := h.frame(); !strings.Contains(f, "newest.go") {
		t.Fatalf("the newest finding should be selected by default:\n%s", f)
	}
	if h.v.sel != 0 {
		t.Fatalf("initial selection = %d, want 0", h.v.sel)
	}

	for i := 0; i < 10; i++ {
		h.send("j")
	}
	if h.v.sel != 2 {
		t.Fatalf("selection = %d after 10 down presses, want it clamped to 2", h.v.sel)
	}
	if f := h.frame(); !strings.Contains(f, "oldest.go") {
		t.Errorf("clamped selection should show the oldest finding in the detail pane:\n%s", f)
	}

	for i := 0; i < 10; i++ {
		h.send("k")
	}
	if h.v.sel != 0 {
		t.Fatalf("selection = %d after 10 up presses, want it clamped to 0", h.v.sel)
	}
	if f := h.frame(); !strings.Contains(f, "newest.go") {
		t.Errorf("clamped selection should show the newest finding again:\n%s", f)
	}

	// g / G jump to the ends; PgDn never runs past the end either.
	h.send("G")
	if h.v.sel != 2 {
		t.Errorf("G selected %d, want 2", h.v.sel)
	}
	h.send("\x1b[6~") // PgDn
	if h.v.sel != 2 {
		t.Errorf("PgDn pushed the selection past the end (%d)", h.v.sel)
	}
	h.send("g")
	if h.v.sel != 0 {
		t.Errorf("g selected %d, want 0", h.v.sel)
	}
	h.send("\x1b[5~") // PgUp
	if h.v.sel != 0 {
		t.Errorf("PgUp pushed the selection before the start (%d)", h.v.sel)
	}
}

func TestTUIViewTokensAndLogsTabs(t *testing.T) {
	h := newTUIHarness(t, 120, 30)
	h.b.addToken("ghp_live_token_value", true, "GitHub")
	h.b.addToken("sk-proj-deadbeef", false, "OpenAI")
	h.b.addLog("first log line")
	h.b.addLog("second log line with ERROR inside")
	h.snapshot()

	h.send("2")
	f := h.frame()
	for _, want := range []string{"TOKENS  2 validated", "valid 1", "invalid 1", "ghp_live_token_value", "sk-proj-deadbeef", "GitHub", "OpenAI"} {
		if !strings.Contains(f, want) {
			t.Errorf("tokens tab is missing %q:\n%s", want, f)
		}
	}
	assertFrameWidths(t, f, 120, 30)

	h.send("3")
	f = h.frame()
	for _, want := range []string{"LOGS", "first log line", "second log line with ERROR inside", "following"} {
		if !strings.Contains(f, want) {
			t.Errorf("logs tab is missing %q:\n%s", want, f)
		}
	}

	// Scrolling back must be visible and G returns to the tail.
	h.send("k")
	if f := h.frame(); !strings.Contains(f, "scrolled back") {
		t.Errorf("scrolling back must be indicated:\n%s", f)
	}
	h.send("G")
	if f := h.frame(); !strings.Contains(f, "following") {
		t.Errorf("G must return to the tail of the log:\n%s", f)
	}
}

func TestTUIViewRendersReviewInTheDetailPane(t *testing.T) {
	h := newTUIHarness(t, 120, 40)
	h.push(tuiTestMatch("m1", "acme/repo", "cfg.go", 12, "ghp_secret_value", "GitHub PAT", 2, time.Now()))

	if f := h.frame(); strings.Contains(f, "AI REVIEW") {
		t.Fatalf("no review should be shown before one arrives")
	}
	h.b.setReview("m1", "", true)
	h.snapshot()
	if f := h.frame(); !strings.Contains(f, "AI REVIEW") || !strings.Contains(f, "running") {
		t.Errorf("a running review must be announced:\n%s", f)
	}
	h.b.setReview("m1", "This is a live token: rotate it now.", false)
	h.snapshot()
	f := h.frame()
	if !strings.Contains(f, "rotate it now") {
		t.Errorf("final review text must appear in the detail pane:\n%s", f)
	}
	if strings.Contains(f, "running") {
		t.Errorf("a finished review must not still look like it is running")
	}
	// A review for a different finding must not leak into this one.
	h.b.setReview("other-id", "unrelated review", false)
	h.snapshot()
	if f := h.frame(); strings.Contains(f, "unrelated review") {
		t.Errorf("review of a non-selected finding leaked into the detail pane")
	}
	// Clearing it removes the section.
	h.b.setReview("m1", "", false)
	h.snapshot()
	if f := h.frame(); strings.Contains(f, "AI REVIEW") {
		t.Errorf("clearing a review must remove the section")
	}
}

func TestTUIViewTinyTerminalShowsTooSmallMessage(t *testing.T) {
	h := newTUIHarness(t, 40, 10)
	h.push(tuiTestMatch("m1", "acme/repo", "cfg.go", 12, "ghp_secret", "GitHub PAT", 3, time.Now()))

	frame := h.frame()
	if !strings.Contains(strings.ToLower(frame), "too small") {
		t.Fatalf("a tiny terminal must render the too-small message:\n%s", frame)
	}
	if strings.Contains(frame, "[1] Findings") || strings.Contains(frame, "DETAIL") {
		t.Errorf("a tiny terminal must not render a partial layout:\n%s", frame)
	}
	assertFrameWidths(t, frame, 40, 10)

	// Just below the minimum, and absurdly small, must both stay rune-safe.
	for _, sz := range [][2]int{{59, 14}, {20, 5}, {8, 1}, {1, 1}} {
		hh := newTUIHarness(t, sz[0], sz[1])
		hh.push(tuiTestMatch("m1", "所有者/日本語リポジトリ", "ディレクトリ/秘密鍵.go", 3, "ghp_日本語トークン", "秘密の署名", 3, time.Now()))
		assertFrameWidths(t, hh.frame(), sz[0], sz[1])
	}
}

func TestTUIViewTruncationIsRuneSafeAtNarrowWidths(t *testing.T) {
	// Direct helper checks with a multi-byte value.
	wide := "所有者/日本語リポジトリ"
	for w := 1; w <= 20; w++ {
		got := tuiTruncate(wide, w)
		if !utf8.ValidString(got) || strings.ContainsRune(got, utf8.RuneError) {
			t.Fatalf("tuiTruncate(%q, %d) = %q split a rune", wide, w, got)
		}
		if cells := tuiDisplayWidth(got); cells > w {
			t.Fatalf("tuiTruncate(%q, %d) = %q is %d cells wide", wide, w, got, cells)
		}
	}
	// A combining mark must not be separated from its base rune in a way that
	// produces invalid UTF-8 (the mark itself is zero-width).
	combining := "e\u0301tude-café-übersicht"
	for w := 1; w <= 24; w++ {
		if got := tuiTruncate(combining, w); !utf8.ValidString(got) {
			t.Fatalf("tuiTruncate(%q, %d) produced invalid UTF-8", combining, w)
		}
	}

	// Frames at every tab, with multi-byte content everywhere, must fit exactly.
	h := newTUIHarness(t, 60, 15)
	h.push(tuiTestMatch("m1", "所有者/日本語リポジトリ", "ディレクトリ/秘密鍵とトークン.go", 42,
		"ghp_日本語トークン値です", "日本語の署名パターン", 3, time.Now()))
	h.b.addLog("スキャン中: 所有者/日本語リポジトリ")
	h.b.addToken("ghp_日本語トークン", true, "GitHub")
	h.b.setReview("m1", "このトークンは有効です。今すぐローテーションしてください。", false)

	for _, size := range [][2]int{{60, 15}, {61, 16}, {80, 24}, {120, 30}, {200, 50}} {
		hh := newTUIHarness(t, size[0], size[1])
		hh.push(tuiTestMatch("m1", "所有者/日本語リポジトリ", "ディレクトリ/秘密鍵とトークン.go", 42,
			"ghp_日本語トークン値です", "日本語の署名パターン", 3, time.Now()))
		hh.b.addLog("スキャン中: 所有者/日本語リポジトリ")
		hh.b.addToken("ghp_日本語トークン", true, "GitHub")
		hh.b.setReview("m1", "このトークンは有効です。", false)
		hh.snapshot()
		for tab := 0; tab < tuiTabCount; tab++ {
			hh.ui.tab = tab
			hh.snapshot()
			assertFrameWidths(t, hh.frame(), size[0], size[1])
		}
	}
}

func TestTUIViewFrameIsBufferedAndFlickerFree(t *testing.T) {
	h := newTUIHarness(t, 100, 20)
	h.push(tuiTestMatch("m1", "o/r", "a.go", 1, "s", "sig", 1, time.Now()))
	frame := h.frame()

	if strings.Contains(frame, "\033[2J") {
		t.Errorf("frames must not clear the whole screen")
	}
	if got := strings.Count(frame, tuiEraseLine); got != 20 {
		t.Errorf("frame has %d per-line erases, want one per row (20)", got)
	}
	if got := strings.Count(frame, tuiCursorHome); got != 1 {
		t.Errorf("frame must home the cursor exactly once, got %d", got)
	}
	if strings.Contains(frame, "\n") && !strings.Contains(frame, "\r\n") {
		t.Errorf("raw-mode frames must use CRLF")
	}
	if strings.Contains(strings.ReplaceAll(frame, "\r\n", ""), "\n") {
		t.Errorf("frame contains a bare LF, which raw mode does not return from")
	}
	// Every line of a full layout is padded to exactly the terminal width, so a
	// redraw overwrites the previous frame cell for cell.
	for i, ln := range tuiFrameLines(t, frame) {
		if got := tuiDisplayWidth(ln); got != 100 {
			t.Errorf("line %d has %d cells, want exactly 100: %q", i, got, ln)
		}
	}
}

func TestTUIViewFooterFollowsTheActiveTab(t *testing.T) {
	h := newTUIHarness(t, 160, 20)
	h.push(tuiTestMatch("m1", "o/r", "a.go", 1, "s", "sig", 1, time.Now()))
	if f := h.frame(); !strings.Contains(f, "o open link") {
		t.Errorf("findings footer must list the findings keys")
	}
	h.send("3")
	if f := h.frame(); !strings.Contains(f, "G bottom") || strings.Contains(f, "o open link") {
		t.Errorf("logs footer must list the log keys")
	}
}

func TestTUIQuitKeyAndCtrlC(t *testing.T) {
	h := newTUIHarness(t, 100, 20)
	if h.quit("q") != true {
		t.Errorf("q must quit")
	}
	h2 := newTUIHarness(t, 100, 20)
	if h2.quit("\x03") != true {
		t.Errorf("Ctrl-C must quit")
	}
	h3 := newTUIHarness(t, 100, 20)
	if h3.quit("Z") != false {
		t.Errorf("an ordinary key must not quit")
	}
	// While filtering, typed characters are input, not commands.
	h4 := newTUIHarness(t, 100, 20)
	h4.send("/")
	if h4.quit("q") {
		t.Errorf("q typed into the filter must be treated as input")
	}
	if h4.ui.input != "q" {
		t.Errorf("filter input = %q, want %q", h4.ui.input, "q")
	}
}

// ─── Input decoding ─────────────────────────────────────────────────────────

func TestTUIKeyDecoderSequences(t *testing.T) {
	cases := []struct {
		in   string
		want []tuiKeyKind
	}{
		{"j", []tuiKeyKind{tuiKeyRune}},
		{"\r", []tuiKeyKind{tuiKeyEnter}},
		{"\x7f", []tuiKeyKind{tuiKeyBackspace}},
		{"\t", []tuiKeyKind{tuiKeyTab}},
		{"\x03", []tuiKeyKind{tuiKeyCtrlC}},
		{"\x1b[A", []tuiKeyKind{tuiKeyUp}},
		{"\x1b[B", []tuiKeyKind{tuiKeyDown}},
		{"\x1b[C", []tuiKeyKind{tuiKeyRight}},
		{"\x1b[D", []tuiKeyKind{tuiKeyLeft}},
		{"\x1bOA", []tuiKeyKind{tuiKeyUp}},
		{"\x1b[5~", []tuiKeyKind{tuiKeyPgUp}},
		{"\x1b[6~", []tuiKeyKind{tuiKeyPgDn}},
		{"\x1b[1;5A", []tuiKeyKind{tuiKeyUp}},
		{"\x1b[Z", []tuiKeyKind{tuiKeyBackTab}},
		{"日", []tuiKeyKind{tuiKeyRune}}, // multi-byte rune assembled from bytes
	}
	for _, tc := range cases {
		d := &tuiKeyDecoder{}
		var got []tuiKeyKind
		for i := 0; i < len(tc.in); i++ {
			for _, k := range d.feed(tc.in[i]) {
				got = append(got, k.kind)
			}
		}
		for _, k := range d.flush() {
			got = append(got, k.kind)
		}
		if len(got) != len(tc.want) {
			t.Errorf("decode(%q) produced %d keys, want %d", tc.in, len(got), len(tc.want))
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("decode(%q)[%d] = %v, want %v", tc.in, i, got[i], tc.want[i])
			}
		}
	}

	// A bare ESC only resolves on the flush; a partial CSI is dropped.
	d := &tuiKeyDecoder{}
	if keys := d.feed(0x1b); len(keys) != 0 {
		t.Errorf("ESC alone must wait for the rest of the sequence")
	}
	if !d.pending() {
		t.Errorf("a pending ESC must be reported as pending")
	}
	if keys := d.flush(); len(keys) != 1 || keys[0].kind != tuiKeyEsc {
		t.Errorf("flush after ESC = %v, want a single Esc key", keys)
	}
	if d.pending() {
		t.Errorf("flush must clear the pending state")
	}
	if keys := d.feed(0x1b); len(keys) != 0 {
		t.Errorf("ESC must not resolve immediately")
	}
	if keys := d.feed('['); len(keys) != 0 {
		t.Errorf("CSI introducer must not resolve yet")
	}
	if keys := d.flush(); len(keys) != 0 {
		t.Errorf("a truncated CSI sequence must be dropped, got %v", keys)
	}

	// A UTF-8 rune must survive being fed one byte at a time.
	d2 := &tuiKeyDecoder{}
	var out []rune
	for _, by := range []byte("日") {
		for _, k := range d2.feed(by) {
			out = append(out, k.r)
		}
	}
	if len(out) != 1 || out[0] != '日' {
		t.Errorf("multi-byte input decoded to %q, want 日", string(out))
	}
}

func TestTUIApplyKeysTypesUnicodeFilterInput(t *testing.T) {
	h := newTUIHarness(t, 100, 20)
	h.push(tuiTestMatch("m1", "所有者/リポジトリ", "秘密鍵.go", 1, "ghp_x", "sig", 1, time.Now()))

	// "秘密" arrives as six bytes and must reach the filter input intact.
	h.send("/")
	for _, by := range []byte("秘密") {
		h.feed(string([]byte{by})) // one raw byte at a time
	}
	if h.ui.input != "秘密" {
		t.Fatalf("filter input = %q, want %q", h.ui.input, "秘密")
	}
	h.send("\r")
	if f := h.frame(); !strings.Contains(f, "秘密鍵.go") {
		t.Errorf("filtering on multi-byte input should still match:\n%s", f)
	}

	// Backspace must remove a whole rune, not a byte.
	h.send("/")
	h.send("\x7f")
	if h.ui.input != "秘" {
		t.Errorf("backspace produced %q, want %q", h.ui.input, "秘")
	}
}

// ─── Buffer semantics ───────────────────────────────────────────────────────

func TestTUIBufferDropsOldestWhenFull(t *testing.T) {
	b := newTUIBuffer()
	for i := 0; i < tuiMaxMatches+50; i++ {
		b.addMatch(&TUIMatch{ID: fmt.Sprintf("m%d", i), Signature: "sig", File: "f.go", Priority: i % 4, Timestamp: time.Now()})
	}
	for i := 0; i < tuiMaxLogs+300; i++ {
		b.addLog(fmt.Sprintf("line %d", i))
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.matches) != tuiMaxMatches {
		t.Errorf("match buffer = %d entries, want it capped at %d", len(b.matches), tuiMaxMatches)
	}
	if len(b.logs) != tuiMaxLogs {
		t.Errorf("log buffer = %d entries, want it capped at %d", len(b.logs), tuiMaxLogs)
	}
	if b.dropped != 50 {
		t.Errorf("dropped counter = %d, want 50", b.dropped)
	}
	if b.total != tuiMaxMatches+50 {
		t.Errorf("total findings = %d, want %d (the counter must survive eviction)", b.total, tuiMaxMatches+50)
	}
	if got := b.matches[0].match.ID; got != "m50" {
		t.Errorf("oldest surviving match = %q, want m50", got)
	}
	if got := b.matches[len(b.matches)-1].match.ID; got != fmt.Sprintf("m%d", tuiMaxMatches+49) {
		t.Errorf("newest match is not the last entry (%q)", got)
	}
}

func TestTUIMatchesAreCopiedOnInsert(t *testing.T) {
	b := newTUIBuffer()
	m := &TUIMatch{ID: "m1", Signature: "first", Repo: "o/r", File: "a.go", Secret: "s1", Priority: 1, Matches: []string{"s1"}, Timestamp: time.Now()}
	b.addMatch(m)
	// The scanner is free to reuse its event struct after publishing it.
	m.Signature = "mutated"
	m.File = "mutated.go"
	m.Matches[0] = "mutated"

	ui := &tuiUI{}
	frame := renderTUIView(ui.view(b, 120, 30, false))
	if strings.Contains(frame, "mutated") {
		t.Errorf("the UI kept a reference to the caller's struct:\n%s", frame)
	}
	if !strings.Contains(frame, "first") || !strings.Contains(frame, "a.go") {
		t.Errorf("the finding was not recorded as pushed:\n%s", frame)
	}
}

func TestTUIEmptyStateRendersWithoutFindings(t *testing.T) {
	h := newTUIHarness(t, 80, 20)
	f := h.frame()
	if !strings.Contains(f, "no findings yet") {
		t.Errorf("empty findings tab should explain itself:\n%s", f)
	}
	if !strings.Contains(f, "waiting for the scanner") {
		t.Errorf("empty status line should explain itself:\n%s", f)
	}
	assertFrameWidths(t, f, 80, 20)
}

// ─── Public API ─────────────────────────────────────────────────────────────

// TestTUIPublicAPIBuffersBeforeStart covers the "pushed before StartTUI is
// visible once it starts, and calls after it returns are still safe" contract,
// plus the redaction and review integration of the exported entry points.
func TestTUIPublicAPIBuffersBeforeStart(t *testing.T) {
	AddTUIMatch(&TUIMatch{
		ID:        "tui-global-marker",
		Signature: "TUI-GLOBAL-SIGNATURE",
		Source:    "Gist",
		URL:       "https://gist.github.com/abc/def",
		Repo:      "gist/abc",
		File:      "creds/tui-global-marker.env",
		Link:      "https://gist.github.com/abc/def#file-tui-global-marker-env",
		Secret:    "ghp_TUIGLOBALMARKERVALUE",
		Matches:   []string{"ghp_TUIGLOBALMARKERVALUE"},
		Line:      9,
		Priority:  3,
		Stars:     4,
		Timestamp: time.Now(),
	})
	AddTUILog("tui-global-log-line")
	AddTUIToken("ghp_tui_global_token", true, "GitHub")
	SetTUIStatus(TUIStatus{Message: "tui-global-status", ReposScanned: 3, RequestsRemaining: 99})
	SetTUIReview("tui-global-marker", "tui-global-review", false)

	// Whatever was pushed before the UI starts must be there in the first frame.
	ui := &tuiUI{started: time.Now()}
	v := ui.view(tuiState, 160, 40, false)
	frame := renderTUIView(v)
	for _, want := range []string{"TUI-GLOBAL-SIGNATURE", "gist/abc", "marker.env:9", "ghp_TUIGLOBALMARKERVALUE", "tui-global-status", "repos 3", "api 99", "tui-global-review"} {
		if !strings.Contains(frame, want) {
			t.Errorf("buffered finding is missing %q from the first frame", want)
		}
	}
	ui.tab = tuiTabLogs
	if f := renderTUIView(ui.view(tuiState, 160, 40, false)); !strings.Contains(f, "tui-global-log-line") {
		t.Errorf("a log line pushed before StartTUI is missing from the logs tab")
	}
	ui.tab = tuiTabTokens
	if f := renderTUIView(ui.view(tuiState, 160, 40, false)); !strings.Contains(f, "ghp_tui_global_token") {
		t.Errorf("a token pushed before StartTUI is missing from the tokens tab")
	}

	// Producers must stay usable and non-blocking after the UI is gone.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			AddTUIMatch(&TUIMatch{ID: fmt.Sprintf("after-%d", i), File: "a.go", Signature: "sig", Priority: 1, Timestamp: time.Now()})
			AddTUILog("after the UI returned")
			AddTUIToken("tok", false, "x")
			SetTUIStatus(TUIStatus{Message: "done"})
			SetTUIReview(fmt.Sprintf("after-%d", i), "r", false)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("producers blocked after the UI returned")
	}
}

func TestTUIAvailableIsFalseWithoutATTY(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "tui-available-*")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer f.Close()

	oldOut := tuiStdout
	tuiStdout = f
	defer func() { tuiStdout = oldOut }()

	if TUIAvailable() {
		t.Errorf("TUIAvailable must be false when stdout is not a terminal")
	}
	if tuiColorEnabled(f) {
		t.Errorf("colour must be disabled when stdout is not a terminal")
	}
}

// TestStartTUIErrorsOnNonTerminalStdout proves StartTUI refuses to run against
// a non-terminal instead of writing escape sequences into a pipe or a file, and
// that it does so before touching anything.
func TestStartTUIErrorsOnNonTerminalStdout(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "tui-notty-*")
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	defer f.Close()

	oldOut, oldIn := tuiStdout, tuiStdin
	tuiStdout, tuiStdin = f, os.Stdin
	defer func() { tuiStdout, tuiStdin = oldOut, oldIn }()

	err = StartTUI()
	if err == nil {
		t.Fatalf("StartTUI must fail when stdout is not a terminal")
	}
	if !strings.Contains(err.Error(), "not a terminal") {
		t.Errorf("error should say what is wrong, got: %v", err)
	}

	// Nothing may have been written: no alt-screen, no raw mode, no cursor moves.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("StartTUI wrote %d bytes to a non-terminal: %q", len(data), data)
	}
}

func TestTUIColourRespectsNoColor(t *testing.T) {
	// The environment decides; restore whatever the test runner had.
	old, hadOld := os.LookupEnv("NO_COLOR")
	t.Cleanup(func() {
		if hadOld {
			os.Setenv("NO_COLOR", old)
		} else {
			os.Unsetenv("NO_COLOR")
		}
	})
	os.Setenv("NO_COLOR", "1")
	if tuiColorEnabled(os.Stdout) {
		t.Errorf("NO_COLOR must disable styling")
	}
}

// ─── Concurrency (exercised under -race) ────────────────────────────────────

func TestTUIConcurrentProducersAndRender(t *testing.T) {
	const (
		writers = 8
		iters   = 120
	)
	stop := make(chan struct{})
	renderDone := make(chan struct{})

	go func() {
		defer close(renderDone)
		ui := &tuiUI{started: time.Now()}
		for {
			select {
			case <-stop:
				return
			default:
			}
			v := ui.view(tuiState, 100, 30, false)
			_ = renderTUIView(v)
			ui.tab = (ui.tab + 1) % tuiTabCount
			time.Sleep(time.Millisecond)
		}
	}()

	var wg sync.WaitGroup
	for g := 0; g < writers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				id := fmt.Sprintf("race-%d-%d", g, i)
				AddTUIMatch(&TUIMatch{
					ID:        id,
					Signature: "sig",
					Source:    "GitHub",
					URL:       "https://github.com/o/r",
					Repo:      "o/r",
					File:      "f.go",
					Link:      "https://github.com/o/r/blob/main/f.go#L1",
					Secret:    "ghp_race",
					Matches:   []string{"ghp_race"},
					Line:      i,
					Priority:  i % 4,
					Timestamp: time.Now(),
				})
				AddTUILog(fmt.Sprintf("log %d from %d", i, g))
				AddTUIToken(fmt.Sprintf("tok-%d-%d", g, i), i%2 == 0, "github")
				SetTUIStatus(TUIStatus{Message: fmt.Sprintf("scanning %d", i), ReposScanned: i, RequestsRemaining: 100 - i})
				SetTUIReview(id, "review text", i%3 == 0)
			}
		}(g)
	}

	wg.Wait()
	close(stop)
	select {
	case <-renderDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the render goroutine did not stop")
	}

	tuiState.mu.Lock()
	defer tuiState.mu.Unlock()
	if len(tuiState.matches) > tuiMaxMatches {
		t.Errorf("match buffer grew past its cap: %d", len(tuiState.matches))
	}
	if len(tuiState.logs) > tuiMaxLogs {
		t.Errorf("log buffer grew past its cap: %d", len(tuiState.logs))
	}
	if len(tuiState.tokens) > tuiMaxTokens {
		t.Errorf("token buffer grew past its cap: %d", len(tuiState.tokens))
	}
	if len(tuiState.reviews) > tuiMaxReviews {
		t.Errorf("review map grew past its cap: %d", len(tuiState.reviews))
	}
	if tuiState.total < writers*iters {
		t.Errorf("total findings = %d, want at least %d", tuiState.total, writers*iters)
	}
}

// TestTUIViewConcurrentSnapshotIsConsistent renders from one goroutine while
// another one mutates, checking that a snapshot never observes a torn list.
func TestTUIViewConcurrentSnapshotIsConsistent(t *testing.T) {
	b := newTUIBuffer()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			b.addMatch(&TUIMatch{ID: fmt.Sprintf("c%d", i), Signature: "sig", Repo: "o/r", File: "f.go", Secret: "s", Priority: 1, Timestamp: time.Now()})
			b.addLog(fmt.Sprintf("l%d", i))
		}
	}()

	ui := &tuiUI{}
	for i := 0; i < 300; i++ {
		v := ui.view(b, 90, 25, false)
		for _, e := range v.matches {
			if e == nil || e.match == nil || e.match.Signature != "sig" {
				t.Fatalf("snapshot returned a torn entry: %+v", e)
			}
		}
	}
	wg.Wait()
}

// ─── Web-dashboard parity ───────────────────────────────────────────────────

func TestTUIPriorityFilterCyclesAndNarrowsTheList(t *testing.T) {
	h := newTUIHarness(t, 120, 30)
	now := time.Now()
	h.push(tuiTestMatch("c1", "o/r", "crit.go", 1, "s", "Sig Crit", 3, now))
	h.push(tuiTestMatch("h1", "o/r", "high.go", 2, "s", "Sig High", 2, now))
	h.push(tuiTestMatch("m1", "o/r", "med.go", 3, "s", "Sig Med", 1, now))
	h.push(tuiTestMatch("l1", "o/r", "low.go", 4, "s", "Sig Low", 0, now))

	if f := h.frame(); !strings.Contains(f, "4 of 4") {
		t.Fatalf("expected all four findings unfiltered:\n%s", f)
	}

	// p cycles: all -> crit -> high -> med -> low -> all.
	h.send("p")
	f := h.frame()
	if !strings.Contains(f, "priority CRIT") || !strings.Contains(f, "1 of 4") {
		t.Errorf("p should filter to critical findings:\n%s", f)
	}
	if strings.Contains(f, "high.go") || strings.Contains(f, "low.go") || strings.Contains(f, "med.go") {
		t.Errorf("the priority filter leaked other priorities:\n%s", f)
	}
	assertFrameWidths(t, f, 120, 30)

	h.send("p")
	if f := h.frame(); !strings.Contains(f, "priority HIGH") || !strings.Contains(f, "high.go") {
		t.Errorf("the second p should select high:\n%s", f)
	}
	// P walks the other way.
	h.send("P")
	if f := h.frame(); !strings.Contains(f, "priority CRIT") {
		t.Errorf("P should cycle back to critical:\n%s", f)
	}
	// c clears both filters.
	h.send("c")
	f = h.frame()
	if !strings.Contains(f, "4 of 4") || strings.Contains(f, "priority CRIT") || strings.Contains(f, "priority HIGH") {
		t.Errorf("c should clear the priority filter too:\n%s", f)
	}
}

func TestTUITopSignaturesLineShowsCounts(t *testing.T) {
	h := newTUIHarness(t, 120, 30)
	now := time.Now()
	h.push(tuiTestMatch("a", "o/r", "a.go", 1, "s", "AWS Access Key", 2, now))
	h.push(tuiTestMatch("b", "o/r", "b.go", 2, "s", "AWS Access Key", 2, now))
	h.push(tuiTestMatch("c", "o/r", "c.go", 3, "s", "Slack Token", 2, now))

	f := h.frame()
	for _, want := range []string{"top", "AWS Access Key 2", "Slack Token 1"} {
		if !strings.Contains(f, want) {
			t.Errorf("top-signatures line is missing %q:\n%s", want, f)
		}
	}
	assertFrameWidths(t, f, 120, 30)
}

func TestTUIHeaderShowsActivityCounters(t *testing.T) {
	old := tuiActivitySnapshot
	tuiActivitySnapshot = func() ActivitySnapshot {
		return ActivitySnapshot{
			Fetching:    []ActivityItem{{URL: "a"}, {URL: "b"}},
			Scanning:    []ActivityItem{{URL: "c"}},
			TotalFailed: 3,
			RateLimited: 2,
		}
	}
	defer func() { tuiActivitySnapshot = old }()

	h := newTUIHarness(t, 200, 30)
	f := h.frame()
	for _, want := range []string{"fetch 2", "scan 1", "failed 3", "limited 2"} {
		if !strings.Contains(f, want) {
			t.Errorf("header is missing %q:\n%s", want, tuiFrameLines(t, f)[0])
		}
	}
	assertFrameWidths(t, f, 200, 30)
}

func TestTUIFileViewShowsCapturedFileAndHighlightsTheLine(t *testing.T) {
	const id = "tui-file-view-1"
	body := "line one\nline two\nSECRET=ghp_x\nline four\n"
	storeMatchFile(id, "https://github.com/o/r", "cfg/.env", body, "ghp_x", 3)
	defer func() {
		fileMu.Lock()
		delete(fileDetails, id)
		fileMu.Unlock()
	}()

	h := newTUIHarness(t, 100, 24)
	h.push(tuiTestMatch(id, "o/r", "cfg/.env", 3, "ghp_x", "GitHub PAT", 2, time.Now()))

	// Opening puts the finding's own line at the top of the viewport.
	h.send("v")
	f := h.frame()
	if !strings.Contains(f, "FILE") || !strings.Contains(f, "cfg/.env:3") {
		t.Fatalf("the file view did not open:\n%s", f)
	}
	if !strings.Contains(f, "line 3 of 5") {
		t.Errorf("the file view should start at the finding's line:\n%s", f)
	}
	if !strings.Contains(f, "SECRET=ghp_x") {
		t.Errorf("the captured file body is missing:\n%s", f)
	}
	assertFrameWidths(t, f, 100, 24)

	// g jumps to the top and shows the whole (short) file.
	h.send("g")
	f = h.frame()
	for _, want := range []string{"line one", "line two", "SECRET=ghp_x", "line four"} {
		if !strings.Contains(f, want) {
			t.Errorf("file view is missing %q after g:\n%s", want, f)
		}
	}
	h.send("j")
	if f := h.frame(); !strings.Contains(f, "line 2 of 5") {
		t.Errorf("j should scroll the file view down:\n%s", f)
	}

	// Esc closes it and the findings list comes back.
	h.send("\x1b")
	f = h.frame()
	if strings.Contains(f, "FILE  cfg/.env") || !strings.Contains(f, "DETAIL") {
		t.Errorf("Esc should close the file view:\n%s", f)
	}
}

func TestTUIFileViewWithoutCapturedFileFlashesHint(t *testing.T) {
	h := newTUIHarness(t, 100, 24)
	h.push(tuiTestMatch("tui-no-file-body", "o/r", "a.go", 1, "s", "sig", 1, time.Now()))
	h.send("v")
	if h.ui.fileView {
		t.Fatalf("the file view opened without any captured content")
	}
	if f := h.frame(); !strings.Contains(f, "no captured file content") {
		t.Errorf("v without a captured file must explain itself:\n%s", f)
	}
}

func TestTUIReviewKeyUnavailableAndHook(t *testing.T) {
	// Without a hook the key reports that review is not wired up.
	old := tuiReviewRequest
	tuiReviewRequest = nil
	defer func() { tuiReviewRequest = old }()

	h := newTUIHarness(t, 100, 24)
	h.push(tuiTestMatch("tui-review-none", "o/r", "a.go", 1, "s", "sig", 1, time.Now()))
	h.send("r")
	if f := h.frame(); !strings.Contains(f, "AI review is not available") {
		t.Errorf("r without a hook must explain itself:\n%s", f)
	}

	// With a hook, the selected finding is handed over and the request confirmed.
	var got *TUIMatch
	tuiReviewRequest = func(m *TUIMatch) error { got = m; return nil }
	h2 := newTUIHarness(t, 100, 24)
	h2.push(tuiTestMatch("tui-review-1", "acme/app", "a.go", 7, "s", "Sig X", 2, time.Now()))
	h2.send("r")
	if got == nil || got.ID != "tui-review-1" {
		t.Fatalf("r did not pass the selected finding to the hook: %+v", got)
	}
	if f := h2.frame(); !strings.Contains(f, "AI review started") {
		t.Errorf("a started review should be confirmed:\n%s", f)
	}

	// A hook that fails must surface the error instead of claiming success.
	tuiReviewRequest = func(m *TUIMatch) error { return fmt.Errorf("no provider") }
	h3 := newTUIHarness(t, 100, 24)
	h3.push(tuiTestMatch("tui-review-2", "o/r", "a.go", 1, "s", "sig", 1, time.Now()))
	h3.send("r")
	if f := h3.frame(); !strings.Contains(f, "no provider") {
		t.Errorf("a failing review request must be reported:\n%s", f)
	}
}

func TestTUITailFitKeepsTheEndAndIsRuneSafe(t *testing.T) {
	wide := "所有者/日本語リポジトリ"
	for w := 1; w <= 20; w++ {
		got := tuiTailFit(wide, w)
		if !utf8.ValidString(got) || strings.ContainsRune(got, utf8.RuneError) {
			t.Fatalf("tuiTailFit(%q, %d) = %q split a rune", wide, w, got)
		}
		if cells := tuiDisplayWidth(got); cells > w {
			t.Fatalf("tuiTailFit(%q, %d) = %q is %d cells wide", wide, w, got, cells)
		}
	}
	if got := tuiTailFit("abcdef", 4); got != "...f" {
		t.Errorf("tuiTailFit(abcdef, 4) = %q, want ...f", got)
	}
	if got := tuiTailFit("short", 10); got != "short" {
		t.Errorf("tuiTailFit must not touch text that already fits, got %q", got)
	}
}

func TestTUIFilterPromptNeverOverflowsWithLongInput(t *testing.T) {
	h := newTUIHarness(t, 60, 15)
	h.push(tuiTestMatch("tui-long-filter", "o/r", "a.go", 1, "s", "sig", 1, time.Now()))
	h.send("/")
	h.send(strings.Repeat("x", 200))
	assertFrameWidths(t, h.frame(), 60, 15)
	if f := h.frame(); !strings.Contains(f, "xxx") {
		t.Errorf("the filter prompt should still show the typed input:\n%s", f)
	}
}

// ─── AI review streaming bridge ─────────────────────────────────────────────

func tuiReviewText(id string) (text string, running, present bool) {
	tuiState.mu.Lock()
	defer tuiState.mu.Unlock()
	r, ok := tuiState.reviews[id]
	if !ok {
		return "", false, false
	}
	return r.Text, r.Running, true
}

// TestTUIReviewFinishKeepsStreamedText covers the ordering hazard in the review
// broker: the job closes its subscriber channels just before it writes the
// result to the store, so finalising from the store could blank text the
// operator already watched stream in. Finalising from the job must not.
func TestTUIReviewFinishKeepsStreamedText(t *testing.T) {
	const matchID = "tui-review-finish"
	SetTUIReview(matchID, "", false)
	defer SetTUIReview(matchID, "", false)

	job := newReviewJob()
	job.publish("hello ")
	job.publish("world")
	job.finish(reviewstore.StatusDone, "")

	// The store has no such review, which is exactly the pre-write window.
	tuiFinishFromJob(matchID, "tui-review-not-in-store", job, "hello world")

	text, running, present := tuiReviewText(matchID)
	if !present {
		t.Fatalf("the finished review is not present")
	}
	if text != "hello world" {
		t.Errorf("review text = %q, want %q", text, "hello world")
	}
	if running {
		t.Errorf("a finished review must not still be marked running")
	}
}

// TestTUIReviewStreamMirrorsDeltas drives the live path: deltas published while
// the UI follows a job must appear, and the final text must survive the job
// finishing.
func TestTUIReviewStreamMirrorsDeltas(t *testing.T) {
	const reviewID = "tui-stream-live"
	const matchID = "tui-stream-live-match"
	job := newReviewJob()
	reviewJobsMu.Lock()
	reviewJobs[reviewID] = job
	reviewJobsMu.Unlock()
	defer dropJob(reviewID)
	SetTUIReview(matchID, "", false)
	defer SetTUIReview(matchID, "", false)

	done := make(chan struct{})
	go func() {
		defer close(done)
		tuiStreamReview(matchID, reviewID)
	}()

	job.publish("part one ")
	deadline := time.Now().Add(3 * time.Second)
	for {
		if text, _, _ := tuiReviewText(matchID); strings.Contains(text, "part one") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the streamed delta never reached the detail pane")
		}
		time.Sleep(5 * time.Millisecond)
	}
	job.publish("part two")
	job.finish(reviewstore.StatusDone, "")
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the review stream did not stop after the job finished")
	}

	text, running, _ := tuiReviewText(matchID)
	if text != "part one part two" || running {
		t.Fatalf("final review = %q running=%v, want %q false", text, running, "part one part two")
	}
}
