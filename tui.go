// tui.go - a real, full-screen terminal UI for the secret scanner.
//
// It is hand-rolled on top of golang.org/x/term: raw mode for the keyboard,
// plain ANSI SGR/CSI/OSC sequences for the frame, and exactly one Write per
// frame so the screen never flickers. No new module dependency is introduced
// on purpose - the scanner has to keep cross-compiling offline.
//
// Concurrency model:
//
//	scanner goroutines --AddTUIMatch/AddTUILog/AddTUIToken/SetTUIStatus--> tuiBuffer
//	                                                                          |
//	                                        snapshot under the mutex (tuiView) |
//	                                                                          v
//	                                                     renderTUIView -> one Write
//
// Every exported entry point is safe at any time: before StartTUI (findings are
// buffered - bounded, oldest dropped - and shown the moment the UI starts),
// while it runs, and after it returns. Producers never block: they take a
// mutex, append, and poke a size-1 notification channel.
//
// Rendering is split into pure functions that take a tuiView plus a width and
// height and return the frame as a string, which is what tui_test.go exercises
// headlessly.

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"
)

// ─── Public API ─────────────────────────────────────────────────────────────

// TUIMatch is one leaked-secret finding, in the shape the terminal UI renders
// it. The fields mirror the web dashboard's Match so main.go can fill either
// from the same scan event.
type TUIMatch struct {
	ID        string
	Signature string
	Source    string // "GitHub", "Gist", "Local", ...
	URL       string // repo URL or local directory
	Repo      string // "owner/repo" when known
	File      string // path within the repo/directory
	Link      string // already-built clickable link (file:// or https://), may be empty
	Secret    string // the exact matched text
	Matches   []string
	Line      int // 1-based line number of the secret
	Priority  int // 3 critical, 2 high, 1 medium, 0 low
	Stars     int
	Timestamp time.Time
}

// TUIStatus is the scanner's live status, shown in the header.
type TUIStatus struct {
	Message           string // free-form status line
	ReposScanned      int
	RequestsRemaining int
	RateLimitReset    time.Time
}

// TUIAvailable reports whether the terminal UI can be used at all: stdout must
// be a terminal that is big enough for the layout. StartTUI additionally needs
// stdin to be a terminal (it has to read the keyboard).
func TUIAvailable() bool {
	out := tuiStdout
	if out == nil || !tuiIsTerminal(out) {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	w, h, err := term.GetSize(int(out.Fd()))
	if err != nil {
		return false
	}
	return w >= tuiMinWidth && h >= tuiMinHeight
}

// AddTUIMatch queues a finding for display. It never blocks and is safe to call
// before, during and after StartTUI. The struct is copied, so the caller may
// reuse or mutate it afterwards.
func AddTUIMatch(m *TUIMatch) { tuiState.addMatch(m) }

// AddTUILog queues one scanner log line. It never blocks and is safe to call at
// any time.
func AddTUILog(line string) { tuiState.addLog(line) }

// AddTUIToken queues one live token-validation result. It never blocks and is
// safe to call at any time.
func AddTUIToken(token string, valid bool, provider string) {
	tuiState.addToken(token, valid, provider)
}

// SetTUIStatus replaces the scanner status shown in the header. It never blocks
// and is safe to call at any time.
func SetTUIStatus(s TUIStatus) { tuiState.setStatus(s) }

// SetTUIReview attaches (or replaces) the AI review text for one finding, as
// identified by TUIMatch.ID. Pass running true while the model is still
// streaming; the detail pane then shows a progress hint until the final text
// arrives. Calling it with empty text and running false removes the entry. It
// never blocks and is safe to call at any time.
func SetTUIReview(id string, text string, running bool) {
	tuiState.setReview(id, text, running)
}

// ─── Tunables ───────────────────────────────────────────────────────────────

const (
	// Bounded buffers: the UI must never grow without limit while a long scan
	// streams into it. Oldest entries are dropped first.
	tuiMaxMatches = 5000
	tuiMaxLogs    = 2000
	tuiMaxTokens  = 2000
	tuiMaxReviews = 512

	// Below this the layout cannot be drawn legibly.
	tuiMinWidth  = 60
	tuiMinHeight = 15

	// Frame cadence: a tick polls the terminal size (Windows has no SIGWINCH)
	// and keeps the clock/spinner alive; state changes redraw immediately.
	tuiTickInterval = 250 * time.Millisecond

	// How long a bare ESC byte is allowed to wait for the rest of a sequence.
	tuiEscTimeout = 30 * time.Millisecond

	// Chrome is deliberately ASCII-only: box-drawing characters are
	// East-Asian-ambiguous width and would break the column maths in terminals
	// that render them double-width.
	tuiCursorHome = "\033[H"
	tuiEraseLine  = "\033[K"
	tuiReset      = "\033[0m"

	// One palette for the whole UI, all bright so a finding looks the same in
	// the header, the list and the detail pane: red+bold for critical, 256-colour
	// orange for high, yellow for medium, green for low, cyan for links and
	// accents, dim for chrome. Colour is applied only through the helpers below,
	// never by hand, so the mapping cannot drift between panes.
	tuiSGRBold   = "\033[1m"
	tuiSGRDim    = "\033[2m"
	tuiSGRRev    = "\033[7m"
	tuiSGRCyan   = "\033[96m"
	tuiSGRGreen  = "\033[92m"
	tuiSGRYellow = "\033[93m"
	tuiSGRRed    = "\033[91m"
	tuiSGROrange = "\033[38;5;208m" // 256-colour orange for "high"
)

// Tab indexes.
const (
	tuiTabFindings = iota
	tuiTabTokens
	tuiTabLogs
	tuiTabHelp
	tuiTabCount
)

var tuiTabNames = [tuiTabCount]string{"Findings", "Tokens", "Logs", "Help"}

// ─── Buffer (producer side) ─────────────────────────────────────────────────

// tuiMatchEntry keeps the match together with a pre-lowercased search blob so
// the filter is one substring test per finding instead of a ToLower pass over
// every field, on every frame.
type tuiMatchEntry struct {
	match *TUIMatch
	blob  string
}

type tuiLogLine struct {
	Text      string
	Timestamp time.Time
}

type tuiTokenResult struct {
	Token     string
	Provider  string
	Valid     bool
	Timestamp time.Time
}

type tuiReview struct {
	Text    string
	Running bool
	Updated time.Time
}

// tuiBuffer is the hand-off point between the scanner and the UI. All access is
// guarded by mu; notify is a size-1 channel used only to wake the render loop,
// never to carry data, so a producer can never block on a slow terminal.
type tuiBuffer struct {
	mu      sync.Mutex
	matches []*tuiMatchEntry // oldest first
	logs    []tuiLogLine     // oldest first
	tokens  []tuiTokenResult // oldest first
	reviews map[string]*tuiReview
	status  TUIStatus

	total   int    // every finding ever queued
	counts  [4]int // by priority: 0 low .. 3 critical
	dropped int    // findings evicted from the bounded buffer
	version uint64 // bumped on every change, so the UI can redraw only when needed
	notify  chan struct{}
}

func newTUIBuffer() *tuiBuffer {
	return &tuiBuffer{
		matches: make([]*tuiMatchEntry, 0, 64),
		logs:    make([]tuiLogLine, 0, 64),
		tokens:  make([]tuiTokenResult, 0, 16),
		reviews: make(map[string]*tuiReview),
		notify:  make(chan struct{}, 1),
	}
}

// tuiState is the process-wide buffer. It is deliberately never nil so that
// AddTUIMatch and friends work before StartTUI is called and after it returns;
// main.go's publish path also guards on this identifier, so the name is part of
// the contract.
var tuiState = newTUIBuffer()

// poke wakes the render loop without ever blocking the caller.
func (b *tuiBuffer) poke() {
	select {
	case b.notify <- struct{}{}:
	default:
	}
}

// tuiTrim keeps the newest max entries of s, releasing the evicted ones.
func tuiTrim[T any](s []T, max int) []T {
	if len(s) <= max {
		return s
	}
	drop := len(s) - max
	n := copy(s, s[drop:])
	var zero T
	for i := n; i < len(s); i++ {
		s[i] = zero
	}
	return s[:n]
}

func (b *tuiBuffer) addMatch(m *TUIMatch) {
	if b == nil || m == nil {
		return
	}
	// Copy so a caller that reuses or mutates the struct cannot race with the
	// renderer.
	cp := *m
	if cp.Timestamp.IsZero() {
		cp.Timestamp = time.Now()
	}
	if len(cp.Matches) > 0 {
		cp.Matches = append([]string(nil), cp.Matches...)
	}
	e := &tuiMatchEntry{match: &cp, blob: tuiMatchBlob(&cp)}

	b.mu.Lock()
	b.matches = append(b.matches, e)
	if len(b.matches) > tuiMaxMatches {
		b.dropped += len(b.matches) - tuiMaxMatches
		b.matches = tuiTrim(b.matches, tuiMaxMatches)
	}
	b.total++
	b.counts[tuiPriorityIndex(cp.Priority)]++
	b.version++
	b.mu.Unlock()
	b.poke()
}

func (b *tuiBuffer) addLog(line string) {
	if b == nil {
		return
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return
	}
	b.mu.Lock()
	b.logs = append(b.logs, tuiLogLine{Text: line, Timestamp: time.Now()})
	b.logs = tuiTrim(b.logs, tuiMaxLogs)
	b.version++
	b.mu.Unlock()
	b.poke()
}

func (b *tuiBuffer) addToken(token string, valid bool, provider string) {
	if b == nil || strings.TrimSpace(token) == "" {
		return
	}
	b.mu.Lock()
	b.tokens = append(b.tokens, tuiTokenResult{
		Token:     strings.TrimSpace(token),
		Provider:  provider,
		Valid:     valid,
		Timestamp: time.Now(),
	})
	b.tokens = tuiTrim(b.tokens, tuiMaxTokens)
	b.version++
	b.mu.Unlock()
	b.poke()
}

func (b *tuiBuffer) setStatus(s TUIStatus) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.status = s
	b.version++
	b.mu.Unlock()
	b.poke()
}

func (b *tuiBuffer) setReview(id string, text string, running bool) {
	if b == nil || strings.TrimSpace(id) == "" {
		return
	}
	b.mu.Lock()
	if !running && strings.TrimSpace(text) == "" {
		delete(b.reviews, id)
	} else {
		b.reviews[id] = &tuiReview{Text: text, Running: running, Updated: time.Now()}
		if len(b.reviews) > tuiMaxReviews {
			b.evictOldestReviewLocked()
		}
	}
	b.version++
	b.mu.Unlock()
	b.poke()
}

func (b *tuiBuffer) evictOldestReviewLocked() {
	var (
		oldestID string
		oldest   time.Time
		found    bool
	)
	for id, r := range b.reviews {
		if !found || r.Updated.Before(oldest) {
			oldestID, oldest, found = id, r.Updated, true
		}
	}
	if found {
		delete(b.reviews, oldestID)
	}
}

// ─── View model (render side) ───────────────────────────────────────────────

// tuiView is an immutable snapshot of everything a frame needs. Render functions
// take one of these plus a width and height and are otherwise pure, which is what
// makes the layout testable without a terminal.
type tuiView struct {
	width  int
	height int
	color  bool

	tab int

	matches       []*tuiMatchEntry // newest first, filters applied
	matchTotal    int
	matchBuffered int
	matchDropped  int
	counts        [4]int

	// prioMask selects which priorities are visible; 0 means "no filter".
	prioMask uint8
	// topSigs is the dashboard's "top signatures" panel, in terminal form.
	topSigs []tuiSigCount

	// Live activity counters, mirroring the dashboard's fetching/scanning panel.
	fetching    int
	scanning    int
	failed      int
	rateLimited int

	tokens  []tuiTokenResult
	logs    []tuiLogLine
	reviews map[string]*tuiReview
	status  TUIStatus

	sel       int
	filter    string
	filtering bool
	input     string

	logBack int

	// File viewer: the captured file body behind the selected finding.
	fileView      bool
	filePath      string
	fileLine      int
	fileLines     []string
	filePresent   bool
	fileTruncated bool
	fileScroll    int

	spin   int
	uptime time.Duration
	now    time.Time
	flash  string

	version uint64
}

// tuiUI holds the interactive state (tab, selection, filter, scroll) that
// survives between frames. It belongs to the render loop goroutine only.
type tuiUI struct {
	tab       int
	prevTab   int
	sel       int
	filter    string
	input     string
	filtering bool
	logBack   int

	// prioMask is the priority filter; 0 means "all" (see effectivePrioMask).
	prioMask uint8
	// fileView shows the captured file body instead of the findings list.
	fileView   bool
	fileScroll int

	started   time.Time
	spin      int
	dirty     bool
	flashMsg  string
	flashTill time.Time
}

func (u *tuiUI) flash(msg string) {
	u.flashMsg = msg
	u.flashTill = time.Now().Add(4 * time.Second)
	u.dirty = true
}

// view snapshots the buffer and overlays the interactive state.
func (u *tuiUI) view(b *tuiBuffer, width, height int, color bool) *tuiView {
	now := time.Now()
	v := &tuiView{
		width:     width,
		height:    height,
		color:     color,
		tab:       u.tab,
		filter:    u.filter,
		filtering: u.filtering,
		input:     u.input,
		spin:      u.spin,
		now:       now,
		prioMask:  u.effectivePrioMask(),
	}
	if v.tab < 0 || v.tab >= tuiTabCount {
		v.tab = tuiTabFindings
	}
	if !u.started.IsZero() {
		v.uptime = now.Sub(u.started)
	}
	if u.flashMsg != "" && now.Before(u.flashTill) {
		v.flash = u.flashMsg
	}

	if b != nil {
		b.mu.Lock()
		v.matchTotal = b.total
		v.matchBuffered = len(b.matches)
		v.matchDropped = b.dropped
		v.counts = b.counts
		v.status = b.status
		v.matches = make([]*tuiMatchEntry, 0, len(b.matches))
		for i := len(b.matches) - 1; i >= 0; i-- { // newest first
			v.matches = append(v.matches, b.matches[i])
		}
		v.tokens = make([]tuiTokenResult, 0, len(b.tokens))
		for i := len(b.tokens) - 1; i >= 0; i-- {
			v.tokens = append(v.tokens, b.tokens[i])
		}
		v.logs = append([]tuiLogLine(nil), b.logs...)
		if len(b.reviews) > 0 {
			v.reviews = make(map[string]*tuiReview, len(b.reviews))
			for id, r := range b.reviews {
				cp := *r
				v.reviews[id] = &cp
			}
		}
		v.version = b.version
		b.mu.Unlock()
	}

	// The dashboard's "top signatures" panel: computed over every buffered
	// finding, before either filter narrows the list, so the summary describes
	// the scan rather than the current view.
	v.topSigs = tuiTopSignatures(v.matches, tuiTopSignaturesShown)

	// Live activity counters (the dashboard's fetching/scanning panel).
	snap := tuiActivitySnapshot()
	v.fetching = len(snap.Fetching)
	v.scanning = len(snap.Scanning)
	v.failed = int(snap.TotalFailed)
	v.rateLimited = int(snap.RateLimited)

	// Live preview while typing; the committed filter otherwise.
	if f := v.effectiveFilter(); f != "" {
		v.matches = tuiFilterMatches(v.matches, f)
	}
	v.matches = tuiFilterPriority(v.matches, v.prioMask)

	// The file viewer resolves the captured body for the selected finding. It is
	// only read while the pane is open, so the common path pays nothing.
	if u.tab == tuiTabFindings && u.fileView {
		v.fileView = true
		if e := v.selectedEntry(); e != nil {
			v.filePath = e.match.File
			v.fileLine = e.match.Line
			if d, ok := tuiLookupMatchFile(e.match.ID); ok {
				v.filePresent = true
				v.fileTruncated = d.Truncated
				v.fileLines = strings.Split(stripAnsi(d.Content), "\n")
			}
		}
		u.fileScroll = tuiClamp(u.fileScroll, 0, len(v.fileLines)-1)
		v.fileScroll = u.fileScroll
	}

	v.logBack = tuiClamp(u.logBack, 0, len(v.logs))
	u.sel = tuiClamp(u.sel, 0, tuiListLenFor(v.tab, v)-1)
	v.sel = u.sel
	return v
}

// effectivePrioMask treats the zero value as "no priority filter", which is what
// a freshly constructed tuiUI has.
func (u *tuiUI) effectivePrioMask() uint8 {
	if u.prioMask == 0 {
		return tuiPrioAll
	}
	return u.prioMask
}

// tuiPrioStates is the order the "p" key cycles through: all, then each
// priority on its own.
var tuiPrioStates = [5]uint8{tuiPrioAll, 0x8, 0x4, 0x2, 0x1}

func (u *tuiUI) cyclePriority(delta int) {
	cur := u.effectivePrioMask()
	idx := 0
	for i, m := range tuiPrioStates {
		if m == cur {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(tuiPrioStates)) % len(tuiPrioStates)
	u.prioMask = tuiPrioStates[idx]
	u.sel = 0
	u.dirty = true
}

func (v *tuiView) effectiveFilter() string {
	if v.filtering {
		return strings.TrimSpace(v.input)
	}
	return strings.TrimSpace(v.filter)
}

// selectedEntry is the finding the detail pane describes.
func (v *tuiView) selectedEntry() *tuiMatchEntry {
	if v == nil || v.sel < 0 || v.sel >= len(v.matches) {
		return nil
	}
	return v.matches[v.sel]
}

func tuiListLenFor(tab int, v *tuiView) int {
	if v == nil {
		return 0
	}
	switch tab {
	case tuiTabTokens:
		return len(v.tokens)
	case tuiTabFindings:
		return len(v.matches)
	}
	return 0
}

func tuiClamp(n, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

func tuiPriorityIndex(p int) int {
	switch {
	case p >= 3:
		return 3
	case p == 2:
		return 2
	case p == 1:
		return 1
	default:
		return 0
	}
}

func tuiPriorityBadge(i int) string {
	switch i {
	case 3:
		return "CRIT"
	case 2:
		return "HIGH"
	case 1:
		return "MED "
	default:
		return "LOW "
	}
}

func tuiPriorityName(i int) string {
	switch i {
	case 3:
		return "critical"
	case 2:
		return "high"
	case 1:
		return "medium"
	default:
		return "low"
	}
}

func tuiPrioritySGR(i int) string {
	switch i {
	case 3:
		return tuiSGRBold + tuiSGRRed
	case 2:
		return tuiSGROrange
	case 1:
		return tuiSGRYellow
	default:
		return tuiSGRGreen
	}
}

// tuiPrioAll is the "every priority visible" mask; bit i is priority index i.
const tuiPrioAll uint8 = 0xf

// tuiTopSignaturesShown is how many signature rows the summary line carries,
// matching the dashboard's top-5 panel.
const tuiTopSignaturesShown = 5

// tuiSigCount is one row of the "top signatures" summary.
type tuiSigCount struct {
	Name  string
	Count int
}

// tuiTopSignatures returns the n signatures with the most findings, breaking
// ties by name so the line is stable from frame to frame.
func tuiTopSignatures(matches []*tuiMatchEntry, n int) []tuiSigCount {
	if n <= 0 || len(matches) == 0 {
		return nil
	}
	counts := make(map[string]int, len(matches))
	for _, e := range matches {
		if e == nil || e.match == nil {
			continue
		}
		name := strings.TrimSpace(e.match.Signature)
		if name == "" {
			name = "(unnamed)"
		}
		counts[name]++
	}
	if len(counts) == 0 {
		return nil
	}
	out := make([]tuiSigCount, 0, len(counts))
	for name, count := range counts {
		out = append(out, tuiSigCount{Name: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// tuiFilterPriority keeps only findings whose priority bit is set in mask. A
// zero mask, or the all-priorities mask, is a no-op.
func tuiFilterPriority(in []*tuiMatchEntry, mask uint8) []*tuiMatchEntry {
	if mask == 0 || mask == tuiPrioAll {
		return in
	}
	out := make([]*tuiMatchEntry, 0, len(in))
	for _, e := range in {
		if e == nil || e.match == nil {
			continue
		}
		if mask&(1<<uint(tuiPriorityIndex(e.match.Priority))) != 0 {
			out = append(out, e)
		}
	}
	return out
}

// tuiPriorityMaskIndex returns the priority index a single-bit mask selects.
func tuiPriorityMaskIndex(mask uint8) int {
	for i := 3; i >= 0; i-- {
		if mask&(1<<uint(i)) != 0 {
			return i
		}
	}
	return 0
}

// tuiActivitySnapshot is a seam so the header's activity counters can be driven
// deterministically from a test. Production reads the scanner's live tracker.
var tuiActivitySnapshot = func() ActivitySnapshot { return activity.Snapshot() }

// tuiLookupMatchFile returns the captured file body behind a match ID, from the
// same cache the dashboard's file viewer reads. The TUI and the dashboard share
// one id space (publish stores the body under the id it hands the TUI), so the
// "v" key works exactly like the web modal.
func tuiLookupMatchFile(id string) (MatchFileDetail, bool) {
	if id == "" {
		return MatchFileDetail{}, false
	}
	fileMu.Lock()
	defer fileMu.Unlock()
	d, ok := fileDetails[id]
	if !ok || d == nil {
		return MatchFileDetail{}, false
	}
	return *d, true
}

// ─── Text helpers (rune-safe, width-aware, escape-safe) ─────────────────────

// tuiRuneWidth is the number of terminal cells a rune occupies. Control runes
// and combining marks take none; East Asian wide/fullwidth runes take two.
func tuiRuneWidth(r rune) int {
	if r == 0 || r < 0x20 || (r >= 0x7f && r < 0xa0) {
		return 0
	}
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) {
		return 0
	}
	if tuiRuneIsWide(r) {
		return 2
	}
	return 1
}

func tuiRuneIsWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115f, // Hangul Jamo
		r >= 0x2e80 && r <= 0x303e, // CJK radicals, Kangxi, CJK symbols
		r >= 0x3041 && r <= 0x33ff, // kana, Hangul compat, CJK compat
		r >= 0x3400 && r <= 0x4dbf, // CJK ext A
		r >= 0x4e00 && r <= 0x9fff, // CJK unified
		r >= 0xa000 && r <= 0xa4cf, // Yi
		r >= 0xa960 && r <= 0xa97f,
		r >= 0xac00 && r <= 0xd7a3, // Hangul syllables
		r >= 0xf900 && r <= 0xfaff, // CJK compat ideographs
		r >= 0xfe10 && r <= 0xfe19,
		r >= 0xfe30 && r <= 0xfe6f,
		r >= 0xff00 && r <= 0xff60, // fullwidth forms
		r >= 0xffe0 && r <= 0xffe6,
		r >= 0x1f300 && r <= 0x1f64f,
		r >= 0x1f680 && r <= 0x1f6ff,
		r >= 0x1f900 && r <= 0x1f9ff,
		r >= 0x20000 && r <= 0x3fffd:
		return true
	}
	return false
}

func tuiDisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += tuiRuneWidth(r)
	}
	return w
}

// tuiClean removes anything that could move the cursor, recolour the frame or
// otherwise escape into the terminal: repos, filenames, log lines and secrets
// all come from scanned (that is, untrusted) content, so an ANSI sequence in a
// crafted repo name must not be able to scramble the display.
func tuiClean(s string) string {
	if !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) }) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, s)
}

// tuiCutIndex is the byte index at which s stops fitting in width cells. It
// always lands on a rune boundary, so truncation can never split a rune.
func tuiCutIndex(s string, width int) int {
	cells := 0
	for i, r := range s {
		w := tuiRuneWidth(r)
		if cells+w > width {
			return i
		}
		cells += w
	}
	return len(s)
}

// tuiTruncate shortens s to at most width cells, marking the cut with "...".
func tuiTruncate(s string, width int) string {
	s = tuiClean(s)
	if width <= 0 {
		return ""
	}
	if tuiDisplayWidth(s) <= width {
		return s
	}
	const marker = "..."
	if width <= len(marker) {
		return s[:tuiCutIndex(s, width)]
	}
	cut := tuiCutIndex(s, width-len(marker))
	return strings.TrimRight(s[:cut], " ") + marker
}

// tuiFit truncates s to width cells and pads it with spaces to exactly width.
func tuiFit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	t := tuiTruncate(s, width)
	if pad := width - tuiDisplayWidth(t); pad > 0 {
		return t + strings.Repeat(" ", pad)
	}
	return t
}

// tuiTailFit is tuiFit's mirror image: when s does not fit it keeps the end,
// prefixing the cut with "...". The filter prompt uses it so the character just
// typed is always the one on screen. The result is always at most width cells:
// a wide rune that cannot fit on its own is dropped rather than overflowing.
func tuiTailFit(s string, width int) string {
	s = tuiClean(s)
	if width <= 0 {
		return ""
	}
	if tuiDisplayWidth(s) <= width {
		return s
	}
	marker := ""
	budget := width
	if width > len("...") {
		marker = "..."
		budget = width - len(marker)
	}
	// Walk back from the end, taking whole runes until the budget is spent.
	start := len(s)
	cells := 0
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(s[:start])
		w := tuiRuneWidth(r)
		if cells+w > budget {
			break
		}
		cells += w
		start -= size
	}
	return marker + s[start:]
}

// tuiWrap hard-wraps text to width cells, preferring to break at a space and
// splitting long unbroken runs (secrets) when it has to. Embedded newlines
// start a new output line.
func tuiWrap(text string, width int) []string {
	if width <= 0 {
		return nil
	}
	var out []string
	for _, para := range strings.Split(tuiClean(text), "\n") {
		para = strings.TrimRight(para, " ")
		if para == "" {
			out = append(out, "")
			continue
		}
		for tuiDisplayWidth(para) > width {
			cut := tuiCutIndex(para, width)
			if cut <= 0 {
				break
			}
			head := para[:cut]
			if i := strings.LastIndexByte(head, ' '); i > width/2 {
				cut = i
				head = para[:cut]
			}
			out = append(out, strings.TrimRight(head, " "))
			para = strings.TrimLeft(para[cut:], " ")
		}
		out = append(out, para)
	}
	return out
}

// tuiClampBytes shortens s to at most n bytes without splitting a rune.
func tuiClampBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// tuiMatchBlob is the pre-lowercased haystack the findings filter searches.
// Fields are capped so a multi-kilobyte private key cannot blow up the buffer.
func tuiMatchBlob(m *TUIMatch) string {
	var sb strings.Builder
	fields := [...]string{
		m.Signature, m.Source, m.URL, m.Repo, m.File, m.Link, m.Secret,
		strings.Join(m.Matches, " "),
	}
	for _, f := range fields {
		if f == "" {
			continue
		}
		sb.WriteString(strings.ToLower(tuiClampBytes(tuiClean(f), 512)))
		sb.WriteByte('\n')
	}
	if m.Line > 0 {
		sb.WriteString(strconv.Itoa(m.Line))
	}
	return sb.String()
}

func tuiFilterMatches(in []*tuiMatchEntry, filter string) []*tuiMatchEntry {
	needle := strings.ToLower(strings.TrimSpace(filter))
	if needle == "" {
		return in
	}
	out := make([]*tuiMatchEntry, 0, len(in))
	for _, e := range in {
		if e == nil {
			continue
		}
		if strings.Contains(e.blob, needle) {
			out = append(out, e)
		}
	}
	return out
}

// tuiSecretPreview applies the same redaction rule as the plain terminal mode:
// private-key bodies and oversized secrets are never printed.
func tuiSecretPreview(secret string) string {
	switch {
	case strings.Contains(secret, "-----BEGIN") || strings.Contains(secret, "-----END"):
		return "[REDACTED - private key content hidden]"
	case len(secret) > 200:
		return "[REDACTED - secret too long to display]"
	default:
		return secret
	}
}

func tuiMatchRepoLabel(m *TUIMatch) string {
	if m == nil {
		return ""
	}
	if m.Repo != "" {
		return m.Repo
	}
	u := strings.TrimSuffix(strings.TrimSuffix(m.URL, "/"), ".git")
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexByte(u, '/'); i >= 0 {
		host := strings.ToLower(u[:i])
		if strings.Contains(host, "github") || strings.Contains(host, "gitlab") ||
			strings.Contains(host, "bitbucket") || strings.Contains(host, "gist") {
			u = u[i+1:]
		}
	}
	if u == "" {
		return "(unknown)"
	}
	return u
}

func tuiMatchFileLabel(m *TUIMatch) string {
	if m == nil {
		return ""
	}
	if m.Line > 0 {
		return fmt.Sprintf("%s:%d", m.File, m.Line)
	}
	return m.File
}

func tuiMatchLink(m *TUIMatch) string {
	if m == nil {
		return ""
	}
	if m.Link != "" {
		return m.Link
	}
	return m.URL
}

// tuiSafeURL strips control bytes so a URL cannot terminate an OSC-8 sequence
// early and leak the rest of its bytes into the frame.
func tuiSafeURL(u string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, u)
}

// tuiDuration renders an elapsed/remaining duration compactly.
func tuiDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func tuiClock(t time.Time) string {
	if t.IsZero() {
		return "--:--:--"
	}
	return t.Format("15:04:05")
}

// ─── Frame builder ──────────────────────────────────────────────────────────

// tuiLine is one finished, exactly-width-padded line. cells records its visible
// width so the assembler can pad without having to parse escape sequences back
// out of text.
type tuiLine struct {
	text  string
	cells int
}

// tuiLineBuf builds a line. The invariant that keeps width maths honest:
// external text only enters through fit/clip (which clean, truncate and pad),
// and colour is only ever applied by paint/dock to text that has already been
// sized.
type tuiLineBuf struct {
	sb    strings.Builder
	cells int
	limit int
}

func newTUILine(width int) *tuiLineBuf {
	b := &tuiLineBuf{limit: width}
	if width > 0 {
		b.sb.Grow(width + 32)
	}
	return b
}

// paint writes already-sized text, optionally wrapped in an SGR pair.
func (b *tuiLineBuf) paint(s, sgr string, color bool) {
	if s == "" {
		return
	}
	if color && sgr != "" {
		b.sb.WriteString(sgr)
		b.sb.WriteString(s)
		b.sb.WriteString(tuiReset)
	} else {
		b.sb.WriteString(s)
	}
	b.cells += tuiDisplayWidth(s)
}

// plain writes chrome text that is known to be a literal.
func (b *tuiLineBuf) plain(s string) { b.paint(s, "", false) }

// clip writes as much of s as still fits on the line.
func (b *tuiLineBuf) clip(s, sgr string, color bool) {
	rem := b.limit - b.cells
	if rem <= 0 || s == "" {
		return
	}
	b.paint(tuiTruncate(s, rem), sgr, color)
}

// fit writes exactly w cells of s (truncated and padded).
func (b *tuiLineBuf) fit(s string, w int, sgr string, color bool) {
	if w <= 0 {
		return
	}
	b.paint(tuiFit(s, w), sgr, color)
}

// dock right-aligns s if there is room for it.
func (b *tuiLineBuf) dock(s, sgr string, color bool) {
	w := tuiDisplayWidth(s)
	if w == 0 || b.cells+w+1 > b.limit {
		return
	}
	if pad := b.limit - w - b.cells; pad > 0 {
		b.sb.WriteString(strings.Repeat(" ", pad))
		b.cells += pad
	}
	b.paint(s, sgr, color)
}

// link writes text as an OSC-8 hyperlink when url is usable, and as plain text
// otherwise (the fallback the task asks for).
func (b *tuiLineBuf) link(url, text string, w int, color bool) {
	if w <= 0 {
		return
	}
	shown := tuiFit(text, w)
	if url == "" || !color {
		b.plain(shown)
		return
	}
	b.sb.WriteString("\033]8;;")
	b.sb.WriteString(tuiSafeURL(url))
	b.sb.WriteString("\033\\")
	b.sb.WriteString(shown)
	b.sb.WriteString("\033]8;;\033\\")
	b.cells += tuiDisplayWidth(shown)
}

func (b *tuiLineBuf) finish() tuiLine {
	if pad := b.limit - b.cells; pad > 0 && b.limit > 0 {
		b.sb.WriteString(strings.Repeat(" ", pad))
		b.cells = b.limit
	}
	return tuiLine{text: b.sb.String(), cells: b.cells}
}

func tuiBlankLine(width int) tuiLine { return newTUILine(width).finish() }

// tuiTextLine is a one-line helper for static body text.
func tuiTextLine(text string, width int, sgr string, color bool) tuiLine {
	b := newTUILine(width)
	b.clip(text, sgr, color)
	return b.finish()
}

func tuiPadLines(lines []tuiLine, width, height int) []tuiLine {
	if height < 0 {
		height = 0
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, tuiBlankLine(width))
	}
	return lines
}

// ─── Frame ──────────────────────────────────────────────────────────────────

// renderTUIView renders a whole frame as one string: cursor home, then one
// erase-to-end-of-line per row. No full-screen clear per frame, so there is
// nothing to flicker.
func renderTUIView(v *tuiView) string {
	if v == nil {
		return ""
	}
	if v.width < tuiMinWidth || v.height < tuiMinHeight {
		return tuiTooSmallFrame(v)
	}

	lines := make([]tuiLine, 0, v.height)
	lines = append(lines, tuiHeaderLine(v))
	lines = append(lines, tuiStatusLine(v))
	lines = append(lines, tuiTabBar(v))
	lines = append(lines, tuiTextLine(strings.Repeat("-", v.width), v.width, tuiSGRDim, v.color))
	lines = append(lines, tuiTabBody(v, tuiBodyHeight(v.height))...)
	lines = append(lines, tuiFooterLine(v))
	lines = append(lines, tuiPromptLine(v))
	lines = tuiPadLines(lines, v.width, v.height)

	var sb strings.Builder
	sb.Grow(v.width*v.height + v.height*8 + 64)
	sb.WriteString(tuiCursorHome)
	for i := 0; i < v.height; i++ {
		if i < len(lines) {
			sb.WriteString(lines[i].text)
		}
		sb.WriteString(tuiEraseLine)
		if i < v.height-1 {
			sb.WriteString("\r\n")
		}
	}
	return sb.String()
}

func tuiBodyHeight(height int) int {
	h := height - 6 // header, status, tab bar, rule, footer, prompt
	if h < 1 {
		h = 1
	}
	return h
}

func tuiTooSmallFrame(v *tuiView) string {
	msg := fmt.Sprintf("Terminal too small: %dx%d - the UI needs at least %dx%d.",
		v.width, v.height, tuiMinWidth, tuiMinHeight)
	hint := "Resize the window, or press q to quit."
	lines := tuiWrap(msg, v.width)
	lines = append(lines, tuiWrap(hint, v.width)...)

	var sb strings.Builder
	sb.Grow(256)
	sb.WriteString(tuiCursorHome)
	for i := 0; i < v.height; i++ {
		if i < len(lines) {
			sb.WriteString(tuiFit(lines[i], v.width))
		}
		sb.WriteString(tuiEraseLine)
		if i < v.height-1 {
			sb.WriteString("\r\n")
		}
	}
	return sb.String()
}

type tuiSeg struct {
	text string
	sgr  string
}

func tuiHeaderLine(v *tuiView) tuiLine {
	b := newTUILine(v.width)
	b.paint("shhgit", tuiSGRBold+tuiSGRCyan, v.color)
	b.paint(" secret scanner", tuiSGRDim, v.color)
	segs := []tuiSeg{
		{fmt.Sprintf("findings %d", v.matchTotal), ""},
		{fmt.Sprintf("crit %d", v.counts[3]), tuiPrioritySGR(3)},
		{fmt.Sprintf("high %d", v.counts[2]), tuiPrioritySGR(2)},
		{fmt.Sprintf("med %d", v.counts[1]), tuiPrioritySGR(1)},
		{fmt.Sprintf("low %d", v.counts[0]), tuiPrioritySGR(0)},
		{fmt.Sprintf("repos %d", v.status.ReposScanned), ""},
		{fmt.Sprintf("api %d", v.status.RequestsRemaining), ""},
		// Activity parity with the dashboard's fetching/scanning panel.
		{fmt.Sprintf("fetch %d", v.fetching), ""},
		{fmt.Sprintf("scan %d", v.scanning), ""},
	}
	if v.failed > 0 {
		segs = append(segs, tuiSeg{fmt.Sprintf("failed %d", v.failed), tuiSGRRed})
	}
	if v.rateLimited > 0 {
		segs = append(segs, tuiSeg{fmt.Sprintf("limited %d", v.rateLimited), tuiSGRYellow})
	}
	for _, s := range segs {
		w := tuiDisplayWidth(s.text) + 3 // " | "
		if b.cells+w > v.width {
			continue
		}
		b.paint(" | ", tuiSGRDim, v.color)
		b.paint(s.text, s.sgr, v.color)
	}
	return b.finish()
}

func tuiStatusLine(v *tuiView) tuiLine {
	b := newTUILine(v.width)
	msg := strings.TrimSpace(v.status.Message)
	if msg == "" {
		msg = "waiting for the scanner to report..."
	}

	// Reserve room for the docked right-hand segment before laying out the text.
	dockText, dockSGR := "", tuiSGRDim
	if !v.status.RateLimitReset.IsZero() && v.status.RateLimitReset.After(v.now) {
		dockText = "rate limit resets in " + tuiDuration(v.status.RateLimitReset.Sub(v.now))
		dockSGR = tuiSGRYellow
	} else if !v.now.IsZero() && v.uptime > 0 {
		dockText = "up " + tuiDuration(v.uptime)
	}
	msgWidth := v.width - 1
	if dockText != "" {
		msgWidth = v.width - tuiDisplayWidth(dockText) - 2
	}
	if msgWidth < 8 {
		msgWidth = v.width - 1
		dockText = ""
	}
	b.paint(" "+tuiTruncate(msg, msgWidth), tuiSGRDim, v.color)
	if dockText != "" {
		b.dock(dockText, dockSGR, v.color)
	}
	return b.finish()
}

func tuiSpinner(v *tuiView) string {
	frames := `-\|/`
	if v.spin < 0 {
		return "|"
	}
	return string(frames[v.spin%len(frames)])
}

func tuiTabBar(v *tuiView) tuiLine {
	b := newTUILine(v.width)
	for i := 0; i < tuiTabCount; i++ {
		label := fmt.Sprintf("[%d] %s", i+1, tuiTabNames[i])
		if i > 0 {
			b.plain(" ")
		}
		if i == v.tab {
			b.paint(" "+label+" ", tuiSGRBold+tuiSGRRev+tuiSGRCyan, v.color)
		} else {
			b.paint(" "+label+" ", tuiSGRDim, v.color)
		}
	}
	b.dock(tuiSpinner(v)+" live", tuiSGRDim, v.color)
	return b.finish()
}

func tuiRule(v *tuiView, label string) tuiLine {
	b := newTUILine(v.width)
	if label != "" {
		b.paint(" "+label+" ", tuiSGRDim+tuiSGRBold, v.color)
	}
	if rem := v.width - b.cells; rem > 0 {
		b.paint(strings.Repeat("-", rem), tuiSGRDim, v.color)
	}
	return b.finish()
}

var tuiFooterKeys = [tuiTabCount]string{
	tuiTabFindings: "q quit | j/k move | PgUp/PgDn | g/G ends | / filter | p priority | c clear | v file | r review | o open link | Tab next | ? help",
	tuiTabTokens:   "q quit | up/down or j/k scroll | PgUp/PgDn page | g/G first/last | Tab next tab | ? help",
	tuiTabLogs:     "q quit | up/down or j/k scroll | PgUp/PgDn page | g top | G bottom | Tab next tab | ? help",
	tuiTabHelp:     "q quit | 1-4 or Tab switch tabs | ? back",
}

func tuiFooterLine(v *tuiView) tuiLine {
	keys := tuiFooterKeys[tuiTabFindings]
	if v.tab >= 0 && v.tab < tuiTabCount {
		keys = tuiFooterKeys[v.tab]
	}
	return tuiTextLine(" "+keys, v.width, tuiSGRDim, v.color)
}

func tuiPromptLine(v *tuiView) tuiLine {
	b := newTUILine(v.width)
	switch {
	case v.flash != "":
		b.clip(" "+v.flash, tuiSGRYellow, v.color)
	case v.filtering:
		b.paint(" filter: ", tuiSGRBold, v.color)
		// Reserve one cell for the cursor so a long input can never push the
		// line past the terminal width. The tail is shown, so the newest typed
		// character stays visible.
		if avail := b.limit - b.cells - 1; avail > 0 {
			b.paint(tuiTailFit(v.input, avail), tuiSGRCyan, v.color)
		}
		b.paint("_", tuiSGRBold, v.color)
		b.clip("  (Enter apply | Esc cancel | Backspace edit)", tuiSGRDim, v.color)
	case strings.TrimSpace(v.filter) != "":
		b.clip(" filter: "+v.filter+"  (c clears)", tuiSGRDim, v.color)
	default:
		b.clip(" press / to filter, p for priority, 1-4 or Tab to switch tabs, ? for help, q to quit", tuiSGRDim, v.color)
	}
	if v.matchDropped > 0 {
		b.dock(fmt.Sprintf("%d older findings dropped", v.matchDropped), tuiSGRDim, v.color)
	}
	return b.finish()
}

// ─── Tab bodies ─────────────────────────────────────────────────────────────

func tuiTabBody(v *tuiView, height int) []tuiLine {
	if v.tab == tuiTabFindings && v.fileView {
		return tuiFileBody(v, height)
	}
	switch v.tab {
	case tuiTabTokens:
		return tuiTokensBody(v, height)
	case tuiTabLogs:
		return tuiLogsBody(v, height)
	case tuiTabHelp:
		return tuiHelpBody(v, height)
	default:
		return tuiFindingsBody(v, height)
	}
}

// tuiListWindow computes the first visible index and visible row count of a
// list that keeps the selection on screen.
func tuiListWindow(n, sel, rows int) (start, count int) {
	if rows < 1 {
		rows = 1
	}
	if n <= rows {
		return 0, n
	}
	start = sel - rows + 1
	if start < 0 {
		start = 0
	}
	if start > n-rows {
		start = n - rows
	}
	return start, rows
}

func tuiFindingsBody(v *tuiView, height int) []tuiLine {
	// Split the body between the list and the detail pane, keeping the detail
	// pane useful on short terminals and the list useful on tall ones.
	detH := height / 3
	if detH < 9 {
		detH = 9
	}
	if detH > height-4 {
		detH = height - 4
	}
	if detH < 2 {
		detH = 2
	}
	listH := height - 1 - detH
	if listH < 2 {
		listH = 2
		detH = height - 1 - listH
		if detH < 1 {
			detH = 1
		}
	}

	lines := make([]tuiLine, 0, height)
	lines = append(lines, tuiFindingsList(v, listH)...)
	lines = append(lines, tuiRule(v, "DETAIL"))
	lines = append(lines, tuiDetailLines(v, detH)...)
	return tuiPadLines(lines, v.width, height)
}

func tuiFindingsList(v *tuiView, listH int) []tuiLine {
	if listH < 1 {
		return nil
	}
	lines := make([]tuiLine, 0, listH)

	n := len(v.matches)
	// The top-signatures summary gets its own line when the list is tall enough
	// that it does not crowd out the findings themselves.
	showTop := n > 0 && listH >= 5 && len(v.topSigs) > 0
	rows := listH - 1
	if showTop {
		rows--
	}
	if rows < 1 {
		rows = 1
	}
	start, count := tuiListWindow(n, v.sel, rows)
	end := start + count

	// Column layout. The fixed columns are: marker 2, time 9, priority 5,
	// source 9; the rest is shared between repo, file:line and signature.
	const fixed = 25
	rem := v.width - fixed
	if rem < 12 {
		rem = 12
	}
	repoW := rem * 26 / 100
	fileW := rem * 30 / 100
	sigW := rem - repoW - fileW

	head := newTUILine(v.width)
	head.paint("  ", "", v.color)
	if f := v.effectiveFilter(); f != "" {
		head.clip(fmt.Sprintf("FINDINGS  %d of %d | filter %q", n, v.matchTotal, f), tuiSGRDim, v.color)
	} else {
		head.clip(fmt.Sprintf("FINDINGS  %d of %d", n, v.matchTotal), tuiSGRDim, v.color)
	}
	if v.prioMask != 0 && v.prioMask != tuiPrioAll {
		pi := tuiPriorityMaskIndex(v.prioMask)
		head.clip(" | priority "+strings.ToUpper(tuiPriorityName(pi)), tuiPrioritySGR(pi), v.color)
	}
	if v.matchBuffered < v.matchTotal {
		head.clip(fmt.Sprintf(" | buffered %d", v.matchBuffered), tuiSGRDim, v.color)
	}
	switch {
	case start > 0 && end < n:
		head.clip(fmt.Sprintf(" | ^%d v%d", start, n-end), tuiSGRDim, v.color)
	case start > 0:
		head.clip(fmt.Sprintf(" | ^%d", start), tuiSGRDim, v.color)
	case end < n:
		head.clip(fmt.Sprintf(" | v%d", n-end), tuiSGRDim, v.color)
	}
	lines = append(lines, head.finish())
	if showTop {
		lines = append(lines, tuiTopSigsLine(v))
	}

	if n == 0 {
		msg := "  no findings yet - the scanner is still working"
		if v.effectiveFilter() != "" {
			msg = "  no finding matches this filter (c clears it)"
		} else if v.prioMask != 0 && v.prioMask != tuiPrioAll {
			msg = "  no finding at this priority (p cycles the filter)"
		}
		for len(lines) < listH {
			lines = append(lines, tuiTextLine(msg, v.width, tuiSGRDim, v.color))
			msg = ""
		}
		return lines
	}

	for i := start; i < end && len(lines) < listH; i++ {
		lines = append(lines, tuiFindingRow(v, v.matches[i], i == v.sel, repoW, fileW, sigW))
	}
	for len(lines) < listH {
		lines = append(lines, tuiBlankLine(v.width))
	}
	return lines
}

func tuiFindingRow(v *tuiView, e *tuiMatchEntry, selected bool, repoW, fileW, sigW int) tuiLine {
	b := newTUILine(v.width)
	m := e.match
	pi := tuiPriorityIndex(m.Priority)

	if selected {
		b.paint("> ", tuiSGRBold+tuiSGRCyan, v.color)
	} else {
		b.plain("  ")
	}
	b.fit(tuiClock(m.Timestamp), 9, tuiSGRDim, v.color)
	b.fit(tuiPriorityBadge(pi), 5, tuiPrioritySGR(pi), v.color)
	b.fit(m.Source, 9, tuiSGRDim, v.color)
	b.fit(tuiMatchRepoLabel(m), repoW, "", v.color)
	b.fit(" "+tuiMatchFileLabel(m), fileW, tuiSGRCyan, v.color)
	sgr := ""
	if selected {
		sgr = tuiSGRBold
	}
	b.fit(" "+m.Signature+tuiStars(m), sigW, sgr, v.color)
	return b.finish()
}

func tuiStars(m *TUIMatch) string {
	if m == nil || m.Stars <= 0 {
		return ""
	}
	return fmt.Sprintf(" *%d", m.Stars)
}

// tuiTopSigsLine is the terminal form of the dashboard's "top signatures" panel:
// the five signatures with the most findings, with their counts.
func tuiTopSigsLine(v *tuiView) tuiLine {
	b := newTUILine(v.width)
	b.paint("  top ", tuiSGRDim+tuiSGRBold, v.color)
	for i, s := range v.topSigs {
		if i > 0 {
			b.clip("  ", tuiSGRDim, v.color)
		}
		b.clip(fmt.Sprintf("%s %d", s.Name, s.Count), tuiSGRDim, v.color)
	}
	return b.finish()
}

// tuiFileBody renders the captured file around a finding - the terminal twin of
// the dashboard's "view file" modal. The finding's own line is highlighted, and
// the view scrolls with the same keys as the list.
func tuiFileBody(v *tuiView, height int) []tuiLine {
	lines := make([]tuiLine, 0, height)

	head := newTUILine(v.width)
	if !v.filePresent {
		head.clip("FILE  no captured file context for this finding", tuiSGRDim+tuiSGRBold, v.color)
		lines = append(lines, head.finish())
		lines = append(lines,
			tuiTextLine("  The scanner keeps the file body only for findings captured in this", v.width, tuiSGRDim, v.color),
			tuiTextLine("  run, and only for a bounded number of them. Press v or Esc to", v.width, tuiSGRDim, v.color),
			tuiTextLine("  return to the finding.", v.width, tuiSGRDim, v.color),
		)
		return tuiPadLines(lines, v.width, height)
	}

	label := v.filePath
	if v.fileLine > 0 {
		label = fmt.Sprintf("%s:%d", v.filePath, v.fileLine)
	}
	if label == "" {
		label = "(unknown file)"
	}
	head.paint("  FILE ", tuiSGRDim+tuiSGRBold, v.color)
	head.clip(label, tuiSGRCyan, v.color)
	if v.fileTruncated {
		head.clip("  (truncated)", tuiSGRYellow, v.color)
	}
	head.dock(fmt.Sprintf("line %d of %d  |  v/Esc close", v.fileScroll+1, len(v.fileLines)), tuiSGRDim, v.color)
	lines = append(lines, head.finish())

	rows := height - 1
	if rows < 1 {
		return tuiPadLines(lines, v.width, height)
	}
	start := tuiClamp(v.fileScroll, 0, len(v.fileLines)-1)
	for i := 0; i < rows && start+i < len(v.fileLines); i++ {
		num := start + i + 1
		b := newTUILine(v.width)
		b.paint(fmt.Sprintf("%5d ", num), tuiSGRDim, v.color)
		sgr := ""
		if num == v.fileLine {
			// The line the secret was found on.
			sgr = tuiSGRBold + tuiSGRYellow
		}
		b.clip(v.fileLines[start+i], sgr, v.color)
		lines = append(lines, b.finish())
	}
	return tuiPadLines(lines, v.width, height)
}

func tuiDetailLines(v *tuiView, detH int) []tuiLine {
	w := v.width
	e := v.selectedEntry()
	if e == nil {
		msg := " no finding selected yet"
		if v.effectiveFilter() != "" {
			msg = " nothing matches the filter"
		}
		return tuiPadLines([]tuiLine{tuiTextLine(msg, w, tuiSGRDim, v.color)}, w, detH)
	}
	m := e.match
	pi := tuiPriorityIndex(m.Priority)

	lines := make([]tuiLine, 0, detH+4)

	// Title: signature + priority + source + stars.
	title := newTUILine(w)
	title.paint(" > ", tuiSGRBold+tuiSGRCyan, v.color)
	title.clip(m.Signature, tuiSGRBold, v.color)
	title.clip(" ["+strings.ToUpper(tuiPriorityName(pi))+"]", tuiPrioritySGR(pi), v.color)
	if m.Source != "" {
		title.clip("  "+m.Source+tuiStars(m), tuiSGRDim, v.color)
	}
	lines = append(lines, title.finish())

	const labelW = 8
	put := func(label, value, sgr string) {
		b := newTUILine(w)
		b.paint(" "+tuiFit(label, labelW), tuiSGRDim, v.color)
		b.clip(value, sgr, v.color)
		lines = append(lines, b.finish())
	}

	put("repo", tuiMatchRepoLabel(m), "")
	put("file", tuiMatchFileLabel(m), tuiSGRCyan)
	if m.URL != "" && m.URL != tuiMatchRepoLabel(m) {
		put("url", m.URL, "")
	}
	if link := tuiMatchLink(m); link != "" {
		l := newTUILine(w)
		l.paint(" "+tuiFit("link", labelW), tuiSGRDim, v.color)
		l.link(link, link, w-labelW-1, v.color)
		lines = append(lines, l.finish())
	}
	if len(m.Matches) > 0 {
		put("matches", fmt.Sprintf("%d matched string(s)", len(m.Matches)), tuiSGRDim)
	}

	// Exact matched text, redacted exactly as the plain terminal mode does.
	secret := tuiSecretPreview(m.Secret)
	if strings.TrimSpace(secret) == "" {
		put("secret", "(not captured)", tuiSGRDim)
	} else {
		indent := labelW + 1
		wrapped := tuiWrap(secret, w-indent)
		for i, part := range wrapped {
			b := newTUILine(w)
			if i == 0 {
				b.paint(" "+tuiFit("secret", labelW), tuiSGRDim, v.color)
			} else {
				b.paint(strings.Repeat(" ", indent), "", v.color)
			}
			b.clip(part, tuiSGRYellow, v.color)
			lines = append(lines, b.finish())
		}
	}

	// AI review, streamed in per finding by the review pipeline.
	if rev, ok := v.reviews[m.ID]; ok && rev != nil {
		head := newTUILine(w)
		head.paint(" AI REVIEW", tuiSGRDim+tuiSGRBold, v.color)
		if rev.Running {
			// The spinner advances on the frame ticker, so a long review still
			// visibly moves instead of looking frozen.
			head.clip(" ("+tuiSpinner(v)+" running...)", tuiSGRYellow, v.color)
		}
		lines = append(lines, head.finish())
		if strings.TrimSpace(rev.Text) == "" {
			text := "  waiting for the model..."
			if !rev.Running {
				text = "  (no review text)"
			}
			lines = append(lines, tuiTextLine(text, w, tuiSGRDim, v.color))
		} else {
			for _, part := range tuiWrap(rev.Text, w-2) {
				lines = append(lines, tuiTextLine("  "+part, w, "", v.color))
			}
		}
	}

	if len(lines) > detH {
		lines = lines[:detH]
		if detH >= 1 {
			lines[detH-1] = tuiTextLine(" ... detail truncated - enlarge the window", w, tuiSGRDim, v.color)
		}
	}
	return tuiPadLines(lines, w, detH)
}

func tuiTokensBody(v *tuiView, height int) []tuiLine {
	lines := make([]tuiLine, 0, height)
	valid := 0
	for _, t := range v.tokens {
		if t.Valid {
			valid++
		}
	}
	head := newTUILine(v.width)
	head.clip(fmt.Sprintf("TOKENS  %d validated", len(v.tokens)), tuiSGRDim, v.color)
	head.clip(fmt.Sprintf(" | valid %d", valid), tuiSGRGreen, v.color)
	head.clip(fmt.Sprintf(" | invalid %d", len(v.tokens)-valid), tuiSGRRed, v.color)
	lines = append(lines, head.finish())

	if len(v.tokens) == 0 {
		lines = append(lines, tuiTextLine("  no token has been validated yet", v.width, tuiSGRDim, v.color))
		return tuiPadLines(lines, v.width, height)
	}

	cols := newTUILine(v.width)
	cols.paint("  ", "", v.color)
	cols.paint(tuiFit("TIME", 9), tuiSGRDim, v.color)
	cols.paint(tuiFit("PROVIDER", 14), tuiSGRDim, v.color)
	cols.paint(tuiFit("STATUS", 8), tuiSGRDim, v.color)
	cols.clip("TOKEN", tuiSGRDim, v.color)
	lines = append(lines, cols.finish())

	rows := height - len(lines)
	if rows < 1 {
		rows = 1
	}
	start, count := tuiListWindow(len(v.tokens), v.sel, rows)
	for i := start; i < start+count && len(lines) < height; i++ {
		t := v.tokens[i]
		b := newTUILine(v.width)
		if i == v.sel {
			b.paint("> ", tuiSGRBold+tuiSGRCyan, v.color)
		} else {
			b.plain("  ")
		}
		b.fit(tuiClock(t.Timestamp), 9, tuiSGRDim, v.color)
		provider := t.Provider
		if provider == "" {
			provider = "(unknown)"
		}
		b.fit(provider, 14, "", v.color)
		if t.Valid {
			b.fit("valid", 8, tuiSGRGreen, v.color)
		} else {
			b.fit("invalid", 8, tuiSGRRed, v.color)
		}
		b.clip(t.Token, tuiSGRYellow, v.color)
		lines = append(lines, b.finish())
	}
	return tuiPadLines(lines, v.width, height)
}

func tuiLogsBody(v *tuiView, height int) []tuiLine {
	lines := make([]tuiLine, 0, height)
	n := len(v.logs)

	head := newTUILine(v.width)
	if v.logBack > 0 {
		head.clip(fmt.Sprintf("LOGS  scrolled back %d line(s) of %d | g top | G bottom", v.logBack, n), tuiSGRYellow, v.color)
	} else {
		head.clip(fmt.Sprintf("LOGS  following (%d line(s))", n), tuiSGRDim, v.color)
	}
	lines = append(lines, head.finish())

	rows := height - 1
	if rows < 1 {
		return tuiPadLines(lines, v.width, height)
	}
	if n == 0 {
		return tuiPadLines(append(lines, tuiTextLine("  no log output yet", v.width, tuiSGRDim, v.color)), v.width, height)
	}

	end := n - v.logBack
	end = tuiClamp(end, 0, n)
	start := end - rows
	if start < 0 {
		start = 0
	}
	for i := start; i < end && len(lines) < height; i++ {
		l := v.logs[i]
		b := newTUILine(v.width)
		b.paint(" "+tuiFit(tuiClock(l.Timestamp), 9), tuiSGRDim, v.color)
		sgr := ""
		if strings.Contains(l.Text, "ERROR") || strings.Contains(l.Text, "error") {
			sgr = tuiSGRRed
		} else if strings.Contains(l.Text, "WARN") || strings.Contains(l.Text, "warn") {
			sgr = tuiSGRYellow
		}
		b.clip(l.Text, sgr, v.color)
		lines = append(lines, b.finish())
	}
	return tuiPadLines(lines, v.width, height)
}

var tuiHelpLines = []string{
	"shhgit - real-time secret scanner",
	"",
	"Findings, log lines and live token checks stream in as the scanner runs;",
	"nothing you type here pauses the scan.",
	"",
	"TABS",
	"  1 Findings   every secret found, newest first, with the exact match",
	"  2 Tokens     live validation results for leaked API tokens",
	"  3 Logs       the scanner's own log stream (follows the tail)",
	"  4 Help       this page",
	"",
	"KEYS ON THE FINDINGS TAB",
	"  up / k, down / j   move the selection",
	"  PgUp / PgDn        page through the list",
	"  g / G              jump to the first / last finding",
	"  /                  filter by repo, file, signature, source or secret",
	"  Enter / Esc        apply / cancel the filter while typing",
	"  p / P              cycle the priority filter (all/crit/high/med/low)",
	"  v                  view the captured file around the finding (Esc closes)",
	"  r                  ask the AI to review the selected finding",
	"  c                  clear the text and priority filters",
	"  o                  open the selected finding's link in a browser",
	"",
	"IN THE FILE VIEW",
	"  up / down, j / k   scroll one line",
	"  PgUp / PgDn        scroll one page",
	"  g / G              jump to the top / bottom",
	"  v or Esc           close the file view",
	"",
	"KEYS ON EVERY TAB",
	"  1-4, Tab           switch tab (Shift-Tab goes back)",
	"  left / right       previous / next tab",
	"  ?                  toggle this help",
	"  q or Ctrl-C        quit - the terminal is always restored",
	"",
	"NOTES",
	"  Secrets are copied into this UI only for display. Private-key bodies",
	"  and secrets longer than 200 characters are redacted, never printed.",
	"  Frames are written in one buffered write and only when something",
	"  changed, so the display stays flicker-free.",
}

func tuiHelpBody(v *tuiView, height int) []tuiLine {
	lines := make([]tuiLine, 0, height)
	if len(tuiHelpLines) > height {
		for _, s := range tuiHelpLines[:height-1] {
			lines = append(lines, tuiTextLine(s, v.width, "", v.color))
		}
		lines = append(lines, tuiTextLine(" ... more help available - enlarge the window", v.width, tuiSGRDim, v.color))
		return tuiPadLines(lines, v.width, height)
	}
	for _, s := range tuiHelpLines {
		sgr := ""
		if strings.TrimSpace(s) == s && s != "" && s == strings.ToUpper(s) {
			sgr = tuiSGRBold + tuiSGRCyan // section headers are upper case
		} else if strings.HasPrefix(s, "  ") {
			sgr = tuiSGRDim
		}
		lines = append(lines, tuiTextLine(s, v.width, sgr, v.color))
	}
	return tuiPadLines(lines, v.width, height)
}

// ─── Input decoding ─────────────────────────────────────────────────────────

type tuiKeyKind int

const (
	tuiKeyNone tuiKeyKind = iota
	tuiKeyRune
	tuiKeyUp
	tuiKeyDown
	tuiKeyLeft
	tuiKeyRight
	tuiKeyPgUp
	tuiKeyPgDn
	tuiKeyHome
	tuiKeyEnd
	tuiKeyEnter
	tuiKeyEsc
	tuiKeyBackspace
	tuiKeyTab
	tuiKeyBackTab
	tuiKeyCtrlC
	tuiKeyCtrlU
)

type tuiKey struct {
	kind tuiKeyKind
	r    rune
}

const (
	tuiDecodeGround = iota
	tuiDecodeEsc
	tuiDecodeCSI
	tuiDecodeSS3
)

// tuiKeyDecoder turns a byte stream into keys. It is a plain state machine so
// the exact escape sequences a terminal sends can be unit-tested without a TTY.
type tuiKeyDecoder struct {
	state  int
	params []byte
	utf8   []byte
}

// pending reports whether a partial sequence is waiting for more bytes; when it
// is, the caller arms the ESC timeout.
func (d *tuiKeyDecoder) pending() bool {
	return d.state != tuiDecodeGround || len(d.utf8) > 0
}

func (d *tuiKeyDecoder) feed(b byte) []tuiKey {
	switch d.state {
	case tuiDecodeEsc:
		switch b {
		case '[':
			d.state = tuiDecodeCSI
			d.params = d.params[:0]
			return nil
		case 'O':
			d.state = tuiDecodeSS3
			return nil
		default:
			// A lone ESC followed by something else: report ESC and re-read the
			// byte from the ground state.
			d.state = tuiDecodeGround
			return append([]tuiKey{{kind: tuiKeyEsc}}, d.feed(b)...)
		}
	case tuiDecodeCSI:
		if b >= 0x40 && b <= 0x7e {
			k := tuiCSIKey(b, d.params)
			d.state = tuiDecodeGround
			d.params = d.params[:0]
			if k.kind == tuiKeyNone {
				return nil
			}
			return []tuiKey{k}
		}
		if len(d.params) < 32 {
			d.params = append(d.params, b)
		}
		return nil
	case tuiDecodeSS3:
		d.state = tuiDecodeGround
		k := tuiSS3Key(b)
		if k.kind == tuiKeyNone {
			return nil
		}
		return []tuiKey{k}
	}

	switch {
	case b == 0x1b:
		d.state = tuiDecodeEsc
		return nil
	case b == '\r' || b == '\n':
		return []tuiKey{{kind: tuiKeyEnter}}
	case b == 0x7f || b == 0x08:
		return []tuiKey{{kind: tuiKeyBackspace}}
	case b == '\t':
		return []tuiKey{{kind: tuiKeyTab}}
	case b == 0x03:
		return []tuiKey{{kind: tuiKeyCtrlC}}
	case b == 0x15:
		return []tuiKey{{kind: tuiKeyCtrlU}}
	case b < 0x20:
		return nil
	case b < 0x80:
		d.utf8 = d.utf8[:0]
		return []tuiKey{{kind: tuiKeyRune, r: rune(b)}}
	default:
		d.utf8 = append(d.utf8, b)
		if !utf8.FullRune(d.utf8) {
			if len(d.utf8) >= utf8.UTFMax {
				d.utf8 = d.utf8[:0]
			}
			return nil
		}
		r, _ := utf8.DecodeRune(d.utf8)
		d.utf8 = d.utf8[:0]
		if r == utf8.RuneError {
			return nil
		}
		return []tuiKey{{kind: tuiKeyRune, r: r}}
	}
}

// flush ends a partial sequence, which happens when a bare ESC was typed (or a
// sequence was cut off mid-flight).
func (d *tuiKeyDecoder) flush() []tuiKey {
	var out []tuiKey
	if d.state == tuiDecodeEsc {
		out = append(out, tuiKey{kind: tuiKeyEsc})
	}
	d.state = tuiDecodeGround
	d.params = d.params[:0]
	d.utf8 = d.utf8[:0]
	return out
}

func tuiFirstParam(params []byte) int {
	n, seen := 0, false
	for _, c := range params {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
			seen = true
			if n > 1000000 {
				n = 1000000
			}
			continue
		}
		if seen {
			return n
		}
	}
	return n
}

func tuiCSIKey(final byte, params []byte) tuiKey {
	switch final {
	case 'A':
		return tuiKey{kind: tuiKeyUp}
	case 'B':
		return tuiKey{kind: tuiKeyDown}
	case 'C':
		return tuiKey{kind: tuiKeyRight}
	case 'D':
		return tuiKey{kind: tuiKeyLeft}
	case 'H':
		return tuiKey{kind: tuiKeyHome}
	case 'F':
		return tuiKey{kind: tuiKeyEnd}
	case 'Z':
		return tuiKey{kind: tuiKeyBackTab}
	case '~':
		switch tuiFirstParam(params) {
		case 1, 7:
			return tuiKey{kind: tuiKeyHome}
		case 4, 8:
			return tuiKey{kind: tuiKeyEnd}
		case 5:
			return tuiKey{kind: tuiKeyPgUp}
		case 6:
			return tuiKey{kind: tuiKeyPgDn}
		}
	}
	return tuiKey{kind: tuiKeyNone}
}

func tuiSS3Key(b byte) tuiKey {
	switch b {
	case 'A':
		return tuiKey{kind: tuiKeyUp}
	case 'B':
		return tuiKey{kind: tuiKeyDown}
	case 'C':
		return tuiKey{kind: tuiKeyRight}
	case 'D':
		return tuiKey{kind: tuiKeyLeft}
	case 'H':
		return tuiKey{kind: tuiKeyHome}
	case 'F':
		return tuiKey{kind: tuiKeyEnd}
	}
	return tuiKey{kind: tuiKeyNone}
}

// ─── Key handling ───────────────────────────────────────────────────────────

func (u *tuiUI) setTab(tab int) {
	if tab < 0 || tab >= tuiTabCount || tab == u.tab {
		return
	}
	if tab == tuiTabHelp && u.tab != tuiTabHelp {
		u.prevTab = u.tab
	}
	u.tab = tab
	u.dirty = true
}

// handleKey applies one key and reports whether the UI should quit.
func (u *tuiUI) handleKey(k tuiKey, v *tuiView) bool {
	if u.filtering {
		switch k.kind {
		case tuiKeyCtrlC:
			return true
		case tuiKeyRune:
			if k.r >= 0x20 && k.r != 0x7f {
				u.input += string(k.r)
				u.dirty = true
			}
			return false
		case tuiKeyEnter:
			u.filter = u.input
			u.filtering = false
			u.sel = 0
			u.dirty = true
			return false
		case tuiKeyEsc:
			u.filtering = false
			u.input = u.filter
			u.sel = 0
			u.dirty = true
			return false
		case tuiKeyBackspace:
			u.input = tuiTrimLastRune(u.input)
			u.sel = 0
			u.dirty = true
			return false
		case tuiKeyCtrlU:
			u.input = ""
			u.sel = 0
			u.dirty = true
			return false
		case tuiKeyUp, tuiKeyDown, tuiKeyPgUp, tuiKeyPgDn, tuiKeyHome, tuiKeyEnd:
			u.navigate(k, v)
			return false
		default:
			return false
		}
	}

	switch k.kind {
	case tuiKeyCtrlC:
		return true
	case tuiKeyRune:
		return u.handleRune(k.r, v)
	case tuiKeyTab:
		u.setTab((u.tab + 1) % tuiTabCount)
		return false
	case tuiKeyBackTab:
		u.setTab((u.tab + tuiTabCount - 1) % tuiTabCount)
		return false
	case tuiKeyEsc:
		// Esc closes the file viewer; outside it, Esc is a no-op so a stray key
		// never destroys the current view.
		if u.fileView {
			u.fileView = false
			u.dirty = true
		}
		return false
	case tuiKeyUp, tuiKeyDown, tuiKeyLeft, tuiKeyRight, tuiKeyPgUp, tuiKeyPgDn, tuiKeyHome, tuiKeyEnd:
		u.navigate(k, v)
		return false
	}
	return false
}

func (u *tuiUI) handleRune(r rune, v *tuiView) bool {
	switch r {
	case 'q', 'Q':
		return true
	case '1', '2', '3', '4':
		u.setTab(int(r - '1'))
	case '/':
		u.filtering = true
		u.input = u.filter
		u.sel = 0
		u.dirty = true
	case 'c':
		// "clear" resets both filters, so one key returns to the full list.
		u.filter = ""
		u.input = ""
		u.filtering = false
		u.prioMask = tuiPrioAll
		u.sel = 0
		u.dirty = true
	case 'o':
		u.openSelected(v)
	case 'p':
		u.cyclePriority(1)
	case 'P':
		u.cyclePriority(-1)
	case 'v':
		u.toggleFileView(v)
	case 'r':
		u.requestReview(v)
	case '?':
		if u.tab == tuiTabHelp {
			u.setTab(u.prevTab)
		} else {
			u.setTab(tuiTabHelp)
		}
	case 'j':
		u.navigate(tuiKey{kind: tuiKeyDown}, v)
	case 'k':
		u.navigate(tuiKey{kind: tuiKeyUp}, v)
	case 'g':
		u.navigate(tuiKey{kind: tuiKeyHome}, v)
	case 'G':
		u.navigate(tuiKey{kind: tuiKeyEnd}, v)
	}
	return false
}

func (u *tuiUI) navigate(k tuiKey, v *tuiView) {
	switch k.kind {
	case tuiKeyLeft:
		u.setTab((u.tab + tuiTabCount - 1) % tuiTabCount)
		return
	case tuiKeyRight:
		u.setTab((u.tab + 1) % tuiTabCount)
		return
	}

	// The file viewer scrolls its own body, but left/right above still switch
	// tabs so the operator is never trapped in it.
	if u.fileView && u.tab == tuiTabFindings {
		u.navigateFile(v, k)
		return
	}

	page := tuiBodyHeight(v.height)
	if page < 1 {
		page = 1
	}

	switch u.tab {
	case tuiTabLogs:
		switch k.kind {
		case tuiKeyUp:
			u.logBack++
		case tuiKeyDown:
			u.logBack--
		case tuiKeyPgUp:
			u.logBack += page
		case tuiKeyPgDn:
			u.logBack -= page
		case tuiKeyHome:
			u.logBack = len(v.logs)
		case tuiKeyEnd:
			u.logBack = 0
		}
		u.logBack = tuiClamp(u.logBack, 0, len(v.logs))
	case tuiTabHelp:
		// Help scrolls with the window; nothing to move.
	default:
		switch k.kind {
		case tuiKeyUp:
			u.moveSelection(v, -1)
		case tuiKeyDown:
			u.moveSelection(v, 1)
		case tuiKeyPgUp:
			u.moveSelection(v, -page)
		case tuiKeyPgDn:
			u.moveSelection(v, page)
		case tuiKeyHome:
			u.sel = 0
			u.dirty = true
		case tuiKeyEnd:
			u.sel = tuiListLenFor(v.tab, v) - 1
			u.dirty = true
		}
	}
	u.dirty = true
}

func (u *tuiUI) moveSelection(v *tuiView, delta int) {
	n := tuiListLenFor(v.tab, v)
	if n <= 0 {
		u.sel = 0
		u.dirty = true
		return
	}
	u.sel = tuiClamp(u.sel+delta, 0, n-1)
	u.dirty = true
}

func (u *tuiUI) openSelected(v *tuiView) {
	if v.tab != tuiTabFindings {
		u.flash("links are opened from the Findings tab")
		return
	}
	e := v.selectedEntry()
	if e == nil {
		u.flash("no finding selected")
		return
	}
	link := tuiMatchLink(e.match)
	if link == "" {
		u.flash("this finding has no link to open")
		return
	}
	openBrowser(link)
	u.flash("opened " + link)
}

// toggleFileView opens or closes the captured-file pane for the selected
// finding: the terminal twin of the dashboard's "view file" modal. It only
// opens when the scanner actually kept a body for this match.
func (u *tuiUI) toggleFileView(v *tuiView) {
	if u.tab != tuiTabFindings {
		u.flash("the file view belongs to the Findings tab")
		return
	}
	if u.fileView {
		u.fileView = false
		u.dirty = true
		return
	}
	e := v.selectedEntry()
	if e == nil {
		u.flash("no finding selected")
		return
	}
	if _, ok := tuiLookupMatchFile(e.match.ID); !ok {
		u.flash("no captured file content for this finding")
		return
	}
	u.fileView = true
	// Put the secret's own line at the top of the viewport when it is known.
	u.fileScroll = 0
	if e.match.Line > 1 {
		u.fileScroll = e.match.Line - 1
	}
	u.dirty = true
}

// navigateFile scrolls the captured-file pane.
func (u *tuiUI) navigateFile(v *tuiView, k tuiKey) {
	page := tuiBodyHeight(v.height) - 2
	if page < 1 {
		page = 1
	}
	maxScroll := len(v.fileLines) - 1
	if maxScroll < 0 {
		maxScroll = 0
	}
	switch k.kind {
	case tuiKeyUp:
		u.fileScroll--
	case tuiKeyDown:
		u.fileScroll++
	case tuiKeyPgUp:
		u.fileScroll -= page
	case tuiKeyPgDn:
		u.fileScroll += page
	case tuiKeyHome:
		u.fileScroll = 0
	case tuiKeyEnd:
		u.fileScroll = maxScroll
	default:
		return
	}
	u.fileScroll = tuiClamp(u.fileScroll, 0, maxScroll)
	u.dirty = true
}

// tuiReviewRequest starts an AI review for one finding and streams it into the
// detail pane. runTUIMode installs the real implementation; a nil value means AI
// review is not wired up, and the "r" key reports that rather than doing nothing.
var tuiReviewRequest func(m *TUIMatch) error

func (u *tuiUI) requestReview(v *tuiView) {
	if v.tab != tuiTabFindings {
		u.flash("reviews are requested from the Findings tab")
		return
	}
	e := v.selectedEntry()
	if e == nil {
		u.flash("no finding selected")
		return
	}
	if tuiReviewRequest == nil {
		u.flash("AI review is not available")
		return
	}
	if err := tuiReviewRequest(e.match); err != nil {
		u.flash("AI review: " + err.Error())
		return
	}
	u.flash("AI review started for " + e.match.Signature)
}

func tuiTrimLastRune(s string) string {
	if s == "" {
		return ""
	}
	_, size := utf8.DecodeLastRuneInString(s)
	return s[:len(s)-size]
}

// ─── Runner ─────────────────────────────────────────────────────────────────

// Overridable for tests: the streams the UI drives. They stay *os.File because
// raw mode and the size query both work on file descriptors.
var (
	tuiStdout *os.File = os.Stdout
	tuiStdin  *os.File = os.Stdin
)

func tuiIsTerminal(f *os.File) bool {
	return f != nil && term.IsTerminal(int(f.Fd()))
}

// tuiColorEnabled decides whether to emit SGR/OSC styling at all.
func tuiColorEnabled(out *os.File) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return tuiIsTerminal(out)
}

// StartTUI runs the interactive UI until the user quits. It returns an error
// without touching the terminal when it cannot run safely (stdout or stdin is
// not a terminal, the window is too small, the console refuses VT mode, raw
// mode is unavailable). On every exit path - return, error or panic - the
// terminal is restored: cursor visible, colours reset, alternate screen left,
// cooked mode back.
func StartTUI() error {
	out, in := tuiStdout, tuiStdin
	if out == nil || in == nil {
		return errors.New("tui: no terminal available")
	}
	outFD, inFD := int(out.Fd()), int(in.Fd())

	// Everything is validated before a single byte is written, so a failed
	// start can never leave the terminal in a strange state.
	if !term.IsTerminal(outFD) {
		return errors.New("tui: stdout is not a terminal")
	}
	if !term.IsTerminal(inFD) {
		return errors.New("tui: stdin is not a terminal (the UI needs the keyboard)")
	}
	w, h, err := term.GetSize(outFD)
	if err != nil {
		return fmt.Errorf("tui: cannot read the terminal size: %w", err)
	}
	if w < tuiMinWidth || h < tuiMinHeight {
		return fmt.Errorf("tui: terminal is %dx%d, the UI needs at least %dx%d", w, h, tuiMinWidth, tuiMinHeight)
	}
	if !enableVT() {
		return errors.New("tui: this console does not support ANSI/VT escape sequences")
	}

	oldState, err := term.MakeRaw(inFD)
	if err != nil {
		return fmt.Errorf("tui: cannot switch the terminal into raw mode: %w", err)
	}

	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		fmt.Fprint(out, tuiReset+"\033[?25h\033[?1049l")
		if err := term.Restore(inFD, oldState); err != nil {
			fmt.Fprintf(out, "%s\n", "tui: could not restore the terminal: "+err.Error())
		}
	}
	defer func() {
		if r := recover(); r != nil {
			restore()
			panic(r)
		}
		restore()
	}()

	// Alternate screen, hidden cursor, blank slate.
	if _, err := io.WriteString(out, "\033[?1049h\033[?25l\033[2J\033[H"); err != nil {
		return fmt.Errorf("tui: cannot use the terminal: %w", err)
	}

	color := tuiColorEnabled(out)
	b := tuiState
	ui := &tuiUI{started: time.Now()}
	dec := &tuiKeyDecoder{}

	// Keyboard reader: one goroutine feeding a buffered channel, so a slow
	// frame can never block the console read and vice versa.
	keys := make(chan byte, 256)
	readErr := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		buf := make([]byte, 64)
		for {
			n, err := in.Read(buf)
			for i := 0; i < n; i++ {
				select {
				case keys <- buf[i]:
				case <-done:
					return
				}
			}
			if err != nil {
				select {
				case readErr <- err:
				default:
				}
				return
			}
		}
	}()

	ticker := time.NewTicker(tuiTickInterval)
	defer ticker.Stop()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	escTimer := time.NewTimer(time.Hour)
	if !escTimer.Stop() {
		<-escTimer.C
	}
	defer escTimer.Stop()
	escC := escTimer.C
	armEsc := func() {
		if !escTimer.Stop() {
			select {
			case <-escTimer.C:
			default:
			}
		}
		escTimer.Reset(tuiEscTimeout)
	}

	var (
		lastVersion uint64
		lastDraw    time.Time
	)
	draw := func(v *tuiView) error {
		if _, err := io.WriteString(out, renderTUIView(v)); err != nil {
			return err
		}
		lastVersion = v.version
		lastDraw = time.Now()
		ui.dirty = false
		return nil
	}

	v := ui.view(b, w, h, color)
	if err := draw(v); err != nil {
		return fmt.Errorf("tui: cannot draw: %w", err)
	}

	for {
		select {
		case by := <-keys:
			for _, k := range dec.feed(by) {
				if ui.handleKey(k, v) {
					return nil
				}
			}
			if dec.pending() {
				armEsc()
			} else {
				if !escTimer.Stop() {
					select {
					case <-escTimer.C:
					default:
					}
				}
			}
			v = ui.view(b, w, h, color)
			if err := draw(v); err != nil {
				return fmt.Errorf("tui: cannot draw: %w", err)
			}

		case <-escC:
			keysOut := dec.flush()
			if len(keysOut) == 0 {
				continue
			}
			for _, k := range keysOut {
				if ui.handleKey(k, v) {
					return nil
				}
			}
			v = ui.view(b, w, h, color)
			if err := draw(v); err != nil {
				return fmt.Errorf("tui: cannot draw: %w", err)
			}

		case <-ticker.C:
			ui.spin++
			resized := false
			if nw, nh, err := term.GetSize(outFD); err == nil && (nw != w || nh != h) {
				w, h, resized = nw, nh, true
			}
			v = ui.view(b, w, h, color)
			if !resized && !ui.dirty && v.version == lastVersion && time.Since(lastDraw) < time.Second {
				continue
			}
			if err := draw(v); err != nil {
				return fmt.Errorf("tui: cannot draw: %w", err)
			}

		case <-b.notify:
			// New findings/logs/tokens: show them without waiting for the tick.
			v = ui.view(b, w, h, color)
			if err := draw(v); err != nil {
				return fmt.Errorf("tui: cannot draw: %w", err)
			}

		case <-sigCh:
			return nil

		case err := <-readErr:
			if err == nil || errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("tui: cannot read the keyboard: %w", err)
		}
	}
}
