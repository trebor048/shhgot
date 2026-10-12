package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/fatih/color"
	"github.com/trebor048/shhgot/core"
	"github.com/trebor048/shhgot/core/httpclient"
	"github.com/trebor048/shhgot/core/notify"
	"github.com/trebor048/shhgot/core/store"
	"github.com/trebor048/shhgot/core/verifiers"
	"golang.org/x/term"
)

// ═══════════════════════════════════════════════════════════════
//  MODE SYSTEM - WEB/TUI/SCANNER
// ═══════════════════════════════════════════════════════════════

func init() {
	// Initialize mode system early (modeConfig is declared in modes.go)
	modeConfig = InitIntegratedMode()
	modeConfig.RemoveModeFlags()
}

// ═══════════════════════════════════════════════════════════════
//  LOG FORMAT PRESETS
//  Set via config.yaml → logFormat: "minimal" | "fancy" |
//  "ultra fancy" | "even more fancy" | "neon"
// ═══════════════════════════════════════════════════════════════

const (
	LogFormatMinimal       = "minimal"
	LogFormatFancy         = "fancy"
	LogFormatUltraFancy    = "ultra fancy"
	LogFormatEvenMoreFancy = "even more fancy"
	LogFormatNeon          = "neon"
)

var session *core.Session
var formatOrder = []string{LogFormatMinimal, LogFormatFancy, LogFormatUltraFancy, LogFormatEvenMoreFancy, LogFormatNeon}
var currentLogFormat = LogFormatFancy

// getSession lazily initializes the session on first use
func getSession() *core.Session {
	if session == nil {
		session = core.GetSession()
	}
	return session
}

func setLogFormat(f string) {
	for _, preset := range formatOrder {
		if f == preset {
			currentLogFormat = f
			return
		}
	}
	currentLogFormat = LogFormatFancy
}

// getLogFormat returns the current log format preset
func getLogFormat() string {
	return currentLogFormat
}

// --------------------------------------------------------------
//
//	HELPERS
//
// --------------------------------------------------------------
func isSignatureExcludedInMode(signature core.Signature, mode string) bool {
	excludedModes := signature.GetExcludeInModes()
	if len(excludedModes) == 0 {
		return false
	}
	for _, excludedMode := range excludedModes {
		if strings.EqualFold(excludedMode, mode) {
			return true
		}
	}
	return false
}

// currentRunMode names the active run mode for signature exclude_in_modes: a
// --local scan is "local", otherwise the selected integrated mode, defaulting to
// "terminal". It was previously compared against the log-format preset, which
// made the documented `exclude_in_modes: ['local']` a no-op.
func currentRunMode() string {
	if o := getSession().Options; o != nil && o.Local != nil && strings.TrimSpace(*o.Local) != "" {
		return "local"
	}
	if modeConfig != nil {
		switch modeConfig.Mode {
		case ModeWeb:
			return "web"
		case ModeTUI:
			return "tui"
		}
	}
	return "terminal"
}

// containsWord reports whether needle occurs in haystack delimited by
// non-alphanumeric characters (or the string boundaries) on both sides.
func containsWord(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	for start := 0; ; {
		i := strings.Index(haystack[start:], needle)
		if i < 0 {
			return false
		}
		beg := start + i
		end := beg + len(needle)
		if (beg == 0 || !isAlnumByte(haystack[beg-1])) && (end == len(haystack) || !isAlnumByte(haystack[end])) {
			return true
		}
		start = beg + 1
	}
}

func isAlnumByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// isSignatureExcludedInMode checks if a signature should be excluded in the current mode

// ── stdout serialization helpers (prevents progress-area corruption) ──

var outputMu sync.Mutex

// webLogCapture, when true, tees all console output into the web log buffer
// so the dashboard can show the live scanner log stream.
var webLogCapture bool

// tuiScreenActive, when true, means the interactive terminal UI owns stdout.
// The scanner's styled output is then diverted into the TUI Logs tab instead of
// being printed, because anything written to stdout would land on top of the
// live frame and corrupt it. It is only set once the UI is known to be able to
// take over the terminal.
var tuiScreenActive bool

// tuiActive reports whether the interactive TUI owns this run. The TUI buffer
// (tuiState) is never nil, so its presence cannot be used as the mode test;
// this consults the selected mode instead.
func tuiActive() bool {
	return modeConfig != nil && modeConfig.Mode == ModeTUI
}

func lockPrintf(format string, args ...interface{}) {
	outputMu.Lock()
	emitLocked(fmt.Sprintf(format, args...))
	outputMu.Unlock()
}

func lockPrintln(args ...interface{}) {
	outputMu.Lock()
	emitLocked(fmt.Sprintln(args...))
	outputMu.Unlock()
}

// carriageReturnLines rewrites s so that every line begins in column 0 by
// putting a bare carriage return in front of it. Styled scanner output already
// carries that guarantee (core prefixes its log lines with "\r\033[K", and the
// block builders start at column 0), but the standard logger - used by the
// regex optimizer and the worker pool - emits plain "\n"-terminated lines.
// On a console left with DISABLE_NEWLINE_AUTO_RETURN set, "\n" advances the
// cursor without returning it to column 0, so those lines would start where the
// previous one ended and march across the screen (the staircase effect).
// "\r" is an ordinary control character rather than an escape sequence, so it
// is safe even on consoles that never enabled virtual-terminal processing.
func carriageReturnLines(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	atLineStart := true
	for i := 0; i < len(s); i++ {
		if atLineStart {
			b.WriteByte('\r')
			atLineStart = false
		}
		c := s[i]
		b.WriteByte(c)
		if c == '\n' {
			atLineStart = true
		}
	}
	return b.String()
}

// liveTerminal reports whether writes to f reach a real terminal. Only then may
// the output carry carriage returns; a pipe, a CI transcript or a log file must
// stay free of control characters.
func liveTerminal(f *os.File) bool {
	return f != nil && term.IsTerminal(int(f.Fd()))
}

// emitLocked writes one already-formatted chunk to the current destination.
// Callers must hold outputMu.
func emitLocked(s string) {
	if tuiScreenActive {
		AddTUILog(s)
		return
	}
	if webLogCapture {
		appendLogLine(s)
	}
	if liveTerminal(os.Stdout) {
		s = carriageReturnLines(s)
	}

	// Use typewriter effect if enabled (skip separator lines).
	//
	// Read the package-level session directly instead of getSession(): this runs
	// under routeEarlyOutput's ConsoleWriter, which is installed before the
	// session exists. core.GetSession's init calls ParseConfig -> Say (webhook
	// validation) -> ConsoleWriter -> here, so triggering the lazy initializer
	// from this path re-entered core.GetSession and blocked forever on its
	// non-reentrant sync.Once. Before init the session is simply absent, and the
	// typewriter is cosmetic, so the nil check below is the correct fallback.
	if session != nil && session.Config != nil && session.Config.TypewriterEffect {
		// Check if this is a separator line (all dashes, equals, or box chars)
		isSeparator := true
		for _, r := range s {
			if r != '─' && r != '━' && r != '═' && r != '-' && r != '=' && r != '\n' && r != '\r' && r != ' ' {
				isSeparator = false
				break
			}
		}

		if isSeparator {
			// Print separator instantly
			fmt.Print(s)
		} else {
			// Type out character by character
			core.TypePrint(s)
		}
	} else {
		fmt.Print(s)
	}
}

// lockWrite emits one pre-built block as a single write under the shared
// console mutex. Every multi-line styled block (startup banner, match cards)
// must be built with the blog* helpers below and emitted here, so background
// worker lines can neither split it mid-line nor slip into its middle.
// Single lines keep using lockPrintf/lockPrintln.
func lockWrite(s string) {
	if s == "" {
		return
	}
	outputMu.Lock()
	emitLocked(s)
	outputMu.Unlock()
}

// blogPrintf appends formatted output to a batch builder. It never touches
// the console, so it is safe to call while building a block.
func blogPrintf(sb *strings.Builder, format string, args ...interface{}) {
	fmt.Fprintf(sb, format, args...)
}

// blogPrintln appends its arguments plus a newline to a batch builder.
func blogPrintln(sb *strings.Builder, args ...interface{}) {
	sb.WriteString(fmt.Sprintln(args...))
}

// lockedWriter funnels the standard logger - used by core's regex optimizer
// and worker pool - through the shared console mutex (and the same TUI/web
// diversions as lockPrintf). Without it those timestamped lines are written
// concurrently with styled output and split it mid-line.
type lockedWriter struct{ toStderr bool }

func (w lockedWriter) Write(p []byte) (int, error) {
	outputMu.Lock()
	defer outputMu.Unlock()
	s := string(p)
	if tuiScreenActive {
		AddTUILog(s)
		return len(p), nil
	}
	if webLogCapture {
		appendLogLine(s)
	}
	dest := os.Stdout
	if w.toStderr {
		dest = os.Stderr
	}
	// The standard logger writes bare "\n"-terminated lines, so they need the
	// same column-0 guarantee as the styled output (see carriageReturnLines).
	if liveTerminal(dest) {
		s = carriageReturnLines(s)
	}
	fmt.Fprint(dest, s)
	return len(p), nil
}

// routeEarlyOutput serializes the session-independent background writers.
// It needs no session, so main() calls it before anything can log.
func routeEarlyOutput() {
	log.SetOutput(lockedWriter{toStderr: true})
	core.ConsoleWriter = func(format string, args ...interface{}) {
		lockPrintf(format, args...)
	}
}

// routeCoreLogger funnels core.Logger output through the shared console mutex
// in plain terminal mode. Web and TUI modes install their own richer hooks
// and must not be clobbered, so this only applies when no hook is set.
// LogWriterIsTerminal keeps the live "\r\033[K" progress prefix exactly as if
// no hook were installed.
func routeCoreLogger() {
	if l := getSession().Log; l != nil && l.LogWriter == nil {
		l.LogWriter = func(line string) {
			lockPrintf("%s", line)
		}
		l.LogWriterIsTerminal = true
	}
}

// ── MINIMAL palette ──────────────────────────────────────────
var (
	minInfo    = color.New(color.FgWhite).SprintFunc()
	minSearch  = color.New(color.FgCyan).SprintFunc()
	minSecret  = color.New(color.FgRed, color.Bold).SprintFunc()
	minFile    = color.New(color.FgYellow).SprintFunc()
	minEntropy = color.New(color.FgMagenta).SprintFunc()
	minDim     = color.New(color.Faint).SprintFunc()
	minLink    = color.New(color.FgHiBlue, color.Underline).SprintFunc()
)

// ── FANCY palette ─────────────────────────────────────────────
var (
	fancySearch  = color.New(color.FgHiCyan, color.Bold).SprintFunc()
	fancySecret  = color.New(color.FgHiRed, color.Bold).SprintFunc()
	fancyFile    = color.New(color.FgHiYellow, color.Bold).SprintFunc()
	fancyEntropy = color.New(color.FgHiMagenta, color.Bold).SprintFunc()
	fancyAccent  = color.New(color.FgHiGreen, color.Bold).SprintFunc()
	fancyArrow   = color.New(color.FgHiMagenta).SprintFunc()
	fancyLink    = color.New(color.FgHiCyan, color.Underline).SprintFunc()
)

// ── ULTRA FANCY palette ───────────────────────────────────────
var (
	ultraSearch  = color.New(color.FgHiCyan, color.Bold, color.Underline).SprintFunc()
	ultraSecret  = color.New(color.FgHiRed, color.Bold, color.Underline).SprintFunc()
	ultraFile    = color.New(color.FgHiYellow, color.Bold, color.Italic).SprintFunc()
	ultraEntropy = color.New(color.FgHiMagenta, color.Bold, color.Italic).SprintFunc()
	ultraAccent  = color.New(color.FgHiGreen, color.Bold).SprintFunc()
	ultraBadge   = color.New(color.FgBlack, color.BgHiCyan).SprintFunc()
	ultraDanger  = color.New(color.FgBlack, color.BgHiRed).SprintFunc()
	ultraLink    = color.New(color.FgHiCyan, color.BgHiBlack, color.Bold).SprintFunc()
)

// ── EVEN MORE FANCY palette ───────────────────────────────────
var (
	cosmicBorder  = color.New(color.FgHiCyan, color.Bold).SprintFunc()
	cosmicTitle   = color.New(color.FgHiYellow, color.Bold, color.Underline).SprintFunc()
	cosmicSearch  = color.New(color.FgHiCyan).SprintFunc()
	cosmicSecret  = color.New(color.FgHiRed, color.Bold).SprintFunc()
	cosmicFile    = color.New(color.FgHiYellow).SprintFunc()
	cosmicEntropy = color.New(color.FgHiMagenta).SprintFunc()
	cosmicAccent  = color.New(color.FgHiGreen).SprintFunc()
)

// ── NEON palette (background highlights) ─────────────────────
var (
	neonSearchLabel  = color.New(color.FgBlack, color.BgHiCyan, color.Bold).SprintFunc()
	neonSecretLabel  = color.New(color.FgBlack, color.BgHiRed, color.Bold).SprintFunc()
	neonFileLabel    = color.New(color.FgBlack, color.BgHiYellow, color.Bold).SprintFunc()
	neonEntropyLabel = color.New(color.FgBlack, color.BgHiMagenta, color.Bold).SprintFunc()
	neonValue        = color.New(color.FgHiWhite, color.Bold).SprintFunc()
	neonMatch        = color.New(color.FgHiGreen, color.Bold).SprintFunc()
	neonStar         = color.New(color.FgHiYellow, color.Bold).SprintFunc()
	neonDim          = color.New(color.Faint, color.Italic).SprintFunc()
	neonLink         = color.New(color.FgBlack, color.BgHiCyan, color.Bold).SprintFunc()
)

// ──────────────────────────────────────────────────────────────
//  HELPERS
// ──────────────────────────────────────────────────────────────

// Pre-compiled regexes for performance (compiled once at startup)
var (
	repoExtractRegex = regexp.MustCompile(`https://github\.com/([^/]+)/([^/]+?)(?:\.git)?(?:/|$)`)
)

// terminalVTSupported records whether the console can render ANSI/OSC 8 output.
// It is set once at scanner startup from enableVT (console_vt_*.go); it defaults
// to true so redirected streams and non-Windows platforms, where VT is either
// irrelevant or always available, keep working.
var terminalVTSupported = true

// linksEnabled reports whether the terminal can be expected to render OSC 8
// hyperlinks. When output is redirected - a pipe, a CI transcript, a log file -
// the escape sequence would be written into the text verbatim, so links degrade to
// their visible label instead. The same applies to a Windows console whose
// virtual-terminal mode could not be switched on.
func linksEnabled() bool {
	if !terminalVTSupported {
		return false
	}
	if os.Getenv("NO_COLOR") != "" || os.Getenv("SHHGIT_NO_LINKS") != "" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// formatHyperlink wraps text in an OSC 8 hyperlink sequence. It is kept separate
// from createClickableLink so the escape sequence itself can be tested without a
// terminal: createClickableLink is the gate that decides whether to use it.
// OSC 8 ; params ; URL ST text OSC 8 ; ; ST, with OSC = \033] and ST = \033\\.
func formatHyperlink(target, text string) string {
	return fmt.Sprintf("\033]8;;%s\033\\%s\033]8;;\033\\", target, text)
}

// createClickableLink creates a terminal hyperlink with ANSI escape sequences.
// Thread-safe as it only works with local variables.
func createClickableLink(target, text string) string {
	if !linksEnabled() {
		return text
	}
	return formatHyperlink(target, text)
}

// oneLine collapses a match onto a single line so a multi-line regex match (or a
// filename with a stray newline) cannot break the one-finding-per-line layout,
// which CI parsers rely on.
func oneLine(s string) string {
	replacer := strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")
	return replacer.Replace(s)
}

// previewMatch renders a matched value for the terminal. Very long values are
// truncated rather than redacted: the operator needs to see what actually
// matched, but a multi-line PEM body or a huge base64 blob must not flood the
// log, the CI transcript or a screenshot. The head of the value is kept so the
// finding stays identifiable.
func previewMatch(match string) string {
	const maxPreview = 120
	s := oneLine(match)
	if len(s) <= maxPreview {
		return s
	}
	// Cut on a rune boundary: slicing at an arbitrary byte split a multi-byte
	// character, so the terminal rendered a replacement glyph at the cut.
	cut := maxPreview
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return fmt.Sprintf("%s... [truncated, %d chars]", s[:cut], len(s))
}

// fileRef renders a finding's location as "path:line", or just the path when the
// line is unknown (a filename/path rule matches the file itself).
func fileRef(filePath string, line int) string {
	if line > 0 {
		return fmt.Sprintf("%s:%d", oneLine(filePath), line)
	}
	return oneLine(filePath)
}

// starSuffix renders the repository's star count in the same form for every
// preset, or an empty string when it is unknown (local scans, gists).
func starSuffix(stars int) string {
	if stars > 0 {
		return fmt.Sprintf("  ★ %d", stars)
	}
	return ""
}

// displayWidth returns the number of terminal cells a string occupies, counting
// East Asian wide characters and emoji as two columns. ANSI escapes must already
// have been stripped by the caller. It exists so the cosmic preset's box stays
// square: padding computed from rune count drifts by one column per emoji.
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		switch {
		case r == 0x200d, r >= 0xfe00 && r <= 0xfe0f: // ZWJ, variation selectors
			// zero width
		case r >= 0x1100 && (r <= 0x115f || // Hangul Jamo
			r == 0x2329 || r == 0x232a ||
			(r >= 0x2e80 && r <= 0xa4cf && r != 0x303f) || // CJK
			(r >= 0xac00 && r <= 0xd7a3) || // Hangul syllables
			(r >= 0xf900 && r <= 0xfaff) ||
			(r >= 0xfe30 && r <= 0xfe6f) ||
			(r >= 0xff00 && r <= 0xff60) ||
			(r >= 0xffe0 && r <= 0xffe6) ||
			(r >= 0x2600 && r <= 0x27bf) || // misc symbols, dingbats
			(r >= 0x1f000 && r <= 0x1faff)): // emoji
			w += 2
		default:
			w++
		}
	}
	return w
}

// extractRepoInfo extracts username, repo, and creates GitHub URLs.
// Thread-safe as it only works with local variables.
func extractRepoInfo(url string) (username, repo, repoUrl string) {
	matches := repoExtractRegex.FindStringSubmatch(url)
	if len(matches) >= 3 {
		username = matches[1]
		repo = matches[2]
		repoUrl = fmt.Sprintf("https://github.com/%s/%s", username, repo)
	}
	return
}

// isAITokenSignature checks if a signature is an AI token (excluding Google)
func isAITokenSignature(sig string) bool {
	sigLower := strings.ToLower(sig)

	// AI providers to include
	aiProviders := []string{
		"openai", "anthropic", "claude", "hugging face", "xai", "grok",
		"cohere", "together", "replicate", "mistral", "groq", "fireworks",
		"anyscale", "ollama", "stability", "elevenlabs", "assemblyai", "deepgram",
		"deepseek", "sambanova", "openrouter", "moonshot", "kimi", "siliconflow",
		"qwen", "tongyi", "alibaba", "baichuan", "chatglm", "yi", "minimax",
		"stepfun", "01ai", "zhipu",
		"perplexity", "nlpcloud", "forefront", "jina", "cerebras", "vllm", "localai",
		"lm studio", "oobabooga", "gpt4all", "privateai", "aleph", "baseten", "modal",
		"runwayml", "midjourney", "dalle", "stable diffusion", "lambda", "paperspace",
		"vast", "runpod", "you.com", "bing", "serper", "tavily", "exa", "brave",
		"wolfram", "mathpix", "ocr", "clarifai", "imagga",
		"ai21", "pinecone", "writer", "jasper",
	}

	// Exclude Google
	if strings.Contains(sigLower, "google") || strings.Contains(sigLower, "gemini") ||
		strings.Contains(sigLower, "palm") || strings.Contains(sigLower, "bard") ||
		strings.Contains(sigLower, "vertex") || strings.Contains(sigLower, "makersuite") {
		return false
	}

	// Check if signature contains any AI provider. The match is word-bounded:
	// short provider names ("exa", "yi", "ocr", "xai") otherwise matched
	// unrelated signature names ("example", "paying", ...) and sent non-AI
	// secrets through the token validator.
	for _, provider := range aiProviders {
		if containsWord(sigLower, provider) {
			return true
		}
	}

	return false
}

// githubFileURL builds the GitHub "blob" URL for a file at a specific line. The
// branch is normalised - a "refs/heads/" prefix is stripped and an empty branch
// defaults to main - and the path is normalised to forward slashes, so the URL
// always points at the file (and the exact line) rather than at the repo root.
func githubFileURL(repoUrl, filePath, branch string, line int) string {
	if branch == "" {
		branch = "main" // Default to main branch
	}
	// Handle different branch reference formats
	branch = strings.TrimPrefix(branch, "refs/heads/")

	// Clean up the file path - remove leading slashes and convert backslashes to forward slashes
	filePath = strings.TrimPrefix(filePath, "/")
	filePath = strings.TrimPrefix(filePath, "\\")
	filePath = strings.ReplaceAll(filePath, "\\", "/")

	fileUrl := fmt.Sprintf("%s/blob/%s/%s", repoUrl, branch, filePath)
	if line > 0 {
		// GitHub's own anchor for "the line this finding is on".
		fileUrl = fmt.Sprintf("%s#L%d", fileUrl, line)
	}
	return fileUrl
}

// createFileLink creates a clickable link to a specific file in the repository,
// pointing at the line of the finding when one is known.
// Thread-safe as it only works with local variables.
func createFileLink(repoUrl, filePath, branch string, line int) string {
	return createClickableLink(githubFileURL(repoUrl, filePath, branch, line), fileLinkLabel(filePath, line))
}

// fileLinkLabel is the visible text of a finding's link: the file's base name plus
// the line number. Keeping it short matters in the terminal, and "path:line" is what
// a terminal without hyperlink support still lets you copy or ctrl-click.
func fileLinkLabel(filePath string, line int) string {
	name := filepath.Base(filePath)
	if line > 0 {
		return fmt.Sprintf("📄 %s:%d", name, line)
	}
	return fmt.Sprintf("📄 %s", name)
}

// localFileURL builds a file:// URL for a finding in a local scan so the terminal can
// hand it to the desktop's file handler. The #L fragment is understood by some
// viewers and ignored by others, which is why the visible label always carries
// "path:line" as well.
func localFileURL(root, filePath string, line int) string {
	abs := filePath
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, filePath)
	}
	if a, err := filepath.Abs(abs); err == nil {
		abs = a
	}
	// url.URL with a "file" scheme and no host renders as file://<path>. The path
	// must start with exactly one slash: on Unix it already does, while a Windows
	// drive path ("C:/Users/...") needs one prepended. Prepending unconditionally
	// produced "file:////home/..." on Unix.
	slashPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	u := url.URL{Scheme: "file", Path: slashPath}
	if line > 0 {
		u.Fragment = fmt.Sprintf("L%d", line)
	}
	return u.String()
}

// gistFileURL builds the canonical gist web URL for a finding, or ok=false when
// rawURL is not a gist. A gist is addressed by its id plus a lowercased,
// dot-to-dash anchor for the file ("#file-hello_world-py"), not by the GitHub
// repository "blob" path.
func gistFileURL(rawURL, filePath string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !strings.EqualFold(u.Hostname(), "gist.github.com") {
		return "", false
	}
	id := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	if id == "" || strings.Contains(id, "/") {
		return "", false
	}
	base := "https://gist.github.com/" + id
	if filePath == "" {
		return base, true
	}
	return base + "#file-" + gistAnchor(filePath), true
}

// gistAnchor renders a file name as GitHub's gist anchor slug: lowercased, with
// every character that is not a letter, digit or underscore replaced by a dash.
func gistAnchor(name string) string {
	name = strings.ToLower(filepath.Base(oneLine(name)))
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// getEnhancedLinkInfo returns enhanced repo info with user/repo display and file links.
// Thread-safe as it only works with local variables and calls thread-safe functions.
func getEnhancedLinkInfo(url, filePath, branch string, line int) (userRepo string, repoLink string, fileLink string) {
	// A gist has no /blob/<branch>/<path> form: its file is addressed by a
	// lowercased dotted anchor ("#file-<name>"). GitHub's code search returns
	// gist hits as host-prefixed slugs, so handle that shape before the
	// owner/repo blob builder, which would otherwise emit a bogus
	// "https://github.com/gist.github.com/<id>/blob/..." link.
	if link, ok := gistFileURL(url, filePath); ok {
		label := fileLinkLabel(filePath, line)
		clickable := createClickableLink(link, label)
		return "gist", clickable, clickable
	}

	username, repo, repoUrl := extractRepoInfo(url)
	if username == "" || repo == "" {
		// Not a GitHub URL - this is a local scan. The link used to be the scanned
		// directory itself, which is useless when a scan covers thousands of files:
		// point it at the file that actually holds the secret instead.
		if filePath != "" {
			link := createClickableLink(localFileURL(url, filePath, line), fileLinkLabel(filePath, line))
			return url, url, link
		}
		return url, url, url
	}

	// Create prominent user/repo display
	userRepo = fmt.Sprintf("%s/%s", color.HiCyanString(username), color.HiMagentaString(repo))

	// Create clickable repo link
	repoLink = createClickableLink(repoUrl, userRepo)

	// Create file-specific link if filePath provided
	if filePath != "" {
		fileLink = createFileLink(repoUrl, filePath, branch, line)
	} else {
		fileLink = repoLink
	}

	return userRepo, repoLink, fileLink
}

// cosmicBox renders a centered 3-line bordered box (Even More Fancy / Cosmic preset).
// Padding is measured in terminal cells, not runes: the titles contain emoji, and a
// rune-count pad drifts by one column per wide glyph, leaving the box ragged.
// blogCosmicBox appends a centered 3-line bordered box (Even More Fancy /
// Cosmic preset) to a batch builder. Padding is measured in terminal cells,
// not runes: the titles contain emoji, and a rune-count pad drifts by one
// column per wide glyph, leaving the box ragged.
func blogCosmicBox(sb *strings.Builder, borderColor func(...interface{}) string, titleColor func(...interface{}) string, title string) {
	const width = 62
	bar := strings.Repeat("═", width)

	tw := displayWidth(title)
	if tw > width {
		// Truncate on rune boundaries so a long title cannot burst the frame.
		runes := []rune(title)
		for len(runes) > 0 && displayWidth(string(runes)) > width {
			runes = runes[:len(runes)-1]
		}
		title = string(runes)
		tw = displayWidth(title)
	}
	pad := (width - tw) / 2
	padded := strings.Repeat(" ", pad) + title + strings.Repeat(" ", width-pad-tw)
	blogPrintln(sb, borderColor("╔"+bar+"╗"))
	blogPrintln(sb, borderColor("║")+titleColor(padded)+borderColor("║"))
	blogPrintln(sb, borderColor("╚"+bar+"╝"))
}

func initLogFormat() {
	f := strings.ToLower(strings.TrimSpace(getSession().Config.LogFormat))
	setLogFormat(f)

	// Announce the active preset with its own style, as one atomic write so
	// background worker lines cannot slip between its lines.
	var sb strings.Builder
	switch getLogFormat() {
	case LogFormatMinimal:
		blogPrintf(&sb, "%s log preset -> %s\n\n", minInfo("◆"), minSearch(strings.ToUpper(getLogFormat())))
	case LogFormatFancy:
		blogPrintf(&sb, "%s Log styling -> %s\n\n", fancyAccent("◆"), fancySearch(strings.ToUpper(getLogFormat())))
	case LogFormatUltraFancy:
		blogPrintf(&sb, "%s %s\n\n", ultraBadge(" PRESET "), ultraAccent(strings.ToUpper(getLogFormat())))
	case LogFormatEvenMoreFancy:
		blogCosmicBox(&sb, cosmicBorder, cosmicTitle, "🎨  LOG PRESET: "+strings.ToUpper(getLogFormat())+"  🎨")
		blogPrintln(&sb)
	case LogFormatNeon:
		blogPrintf(&sb, "%s %s\n\n",
			neonSearchLabel(" PRESET "),
			neonValue(strings.ToUpper(getLogFormat())))
	}
	lockWrite(sb.String())
}

// ──────────────────────────────────────────────────────────────
//  LOG FUNCTIONS  (5 presets each)
// ──────────────────────────────────────────────────────────────

// searchSignatureName is the pseudo-signature reported for --search-query hits.
// It matches the value published to the dashboard and written to the CSV.
const searchSignatureName = "Search Query"

// logSearch logs a search-query match event, as one atomic write so
// background worker lines cannot split the card mid-scan.
func logSearch(count int, url, file, matches, branch string, line, stars int) {
	plural := core.Pluralize(count, "match", "matches")
	starStr := starSuffix(stars)
	userRepo, _, fileLink := getEnhancedLinkInfo(url, file, branch, line)
	fileAt := fileRef(file, line)
	matchText := previewMatch(matches)

	var sb strings.Builder
	switch getLogFormat() {

	// ── 1. MINIMAL ────────────────────────────────────────────
	// Exactly one line per finding: greppable, and safe to parse in CI.
	case LogFormatMinimal:
		blogPrintf(&sb, "%s %s  %s  %s  match=%s%s  %s\n",
			minSearch("🔍"),
			userRepo,
			minInfo(fileAt),
			minEntropy(searchSignatureName),
			minDim(matchText),
			minDim(starStr),
			minLink(fileLink))

	// ── 2. FANCY ──────────────────────────────────────────────
	case LogFormatFancy:
		blogPrintln(&sb, color.HiBlackString(strings.Repeat("━", 80)))
		blogPrintf(&sb, "%s  %s  %s  %s%s  %s\n",
			fancySearch("🔍 [SEARCH]"),
			fancyAccent(fmt.Sprintf("%d %s", count, plural)),
			fancyAccent(searchSignatureName),
			fancyFile(fileAt),
			color.HiBlueString(starStr),
			userRepo)
		blogPrintf(&sb, "  %s %s\n", fancyArrow("match:"), matchText)
		blogPrintf(&sb, "  %s %s\n", fancyLink("link: "), fileLink)

	// ── 3. ULTRA FANCY ────────────────────────────────────────
	case LogFormatUltraFancy:
		blogPrintln(&sb, color.HiBlackString(strings.Repeat("━", 80)))
		blogPrintf(&sb, "%s  %s  %s  %s%s  %s\n",
			ultraBadge(" 🔍 SEARCH "),
			ultraAccent(fmt.Sprintf("%d %s", count, plural)),
			ultraSearch(searchSignatureName),
			ultraFile(fileAt),
			ultraAccent(starStr),
			userRepo)
		blogPrintf(&sb, "  %s %s\n", ultraSecret("match:"), matchText)
		blogPrintf(&sb, "  %s %s\n", ultraLink("link: "), fileLink)

	// ── 4. EVEN MORE FANCY (Cosmic) ───────────────────────────
	case LogFormatEvenMoreFancy:
		blogPrintln(&sb, cosmicBorder(strings.Repeat("═", 80)))
		blogCosmicBox(&sb, cosmicSearch, cosmicTitle, "🌌  🔍 SEARCH MATCH  🌌")
		blogPrintf(&sb, "  %s %d %s%s  %s\n",
			cosmicSearch("◈"),
			count, plural,
			starStr,
			cosmicAccent(searchSignatureName))
		blogPrintf(&sb, "  %s file: %s  %s\n", cosmicSearch("◈"), cosmicFile(fileAt), userRepo)
		blogPrintf(&sb, "  %s match: %s\n", cosmicAccent("➜"), matchText)
		blogPrintf(&sb, "  %s link:  %s\n", cosmicAccent("➜"), fileLink)
		blogPrintln(&sb, cosmicBorder(strings.Repeat("═", 80)))

	// ── 5. NEON ───────────────────────────────────────────────
	default:
		blogPrintln(&sb, neonSearchLabel(strings.Repeat("─", 80)))
		blogPrintf(&sb, "%s  %s  %s%s  %s\n",
			neonSearchLabel(" 🔍 SEARCH "),
			neonValue(searchSignatureName),
			neonDim(fileAt),
			neonStar(starStr),
			userRepo)
		blogPrintf(&sb, "  %s  %s %s\n",
			neonDim(fmt.Sprintf("%d %s", count, plural)),
			neonMatch("match:"),
			matchText)
		blogPrintf(&sb, "  %s %s\n", neonLink("link: "), fileLink)
	}
	lockWrite(sb.String())
}

// logSecret logs a signature/secret match event with visual separation, as
// one atomic write so background worker lines cannot split it mid-scan.
func logSecret(count int, url, sig, file, matches, branch string, line, stars int) {
	plural := core.Pluralize(count, "match", "matches")
	starStr := starSuffix(stars)
	userRepo, _, fileLink := getEnhancedLinkInfo(url, file, branch, line)
	fileAt := fileRef(file, line)
	sigText := oneLine(sig)
	matchText := previewMatch(matches)

	var sb strings.Builder
	switch getLogFormat() {
	// ── 1. MINIMAL ────────────────────────────────────────────
	// Exactly one line per finding: greppable, and safe to parse in CI.
	case LogFormatMinimal:
		blogPrintf(&sb, "%s %s  %s  %s  match=%s%s  %s\n",
			minSecret("🚨"),
			userRepo,
			minInfo(fileAt),
			minEntropy(sigText),
			minDim(matchText),
			minDim(starStr),
			minLink(fileLink))

	// ── 2. FANCY ──────────────────────────────────────────────
	case LogFormatFancy:
		blogPrintln(&sb, color.HiBlackString(strings.Repeat("━", 80)))
		blogPrintf(&sb, "%s  %s  %s  %s%s  %s\n",
			fancySecret("🚨 [SECRET]"),
			fancyAccent(fmt.Sprintf("%d %s", count, plural)),
			fancyAccent(sigText),
			fancyFile(fileAt),
			color.HiBlueString(starStr),
			userRepo)
		blogPrintf(&sb, "  %s %s\n", fancyArrow("match:"), matchText)
		blogPrintf(&sb, "  %s %s\n", fancyLink("link: "), fileLink)

	// ── 3. ULTRA FANCY ────────────────────────────────────────
	case LogFormatUltraFancy:
		blogPrintln(&sb, color.HiBlackString(strings.Repeat("━", 80)))
		blogPrintf(&sb, "%s  %s  %s  %s%s  %s\n",
			ultraDanger(" 🚨 SECRET "),
			ultraAccent(fmt.Sprintf("%d %s", count, plural)),
			ultraSecret(sigText),
			ultraFile(fileAt),
			ultraAccent(starStr),
			userRepo)
		blogPrintf(&sb, "  %s %s\n", ultraSecret("match:"), matchText)
		blogPrintf(&sb, "  %s %s\n", ultraLink("link: "), fileLink)

	// ── 4. EVEN MORE FANCY (Cosmic) ───────────────────────────
	case LogFormatEvenMoreFancy:
		blogPrintln(&sb, cosmicBorder(strings.Repeat("═", 80)))
		blogCosmicBox(&sb, cosmicSecret, cosmicTitle, "🔥  🚨 LEGENDARY SECRET BREACH  🔥")
		blogPrintf(&sb, "  %s %d %s%s  %s\n",
			cosmicSecret("║"),
			count, plural,
			starStr,
			cosmicAccent(sigText))
		blogPrintf(&sb, "  %s file: %s  %s\n", cosmicSecret("║"), cosmicFile(fileAt), userRepo)
		blogPrintf(&sb, "  %s match: %s\n", cosmicAccent("➜"), matchText)
		blogPrintf(&sb, "  %s link:  %s\n", cosmicAccent("➜"), fileLink)
		blogPrintln(&sb, cosmicBorder(strings.Repeat("═", 80)))

	// ── 5. NEON ───────────────────────────────────────────────
	default:
		blogPrintln(&sb, neonSecretLabel(strings.Repeat("─", 80)))
		blogPrintf(&sb, "%s  %s  %s%s  %s\n",
			neonSecretLabel(" 🚨 SECRET "),
			neonValue(sigText),
			neonDim(fileAt),
			neonStar(starStr),
			userRepo)
		blogPrintf(&sb, "  %s  %s %s\n",
			neonDim(fmt.Sprintf("%d %s", count, plural)),
			neonMatch("match:"),
			matchText)
		blogPrintf(&sb, "  %s %s\n", neonLink("link: "), fileLink)
	}
	lockWrite(sb.String())
}

// logFile logs a file-pattern match event with visual separation, as one
// atomic write so background worker lines cannot split it mid-scan.
func logFile(url, sig, file, branch string, stars int) {
	// A file/name rule matches the file itself, so there is no line to point at.
	userRepo, _, fileLink := getEnhancedLinkInfo(url, file, branch, 0)
	fileAt := oneLine(file)
	sigText := oneLine(sig)
	starStr := starSuffix(stars)

	// For .env files, show that we're including full content in webhook
	isEnvFile := strings.HasSuffix(strings.ToLower(file), ".env") ||
		strings.Contains(strings.ToLower(file), ".env.") ||
		strings.HasPrefix(strings.ToLower(filepath.Base(file)), "env.")

	envNotice := ""
	if isEnvFile {
		envNotice = " [FULL CONTENT → WEBHOOK]"
	}

	var sb strings.Builder
	switch getLogFormat() {
	// ── 1. MINIMAL ────────────────────────────────────────────
	// Exactly one line per finding: greppable, and safe to parse in CI.
	case LogFormatMinimal:
		blogPrintf(&sb, "%s %s  %s  %s  match=%s%s  %s%s\n",
			minFile("📄"),
			userRepo,
			minInfo(fileAt),
			minEntropy(sigText),
			minDim(fileAt),
			minDim(starStr),
			minLink(fileLink),
			minDim(oneLine(envNotice)))

	// ── 2. FANCY ──────────────────────────────────────────────
	case LogFormatFancy:
		blogPrintln(&sb, color.HiBlackString(strings.Repeat("━", 80)))
		blogPrintf(&sb, "%s  %s  %s%s  %s%s\n",
			fancyFile("📄 [FILE]"),
			fancyAccent(sigText),
			fancyFile(fileAt),
			color.HiBlueString(starStr),
			userRepo,
			color.HiGreenString(envNotice))
		blogPrintf(&sb, "  %s %s\n", fancyArrow("match:"), fileAt)
		blogPrintf(&sb, "  %s %s\n", fancyLink("link: "), fileLink)

	// ── 3. ULTRA FANCY ────────────────────────────────────────
	case LogFormatUltraFancy:
		blogPrintln(&sb, color.HiBlackString(strings.Repeat("━", 80)))
		blogPrintf(&sb, "%s  %s  %s%s  %s%s\n",
			ultraBadge(" 📄 FILE "),
			ultraAccent(sigText),
			ultraFile(fileAt),
			ultraAccent(starStr),
			userRepo,
			ultraSearch(envNotice))
		blogPrintf(&sb, "  %s %s\n", ultraSecret("match:"), fileAt)
		blogPrintf(&sb, "  %s %s\n", ultraLink("link: "), fileLink)

	// ── 4. EVEN MORE FANCY (Cosmic) ───────────────────────────
	case LogFormatEvenMoreFancy:
		blogPrintln(&sb, cosmicBorder(strings.Repeat("═", 80)))
		blogCosmicBox(&sb, cosmicFile, cosmicTitle, "📁  📄 EPIC FILE PATTERN MATCH  📁")
		blogPrintf(&sb, "  %s %s%s  %s\n",
			cosmicFile("◈"),
			cosmicAccent(sigText),
			starStr,
			userRepo)
		blogPrintf(&sb, "  %s file: %s%s\n", cosmicFile("◈"), cosmicFile(fileAt), cosmicSearch(envNotice))
		blogPrintf(&sb, "  %s match: %s\n", cosmicAccent("➜"), fileAt)
		blogPrintf(&sb, "  %s link:  %s\n", cosmicAccent("➜"), fileLink)
		blogPrintln(&sb, cosmicBorder(strings.Repeat("═", 80)))

	// ── 5. NEON ───────────────────────────────────────────────
	default:
		blogPrintln(&sb, neonFileLabel(strings.Repeat("─", 80)))
		blogPrintf(&sb, "%s  %s  %s%s  %s%s\n",
			neonFileLabel(" 📄 FILE "),
			neonValue(sigText),
			neonDim(fileAt),
			neonStar(starStr),
			userRepo,
			neonStar(envNotice))
		blogPrintf(&sb, "  %s %s\n", neonMatch("match:"), fileAt)
		blogPrintf(&sb, "  %s %s\n", neonLink("link: "), fileLink)
	}
	lockWrite(sb.String())
}

// entropySignatureName is the pseudo-signature reported for high-entropy hits.
// It matches the value published to the dashboard and written to the CSV.
const entropySignatureName = "High entropy string"

// logEntropy logs a high-entropy string detection event with visual
// separation, as one atomic write so background worker lines cannot split it.
func logEntropy(url, file, line, branch string, lineNo, stars int) {
	userRepo, _, fileLink := getEnhancedLinkInfo(url, file, branch, lineNo)
	fileAt := fileRef(file, lineNo)
	matchText := previewMatch(line)
	starStr := starSuffix(stars)

	var sb strings.Builder
	switch getLogFormat() {
	// ── 1. MINIMAL ────────────────────────────────────────────
	// Exactly one line per finding: greppable, and safe to parse in CI.
	case LogFormatMinimal:
		blogPrintf(&sb, "%s %s  %s  %s  match=%s%s  %s\n",
			minEntropy("⚡"),
			userRepo,
			minInfo(fileAt),
			minEntropy(entropySignatureName),
			minDim(matchText),
			minDim(starStr),
			minLink(fileLink))

	// ── 2. FANCY ──────────────────────────────────────────────
	case LogFormatFancy:
		blogPrintln(&sb, color.HiBlackString(strings.Repeat("━", 80)))
		blogPrintf(&sb, "%s  %s  %s%s  %s\n",
			fancyEntropy("⚡ [ENTROPY]"),
			fancyAccent(entropySignatureName),
			fancyFile(fileAt),
			color.HiBlueString(starStr),
			userRepo)
		blogPrintf(&sb, "  %s %s\n", fancyArrow("match:"), matchText)
		blogPrintf(&sb, "  %s %s\n", fancyLink("link: "), fileLink)

	// ── 3. ULTRA FANCY ────────────────────────────────────────
	case LogFormatUltraFancy:
		blogPrintln(&sb, color.HiBlackString(strings.Repeat("━", 80)))
		blogPrintf(&sb, "%s  %s  %s%s  %s\n",
			ultraBadge(" ⚡ ENTROPY "),
			ultraEntropy(entropySignatureName),
			ultraFile(fileAt),
			ultraAccent(starStr),
			userRepo)
		blogPrintf(&sb, "  %s %s\n", ultraSecret("match:"), matchText)
		blogPrintf(&sb, "  %s %s\n", ultraLink("link: "), fileLink)

	// ── 4. EVEN MORE FANCY (Cosmic) ───────────────────────────
	case LogFormatEvenMoreFancy:
		blogPrintln(&sb, cosmicBorder(strings.Repeat("═", 80)))
		blogCosmicBox(&sb, cosmicEntropy, cosmicTitle, "⚡  ⚙️  COSMIC HIGH ENTROPY DETECTED  ⚙️  ⚡")
		blogPrintf(&sb, "  %s %s%s  %s\n",
			cosmicEntropy("◈"),
			cosmicAccent(entropySignatureName),
			starStr,
			userRepo)
		blogPrintf(&sb, "  %s file: %s\n", cosmicEntropy("◈"), cosmicFile(fileAt))
		blogPrintf(&sb, "  %s match: %s\n", cosmicAccent("➜"), matchText)
		blogPrintf(&sb, "  %s link:  %s\n", cosmicAccent("➜"), fileLink)
		blogPrintln(&sb, cosmicBorder(strings.Repeat("═", 80)))

	// ── 5. NEON ───────────────────────────────────────────────
	default:
		blogPrintln(&sb, neonEntropyLabel(strings.Repeat("─", 80)))
		blogPrintf(&sb, "%s  %s  %s%s  %s\n",
			neonEntropyLabel(" ⚡ ENTROPY "),
			neonValue(entropySignatureName),
			neonDim(fileAt),
			neonStar(starStr),
			userRepo)
		blogPrintf(&sb, "  %s %s\n", neonMatch("match:"), matchText)
		blogPrintf(&sb, "  %s %s\n", neonLink("link: "), fileLink)
	}
	lockWrite(sb.String())
}

// ──────────────────────────────────────────────────────────────
//  MATCH EVENT
// ──────────────────────────────────────────────────────────────

type MatchEvent struct {
	Url         string
	Matches     []string
	Signature   string
	File        string
	Stars       int
	Source      core.GitResourceType
	Priority    int
	Color       string
	FileContent string
	Secret      string
	SecretLine  int
	// Severity is the finding's dynamic severity, computed by publish() from
	// the verifier outcome (or the rule priority when none is known) and used
	// for the webhook embed colour.
	Severity string
}

// secretLineNum returns the 1-based line number of the first occurrence of
// secret in contents, or 0 when the secret is not present.
func secretLineNum(contents []byte, secret string) int {
	if secret == "" || len(contents) == 0 {
		return 0
	}
	idx := bytes.Index(contents, []byte(secret))
	if idx < 0 {
		return 0
	}
	return bytes.Count(contents[:idx], []byte{'\n'}) + 1
}

// ──────────────────────────────────────────────────────────────
//  CORE PROCESSING
// ──────────────────────────────────────────────────────────────

func ProcessRepositories() {
	threadNum := *getSession().Options.Threads
	// Cap from config (default 8) to keep a scan from saturating the machine. A
	// configured cap below 1 would start no workers and stall the scan, so the
	// worker count is floored at one.
	maxThreads := getSession().Config.Performance.Int(getSession().Config.Performance.MaxRepositoryThreads, 8)
	if threadNum > maxThreads {
		threadNum = maxThreads
	}
	if threadNum < 1 {
		threadNum = 1
	}

	// Create worker pool with buffered channels
	repoQueue := make(chan core.GitResource, threadNum*2)

	// Start workers with unique IDs for progress tracking
	for i := 0; i < threadNum; i++ {
		workerID := i
		go func() {
			for repository := range repoQueue {
				processRepository(repository, workerID)
			}
		}()
	}

	// Distribute work
	go func() {
		for repository := range getSession().Repositories {
			repoQueue <- repository
		}
	}()
}

func processRepository(repository core.GitResource, workerID int) {
	repo, err := core.GetRepository(getSession(), repository.Id)
	if err != nil {
		getSession().Log.Warn("Failed to retrieve repository %d: %s", repository.Id, err)
		return
	}

	// scanning.skip_forks / skip_archived. Both default to false, so the
	// compiled-in behaviour is unchanged unless the operator opts in.
	scanCfg := getSession().Config.Scanning
	if scanCfg.Bool(scanCfg.SkipForks, false) && repo.GetFork() {
		getSession().Log.Debug("[%s] Skipping fork (scanning.skip_forks)", repository.Url)
		return
	}
	if scanCfg.Bool(scanCfg.SkipArchived, false) && repo.GetArchived() {
		getSession().Log.Debug("[%s] Skipping archived repository (scanning.skip_archived)", repository.Url)
		return
	}

	if repo.GetPermissions()["pull"] &&
		uint(repo.GetStargazersCount()) >= *getSession().Options.MinimumStars &&
		uint(repo.GetSize()) < *getSession().Options.MaximumRepositorySize {
		processRepositoryOrGist(repo.GetCloneURL(), repository.Ref, repo.GetStargazersCount(), core.GITHUB_SOURCE, workerID)
	}
}

func ProcessGists() {
	threadNum := *getSession().Options.Threads
	// Cap from config (default 5) to avoid hammering the gist API.
	maxThreads := getSession().Config.Performance.Int(getSession().Config.Performance.MaxGistThreads, 5)
	if threadNum > maxThreads {
		threadNum = maxThreads
	}
	if threadNum < 1 {
		threadNum = 1
	}

	// Create worker pool with buffered channels
	gistQueue := make(chan string, threadNum*2)

	// Start workers with unique IDs (offset to not collide with repo workers)
	for i := 0; i < threadNum; i++ {
		workerID := 100 + i
		go func() {
			for gistUrl := range gistQueue {
				processRepositoryOrGist(gistUrl, "", -1, core.GIST_SOURCE, workerID)
			}
		}()
	}

	// Distribute work
	go func() {
		for gistUrl := range getSession().Gists {
			gistQueue <- gistUrl
		}
	}()
}

func ProcessComments() {
	threadNum := *getSession().Options.Threads
	// Cap from config (default 3) to avoid hammering the API.
	maxThreads := getSession().Config.Performance.Int(getSession().Config.Performance.MaxCommentThreads, 3)
	if threadNum > maxThreads {
		threadNum = maxThreads
	}
	if threadNum < 1 {
		threadNum = 1
	}

	// Create worker pool with buffered channels
	commentQueue := make(chan core.Comment, threadNum*2)

	// Start workers
	for i := 0; i < threadNum; i++ {
		go func() {
			for comment := range commentQueue {
				processComment(comment)
			}
		}()
	}

	// Distribute work
	go func() {
		for comment := range getSession().Comments {
			commentQueue <- comment
		}
	}()
}

func processComment(comment core.Comment) {
	// Key the temp dir on the comment's URL, not its body: two comments with
	// identical bodies (the same leaked token pasted twice) would otherwise
	// share one directory, scan it concurrently and publish duplicate findings,
	// and one worker's RemoveAll could delete the directory mid-scan of the
	// other. Comment URLs are unique.
	dir := core.GetTempDir(core.GetHash(comment.Url))
	// Checked: when this write failed, the scan below ran against a directory with
	// no comment in it and the finding was silently lost.
	if err := os.WriteFile(filepath.Join(dir, "comment.ignore"), []byte(comment.Body), 0644); err != nil {
		getSession().Log.Warn("Could not stage comment %s for scanning: %s", comment.Url, err)
		return
	}
	if !checkSignatures(dir, comment.Url, "", 0, core.GITHUB_COMMENT) {
		os.RemoveAll(dir)
	}

	// Mark directory as processed and trigger cleanup
	getSession().CleanupManager.MarkProcessed(dir)
	if err := getSession().CleanupManager.CleanupIfNeeded(); err != nil {
		getSession().Log.Debug("Cleanup error: %s", err.Error())
	}
}

func processRepositoryOrGist(url string, ref string, stars int, source core.GitResourceType, workerID int) {
	// The operator may have skipped this repository from the dashboard while it
	// waited in the queue. Check before doing any work at all.
	if activity.IsSkipped(url) {
		return
	}

	dir := core.GetTempDir(core.GetHash(url))

	// Register the checkout as live: temp dirs are deterministic per URL, so
	// without this a stale "processed" entry lets the disk cleanup RemoveAll
	// the directory while this scan is standing in it.
	getSession().CleanupManager.BeginDir(dir)
	defer getSession().CleanupManager.EndDir(dir)

	activity.StartFetch(url)

	_, err := core.CloneRepository(getSession(), url, ref, dir, nil)
	if err != nil {
		getSession().Log.Debug("[%s] Cloning failed: %s", url, err.Error())
		core.GlobalScanProgress.RecordError()
		os.RemoveAll(dir)
		activity.FailRepo(url)
		return
	}

	// Skipped while the clone ran: the clone cannot be interrupted, so this is the
	// first checkpoint after it. Drop what was fetched and stop before scanning.
	if activity.IsSkipped(url) {
		os.RemoveAll(dir)
		activity.FailRepo(url)
		return
	}

	activity.StartScan(url)
	core.GlobalScanProgress.BeginScan(url)
	getSession().Log.Debug("[%s] Cloning %s into %s",
		url, ref,
		strings.Replace(dir, *getSession().Options.TempDirectory, "", -1))
	core.ApplySandboxToRepo(dir)
	checkSignatures(dir, url, ref, stars, source)
	core.GlobalScanProgress.EndScan()
	activity.FinishScan(url)

	// Mark directory as processed for cleanup tracking
	getSession().CleanupManager.MarkProcessed(dir)

	// Check if cleanup is needed
	if err := getSession().CleanupManager.CleanupIfNeeded(); err != nil {
		getSession().Log.Debug("Cleanup error: %s", err.Error())
	}
}

// isViteOnlyEnvFile reports whether an environment file contains nothing but
// public Vite (VITE_*) variables, comments, or blank lines. Vite exposes
// VITE_* vars to the client bundle by design, so a file that ONLY carries
// those is not a finding. Files mixing VITE_ with real keys are still scanned.
func isViteOnlyEnvFile(file core.MatchFile) bool {
	base := strings.ToLower(file.Filename)
	if !strings.HasPrefix(base, ".env") && !strings.HasPrefix(base, "env.") {
		return false
	}
	hasAssignment := false
	for _, line := range strings.Split(string(file.Contents), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, ";") {
			continue
		}
		hasAssignment = true
		lower := strings.ToLower(t)
		if !(strings.HasPrefix(lower, "vite_") && (strings.Contains(t, "=") || strings.Contains(t, ":"))) {
			return false
		}
	}
	return hasAssignment
}

// isTestFixturePath reports whether a repo-relative path points at test
// fixtures, which overwhelmingly contain fake placeholder credentials.
func isTestFixturePath(rel string) bool {
	lower := strings.ToLower(rel)
	if strings.Contains(lower, "/tests/") || strings.Contains(lower, "/__tests__/") ||
		strings.Contains(lower, "/test/") || strings.Contains(lower, "/spec/") ||
		strings.Contains(lower, "/testdata/") || strings.Contains(lower, "/fixtures/") {
		return true
	}
	base := lower
	if idx := strings.LastIndex(lower, "/"); idx >= 0 {
		base = lower[idx+1:]
	}
	return strings.Contains(base, "_test.") || strings.Contains(base, ".spec.") ||
		strings.HasPrefix(base, "test_") || strings.HasPrefix(base, "tests_")
}

// isPasswordFamilySignature reports whether a signature detects passwords —
// the class of match that most often fires on fake test-fixture credentials.
func isPasswordFamilySignature(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "password") || strings.Contains(lower, "passwd") || strings.Contains(lower, "pwd")
}

// isDocSkillPath reports whether a repo-relative path points at AI-agent
// skill/instruction files (.agents/skills, .cursor/rules, .github/skills, ...).
// Those files are documentation: secrets inside them are examples, not findings.
func isDocSkillPath(rel string) bool {
	lower := strings.ToLower(rel)
	for _, seg := range []string{"/.agents/", "/.cursor/", "/.claude/", "/skills/", "/rules/", "/prompts/"} {
		if strings.Contains(lower, seg) {
			return true
		}
	}
	return strings.HasPrefix(lower, ".agents/") || strings.HasPrefix(lower, ".cursor/") || strings.HasPrefix(lower, ".claude/")
}

// hasCompletePemBlock reports whether the content contains a PEM footer
// ("-----END") after its header. A bare "-----BEGIN ...-----" line with no
// closing footer is a doc fragment/example, not a real key.
func hasCompletePemBlock(contents string) bool {
	lower := strings.ToLower(contents)
	begin := strings.Index(lower, "-----begin")
	if begin < 0 {
		return false
	}
	return strings.Index(lower[begin:], "-----end") > 0
}

func checkSignatures(dir string, url string, ref string, stars int, source core.GitResourceType) (matchedAny bool) {
	cfg := getSession().Config

	// Per-repository file cap. scanning.max_file_count wins; the legacy
	// performance.max_file_count key still works; 10000 is the compiled-in
	// default. Repositories above the cap are skipped entirely — this is what
	// keeps a spam repo of thousands of junk files from being walked in full.
	maxFileCount := cfg.MaxFileCount(10000)

	// Optional wall-clock cap on the scan phase (scanning.scan_timeout_seconds).
	// Timed from here so it also bounds the file-count walk below on a huge repo.
	var scanDeadline time.Time
	scanTimeoutSecs := cfg.Scanning.Int(cfg.Scanning.ScanTimeoutSecs, 0)
	if scanTimeoutSecs > 0 {
		// Cap so the seconds->nanoseconds conversion cannot overflow
		// time.Duration into a negative (already-passed) deadline.
		const maxTimeoutSecs = 1 << 31 // ~68 years
		if scanTimeoutSecs > maxTimeoutSecs {
			scanTimeoutSecs = maxTimeoutSecs
		}
		scanDeadline = time.Now().Add(time.Duration(scanTimeoutSecs) * time.Second)
	}

	// Optional per-repository byte budget (scanning.max_scan_mb, 0 = no limit).
	// A wall-clock timeout depends on how loaded the machine is; a byte budget
	// stops a pathological repository in a predictable amount of work. The
	// value is capped before the shift so a huge config value cannot overflow
	// int64 into a negative and silently disable the budget.
	maxScanMB := int64(cfg.Scanning.Int(cfg.Scanning.MaxScanMB, 0))
	var maxScanBytes int64
	if maxScanMB > 0 {
		const maxScanMBValue = int64(1) << 42 // (1<<42)<<20 stays below MaxInt64
		if maxScanMB > maxScanMBValue {
			maxScanMB = maxScanMBValue
		}
		maxScanBytes = maxScanMB << 20
	}
	var bytesScanned int64

	// One walk of the tree yields both the scannable files and the total count
	// the size guard needs. These used to be two full traversals of the same
	// tree (a FastWalk for the count, then a filepath.Walk for the files).
	files, fileCount := core.GetMatchingFilesCounted(dir)

	if fileCount > maxFileCount {
		lockPrintf("[SKIP] Repository has %d files (max: %d): %s\n", fileCount, maxFileCount, url)
		return false
	}

	// CRITICAL PERFORMANCE FIX: Pre-compile search query regex ONCE outside the file loop
	var queryRegex *regexp.Regexp
	if q := strOpt(getSession().Options.SearchQuery); q != "" {
		re, err := regexp.Compile(q)
		if err != nil {
			// MustCompile here panicked on a malformed query - "(" was enough - and
			// took the whole scan down with a stack trace.
			getSession().Log.Error("--search-query is not a valid regular expression: %v", err)
			exitScanner(1)
			return false
		}
		queryRegex = re
	}

	// len(files) is the exact number of files this scan will process, so it is
	// the right denominator for the progress percentage. fileCount above is the
	// size guard and counts every file, including the blacklisted ones.
	core.GlobalScanProgress.SetTotalFiles(len(files))

	for i, file := range files {
		// Checked per file so a runaway scan stops within one file rather than
		// after the whole repository.
		if !scanDeadline.IsZero() && time.Now().After(scanDeadline) {
			lockPrintf("[SKIP] Scan timed out after %ds (%d/%d files): %s\n", scanTimeoutSecs, i, len(files), url)
			return matchedAny
		}
		// The operator may have skipped this repository while the scan was running.
		// Checking per file is what makes a skip land in a reasonable time on a
		// large repository; the caller drops the directory afterwards.
		if activity.IsSkipped(url) {
			getSession().Log.Debug("[%s] Skipped by operator, stopping scan", url)
			return matchedAny
		}
		core.GlobalScanProgress.FileScanned(file.Path)

		// MatchFile.Contents is loaded lazily, and every match engine below reads the
		// field directly. Reading it without loading saw nil for every file, which
		// silently disabled --search-query, the high-entropy scan, the PEM
		// completeness check, the Vite-only-env filter, --scan-keys and the
		// dashboard's file viewer. Loading it here, once per file, has a second
		// effect: Signature.Match takes MatchFile by value, so its own GetContents
		// call only ever cached into the copy it was given - meaning a contents-based
		// signature re-read the file from disk, once per signature, up to 305 times
		// per file. The load has to happen before any of those readers, including
		// isViteOnlyEnvFile.
		file.Contents = file.GetContents()
		if maxScanBytes > 0 {
			bytesScanned += int64(len(file.Contents))
			if bytesScanned > maxScanBytes {
				lockPrintf("[SKIP] Scan byte budget reached (%d MB): %s\n", maxScanBytes>>20, url)
				return matchedAny
			}
		}
		// Env files whose only assignments are public VITE_* variables (Vite
		// client-side envs ship to the browser by design) are not findings.
		if isViteOnlyEnvFile(file) {
			continue
		}

		var (
			matches          []string
			relativeFileName string
			// fileMatched is per-file. The cleanup below used the accumulated
			// matchedAny, so after the first finding anywhere in the repository
			// no further unmatched file was ever removed and the temp tree grew
			// unbounded for the rest of the scan.
			fileMatched bool
		)

		// Extract path relative to the cloned repo directory
		// dir is like: C:\Users\me\AppData\Local\Temp\shhgit\hash\
		// file.Path is like: C:\Users\me\AppData\Local\Temp\shhgit\hash\path\to\file.txt
		// We want: path/to/file.txt
		relativeFileName = file.Path

		// Normalize paths to use forward slashes for consistent comparison
		normalizedDir := strings.ReplaceAll(dir, "\\", "/")
		normalizedFilePath := strings.ReplaceAll(relativeFileName, "\\", "/")

		// Ensure dir has a trailing separator for proper trimming
		if !strings.HasSuffix(normalizedDir, "/") {
			normalizedDir += "/"
		}

		// Strip the directory prefix from the file path
		relativeFileName = strings.TrimPrefix(normalizedFilePath, normalizedDir)
		relativeFileName = strings.TrimPrefix(relativeFileName, "/")
		relativeFileName = strings.TrimPrefix(relativeFileName, "\\")
		relativeFileName = strings.ReplaceAll(relativeFileName, "\\", "/")

		if queryRegex != nil {
			// Use pre-compiled regex from outside the loop (PERFORMANCE FIX)
			for _, match := range queryRegex.FindAllSubmatch(file.Contents, -1) {
				matches = append(matches, string(match[0]))
			}
			if len(matches) > 0 {
				// A search-query hit is a finding like any other: without this the
				// scan printed the match and then reported "No secrets found", and
				// exited 0, so a CI job using --search-query never failed.
				matchedAny = true
				fileMatched = true
				count := len(matches)
				m := strings.Join(matches, ", ")
				// The line of the finding has to be computed from the secret itself,
				// not from the joined summary string, or the link points nowhere.
				lineNo := secretLineNum(file.Contents, matches[0])
				// Search-query matches must feed the web dashboard too, exactly
				// like signature matches — otherwise they show in the terminal
				// but never appear in the Matches tab.
				publish(&MatchEvent{Source: source, Url: url, Matches: matches, Signature: searchSignatureName, File: relativeFileName, Stars: stars, Priority: 0, Color: "", FileContent: string(file.Contents), Secret: matches[0], SecretLine: lineNo})
				logSearch(count, url, relativeFileName, m, ref, lineNo, stars)
				getSession().WriteToCsv([]string{url, searchSignatureName, relativeFileName, m})
				getSession().LogMatch(searchSignatureName, url, relativeFileName, matches)
			}
		} else if scanContents(url, ref, relativeFileName, "", stars, file.Contents, source) {
			matchedAny = true
			fileMatched = true
		}

		if !fileMatched && len(strOpt(getSession().Options.Local)) <= 0 {
			if err := os.Remove(file.Path); err != nil {
				getSession().Log.Debug("cleanup: could not remove scanned temp file %s: %s", file.Path, err)
			}
		}
	}
	return
}

// sourceName converts a GitResourceType to a human-readable string.
func sourceName(s core.GitResourceType) string {
	switch s {
	case core.LOCAL_SOURCE:
		return "local"
	case core.GITHUB_SOURCE:
		return "github"
	case core.GITHUB_COMMENT:
		return "comment"
	case core.GIST_SOURCE:
		return "gist"
	case core.BITBUCKET_SOURCE:
		return "bitbucket"
	case core.GITLAB_SOURCE:
		return "gitlab"
	case core.GITHUB_SEARCH_SOURCE:
		return "github-search"
	case core.PASTEBIN_SOURCE:
		return "pastebin"
	case core.SEARCHCODE_SOURCE:
		return "searchcode"
	case core.SOURCEGRAPH_SOURCE:
		return "sourcegraph"
	case core.GITHUB_COMMITS_SOURCE:
		return "github-commits"
	case core.GITHUB_ISSUES_SOURCE:
		return "github-issues"
	case core.GREPAPP_SOURCE:
		return "grepapp"
	case core.PSBDMP_SOURCE:
		return "psbdmp"
	default:
		return "unknown"
	}
}

// matchIDSeq guarantees uniqueness across concurrent publishers.
var matchIDSeq int64

// newMatchID returns a process-unique match ID. time.Now().UnixNano() alone
// is NOT unique on Windows: the system clock has coarse resolution, so scan
// threads publishing matches concurrently can land on the same tick and
// produce duplicate IDs. The web dashboard keys match cards by ID, so
// duplicate IDs silently collapse distinct matches into one card (matches
// visibly "disappear"). The atomic counter makes every ID unique while the
// UnixNano prefix keeps IDs monotonically increasing for list ordering.
func newMatchID() string {
	return fmt.Sprintf("%d-%d", time.Now().UnixNano(), atomic.AddInt64(&matchIDSeq, 1))
}

// liveHTTPClient bounds the optional --live POST. http.DefaultClient has no
// timeout, so one hung endpoint could otherwise hold a goroutine forever.
// It also rejects cross-origin redirects to prevent SSRF via redirect chains.
var liveHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) == 0 {
			return nil
		}
		origin := via[0].URL
		if req.URL.Scheme != origin.Scheme || req.URL.Host != origin.Host {
			return fmt.Errorf("live: refusing redirect from %s://%s to %s://%s",
				origin.Scheme, origin.Host, req.URL.Scheme, req.URL.Host)
		}
		// SSRF guard on redirect target
		if err := httpclient.SafeURL(req.URL.String()); err != nil {
			return fmt.Errorf("live: refusing redirect to %s: %w", req.URL, err)
		}
		return nil
	},
}

// postLive delivers one match to the configured --live endpoint. It runs on its
// own goroutine and drains a bounded amount of the response so the connection
// can be reused; delivery failures are intentionally silent, exactly as the
// inline send was.
func postLive(url string, body []byte) {
	// SSRF guard: validate the target URL before any request is made.
	if err := httpclient.SafeURL(url); err != nil {
		return
	}
	resp, err := liveHTTPClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
}

// findingFingerprint derives the cross-run dedupe key for an event. It mirrors
// store.Fingerprint's contract: signature + source + match + repo + file. The
// "match" component is the raw secret when one was captured, otherwise the
// joined match list, so the same finding in the same file is stable across runs.
func findingFingerprint(event *MatchEvent) string {
	match := event.Secret
	if match == "" && len(event.Matches) > 0 {
		match = strings.Join(event.Matches, ",")
	}
	return store.Fingerprint(event.Signature, sourceName(event.Source), match, event.Url, event.File)
}

// boolOpt dereferences an optional bool flag, treating nil as false.
func boolOpt(p *bool) bool { return p != nil && *p }

// strOpt dereferences an optional string flag, treating nil as "".
func strOpt(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// uintOpt dereferences an optional uint flag, treating nil as 0.
func uintOpt(p *uint) uint {
	if p == nil {
		return 0
	}
	return *p
}

// floatOpt dereferences an optional float64 flag, treating nil as 0.
func floatOpt(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// boolPtr returns a pointer to b. It is how an optional "known" tri-state is
// represented on the wire: nil means "no verifier outcome", false/true mean a
// verifier rejected/accepted the secret.
func boolPtr(b bool) *bool { return &b }

type cachedOutcome struct {
	provider string
	outcome  verifiers.Outcome
}

// outcomeCache holds the verifier outcomes resolved during this run, keyed by
// the masked token form the OnOutcome callback carries. It lets publish() set a
// finding's severity immediately when the same secret was verified earlier in
// the run: TestAndLogToken caches its own verdict and does not fire OnOutcome a
// second time, so the callback alone would miss those repeats.
//
// The key is the 17-char masked prefix because the callback deliberately never
// carries the full secret. Two distinct secrets sharing a prefix therefore
// share an entry (last write wins); in practice same-prefix secrets share an
// issuer, provider and status, so this cannot mis-severitise a finding. Keying
// by a full-secret hash would require passing full secrets through the
// callback layer, which is worse than the collision it would fix.
//
// It is bounded: a long scan over a repository of many distinct secrets would
// otherwise keep every verdict resident for the life of the process.
const maxOutcomeCache = 10000

var (
	outcomeCacheMu sync.RWMutex
	outcomeCache   = map[string]cachedOutcome{}
	// outcomeCacheOrder records insertion order for the map above. Its length
	// always matches len(outcomeCache): a key is appended only when first
	// stored, and eviction removes it from both.
	outcomeCacheOrder []string
)

// maskedOutcomeKey reproduces the masking core.TokenValidator applies before it
// invokes OnOutcome: the first 17 characters plus an ellipsis for a long token,
// otherwise the token unchanged.
func maskedOutcomeKey(secret string) string {
	if secret == "" {
		return ""
	}
	if len(secret) > 20 {
		return secret[:17] + "..."
	}
	return secret
}

// rememberOutcome records a resolved verifier outcome and its provider against
// the masked token key.
func rememberOutcome(token string, provider string, o verifiers.Outcome) {
	key := maskedOutcomeKey(token)
	if key == "" {
		return
	}
	outcomeCacheMu.Lock()
	if _, exists := outcomeCache[key]; !exists {
		outcomeCacheOrder = append(outcomeCacheOrder, key)
		for len(outcomeCacheOrder) > maxOutcomeCache {
			oldest := outcomeCacheOrder[0]
			outcomeCacheOrder = outcomeCacheOrder[1:]
			delete(outcomeCache, oldest)
		}
	}
	outcomeCache[key] = cachedOutcome{provider: provider, outcome: o}
	outcomeCacheMu.Unlock()
}

// cachedFor returns the outcome and provider recorded for secret, if any.
func cachedFor(secret string) (verifiers.Outcome, string, bool) {
	key := maskedOutcomeKey(secret)
	if key == "" {
		return verifiers.Outcome{}, "", false
	}
	outcomeCacheMu.RLock()
	entry, ok := outcomeCache[key]
	outcomeCacheMu.RUnlock()
	if !ok {
		return verifiers.Outcome{}, "", false
	}
	return entry.outcome, entry.provider, true
}

// outcomeForSecret returns the verifier outcome recorded for secret, or nil when
// it was never verified. publish() passes the result to core.Classify, so nil
// falls back to the signature-priority mapping.
func outcomeForSecret(secret string) *verifiers.Outcome {
	o, _, ok := cachedFor(secret)
	if !ok {
		return nil
	}
	return &o
}

// installOutcomeRecorder installs the shared OnOutcome sink on the session
// validator unless a caller (web/TUI) already installed one. Web/TUI install a
// callback that composes the shared recorder with their UI enrichment, so this
// only ever fills the gap for terminal mode: without it terminal-mode findings
// kept the priority fallback even for verified keys, so the webhook, dashboard
// and findings store disagreed with web/TUI.
func installOutcomeRecorder() {
	if tv := getSession().TokenValidator; tv != nil && tv.OnOutcome == nil {
		tv.OnOutcome = rememberOutcome
	}
}

// ──────────────────────────────────────────────────────────────
//
//	NOTIFICATIONS (Slice 9)
//
// ──────────────────────────────────────────────────────────────
//
// notifyDispatcher is built once at scanner start from notify.channels and
// kept for the life of the process. Delivery is IN ADDITION to the webhook
// queue, never a replacement: a finding reaches both unless a channel's own
// severity floor or the quiet-hours policy suppresses it. Everything is
// best-effort: a delivery failure is logged and marks the notification path
// degraded, but never interrupts the scan.
var (
	notifyDispatcher *notify.Dispatcher

	notifyMu       sync.Mutex
	notifyChannels []string
	notifyDegraded bool
)

// initNotifications builds the process-wide dispatcher from the notify config
// and runs the opt-in startup test delivery. It is called from executeScanner,
// which all three modes (terminal, web, tui) funnel through, so the dispatcher
// exists before any worker can publish a finding.
func initNotifications() {
	cfg := getSession().Config
	d, _, ok := cfg.NotifySetup()
	if !ok {
		notifyMu.Lock()
		notifyDispatcher, notifyChannels, notifyDegraded = nil, nil, false
		notifyMu.Unlock()
		return
	}

	names := make([]string, 0, len(cfg.Notify.Channels))
	for _, ch := range cfg.NotifyChannels() {
		names = append(names, ch.Name())
	}
	notifyMu.Lock()
	notifyDispatcher = d
	notifyChannels = names
	notifyDegraded = false
	notifyMu.Unlock()

	maybeTestNotifications()
}

// notifyDispatcherRef returns the process-wide dispatcher. The read is guarded
// because the GitLab/search source goroutines can publish while scanner start
// is still installing the dispatcher.
func notifyDispatcherRef() *notify.Dispatcher {
	notifyMu.Lock()
	defer notifyMu.Unlock()
	return notifyDispatcher
}

// maybeTestNotifications sends one synthetic critical event to every configured
// channel when notify.test_on_start is explicitly true (default false). It is
// deliberately off by default so an existing operator's channels are never
// pinged just by starting a scan. Every result is routed through the session
// logger, never raw stdout, so web and TUI capture it like any other log line.
func maybeTestNotifications() {
	d := notifyDispatcherRef()
	if d == nil {
		return
	}
	cfg := getSession().Config
	if cfg == nil || !boolOpt(cfg.Notify.TestOnStart) {
		return
	}
	log := getSession().Log
	failed := false
	for _, ch := range cfg.NotifyChannels() {
		if err := d.TestDeliver(ch); err != nil {
			log.Warn("[notify] channel %q test delivery failed: %s", ch.Name(), err)
			failed = true
			continue
		}
		log.Info("[notify] channel %q test delivery ok", ch.Name())
	}
	if failed {
		setNotifyDegraded(true)
	}
}

// notifyStatusSnapshot returns the configured channel names and whether the
// most recent delivery attempt failed. Both are read under notifyMu because a
// delivery worker updates the flag while the dashboard's /health may read it.
func notifyStatusSnapshot() (names []string, degraded bool) {
	notifyMu.Lock()
	defer notifyMu.Unlock()
	names = make([]string, len(notifyChannels))
	copy(names, notifyChannels)
	return names, notifyDegraded
}

// setNotifyDegraded records the notification path's health for /health.
func setNotifyDegraded(v bool) {
	notifyMu.Lock()
	notifyDegraded = v
	notifyMu.Unlock()
}

// recordNotifyResults logs per-channel delivery failures and updates the
// degraded flag. A run in which every channel succeeded clears it. The logger
// is passed in rather than read from the session so a delivery goroutine that
// outlives a test cannot race the package-level session swap.
func recordNotifyResults(results []notify.DeliveryResult, log *core.Logger) {
	failed := false
	for _, r := range results {
		if r.Err == nil {
			continue
		}
		failed = true
		if log != nil {
			log.Debug("[notify] channel %q delivery failed: %s", r.Channel, r.Err)
		}
	}
	setNotifyDegraded(failed)
}

// notifySeverityFromString converts a finding's wire severity name back to the
// notify package's rank. Unknown values fall to low, matching the channel floor
// default.
func notifySeverityFromString(s string) notify.Severity {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical":
		return notify.SevCritical
	case "high":
		return notify.SevHigh
	case "medium":
		return notify.SevMedium
	default:
		return notify.SevLow
	}
}

// notifyEventFromMatch converts a published finding into the transport-neutral
// notify.Event. The raw secret is carried through: every channel masks it when
// it renders the payload.
func notifyEventFromMatch(event *MatchEvent, provider string, outcome *verifiers.Outcome) notify.Event {
	secret := event.Secret
	if secret == "" && len(event.Matches) > 0 {
		secret = event.Matches[0]
	}
	ev := notify.Event{
		Signature: event.Signature,
		Secret:    secret,
		URL:       event.Url,
		Repo:      event.Url,
		File:      event.File,
		Line:      event.SecretLine,
		Priority:  event.Priority,
		Severity:  notifySeverityFromString(event.Severity),
		Provider:  provider,
	}
	if outcome != nil {
		ev.Tier = outcome.Tier
		ev.Balance = outcome.Balance
		ev.Identity = outcome.Identity
	}
	return ev
}

// ──────────────────────────────────────────────────────────────
//  DAILY DIGEST (Slice 9)
// ──────────────────────────────────────────────────────────────

// digestInterval is how often the findings digest runs. Production uses a day;
// it is a var so tests can shorten it without a real wait.
var digestInterval = 24 * time.Hour

// startDailyDigest launches the findings digest goroutine. It reports whether
// the goroutine started. When findings storage is disabled or degraded there is
// nothing to read, so it logs one note and does not start: the operator should
// know the digest will not fire.
func startDailyDigest() bool {
	st := getSession().Store
	if !getSession().Config.StoreEnabled() || st == nil || st.Degraded() {
		// Debug rather than Info: storage is off by default, so a note at Info
		// would add a line to every existing operator's scan output.
		getSession().Log.Debug("[digest] disabled: findings storage is not enabled")
		return false
	}
	go func() {
		ticker := time.NewTicker(digestInterval)
		defer ticker.Stop()
		for range ticker.C {
			logDigest(st)
		}
	}()
	return true
}

// logDigest summarises the findings recorded in the last digestInterval through
// the session logger and then delivers the same summary to every configured
// alert channel and to the main webhook. Delivery is best-effort: each failure
// is logged and never interrupts the scan or the digest loop.
func logDigest(st store.Store) {
	recs, err := st.List(store.Filter{Since: time.Now().Add(-digestInterval)})
	if err != nil {
		getSession().Log.Warn("[digest] could not read findings store: %s", err)
		return
	}
	summary := formatDigest(recs, digestInterval)
	getSession().Log.Info("[digest] %s", summary)
	deliverDigest(recs, summary)
}

// deliverDigest sends the daily summary to the extra alert channels (one
// synthetic event per severity bucket, so a channel's own severity floor still
// applies) and to the main webhook. The webhook is skipped when none is
// configured, exactly like the per-finding path.
func deliverDigest(recs []store.Record, summary string) {
	log := getSession().Log
	if d := notifyDispatcherRef(); d != nil {
		for _, ev := range digestEvents(recs, summary) {
			recordNotifyResults(d.Deliver(ev), log)
		}
	}
	cfg := getSession().Config
	if cfg != nil && strings.TrimSpace(cfg.Webhook) != "" {
		core.SendMatchWebhook(cfg.Webhook, cfg.WebhookPayload, "", "shhgit daily digest", "", []string{summary}, "", 0, "", digestSeverity(recs), nil)
	}
}

// digestEvents builds one synthetic event per non-empty severity bucket so a
// channel configured with a min_severity floor only receives a digest when that
// bucket has findings. The rendered summary rides in the Repository field, the
// free-text slot the shared channel renderers always print.
func digestEvents(recs []store.Record, summary string) []notify.Event {
	counts := map[string]int{}
	for _, r := range recs {
		sev := strings.ToLower(strings.TrimSpace(r.Severity))
		if sev == "" {
			sev = "unclassified"
		}
		counts[sev]++
	}
	order := []struct {
		name string
		rank notify.Severity
	}{
		{"critical", notify.SevCritical},
		{"high", notify.SevHigh},
		{"medium", notify.SevMedium},
		{"low", notify.SevLow},
	}
	var events []notify.Event
	for _, b := range order {
		n := counts[b.name]
		if n == 0 {
			continue
		}
		events = append(events, notify.Event{
			Signature: fmt.Sprintf("shhgit daily digest: %d %s finding(s)", n, b.name),
			Repo:      summary,
			Severity:  b.rank,
		})
	}
	return events
}

// digestSeverity returns the highest severity present in the window, or "low"
// when the records carry no recognised severity, for the webhook's embed accent.
func digestSeverity(recs []store.Record) string {
	rank := map[string]int{"low": 1, "medium": 2, "high": 3, "critical": 4}
	best, bestRank := "", 0
	for _, r := range recs {
		s := strings.ToLower(strings.TrimSpace(r.Severity))
		if rank[s] > bestRank {
			best, bestRank = s, rank[s]
		}
	}
	if best == "" {
		return "low"
	}
	return best
}

// formatDigest renders a one-line summary of records over a window: the total,
// a per-severity breakdown and the most frequent signature.
func formatDigest(recs []store.Record, window time.Duration) string {
	if len(recs) == 0 {
		return fmt.Sprintf("no findings in the last %s", humanDuration(window))
	}
	bySeverity := map[string]int{}
	bySignature := map[string]int{}
	for _, r := range recs {
		sev := r.Severity
		if sev == "" {
			sev = "unclassified"
		}
		bySeverity[sev]++
		bySignature[r.Signature]++
	}
	top, topN := "", 0
	for s, n := range bySignature {
		if n > topN || (n == topN && s < top) {
			top, topN = s, n
		}
	}
	return fmt.Sprintf("%d findings in the last %s (critical %d, high %d, medium %d, low %d); top: %s (%d)",
		len(recs), humanDuration(window),
		bySeverity["critical"], bySeverity["high"], bySeverity["medium"], bySeverity["low"],
		top, topN)
}

// humanDuration renders the common whole-hour/day durations compactly.
func humanDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	default:
		return d.String()
	}
}

// ──────────────────────────────────────────────────────────────
//  REMOTE DISCOVERY SOURCES (GitLab, Pastebin, searchcode, Sourcegraph)
// ──────────────────────────────────────────────────────────────

// Source seams. Tests point the APIs and transports at httptest servers;
// production leaves them empty/nil, which selects the public hosts and the
// shared resilient client.
var (
	gitLabBaseURL string
	gitLabDoer    core.Doer

	pastebinBaseURL   string
	pastebinRawBase   string
	pastebinPublicRaw string
	pastebinDoer      core.Doer

	searchCodeBaseURL string
	searchCodeDoer    core.Doer

	sourcegraphBaseURL string
	sourcegraphDoer    core.Doer

	// gitHubSearchBaseURL/gitHubSearchDoer are shared by the commit- and
	// issue-search sources, which hit the same API host.
	gitHubSearchBaseURL string
	gitHubSearchDoer    core.Doer

	grepAppBaseURL string
	grepAppDoer    core.Doer

	psbdmpBaseURL string
	psbdmpDoer    core.Doer

	bitbucketBaseURL string
	bitbucketDoer    core.Doer
)

// sourceStartBlocked reports whether a background remote source must not start
// for this run: a --local scan needs no network source, and the one-shot
// store/token commands never publish, so starting a polling source for them
// would be pure waste and could make a token command unexpectedly hit the
// network. Every remote source shares this guard so they cannot drift.
func sourceStartBlocked() bool {
	o := getSession().Options
	if strings.TrimSpace(*o.Local) != "" {
		return true
	}
	return boolOpt(o.TestTokens) || strings.TrimSpace(strOpt(o.ScanKeys)) != "" ||
		boolOpt(o.Export) || boolOpt(o.Stats) ||
		(o.ExportEncrypted != nil && *o.ExportEncrypted != "") ||
		(o.Decrypt != nil && *o.Decrypt != "")
}

// startGitLab launches the GitLab public-snippet source in the background when
// sources.gitlab.enabled is true. Like code search it is a remote-scan feature:
// a --local scan or a one-shot store/token command must not start it. It polls
// at the configured interval until the process exits.
func startGitLab() {
	if !getSession().Config.GitLabEnabled() {
		return
	}
	if sourceStartBlocked() {
		return
	}
	_, interval := getSession().Config.GitLabValues()
	go func() {
		for {
			runGitLab()
			if interval <= 0 {
				return
			}
			time.Sleep(time.Duration(interval) * time.Second)
		}
	}()
}

// runGitLab performs one polling pass: list the public snippets, download each
// allowlisted raw body and feed it to the shared scanContents engine, so a
// GitLab finding is produced by exactly the same signatures and suppression
// rules as a cloned repository. A listing or download failure is logged and
// skipped; one bad snippet never stops the pass.
func runGitLab() {
	log := getSession().Log
	perPage, _ := getSession().Config.GitLabValues()
	doer := gitLabDoer
	if doer == nil {
		doer = core.SharedHTTPClient()
	}
	client := &core.GitLabClient{Doer: doer, BaseURL: gitLabBaseURL, PerPage: perPage}

	snippets, err := client.PublicSnippets(0)
	if err != nil {
		log.Warn("[gitlab] snippet listing failed: %s", err)
		return
	}
	for _, sn := range snippets {
		if core.IsSkippableFile(sn.FileName) {
			continue
		}
		body, err := client.SnippetContent(sn.RawURL)
		if err != nil {
			log.Debug("[gitlab] skipping snippet %d: %s", sn.ID, err)
			continue
		}
		displayURL := sn.WebURL
		if displayURL == "" {
			displayURL = sn.RawURL
		}
		scanContents(displayURL, "", sn.FileName, displayURL, -1, body, core.GITLAB_SOURCE)
	}
}

// startPastebin launches the Pastebin Scraping API source when
// sources.pastebin.enabled is true. It is a remote-scan feature with the same
// --local / one-shot guard as GitLab, and polls at the configured interval.
func startPastebin() {
	if !getSession().Config.PastebinEnabled() {
		return
	}
	if sourceStartBlocked() {
		return
	}
	_, _, interval := getSession().Config.PastebinValues()
	go func() {
		for {
			runPastebin()
			if interval <= 0 {
				return
			}
			time.Sleep(time.Duration(interval) * time.Second)
		}
	}()
}

// runPastebin performs one polling pass: list the most recent public pastes and
// download each body, feeding it to the shared scanContents engine so a
// Pastebin finding is produced by exactly the same signatures and suppression
// rules as a cloned repository. api_key selects the Scraping API item endpoint
// over the public raw endpoint; a listing or download failure is logged and
// skipped, never aborting the pass.
func runPastebin() {
	log := getSession().Log
	apiKey, limit, _ := getSession().Config.PastebinValues()
	doer := pastebinDoer
	if doer == nil {
		doer = core.SharedHTTPClient()
	}
	client := core.NewPastebinClient(doer, pastebinBaseURL)
	if pastebinRawBase != "" {
		client.RawBase = pastebinRawBase
	}
	if pastebinPublicRaw != "" {
		client.PublicRaw = pastebinPublicRaw
	}

	pastes, err := client.ListRecent(limit)
	if err != nil {
		log.Warn("[pastebin] paste listing failed: %s", err)
		return
	}
	useScrapeAPI := strings.TrimSpace(apiKey) != ""
	for _, paste := range pastes {
		body, err := client.PasteContent(paste.Key, useScrapeAPI)
		if err != nil {
			log.Debug("[pastebin] skipping %s: %s", paste.Key, err)
			continue
		}
		scanContents(paste.URL, "", paste.URL, paste.URL, -1, body, core.PASTEBIN_SOURCE)
	}
}

// startSearchCode launches the searchcode.com source when
// sources.searchcode.enabled is true, with the same remote-scan guard.
func startSearchCode() {
	if !getSession().Config.SearchCodeEnabled() {
		return
	}
	if sourceStartBlocked() {
		return
	}
	_, _, _, interval := getSession().Config.SearchCodeValues()
	go func() {
		for {
			runSearchCode()
			if interval <= 0 {
				return
			}
			time.Sleep(time.Duration(interval) * time.Second)
		}
	}()
}

// runSearchCode performs one polling pass: for each configured query it walks
// the pages, then scans every result's reconstructed content with the shared
// scanContents engine and tags the finding SEARCHCODE_SOURCE. An empty query
// list falls back to core.SearchCodeKeywords. A query failure is logged and the
// next query is still attempted.
func runSearchCode() {
	log := getSession().Log
	queries, perPage, maxPages, _ := getSession().Config.SearchCodeValues()
	if len(queries) == 0 {
		queries = core.SearchCodeKeywords
	}
	doer := searchCodeDoer
	if doer == nil {
		doer = core.SharedHTTPClient()
	}
	client := core.NewSearchCodeClient(doer, searchCodeBaseURL)
	client.PerPage = perPage

	for _, query := range queries {
		results, err := client.SearchKeyword(query, maxPages)
		if err != nil {
			log.Warn("[searchcode] query %q failed: %s", query, err)
		}
		for _, result := range results {
			content := result.Content()
			if content == "" {
				continue
			}
			displayURL := strings.TrimSpace(result.URL)
			if displayURL == "" {
				displayURL = strings.TrimSpace(result.Repo)
			}
			displayPath := strings.TrimSpace(result.Filename)
			if displayPath == "" {
				displayPath = displayURL
			}
			scanContents("", "", displayPath, displayURL, -1, []byte(content), core.SEARCHCODE_SOURCE)
		}
	}
}

// startSourcegraph launches the Sourcegraph stream-search source when
// sources.sourcegraph.enabled is true, with the same remote-scan guard.
func startSourcegraph() {
	if !getSession().Config.SourcegraphEnabled() {
		return
	}
	if sourceStartBlocked() {
		return
	}
	_, _, interval, _ := getSession().Config.SourcegraphValues()
	go func() {
		for {
			runSourcegraph()
			if interval <= 0 {
				return
			}
			time.Sleep(time.Duration(interval) * time.Second)
		}
	}()
}

// runSourcegraph performs one polling pass: for each configured query it scans
// the matched preview lines with the shared scanContents engine, tagged
// SOURCEGRAPH_SOURCE and linked to the Sourcegraph blob URL. An empty query
// list falls back to a small default list derived from the loaded signature
// anchors; when none are derivable the pass does nothing. A query failure is
// logged and the next query is still attempted.
func runSourcegraph() {
	log := getSession().Log
	queries, maxResults, _, token := getSession().Config.SourcegraphValues()
	if len(queries) == 0 {
		queries = core.DefaultSourcegraphQueries(core.SignatureAnchors(getSession().Signatures), 0)
	}
	if len(queries) == 0 {
		log.Warn("[sourcegraph] no queries configured and no signature anchors to derive them from")
		return
	}
	doer := sourcegraphDoer
	if doer == nil {
		doer = core.SharedHTTPClient()
	}
	client := core.NewSourcegraphClient(doer, sourcegraphBaseURL)
	client.Token = token

	for _, query := range queries {
		matches, err := client.Search(query, maxResults)
		if err != nil {
			log.Warn("[sourcegraph] query %q failed: %s", query, err)
		}
		for _, m := range matches {
			if strings.TrimSpace(m.Preview) == "" {
				continue
			}
			repo := strings.Trim(strings.TrimSpace(m.Repository), "/")
			path := strings.Trim(strings.TrimSpace(m.Path), "/")
			displayURL := "https://sourcegraph.com/" + repo + "/-/blob/" + path
			displayPath := path
			if displayPath == "" {
				displayPath = displayURL
			}
			scanContents("", "", displayPath, displayURL, -1, []byte(m.Preview), core.SOURCEGRAPH_SOURCE)
		}
	}
}

// gitLabBlobSearchMaxPages bounds how many 100-blob pages each GitLab blob
// query walks. Advanced code search is the most expensive GitLab call.
const gitLabBlobSearchMaxPages = 3

// startGitHubCommits launches the GitHub commit-search source when
// sources.github_commits.enabled is true, with the same remote-scan guard and
// interval polling as the other remote sources.
func startGitHubCommits() {
	if !getSession().Config.GitHubCommitsEnabled() {
		return
	}
	if sourceStartBlocked() {
		return
	}
	_, _, interval := getSession().Config.GitHubCommitsValues()
	go func() {
		for {
			runGitHubCommits()
			if interval <= 0 {
				return
			}
			time.Sleep(time.Duration(interval) * time.Second)
		}
	}()
}

// runGitHubCommits performs one polling pass: for each configured (or derived)
// query it scans the returned commit messages with the shared scanContents
// engine, tagged GITHUB_COMMITS_SOURCE and linked to the commit HTML URL. A
// missing token skips the pass, since commit search is authenticated.
func runGitHubCommits() {
	log := getSession().Log
	queries, maxResults, _ := getSession().Config.GitHubCommitsValues()
	if len(queries) == 0 {
		queries = core.DefaultSourceQueries(core.SignatureAnchors(getSession().Signatures), 0)
	}
	if len(queries) == 0 {
		log.Warn("[github-commits] no queries configured and no signature anchors to derive them from")
		return
	}
	token := firstGitHubToken()
	if token == "" {
		log.Warn("[github-commits] enabled but no GitHub access token is configured; commit search requires a token")
		return
	}
	doer := gitHubSearchDoer
	if doer == nil {
		doer = core.SharedHTTPClient()
	}
	client := core.NewGitHubSearchClient(doer, gitHubSearchBaseURL, token)

	for _, query := range queries {
		hits, err := client.SearchCommits(query, maxResults)
		if err != nil {
			log.Warn("[github-commits] query %q failed: %s", query, err)
		}
		for _, hit := range hits {
			if strings.TrimSpace(hit.Message) == "" {
				continue
			}
			scanContents("", "", hit.HTMLURL, hit.HTMLURL, -1, []byte(hit.Message), core.GITHUB_COMMITS_SOURCE)
		}
	}
}

// startGitHubIssues launches the GitHub issue-search source when
// sources.github_issues.enabled is true, with the same guard as the other
// remote sources.
func startGitHubIssues() {
	if !getSession().Config.GitHubIssuesEnabled() {
		return
	}
	if sourceStartBlocked() {
		return
	}
	_, _, interval := getSession().Config.GitHubIssuesValues()
	go func() {
		for {
			runGitHubIssues()
			if interval <= 0 {
				return
			}
			time.Sleep(time.Duration(interval) * time.Second)
		}
	}()
}

// runGitHubIssues performs one polling pass: for each configured (or derived)
// query it scans each issue/PR title and body with the shared scanContents
// engine, tagged GITHUB_ISSUES_SOURCE and linked to the issue HTML URL.
func runGitHubIssues() {
	log := getSession().Log
	queries, maxResults, _ := getSession().Config.GitHubIssuesValues()
	if len(queries) == 0 {
		queries = core.DefaultSourceQueries(core.SignatureAnchors(getSession().Signatures), 0)
	}
	if len(queries) == 0 {
		log.Warn("[github-issues] no queries configured and no signature anchors to derive them from")
		return
	}
	token := firstGitHubToken()
	if token == "" {
		log.Warn("[github-issues] enabled but no GitHub access token is configured; issue search requires a token")
		return
	}
	doer := gitHubSearchDoer
	if doer == nil {
		doer = core.SharedHTTPClient()
	}
	client := core.NewGitHubSearchClient(doer, gitHubSearchBaseURL, token)

	for _, query := range queries {
		hits, err := client.SearchIssues(query, maxResults)
		if err != nil {
			log.Warn("[github-issues] query %q failed: %s", query, err)
		}
		for _, hit := range hits {
			body := hit.Title + "\n" + hit.Body
			if strings.TrimSpace(body) == "" {
				continue
			}
			scanContents("", "", hit.HTMLURL, hit.HTMLURL, -1, []byte(body), core.GITHUB_ISSUES_SOURCE)
		}
	}
}

// startGitLabBlobs launches the GitLab blobs (code search) source when
// sources.gitlab.search_enabled is true. It stops permanently once GitLab
// refuses the scope as a Premium/Ultimate feature.
func startGitLabBlobs() {
	if !getSession().Config.GitLabBlobsEnabled() {
		return
	}
	if sourceStartBlocked() {
		return
	}
	_, interval := getSession().Config.GitLabBlobValues()
	go func() {
		for {
			if !runGitLabBlobs() {
				return
			}
			if interval <= 0 {
				return
			}
			time.Sleep(time.Duration(interval) * time.Second)
		}
	}()
}

// runGitLabBlobs performs one polling pass over signature-derived queries. It
// returns false when the source should stop for good: no query is derivable, no
// token is configured, or GitLab refused the blobs scope as an advanced-search
// (Premium/Ultimate) feature.
func runGitLabBlobs() bool {
	log := getSession().Log
	token, _ := getSession().Config.GitLabBlobValues()
	if strings.TrimSpace(token) == "" {
		log.Warn("[gitlab] blob search is enabled but sources.gitlab.token is empty; blob search requires a token")
		return false
	}
	queries := core.DefaultSourceQueries(core.SignatureAnchors(getSession().Signatures), 0)
	if len(queries) == 0 {
		log.Warn("[gitlab] blob search is enabled but no signature anchors are available to derive queries from")
		return false
	}
	doer := gitLabDoer
	if doer == nil {
		doer = core.SharedHTTPClient()
	}
	perPage, _ := getSession().Config.GitLabValues()
	client := &core.GitLabClient{Doer: doer, BaseURL: gitLabBaseURL, PerPage: perPage, Token: token}
	projects := map[int]string{}

	for _, query := range queries {
		blobs, err := client.SearchBlobsAll(query, gitLabBlobSearchMaxPages)
		if errors.Is(err, core.ErrGitLabSearchUnavailable) {
			log.Warn("[gitlab] blob search unavailable (%s); disabling the GitLab blob source", err)
			return false
		}
		if err != nil {
			log.Warn("[gitlab] blob query %q failed: %s", query, err)
		}
		for _, b := range blobs {
			if strings.TrimSpace(b.Data) == "" {
				continue
			}
			project := gitLabProjectPath(doer, gitLabBaseURL, token, b.ProjectID, projects)
			displayURL := gitLabBlobWebURL(gitLabBaseURL, project, b.Ref, b.Path)
			scanContents("", b.Ref, b.Path, displayURL, -1, []byte(b.Data), core.GITLAB_SOURCE)
		}
	}
	return true
}

// gitLabProjectPath resolves a GitLab project id to its path_with_namespace for
// the blob web URL, caching results for the life of one pass. On any failure it
// returns "" so the caller falls back to the raw blob path.
func gitLabProjectPath(doer core.Doer, base, token string, id int, cache map[int]string) string {
	if id <= 0 {
		return ""
	}
	if path, ok := cache[id]; ok {
		return path
	}
	root := strings.TrimRight(strings.TrimSpace(base), "/")
	if root == "" {
		root = "https://gitlab.com"
	}
	req, err := http.NewRequest(http.MethodGet, root+"/api/v4/projects/"+strconv.Itoa(id), nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "shhgit2")
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("PRIVATE-TOKEN", token)
	}
	resp, err := doer.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var payload struct {
		PathWithNamespace string `json:"path_with_namespace"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return ""
	}
	path := strings.TrimSpace(payload.PathWithNamespace)
	cache[id] = path
	return path
}

// gitLabBlobWebURL builds the human-facing blob URL. When the project path
// could not be resolved it falls back to the raw path under the instance root.
func gitLabBlobWebURL(base, projectPath, ref, path string) string {
	root := strings.TrimRight(strings.TrimSpace(base), "/")
	if root == "" {
		root = "https://gitlab.com"
	}
	projectPath = strings.Trim(projectPath, "/")
	ref = strings.Trim(ref, "/")
	path = strings.TrimLeft(path, "/")
	if projectPath == "" {
		return root + "/" + path
	}
	return root + "/" + projectPath + "/-/blob/" + ref + "/" + path
}

// startGrepApp launches the grep.app source when sources.grepapp.enabled is
// true, with the same guard as the other remote sources.
func startGrepApp() {
	if !getSession().Config.GrepAppEnabled() {
		return
	}
	if sourceStartBlocked() {
		return
	}
	_, _, interval := getSession().Config.GrepAppValues()
	go func() {
		for {
			runGrepApp()
			if interval <= 0 {
				return
			}
			time.Sleep(time.Duration(interval) * time.Second)
		}
	}()
}

// runGrepApp performs one polling pass: for each configured (or derived) query
// it scans the matched code snippets with the shared scanContents engine,
// tagged GREPAPP_SOURCE and linked to the GitHub blob URL.
func runGrepApp() {
	log := getSession().Log
	queries, maxResults, _ := getSession().Config.GrepAppValues()
	if len(queries) == 0 {
		queries = core.DefaultSourceQueries(core.SignatureAnchors(getSession().Signatures), 0)
	}
	if len(queries) == 0 {
		log.Warn("[grepapp] no queries configured and no signature anchors to derive them from")
		return
	}
	doer := grepAppDoer
	if doer == nil {
		doer = core.SharedHTTPClient()
	}
	client := core.NewGrepAppClient(doer, grepAppBaseURL)

	for _, query := range queries {
		hits, err := client.Search(query, maxResults)
		if err != nil {
			log.Warn("[grepapp] query %q failed: %s", query, err)
		}
		for _, hit := range hits {
			if strings.TrimSpace(hit.Code) == "" {
				continue
			}
			scanContents("", hit.Branch, hit.Path, client.BlobURL(hit), -1, []byte(hit.Code), core.GREPAPP_SOURCE)
		}
	}
}

// startPsbdmp launches the psbdmp.ws source when sources.psbdmp.enabled is
// true, with the same guard as the other remote sources.
func startPsbdmp() {
	if !getSession().Config.PsbdmpEnabled() {
		return
	}
	if sourceStartBlocked() {
		return
	}
	_, _, interval := getSession().Config.PsbdmpValues()
	go func() {
		for {
			runPsbdmp()
			if interval <= 0 {
				return
			}
			time.Sleep(time.Duration(interval) * time.Second)
		}
	}()
}

// runPsbdmp performs one polling pass: for each configured (or derived) query
// it scans the returned paste bodies with the shared scanContents engine,
// tagged PSBDMP_SOURCE and linked to the Pastebin URL. A search entry with no
// inline text is downloaded through the dump endpoint first.
func runPsbdmp() {
	log := getSession().Log
	queries, maxResults, _ := getSession().Config.PsbdmpValues()
	if len(queries) == 0 {
		queries = core.DefaultSourceQueries(core.SignatureAnchors(getSession().Signatures), 0)
	}
	if len(queries) == 0 {
		log.Warn("[psbdmp] no queries configured and no signature anchors to derive them from")
		return
	}
	doer := psbdmpDoer
	if doer == nil {
		doer = core.SharedHTTPClient()
	}
	client := core.NewPsbdmpClient(doer, psbdmpBaseURL)

	for _, query := range queries {
		pastes, err := client.Search(query, maxResults)
		if err != nil {
			log.Warn("[psbdmp] query %q failed: %s", query, err)
		}
		for _, paste := range pastes {
			body := []byte(paste.Text)
			if len(bytes.TrimSpace(body)) == 0 {
				dumped, derr := client.Dump(paste.ID)
				if derr != nil {
					log.Debug("[psbdmp] skipping %s: %s", paste.ID, derr)
					continue
				}
				body = dumped
			}
			if len(bytes.TrimSpace(body)) == 0 {
				continue
			}
			scanContents("", "", paste.URL, paste.URL, -1, body, core.PSBDMP_SOURCE)
		}
	}
}

// startBitbucket launches the Bitbucket workspace source when
// sources.bitbucket.enabled is true, with the same guard as the other remote
// sources.
func startBitbucket() {
	if !getSession().Config.BitbucketEnabled() {
		return
	}
	if sourceStartBlocked() {
		return
	}
	_, _, _, interval := getSession().Config.BitbucketValues()
	go func() {
		for {
			runBitbucket()
			if interval <= 0 {
				return
			}
			time.Sleep(time.Duration(interval) * time.Second)
		}
	}()
}

// runBitbucket performs one polling pass: for each configured workspace it
// lists the repositories, walks each repository's files and scans every file
// body with the shared scanContents engine, tagged BITBUCKET_SOURCE and linked
// to the file's raw URL. A workspace or repository failure is logged and the
// walk continues.
func runBitbucket() {
	log := getSession().Log
	workspaces, maxRepos, maxFiles, _ := getSession().Config.BitbucketValues()
	if len(workspaces) == 0 {
		log.Warn("[bitbucket] enabled but no workspaces are configured")
		return
	}
	doer := bitbucketDoer
	if doer == nil {
		doer = core.SharedHTTPClient()
	}
	client := core.NewBitbucketClient(doer, bitbucketBaseURL)

	for _, workspace := range workspaces {
		workspace = strings.TrimSpace(workspace)
		if workspace == "" {
			continue
		}
		repos, err := client.ListRepos(workspace, maxRepos)
		if err != nil {
			log.Warn("[bitbucket] workspace %q listing failed: %s", workspace, err)
		}
		for _, repo := range repos {
			files, err := client.ListFiles(workspace, repo.Slug, repo.MainBranch, maxFiles)
			if err != nil {
				log.Warn("[bitbucket] %q file listing failed: %s", repo.FullName, err)
			}
			for _, f := range files {
				if core.IsSkippableFile(f.Path) {
					continue
				}
				body, ferr := client.FileContent(f.RawURL)
				if ferr != nil {
					log.Debug("[bitbucket] skipping %s: %s", f.Path, ferr)
					continue
				}
				scanContents("", repo.MainBranch, f.Path, f.RawURL, -1, body, core.BITBUCKET_SOURCE)
			}
		}
	}
}

// deliverWebhook enqueues one finding to the configured webhook. onConfirm is
// invoked exactly once with the final outcome: true when the endpoint accepted
// the message (or the item was already delivered inside the queue's dedupe
// window), false when the item was dropped or every retry failed. It never
// blocks - the queue sends on its own goroutine - so a slow or dead endpoint
// cannot throttle the scanner.
//
// It is a package variable so tests can simulate an unconfirmed delivery.
var deliverWebhook = func(event *MatchEvent, onConfirm func(ok bool)) {
	cfg := getSession().Config
	// The webhook subsystem's latency spans enqueue to the queue's final
	// outcome, so it reflects a real delivery rather than an instantaneous
	// enqueue. A dropped or permanently failed delivery counts as an error.
	var start time.Time
	if core.MetricsCollectionEnabled() {
		start = time.Now()
	}
	core.SendMatchWebhook(
		cfg.Webhook,
		cfg.WebhookPayload,
		event.Url,
		event.Signature,
		event.File,
		event.Matches,
		event.Color,
		event.Priority,
		event.FileContent,
		event.Severity,
		func(ok bool) {
			if core.MetricsCollectionEnabled() {
				core.DefaultMetrics.ObserveLatency(core.SubsystemWebhook, time.Since(start))
				core.DefaultMetrics.IncOperations(core.SubsystemWebhook)
				if !ok {
					core.DefaultMetrics.IncError(core.SubsystemWebhook)
				}
			}
			if onConfirm != nil {
				onConfirm(ok)
			}
		},
	)
}

func publish(event *MatchEvent) {
	// One id for every surface: the web hub, the TUI and the findings store
	// each minted their own, so the same finding could never be correlated
	// across them (and the captured file body was stored twice under two
	// keys). Mint once, share everywhere.
	id := newMatchID()

	// The finding funnel is also the scan subsystem's hot path: time it end to
	// end (including any dedupe/AI-filter early return) and count one operation
	// per finding.
	if core.MetricsCollectionEnabled() {
		start := time.Now()
		defer func() {
			core.DefaultMetrics.ObserveLatency(core.SubsystemScan, time.Since(start))
			core.DefaultMetrics.IncOperations(core.SubsystemScan)
		}()
	}

	sess := getSession()

	// Cross-run dedupe. The fingerprint is computed before delivery; when the
	// store already knows it (and --no-dedupe is not set) the finding is a
	// repeat and is not re-published to any sink. A suppressed finding never
	// reaches the write below, so it is never persisted.
	//
	// The store and dedupe are gated separately: --no-dedupe must only skip the
	// Seen read, not persistence. Gating both on the same bool meant a
	// --no-dedupe run appended nothing, so --export always came back empty.
	storeEnabled := sess.Store != nil && sess.Config != nil && sess.Config.StoreEnabled()
	dedupe := storeEnabled && !boolOpt(sess.Options.NoDedupe)
	var fingerprint string
	if storeEnabled {
		fingerprint = findingFingerprint(event)
	}
	if dedupe && sess.Store.Seen(fingerprint) {
		return
	}

	// Dynamic severity (Slice 6): derived from the verifier outcome when one is
	// already known, otherwise from the rule priority. It is computed once here
	// and shared by the dashboard, TUI, webhook and findings store so the four
	// surfaces cannot disagree.
	outcome := outcomeForSecret(event.Secret)
	_, providerName, _ := cachedFor(event.Secret)
	severity := core.Classify(event.Priority, outcome).String()
	event.Severity = severity

	// Bounded context and base-URL extraction (Slice 4). Both are opt-in:
	// scanning.context_lines defaults to 0, so an existing scan captures nothing
	// extra until the operator turns it on.
	var contextText, baseURL string
	if sess.Config != nil {
		if lines := sess.Config.Scanning.Int(sess.Config.Scanning.ContextLines, 0); lines > 0 &&
			event.FileContent != "" && event.SecretLine > 0 {
			contextText = core.ExtractContext([]byte(event.FileContent), event.SecretLine, lines, maxStoreFileBytes)
			baseURL, _ = core.ExtractBaseURL([]byte(event.FileContent))
		}
	}

	// AI false-positive pre-filter (Slice 8, opt-in). It runs before any sink, so
	// a suppressed finding reaches neither the dashboard, the TUI, the --live
	// endpoint, the webhook nor the findings store: it is recorded in the
	// filter's bounded ring and dropped. The filter fails open, so a timeout, a
	// full queue or a classifier error keeps the finding.
	if f := getAIFilter(); f != nil {
		if verdict := f.Consider(event, contextText); verdict.Suppress {
			f.Record(event, verdict)
			return
		}
	}

	// matches_found counts findings that reach a sink. It sits below the
	// dedupe and AI-filter early returns: counting above them inflated the
	// dashboard with suppressed/duplicates that no sink ever saw.
	core.GlobalScanProgress.MatchFound()

	// The Tokens-tab row for this secret was pushed by OnOutcome during
	// matching, before the match existed and its rule priority was known. Now
	// that severity is computed, copy it onto that row so the two tabs agree.
	syncTokenSeverity(maskedOutcomeKey(event.Secret), severity)

	// Verified is a tri-state on the wire: nil until a verifier reports, then
	// true/false for an accepted/rejected secret. A quota-limited credential is
	// accepted-but-throttled, so it is verified exactly as core's outcomeValid
	// treats it.
	var verified *bool
	if outcome != nil {
		verified = boolPtr(outcome.Status == verifiers.Valid || outcome.Status == verifiers.QuotaLimited)
	}

	// Feed the in-process web dashboard (when running with --web).
	if webHub != nil {
		m := &Match{
			ID:        id,
			Timestamp: time.Now(),
			Source:    sourceName(event.Source),
			URL:       event.Url,
			File:      event.File,
			Signature: event.Signature,
			Matches:   event.Matches,
			Secret:    event.Secret,
			Line:      event.SecretLine,
			HasFile:   event.FileContent != "",
			Stars:     event.Stars,
			Priority:  event.Priority,
			Color:     event.Color,
			BaseURL:   baseURL,
			Context:   contextText,
			Severity:  severity,
			Verified:  verified,
		}
		// Blocking, deliberately. This used to be a non-blocking send with a
		// default that discarded the match when the hub's 256-slot buffer was
		// full, so a scanner that outran the hub lost findings silently - they
		// never reached h.matches either, so a dashboard reload could not recover
		// them. The hub loop never blocks (its per-client sends are non-blocking
		// and a slow client is dropped instead), so waiting here only throttles
		// the scanner to the hub's pace and loses nothing.
		webHub.broadcast <- m
		if event.FileContent != "" {
			storeMatchFile(m.ID, event.Url, event.File, event.FileContent, event.Secret, event.SecretLine)
		}
	}

	// Feed the TUI (when running with --tui). tuiState is deliberately never
	// nil, so its presence is not a mode test: gating on it made every
	// terminal/web run retain finding records in the TUI buffer forever.
	if tuiActive() {
		AddTUIMatch(&TUIMatch{
			ID:        id,
			Timestamp: time.Now(),
			Source:    sourceName(event.Source),
			URL:       event.Url,
			File:      event.File,
			Signature: event.Signature,
			Matches:   event.Matches,
			Secret:    event.Secret,
			Line:      event.SecretLine,
			Stars:     event.Stars,
			Priority:  event.Priority,
			Severity:  severity,
		})
		// Keep the captured file body under the same id the UI sees, so the "v"
		// key can show the file around the finding exactly like the dashboard's
		// viewer. Only while the TUI owns the screen: in web mode the branch
		// above already stored it under the dashboard's own id.
		if tuiScreenActive && event.FileContent != "" {
			storeMatchFile(id, event.Url, event.File, event.FileContent, event.Secret, event.SecretLine)
		}
	}

	if len(strOpt(getSession().Options.Live)) > 0 {
		// Convert MatchEvent to server Match format
		match := map[string]interface{}{
			"timestamp": time.Now().Format(time.RFC3339),
			"source":    event.Source,
			"url":       event.Url,
			"file":      event.File,
			"signature": event.Signature,
			"matches":   event.Matches,
			"secret":    event.Secret,
			"line":      event.SecretLine,
			"stars":     event.Stars,
			"priority":  event.Priority,
			"color":     event.Color,
			"severity":  severity,
			"base_url":  baseURL,
			"context":   contextText,
		}
		data, _ := json.Marshal(match)
		// Off the scanner's goroutine and through a bounded client. This was an
		// inline http.Post on http.DefaultClient, which has no timeout and whose
		// response body was never closed: a hung --live endpoint stalled every
		// worker and leaked the connection.
		go postLive(strOpt(getSession().Options.Live), data)
	}

	// Persist the finding only after its delivery path is confirmed. A failed or
	// unconfirmed delivery is deliberately left unwritten so the next run
	// re-alerts it. The record is built up-front because confirmation can arrive
	// later, on the webhook queue's goroutine, after publish has returned.
	persist := func() {}
	if storeEnabled {
		rec := store.Record{
			ID:          id,
			Fingerprint: fingerprint,
			Timestamp:   time.Now(),
			Source:      sourceName(event.Source),
			URL:         event.Url,
			File:        event.File,
			Signature:   event.Signature,
			Matches:     event.Matches,
			Secret:      event.Secret,
			Line:        event.SecretLine,
			Stars:       event.Stars,
			Priority:    event.Priority,
			Severity:    severity,
		}
		// The verifier metadata is only known when a validation already
		// completed; it is left empty otherwise, as the store contract allows.
		if outcome != nil {
			rec.Provider = providerName
			rec.Tier = outcome.Tier
			rec.Balance = outcome.Balance
		}
		persist = func() {
			if err := sess.Store.Append(rec); err != nil {
				sess.Log.Warn("Findings store append failed; the finding was not persisted: %s", err)
			}
		}
	}

	// Send EVERY match to the main webhook. The queue enqueues without blocking
	// (the actual HTTP send runs on the queue's goroutine) and reports the final
	// outcome through onConfirm, so the finding is recorded only once the alert
	// has actually gone out. A delayed callback can never throttle the scanner.
	webhookConfigured := sess.Config != nil && len(sess.Config.Webhook) > 0
	if webhookConfigured {
		deliverWebhook(event, func(ok bool) {
			if ok {
				persist()
			}
		})
	}

	// Extra alert channels (Slice 9). This runs alongside the webhook above, not
	// instead of it. Delivery is off the scanner's goroutine because a channel
	// may do real I/O (HTTP, SMTP, file); the dispatcher applies the quiet-hours
	// policy and each channel's severity floor itself.
	if d := notifyDispatcherRef(); d != nil {
		nev := notifyEventFromMatch(event, providerName, outcome)
		go func() {
			recordNotifyResults(d.Deliver(nev), sess.Log)
		}()
	}

	// A broadcast-only run has no webhook whose delivery to await, so the
	// finding is confirmed (and recorded) immediately.
	if !webhookConfigured {
		persist()
	}
}

// ──────────────────────────────────────────────────────────────
//  FINDINGS STORE CLI (--export / --export-encrypted / --decrypt)
// ──────────────────────────────────────────────────────────────

// storePassphraseEnv names the environment variable that supplies the export
// passphrase. A passphrase is never accepted as a command-line argument: argv is
// visible to other processes and lands in shell history.
const storePassphraseEnv = "SHHGIT_STORE_PASSPHRASE"

// resolvePassphrase returns the export/decrypt passphrase from the environment,
// falling back to an interactive terminal prompt. confirm asks for the
// passphrase a second time (used when creating an encrypted export).
func resolvePassphrase(confirm bool) (string, error) {
	if v := os.Getenv(storePassphraseEnv); v != "" {
		return v, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("set %s or run on a terminal to enter a passphrase", storePassphraseEnv)
	}
	fmt.Fprint(os.Stderr, "Passphrase: ")
	first, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if len(first) == 0 {
		return "", errors.New("empty passphrase")
	}
	if !confirm {
		return string(first), nil
	}
	fmt.Fprint(os.Stderr, "Confirm passphrase: ")
	second, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(first, second) {
		return "", errors.New("passphrases do not match")
	}
	return string(first), nil
}

// exportFindings lists every record in the store and writes it in the requested
// format. An empty/unknown format value falls back to JSONL; anything other
// than csv/jsonl is rejected so a typo cannot silently emit the wrong format.
func exportFindings(st store.Store, format string, w io.Writer) error {
	if st == nil {
		return errors.New("findings store is unavailable")
	}
	recs, err := st.List(store.Filter{})
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "jsonl":
		return store.ExportJSONL(w, recs)
	case "csv":
		return store.ExportCSV(w, recs)
	default:
		return fmt.Errorf("unknown export format %q (want csv or jsonl)", format)
	}
}

// exportFindingsEncrypted writes an AES-256-GCM + scrypt encrypted export of
// every stored record to path.
func exportFindingsEncrypted(st store.Store, path, passphrase string) error {
	if st == nil {
		return errors.New("findings store is unavailable")
	}
	recs, err := st.List(store.Filter{})
	if err != nil {
		return err
	}
	return store.ExportEncrypted(path, recs, passphrase)
}

// decryptFindings decrypts an encrypted export and writes the records as JSONL.
func decryptFindings(path, passphrase string, w io.Writer) error {
	recs, err := store.DecryptFile(path, passphrase)
	if err != nil {
		return err
	}
	return store.ExportJSONL(w, recs)
}

// printStoreStats renders the findings store totals for --stats: the record
// count, a severity breakdown and the most frequent signatures and providers.
// It reads the whole store once, like --export.
func printStoreStats(st store.Store) error {
	if st == nil {
		return errors.New("findings store is unavailable")
	}
	recs, err := st.List(store.Filter{})
	if err != nil {
		return err
	}
	lockWrite(formatStoreStats(recs))
	return nil
}

// formatStoreStats renders the --stats report for a set of records. It is pure
// so the output can be asserted without touching stdout.
func formatStoreStats(recs []store.Record) string {
	bySeverity := map[string]int{}
	bySignature := map[string]int{}
	byProvider := map[string]int{}
	for _, r := range recs {
		sev := strings.ToLower(strings.TrimSpace(r.Severity))
		if sev == "" {
			sev = "unclassified"
		}
		bySeverity[sev]++
		if r.Signature != "" {
			bySignature[r.Signature]++
		}
		if r.Provider != "" {
			byProvider[r.Provider]++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[*] Findings store totals\n")
	fmt.Fprintf(&b, "    records:      %d\n", len(recs))
	fmt.Fprintf(&b, "    by severity:  critical %d, high %d, medium %d, low %d, unclassified %d\n",
		bySeverity["critical"], bySeverity["high"], bySeverity["medium"], bySeverity["low"], bySeverity["unclassified"])
	writeTopCounts(&b, "signature", bySignature)
	writeTopCounts(&b, "provider", byProvider)
	return b.String()
}

// writeTopCounts writes the ten most frequent keys of counts, ordering ties by
// name so the output is stable run to run.
func writeTopCounts(b *strings.Builder, label string, counts map[string]int) {
	type kv struct {
		name  string
		count int
	}
	all := make([]kv, 0, len(counts))
	for name, count := range counts {
		all = append(all, kv{name, count})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].count != all[j].count {
			return all[i].count > all[j].count
		}
		return all[i].name < all[j].name
	})
	limit := 10
	if len(all) < limit {
		limit = len(all)
	}
	fmt.Fprintf(b, "    top %ss:\n", label)
	if limit == 0 {
		fmt.Fprintf(b, "      (none)\n")
		return
	}
	for i := 0; i < limit; i++ {
		fmt.Fprintf(b, "      %2d. %s (%d)\n", i+1, all[i].name, all[i].count)
	}
}

// handleStoreCLI runs the one-shot store commands. It returns true when a
// command ran, so the caller skips the scan. Success exits 0; any failure is
// reported and exits 1. exitScanner only returns in web/TUI mode, so the caller
// must also return here to avoid falling through into a full scan.
func handleStoreCLI() bool {
	opts := getSession().Options
	switch {
	case opts.Decrypt != nil && *opts.Decrypt != "":
		pass, err := resolvePassphrase(false)
		if err == nil {
			err = decryptFindings(*opts.Decrypt, pass, os.Stdout)
		}
		if err != nil {
			lockPrintf("[!] --decrypt failed: %s\n", err)
			exitScanner(1)
			return true
		}
		exitScanner(0)
		return true

	case opts.ExportEncrypted != nil && *opts.ExportEncrypted != "":
		pass, err := resolvePassphrase(true)
		if err == nil {
			err = exportFindingsEncrypted(getSession().Store, *opts.ExportEncrypted, pass)
		}
		if err != nil {
			lockPrintf("[!] --export-encrypted failed: %s\n", err)
			exitScanner(1)
			return true
		}
		lockPrintf("[*] Encrypted findings export written to %s\n", *opts.ExportEncrypted)
		exitScanner(0)
		return true

	case opts.Export != nil && *opts.Export:
		if !getSession().Config.StoreEnabled() {
			lockPrintf("[!] storage.enabled is false; the export will be empty\n")
		}
		if err := exportFindings(getSession().Store, *opts.ExportFormat, os.Stdout); err != nil {
			lockPrintf("[!] --export failed: %s\n", err)
			exitScanner(1)
			return true
		}
		exitScanner(0)
		return true

	case opts.Stats != nil && *opts.Stats:
		if !getSession().Config.StoreEnabled() {
			lockPrintf("[!] storage.enabled is false; there are no findings to report\n")
			exitScanner(0)
			return true
		}
		if err := printStoreStats(getSession().Store); err != nil {
			lockPrintf("[!] --stats failed: %s\n", err)
			exitScanner(1)
			return true
		}
		exitScanner(0)
		return true
	}
	return false
}

// ──────────────────────────────────────────────────────────────
//  MAIN
// ──────────────────────────────────────────────────────────────

func main() {
	// Repair the console before the first byte is written: a previous run can
	// leave DISABLE_NEWLINE_AUTO_RETURN set, which makes "\n" advance without
	// returning to column 0 and turns the output into a staircase.
	repairNewlineMode()

	// Serialize every background writer before anything can log: the worker
	// pool and regex optimizer log through the standard logger from other
	// goroutines, and without this their lines split the styled output.
	routeEarlyOutput()

	// Print mode information BEFORE initializing session
	fmt.Println()
	modeConfig.PrintModeInfo()
	fmt.Println()

	// Dispatch on the mode selected by InitIntegratedMode/scanModeArgs. The mode
	// flags are stripped before flag.Parse, so this is the only place that
	// decides which UI runs. scanModeArgs maps both "terminal" and "scanner" onto
	// ModeDefault, so the default arm covers both.
	switch modeConfig.Mode {
	case ModeWeb:
		runWebMode()
	case ModeTUI:
		runTUIMode()
	default:
		// Default, --terminal and --scanner: the scanner runs in the foreground
		// and every finding is printed as it is found.
		runScannerCLI()
	}
}

// exitScanner terminates a one-shot scanner flow. In web and TUI mode the
// scanner runs alongside a UI that owns the process lifetime, so it must NOT
// kill the process: it just returns and the UI keeps serving results. In the
// plain terminal mode it exits with the scan's status code (1 on a hit, 0
// clean), which is what CI depends on.
//
// webhookDrainTimeout bounds the pre-exit webhook drain: long enough for a
// burst of findings to go out, short enough that a dead endpoint cannot hang
// a one-shot run forever.
const webhookDrainTimeout = 30 * time.Second

func exitScanner(code int) {
	if modeConfig != nil && (modeConfig.Mode == ModeWeb || modeConfig.Mode == ModeTUI) {
		return
	}
	// Stop the AI filter's worker pool before the process exits. It is a no-op
	// when the filter is disabled (nil).
	getAIFilter().Close()
	// Drain the webhook queue before the store closes and the process exits:
	// every queued alert still fires its on-confirm callback, so findings are
	// persisted once their alert goes out instead of being lost with the
	// process. Bounded so a dead endpoint cannot hang a one-shot run forever.
	if !core.DrainWebhookQueue(webhookDrainTimeout) {
		getSession().Log.Warn("Webhook queue did not empty in %s; exiting with alerts undelivered", webhookDrainTimeout)
	}
	getSession().CloseStore()
	os.Exit(code)
}

// runWebMode starts the web dashboard and runs the scanner in-process,
// feeding matches and logs into the embedded dark-mode UI.
func runWebMode() {
	// First byte of work: force the console visible. See restoreConsole.
	restoreConsole()
	initLogFormat()
	// The terminal stays open in --web mode: it shows the startup banner and
	// the scanner log while the dashboard runs in the browser.

	// Initialize the web hub before the scanner starts so no matches are lost.
	ensureWebHub()
	// Ring-buffer caps from config (performance.max_*), applied before the hub
	// and the stores can accumulate anything.
	configureWebLimits(getSession().Config.Performance)
	webLogCapture = true

	// The web dashboard renders ANSI colours client-side, so force the
	// terminal palette to emit escape codes even though stdout is not a TTY
	// (fatih/color disables them by default when output is redirected).
	color.NoColor = false

	// Capture scanner log output (Logger writes) into the web log buffer so
	// the dashboard's Logs tab shows everything. The locked printer keeps
	// those lines from interleaving with live match output.
	if getSession().Log != nil {
		getSession().Log.LogWriter = func(line string) {
			lockPrintf("%s", line)
		}
		getSession().Log.LogWriterIsTerminal = false
	}

	// Route token-validation results into the web "Tokens" view. Without this
	// hook the tab is permanently empty: /api/tokens always returns
	// {"tokens":[]} and no "token" feed event is ever published.
	if getSession().TokenValidator != nil {
		getSession().TokenValidator.OnTokenResult = func(token string, valid bool, provider string) {
			pushTokenResult(token, valid, provider)
		}
		// The richer verifier outcome arrives after the basic verdict. Record it
		// for later publishes of the same secret, enrich the Tokens tab row, and
		// upgrade any finding still in the hub with its verified severity.
		getSession().TokenValidator.OnOutcome = func(token string, provider string, o verifiers.Outcome) {
			rememberOutcome(token, provider, o)
			// classify with the finding's own priority so the Tokens tab and the
			// Matches tab cannot disagree.
			priority := updateHubMatchOutcome(token, o)
			pushTokenOutcome(token, provider, o, priority)
		}
	}

	addr := fmt.Sprintf("http://%s:%s", modeConfig.WebHost, modeConfig.WebPort)
	fmt.Printf("🌐 Starting web server on %s\n", addr)
	fmt.Println()

	// Wire the AI security review (settings store + review store) BEFORE the
	// scanner starts. aiSettingsStore is a package global that initAIFilter (run
	// by runScannerWork) reads; writing it after the goroutine is launched is a
	// data race under -race. It is optional: on failure the dashboard still runs
	// and the Review and Settings tabs report that it is unavailable.
	if err := initAIReview(getSession().Config); err != nil {
		fmt.Printf("⚠️  AI review unavailable: %v\n", err)
	}

	// Strict ordering: the full banner (plus startup summary) types out
	// first, then a beat for the server to bind, then the "opening" message,
	// then a readable pause so the operator sees it before the browser steals
	// focus - and only then the browser. The scanner starts last.
	go func() {
		printStartupBlock()
		// Let the web server bind before the browser hits it.
		time.Sleep(800 * time.Millisecond)
		fmt.Printf("🌐 Opening dashboard in your browser: %s\n", addr)
		// Readable pause between the message and the focus steal.
		time.Sleep(2 * time.Second)
		_ = openBrowser(addr)
		runScannerWork()
	}()

	// Start web server (blocking)
	err := modeConfig.SetupIntegratedUI()
	if !core.DrainWebhookQueue(webhookDrainTimeout) {
		getSession().Log.Warn("Webhook queue did not empty in %s; exiting with alerts undelivered", webhookDrainTimeout)
	}
	getSession().CloseStore()
	if err != nil {
		fmt.Printf("❌ Web server error: %v\n", err)
		os.Exit(1)
	}
}

// tuiCanStart reports whether the interactive UI can actually take over the
// terminal. TUIAvailable() covers stdout and the window size; the UI also needs
// a keyboard, so stdin must be a terminal too.
func tuiCanStart() bool {
	return TUIAvailable() && tuiIsTerminal(tuiStdin)
}

// runTUIMode runs the interactive terminal UI around the scanner. The UI owns
// the screen: the scanner runs in a goroutine and feeds the UI through
// publish()->AddTUIMatch plus the log/token hooks below, and nothing is written
// to stdout while the UI is up. If the terminal cannot support the UI - it is
// piped, TERM=dumb, too small, or stdin is not a TTY - this degrades to the
// plain terminal mode with a clear message and never emits escape sequences
// into a pipe or file.
func runTUIMode() {
	if !tuiCanStart() {
		fmt.Println("⚠️  --tui requested, but this terminal cannot run the interactive UI")
		fmt.Println("   (stdout or stdin is not a TTY, TERM=dumb, or the window is too small).")
		fmt.Println("   Falling back to the plain terminal live match feed.")
		fmt.Println()
		// Terminal mode is now the real mode: exitScanner must be allowed to
		// exit with the scan's status code instead of returning to a UI that
		// will never start.
		modeConfig.Mode = ModeDefault
		runScannerCLI()
		return
	}

	initLogFormat()

	// The UI draws its own colours, and the scanner's log lines are captured
	// into the Logs tab as plain text. Disable the terminal palette so those
	// lines do not carry ANSI escapes into the tab.
	color.NoColor = true

	// From here on the UI owns stdout: scanner output goes to the Logs tab, not
	// the screen, and live token results go to the Tokens tab.
	tuiScreenActive = true
	if getSession().Log != nil {
		getSession().Log.LogWriter = func(line string) {
			AddTUILog(line)
		}
	}
	if getSession().TokenValidator != nil {
		getSession().TokenValidator.OnTokenResult = func(token string, valid bool, provider string) {
			AddTUIToken(token, valid, provider)
		}
		// Mirror the web path: remember the outcome and refresh the severity
		// label on any finding for this secret that the TUI already holds.
		getSession().TokenValidator.OnOutcome = func(token string, provider string, o verifiers.Outcome) {
			rememberOutcome(token, provider, o)
			AddTUITokenOutcome(token, o)
		}
	}

	// Enable the AI review engine so the "r" key can ask the model about the
	// selected finding. It is optional: without a configured provider the key
	// reports that review is unavailable instead of failing the UI. The hook is
	// installed here, not in an init(), so tests never start a real review.
	if err := initAIReview(getSession().Config); err != nil {
		AddTUILog("AI review unavailable: " + err.Error())
	}
	tuiReviewRequest = tuiStartReview

	// The scanner runs in the background; every finding reaches the UI through
	// publish(). StartTUI then blocks until the operator quits.
	go runScanner()

	err := StartTUI()
	getSession().CloseStore()
	if err != nil {
		// Preflight (tuiCanStart) makes this practically unreachable. Do not
		// reset tuiScreenActive here: it is written once before the scanner
		// goroutine starts, and writing it again would race with any in-flight
		// lockPrintf call. The process is about to exit anyway, and the error
		// goes straight to stderr rather than through the locked printer.
		fmt.Fprintf(os.Stderr, "❌ Terminal UI error: %v\n", err)
	}
}

// printStartupBlock emits the preset-aware banner plus the startup summary
// as one atomic write, so background worker lines cannot land in the middle
// of the banner while the scanner spins up.
func printStartupBlock() {
	// Print animated banner FIRST (directly to console, not through lockWrite)
	// This happens before any other output
	core.PrintAnimatedBanner()

	// Now build the rest of the startup info
	var sb strings.Builder

	// Add author info based on format
	switch getLogFormat() {
	case LogFormatMinimal:
		blogPrintf(&sb, "%s %s\n", minSearch(""), minDim(core.Author))

	case LogFormatFancy:
		blogPrintln(&sb, "  "+color.HiCyanString(core.Author))

	case LogFormatUltraFancy:
		blogPrintf(&sb, "  %s  %s\n",
			ultraBadge(" ULTRA FANCY "),
			ultraAccent(core.Author))

	case LogFormatEvenMoreFancy:
		blogCosmicBox(&sb, cosmicBorder, cosmicTitle, "✨  SHHGIT - EVEN MORE FANCY MODE  ✨")
		blogPrintf(&sb, "  %s\n\n", cosmicAccent(core.Author))

	case LogFormatNeon:
		blogPrintf(&sb, "  %s %s\n",
			neonEntropyLabel(" NEON MODE "),
			neonValue(core.Author))
	}

	// ── Startup summary ───────────────────────────────────────
	switch getLogFormat() {
	case LogFormatMinimal:
		blogPrintf(&sb, "%s sigs:%s  threads:%s  tmp:%s\n\n",
			minDim("◆"),
			minSearch(fmt.Sprintf("%d", len(getSession().Signatures))),
			minSearch(fmt.Sprintf("%d", *getSession().Options.Threads)),
			minDim(*getSession().Options.TempDirectory))
	case LogFormatNeon:
		blogPrintf(&sb, "%s sigs %s  threads %s  tmp %s  style %s\n\n",
			neonDim("◆"),
			neonStar(fmt.Sprintf("%d", len(getSession().Signatures))),
			neonStar(fmt.Sprintf("%d", *getSession().Options.Threads)),
			neonDim(*getSession().Options.TempDirectory),
			neonSearchLabel(" "+strings.ToUpper(getLogFormat())+" "))
	default:
		blogPrintf(&sb, "[*] Loaded %s signatures  .  %s threads  .  tmp: %s  .  style: %s\n\n",
			color.HiCyanString("%d", len(getSession().Signatures)),
			color.HiCyanString("%d", *getSession().Options.Threads),
			color.HiBlueString(*getSession().Options.TempDirectory),
			color.HiGreenString(strings.ToUpper(getLogFormat())))
	}
	lockWrite(sb.String())
}

// oneShotCommand reports whether this run is a one-shot store/token command
// that never publishes a finding, so the AI filter, notification dispatcher and
// outcome recorder must not be started for it.
func oneShotCommand() bool {
	o := getSession().Options
	if o == nil {
		return false
	}
	if boolOpt(o.TestTokens) || boolOpt(o.Export) || boolOpt(o.Stats) {
		return true
	}
	if o.ScanKeys != nil && strings.TrimSpace(*o.ScanKeys) != "" {
		return true
	}
	if o.ExportEncrypted != nil && *o.ExportEncrypted != "" {
		return true
	}
	return o.Decrypt != nil && *o.Decrypt != ""
}

// initPublishInfrastructure initialises everything publish() reads before any
// source goroutine can call it: the AI false-positive filter, the notification
// dispatcher, the daily digest and the outcome recorder. It must run before
// startCodeSearch/startGitLab, because both spawn goroutines that publish and
// validate tokens; installing these afterwards raced those goroutines.
// It is a no-op for one-shot commands, which never publish.
func initPublishInfrastructure() {
	if oneShotCommand() {
		return
	}
	// Start each scan with a clean metrics snapshot so the dashboard and TUI
	// report this run's numbers rather than a process-lifetime accumulation.
	if core.MetricsCollectionEnabled() {
		core.DefaultMetrics.Reset()
	}
	setAIFilter(initAIFilter(getSession().Config))
	initNotifications()
	startDailyDigest()
	installOutcomeRecorder()
}

// runScanner executes the scanner with UI reporting: startup banner first,
// then the scan work. --web mode needs the banner on its own schedule (it
// must finish before the browser opens), so the work half lives in
// runScannerWork and --web drives the two halves in order.
func runScanner() {
	printStartupBlock()
	runScannerWork()
}

// runScannerWork is everything runScanner does after the startup banner.
func runScannerWork() {

	// Initialise the publish sinks before starting any source that can publish.
	initPublishInfrastructure()

	// The opt-in GitHub code-search source runs alongside the events/gist
	// workers; it is a no-op unless search is enabled and this is a remote scan.
	startCodeSearch()

	// The opt-in GitLab public-snippet source runs alongside the GitHub workers
	// for the same reason: it must start under --web/--tui too.
	startGitLab()

	// The remaining remote discovery sources share the same guard and lifecycle.
	startPastebin()
	startSearchCode()
	startSourcegraph()
	startGitHubCommits()
	startGitHubIssues()
	startGitLabBlobs()
	startGrepApp()
	startPsbdmp()
	startBitbucket()

	// Execute scanner logic
	executeScanner()
}

// runScannerCLI runs scanner with full CLI output
func runScannerCLI() {
	// A classic Windows console does not interpret ANSI or OSC 8 escape sequences
	// until virtual-terminal processing is switched on. Enable it before the first
	// styled line and record the result, so colours and hyperlinks either render
	// or degrade to plain text - never to raw escape sequences.
	terminalVTSupported = enableVT()

	// Funnel core.Logger through the shared console mutex before the session
	// (and its background workers) can emit anything.
	routeCoreLogger()

	initLogFormat()
	printStartupBlock()

	// Terminal mode must start the code-search source too: it bypasses
	// runScanner, so without this --search would only work under --web/--tui.
	// Initialise the publish sinks before starting any source that can publish.
	initPublishInfrastructure()

	startCodeSearch()

	// Same for the GitLab source: terminal mode bypasses runScanner.
	startGitLab()

	// Same for the remaining remote discovery sources.
	startPastebin()
	startSearchCode()
	startSourcegraph()
	startGitHubCommits()
	startGitHubIssues()
	startGitLabBlobs()
	startGrepApp()
	startPsbdmp()
	startBitbucket()

	executeScanner()
}

// printScanPlan reports what a scan of dir would do, without reading a file.
func printScanPlan(dir string) {
	plan, err := core.PlanScan(dir)
	if err != nil {
		lockPrintf("[DRY RUN] could not walk %s: %s\n", dir, err)
		return
	}
	lockPrintf("[DRY RUN] %s\n", dir)
	lockPrintf("  files seen:      %d\n", plan.Total)
	lockPrintf("  would scan:      %d\n", plan.Scannable)
	lockPrintf("  skipped (size):  %d\n", plan.SkippedSize)
	lockPrintf("  skipped (ext):   %d\n", plan.SkippedExt)
	lockPrintf("  skipped (path):  %d\n", plan.SkippedPath)
}

// executeScanner runs the scanner core logic
func executeScanner() {
	// One-shot store commands (--export/--export-encrypted/--decrypt) run
	// before any network or pruning work so they never need a GitHub token.
	if handleStoreCLI() {
		return
	}

	// ── Auto-prune invalid GitHub tokens from config (every startup) ─────
	// Also install the runtime hook so a token that starts returning 401
	// while the scanner runs is removed from config.yaml immediately.
	installRuntimeTokenPruner()
	pruneInvalidGitHubTokens()

	// Point core's rate-limit counter at the dashboard's. core can only bump its
	// own ProgressManager counter, which nothing reads; the "Rate limited" stat
	// and the TUI's "limited N" both come from activity, so without this they
	// stayed at zero for the whole run.
	if p := getSession().Progress; p != nil {
		p.OnRateLimited = func() { activity.IncRateLimited() }
	}

	// ── Dispatch ──────────────────────────────────────────────
	if *getSession().Options.TestTokens {
		validator := core.NewTokenValidator(getSession().Log)
		var tokensToTest []string

		// Collect tokens from all config sections and detect AI-related ones
		allTokens := make(map[string]string) // token -> detected provider

		// 1. Explicitly listed AI tokens
		for _, token := range getSession().Config.AITokens {
			allTokens[token] = validator.DetectProvider(token)
		}

		// 2. Check github_access_tokens for any AI tokens via provider detection
		for _, token := range getSession().GitHubTokens() {
			provider := validator.DetectProvider(token)
			if isAITokenSignature(provider) {
				allTokens[token] = provider
			}
		}

		// Build deduplicated list grouped by provider
		providerCount := make(map[string]int)
		for token, provider := range allTokens {
			tokensToTest = append(tokensToTest, token)
			providerCount[provider]++
		}

		if len(tokensToTest) == 0 {
			getSession().Log.Warn("No AI tokens found in config")
			lockPrintf("[*] No AI tokens found in config. Add tokens to config.yaml under 'ai_tokens' or 'github_access_tokens'.\n")
		} else {
			lockPrintf("[*] Testing %d AI tokens from config...\n\n", len(tokensToTest))
			for provider, count := range providerCount {
				lockPrintf("    %s: %d token(s)\n", provider, count)
			}
			lockPrintln()
			validator.TestTokens(tokensToTest)
		}
		exitScanner(0)
		// In web/TUI mode exitScanner returns instead of exiting; stop here so a
		// one-shot --test-tokens run does not fall through into the scanner and
		// start demanding a GitHub token it was never meant to use.
		return
	}

	if len(strOpt(getSession().Options.ScanKeys)) > 0 {
		lockPrintf("[*] Scanning directory for API keys: %s\n", color.HiYellowString(*getSession().Options.ScanKeys))
		keyValidator := core.NewKeyValidator(getSession().Log)

		// Open log file for keys
		logFilename := fmt.Sprintf("found_keys_%d.txt", time.Now().Unix())
		if err := keyValidator.OpenLogFile(logFilename); err != nil {
			lockPrintf("[!] Error opening log file: %s\n", err)
		} else {
			lockPrintf("[*] Logging keys to: %s\n", logFilename)
			defer keyValidator.CloseLogFile()
		}

		validatedKeys := keyValidator.ScanDirectoryForKeys(*getSession().Options.ScanKeys, getSession())

		validCount := 0
		invalidCount := 0

		lockPrintf("\n[*] Validation Results:\n")
		lockPrintf("%-20s %-50s %s\n", "Provider", "Key", "Valid")
		lockPrintln(strings.Repeat("-", 90))

		for _, vk := range validatedKeys {
			status := "✓ VALID"
			if !vk.Valid {
				status = "✗ INVALID"
				invalidCount++
			} else {
				validCount++
			}

			keyDisplay := vk.Key
			if len(keyDisplay) > 40 {
				keyDisplay = keyDisplay[:20] + "..." + keyDisplay[len(keyDisplay)-17:]
			}

			lockPrintf("%-20s %-50s %s\n", vk.Provider, keyDisplay, status)
		}

		lockPrintf("\n[*] Summary: %d valid, %d invalid out of %d keys found\n", validCount, invalidCount, len(validatedKeys))
		lockPrintf("[*] All keys logged to: %s\n", logFilename)
		exitScanner(0)
		// Same as --test-tokens: exitScanner only returns in web/TUI mode, so
		// return explicitly rather than falling through to the GitHub workers.
		return
	}

	// The AI filter, notification dispatcher, daily digest and outcome recorder
	// are installed by initPublishInfrastructure before the sources start (see
	// runScanner / runScannerCLI), so they are never observed mid-setup.

	if len(strOpt(getSession().Options.Local)) > 0 {
		if boolOpt(getSession().Options.DryRun) {
			printScanPlan(*getSession().Options.Local)
			exitScanner(0)
			return
		}
		lockPrintf("[*] Scanning local: %s\n", color.HiYellowString(*getSession().Options.Local))
		rc := 0
		if checkSignatures(*getSession().Options.Local, *getSession().Options.Local, "", -1, core.LOCAL_SOURCE) {
			rc = 1
		} else {
			lockPrintf("[*] No secrets found in %s\n", color.HiBlueString(*getSession().Options.Local))
		}
		getSession().MatchLogger.Close()
		exitScanner(rc)

		// --local scans the given directory and nothing else, so there is no
		// GitHub work to start. Falling through to the workers below would make
		// them demand a GitHub token that --local explicitly does not require,
		// which killed the process - and with it the web dashboard - the moment
		// a local scan finished. In console mode exitScanner has already
		// exited; in web mode this keeps the dashboard serving the results.
		return
	}

	if strOpt(getSession().Options.SearchQuery) != "" {
		lockPrintf("[*] Search query %s — only returning matching results.\n",
			color.HiYellowString(*getSession().Options.SearchQuery))
	}

	// Start background workers
	go core.GetRepositories(getSession())
	go ProcessRepositories()
	go ProcessComments()

	if *getSession().Options.ProcessGists {
		go core.GetGists(getSession())
		go ProcessGists()
	}

	// Block forever - workers run as goroutines
	select {}
}
