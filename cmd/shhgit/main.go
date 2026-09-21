package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/fatih/color"
	"github.com/trebor048/shhgot/core"
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

// isSignatureExcludedInMode checks if a signature should be excluded in the current mode

// Shared across all presets
var (
	styleBold      = color.New(color.Bold).SprintFunc()
	styleUnderline = color.New(color.Underline).SprintFunc()
	styleItalic    = color.New(color.Italic).SprintFunc()
)

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
	fmt.Print(s)
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
	minRepo    = color.New(color.FgHiMagenta, color.Bold).SprintFunc()
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
	fancyRepo    = color.New(color.FgHiMagenta, color.Bold, color.Underline).SprintFunc()
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
	ultraRepo    = color.New(color.FgHiMagenta, color.BgHiBlack, color.Bold, color.Underline).SprintFunc()
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
	cosmicRepo    = color.New(color.FgHiMagenta, color.Bold, color.BlinkSlow).SprintFunc()
	cosmicLink    = color.New(color.FgHiCyan, color.Bold, color.Underline).SprintFunc()
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
	neonRepo         = color.New(color.FgBlack, color.BgHiMagenta, color.Bold, color.Underline).SprintFunc()
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

	// Check if signature contains any AI provider
	for _, provider := range aiProviders {
		if strings.Contains(sigLower, provider) {
			return true
		}
	}

	return false
}

// isCryptoSignature checks if a signature is crypto-related
func isCryptoSignature(sig string) bool {
	sigLower := strings.ToLower(sig)

	cryptoKeywords := []string{
		"ethereum", "bitcoin", "wallet", "private key", "seed", "mnemonic",
		"recovery", "bip39", "solana", "cardano", "ripple", "polkadot",
		"monero", "zcash", "litecoin", "dogecoin", "tron", "arbitrum",
		"optimism", "polygon", "avalanche", "fantom", "harmony", "celo",
		"cosmos", "osmosis", "juno", "secret", "band", "near", "algorand",
		"aptos", "sui", "starknet", "flow", "hedera", "icp", "filecoin",
		"arweave", "thorchain", "stacks", "kaspa", "ergo", "nervos",
		"zilliqa", "elrond", "vechain", "theta", "icon", "tezos", "neo",
		"ontology", "waves", "eos", "telos", "crypto", "blockchain",
		"web3", "nft", "defi", "token", "address", "keypair",
	}

	for _, keyword := range cryptoKeywords {
		if strings.Contains(sigLower, keyword) {
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

// getEnhancedLinkInfo returns enhanced repo info with user/repo display and file links.
// Thread-safe as it only works with local variables and calls thread-safe functions.
func getEnhancedLinkInfo(url, filePath, branch string, line int) (userRepo string, repoLink string, fileLink string) {
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

// cosmicBox renders a centered 3-line bordered box (Even More Fancy / Cosmic preset).
func cosmicBox(borderColor func(...interface{}) string, titleColor func(...interface{}) string, title string) {
	var sb strings.Builder
	blogCosmicBox(&sb, borderColor, titleColor, title)
	lockWrite(sb.String())
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
	// Cap from config (default 20) to prevent overwhelming the machine.
	if cap := getSession().Config.Performance.Int(getSession().Config.Performance.MaxRepositoryThreads, 20); threadNum > cap {
		threadNum = cap
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
	if repo.GetPermissions()["pull"] &&
		uint(repo.GetStargazersCount()) >= *getSession().Options.MinimumStars &&
		uint(repo.GetSize()) < *getSession().Options.MaximumRepositorySize {
		processRepositoryOrGist(repo.GetCloneURL(), repository.Ref, repo.GetStargazersCount(), core.GITHUB_SOURCE, workerID)
	}
}

func ProcessGists() {
	threadNum := *getSession().Options.Threads
	// Cap from config (default 5) to avoid hammering the gist API.
	if cap := getSession().Config.Performance.Int(getSession().Config.Performance.MaxGistThreads, 5); threadNum > cap {
		threadNum = cap
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
	if cap := getSession().Config.Performance.Int(getSession().Config.Performance.MaxCommentThreads, 3); threadNum > cap {
		threadNum = cap
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
	dir := core.GetTempDir(core.GetHash(comment.Body))
	// Checked: when this write failed, the scan below ran against a directory with
	// no comment in it and the finding was silently lost.
	if err := ioutil.WriteFile(filepath.Join(dir, "comment.ignore"), []byte(comment.Body), 0644); err != nil {
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

	activity.StartFetch(url)

	var gitProgress *core.GitProgressWriter

	_, err := core.CloneRepository(getSession(), url, ref, dir, gitProgress)
	if err != nil {
		getSession().Log.Debug("[%s] Cloning failed: %s", url, err.Error())
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
	// Count files first and skip if too many (prevents slowdowns on huge repos)
	maxFileCount := getSession().Config.Performance.Int(getSession().Config.Performance.MaxFileCount, 10000)
	scanner := core.NewDirectoryScanner(getSession().Log)
	fileCount := scanner.GetFileCount(dir)

	if fileCount > maxFileCount {
		lockPrintf("[SKIP] Repository has %d files (max: %d): %s\n", fileCount, maxFileCount, url)
		return false
	}

	// CRITICAL PERFORMANCE FIX: Pre-compile search query regex ONCE outside the file loop
	var queryRegex *regexp.Regexp
	if q := *getSession().Options.SearchQuery; q != "" {
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

	files := core.GetMatchingFiles(dir)
	// len(files) is the exact number of files this scan will process, so it is
	// the right denominator for the progress percentage. The GetFileCount above
	// is only a size guard and counts every file, including the blacklisted
	// ones this slice drops.
	core.GlobalScanProgress.SetTotalFiles(len(files))
	for _, file := range files {
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

		// Attach the repository to the file so verifier webhooks can name it;
		// applyVerifier used to hardcode "unknown" because MatchFile carried
		// no repo context.
		file.Repo = url

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
		} else {
			for _, signature := range getSession().Signatures {
				// Check if signature should be excluded in current mode
				currentMode := getLogFormat()
				if isSignatureExcludedInMode(signature, currentMode) {
					continue
				}

				if matched, part := signature.Match(file); matched {
					if part == core.PartContents {
						if matches = signature.GetContentsMatches(file); len(matches) > 0 {
							// Test fixtures (e.g. backend/tests/*) overwhelmingly
							// contain fake placeholder passwords — skip them.
							if isPasswordFamilySignature(signature.Name()) && isTestFixturePath(relativeFileName) {
								continue
							}
							// AI-agent skill/instruction files are documentation:
							// any secret shown inside them is an example, not a finding.
							if isDocSkillPath(relativeFileName) {
								continue
							}
							// A PEM header with no closing footer is a doc fragment,
							// not a key (e.g. "-----BEGIN PRIVATE KEY-----" in a tutorial).
							if strings.HasPrefix(matches[0], "-----BEGIN") && !hasCompletePemBlock(string(file.Contents)) {
								continue
							}
							count := len(matches)
							m := strings.Join(matches, ", ")

							// Capture the full file and the first raw secret so
							// the web dashboard can render "view file" with
							// syntax highlighting and the secret highlighted.
							// Cloned repos are deleted after scanning, so the
							// content must be captured here.
							fileContent := string(file.Contents)
							secret := matches[0]

							publish(&MatchEvent{Source: source, Url: url, Matches: matches, Signature: signature.Name(), File: relativeFileName, Stars: stars, Priority: signature.GetPriority(), Color: signature.GetColor(), FileContent: fileContent, Secret: secret, SecretLine: secretLineNum(file.Contents, secret)})
							matchedAny = true
							fileMatched = true
							logSecret(count, url, signature.Name(), relativeFileName, m, ref, secretLineNum(file.Contents, secret), stars)
							getSession().WriteToCsv([]string{url, signature.Name(), relativeFileName, m})
							getSession().LogMatch(signature.Name(), url, relativeFileName, matches)

							// Add GitHub tokens to session for dynamic fetching
							if signature.IsTokenType() {
								for _, token := range matches {
									getSession().AddGitHubToken(token)
								}
							}

							// Test tokens if this is an AI token signature
							sigName := strings.ToLower(signature.Name())
							if isAITokenSignature(sigName) {
								getSession().TokenValidator.TestMatchedTokens(matches, signature.GetColor())
							}
						}
					} else {
						if *getSession().Options.PathChecks {
							publish(&MatchEvent{Source: source, Url: url, Matches: matches, Signature: signature.Name(), File: relativeFileName, Stars: stars, Priority: signature.GetPriority(), Color: signature.GetColor(), FileContent: string(file.Contents)})
							matchedAny = true
							fileMatched = true
							logFile(url, signature.Name(), relativeFileName, ref, stars)
							getSession().WriteToCsv([]string{url, signature.Name(), relativeFileName, ""})
							getSession().LogMatch(signature.Name(), url, relativeFileName, []string{relativeFileName})
						}
						if *getSession().Options.EntropyThreshold > 0 && file.CanCheckEntropy() {
							scanner := bufio.NewScanner(bytes.NewReader(file.Contents))
							lineNo := 0
							for scanner.Scan() {
								lineNo++
								line := scanner.Text()
								if len(line) > 6 && len(line) < 100 {
									entropy := core.GetEntropy(line)
									if entropy >= *getSession().Options.EntropyThreshold {
										blacklistedMatch := false
										for _, blacklistedString := range getSession().Config.BlacklistedStrings {
											if strings.Contains(strings.ToLower(line), strings.ToLower(blacklistedString)) {
												blacklistedMatch = true
											}
										}
										if !blacklistedMatch {
											publish(&MatchEvent{Source: source, Url: url, Matches: []string{line}, Signature: entropySignatureName, File: relativeFileName, Stars: stars, Priority: 0, Color: "", FileContent: string(file.Contents), Secret: line, SecretLine: lineNo})
											matchedAny = true
											fileMatched = true
											logEntropy(url, relativeFileName, line, ref, lineNo, stars)
											getSession().WriteToCsv([]string{url, entropySignatureName, relativeFileName, line})
											getSession().LogMatch(entropySignatureName, url, relativeFileName, []string{line})
										}
									}
								}
							}
						}
					}
				}
			}
		}

		if !fileMatched && len(*getSession().Options.Local) <= 0 {
			os.Remove(file.Path)
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
var liveHTTPClient = &http.Client{Timeout: 10 * time.Second}

// postLive delivers one match to the configured --live endpoint. It runs on its
// own goroutine and drains a bounded amount of the response so the connection
// can be reused; delivery failures are intentionally silent, exactly as the
// inline send was.
func postLive(url string, body []byte) {
	resp, err := liveHTTPClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
}

func publish(event *MatchEvent) {
	// Every finding funnels through here, so this is the one place the scan
	// progress counter needs to be bumped.
	core.GlobalScanProgress.MatchFound()

	// Feed the in-process web dashboard (when running with --web).
	if webHub != nil {
		m := &Match{
			ID:        newMatchID(),
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

	// Feed the TUI (when running with --tui).
	if tuiState != nil {
		id := newMatchID()
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
		})
		// Keep the captured file body under the same id the UI sees, so the "v"
		// key can show the file around the finding exactly like the dashboard's
		// viewer. Only while the TUI owns the screen: in web mode the branch
		// above already stored it under the dashboard's own id.
		if tuiScreenActive && event.FileContent != "" {
			storeMatchFile(id, event.Url, event.File, event.FileContent, event.Secret, event.SecretLine)
		}
	}

	if len(*getSession().Options.Live) > 0 {
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
		}
		data, _ := json.Marshal(match)
		// Off the scanner's goroutine and through a bounded client. This was an
		// inline http.Post on http.DefaultClient, which has no timeout and whose
		// response body was never closed: a hung --live endpoint stalled every
		// worker and leaked the connection.
		go postLive(*getSession().Options.Live, data)
	}

	// Send EVERY match to the main webhook.
	if len(getSession().Config.Webhook) > 0 {
		go core.SendMatchWebhook(
			getSession().Config.Webhook,
			getSession().Config.WebhookPayload,
			event.Url,
			event.Signature,
			event.File,
			event.Matches,
			event.Color,
			event.Priority,
			event.FileContent,
		)
	}

	// Send AI tokens (excluding Google) to their dedicated webhook
	if len(getSession().Config.WebhookAITokens) > 0 && isAITokenSignature(event.Signature) {
		go core.SendMatchWebhook(
			getSession().Config.WebhookAITokens,
			getSession().Config.WebhookPayload,
			event.Url,
			event.Signature,
			event.File,
			event.Matches,
			event.Color,
			event.Priority,
			event.FileContent,
		)
	}

	// Send crypto-related matches to their dedicated webhook
	if len(getSession().Config.WebhookCrypto) > 0 && isCryptoSignature(event.Signature) {
		go core.SendMatchWebhook(
			getSession().Config.WebhookCrypto,
			getSession().Config.WebhookPayload,
			event.Url,
			event.Signature,
			event.File,
			event.Matches,
			event.Color,
			event.Priority,
			event.FileContent,
		)
	}
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
	// decides which UI runs. ModeScanner is an alias of ModeDefault and is never
	// selected (scanModeArgs maps "scanner" onto ModeDefault), so the default arm
	// covers both.
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
func exitScanner(code int) {
	if modeConfig != nil && (modeConfig.Mode == ModeWeb || modeConfig.Mode == ModeTUI) {
		return
	}
	os.Exit(code)
}

// runWebMode starts the web dashboard and runs the scanner in-process,
// feeding matches and logs into the embedded dark-mode UI.
func runWebMode() {
	initLogFormat()
	// In --web mode the dashboard is the interface: hide the console window
	// so only the browser UI is visible (scanner still runs in-process).
	hideConsole()

	// Initialize the web hub before the scanner starts so no matches are lost.
	ensureWebHub()
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
	}

	addr := fmt.Sprintf("http://%s:%s", modeConfig.WebHost, modeConfig.WebPort)
	fmt.Printf("🌐 Starting web server on %s\n", addr)
	fmt.Println("   Open browser to view dashboard")
	fmt.Println()

	// Open the dashboard in the default browser shortly after the server binds.
	go func() {
		time.Sleep(800 * time.Millisecond)
		openBrowser(addr)
	}()

	// Start scanner in background AND serve the web UI concurrently.
	go runScanner()

	// Wire the AI security review (settings store + review store) before the
	// web server starts. It is optional: on failure the dashboard still runs and
	// the Review and Settings tabs report that it is unavailable.
	if err := initAIReview(getSession().Config); err != nil {
		fmt.Printf("⚠️  AI review unavailable: %v\n", err)
	}

	// Start web server (blocking)
	if err := modeConfig.SetupIntegratedUI(); err != nil {
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

	if err := StartTUI(); err != nil {
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
	var sb strings.Builder
	// ── Banner (preset-aware) ──────────────────────────────────
	switch getLogFormat() {
	case LogFormatMinimal:
		blogPrintf(&sb, "%s %s\n", minSearch("shhgit"), minDim(core.Author))

	case LogFormatFancy:
		blogPrintln(&sb, color.HiBlueString(core.Banner))
		blogPrintln(&sb, "  "+color.HiCyanString(core.Author))

	case LogFormatUltraFancy:
		blogPrintln(&sb, color.HiBlueString(core.Banner))
		blogPrintf(&sb, "  %s  %s\n",
			ultraBadge(" ULTRA FANCY "),
			ultraAccent(core.Author))

	case LogFormatEvenMoreFancy:
		blogCosmicBox(&sb, cosmicBorder, cosmicTitle, "✨  SHHGIT - EVEN MORE FANCY MODE  ✨")
		blogPrintf(&sb, "  %s\n\n", cosmicAccent(core.Author))

	case LogFormatNeon:
		blogPrintln(&sb, color.HiMagentaString(core.Banner))
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

// runScanner executes the scanner with UI reporting
func runScanner() {
	// Original banner output suppressed if UI is active
	if modeConfig.Mode == ModeScanner {
		printStartupBlock()
	}

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

	executeScanner()
}

// executeScanner runs the scanner core logic
func executeScanner() {
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
		for _, token := range getSession().Config.GitHubAccessTokens {
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

	if len(*getSession().Options.ScanKeys) > 0 {
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

	if len(*getSession().Options.Local) > 0 {
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

	if *getSession().Options.SearchQuery != "" {
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
