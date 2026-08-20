package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/eth0izzle/shhgit/core"
	"github.com/fatih/color"
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

var session = core.GetSession()
var formatOrder = []string{LogFormatMinimal, LogFormatFancy, LogFormatUltraFancy, LogFormatEvenMoreFancy, LogFormatNeon}
var currentLogFormat = LogFormatFancy

func getLogFormat() string { return currentLogFormat }

func setLogFormat(f string) {
	for _, preset := range formatOrder {
		if f == preset {
			currentLogFormat = f
			return
		}
	}
	currentLogFormat = LogFormatFancy
}

// --------------------------------------------------------------
//  HELPERS
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

func lockPrintf(format string, args ...interface{}) {
	outputMu.Lock()
	s := fmt.Sprintf(format, args...)
	fmt.Print(s)
	if webLogCapture {
		appendLogLine(s)
	}
	outputMu.Unlock()
}

func lockPrintln(args ...interface{}) {
	outputMu.Lock()
	s := fmt.Sprintln(args...)
	fmt.Print(s)
	if webLogCapture {
		appendLogLine(s)
	}
	outputMu.Unlock()
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

// colorizeGitHubURL colors the username/repo portion of a GitHub URL magenta.
func colorizeGitHubURL(url string) string {
	re := regexp.MustCompile(`https://github\.com/([^/]+)/([^/]+)`)
	matches := re.FindStringSubmatch(url)
	if len(matches) == 3 {
		return "https://github.com/" +
			color.HiMagentaString(matches[1]) + "/" +
			color.HiMagentaString(matches[2])
	}
	return url
}

// extractRepoName extracts the username/repo from a GitHub URL.
// Thread-safe as it only works with local variables.
func extractRepoName(url string) string {
	re := regexp.MustCompile(`https://github\.com/([^/]+)/([^/]+?)(?:\.git)?(?:/|$)`)
	matches := re.FindStringSubmatch(url)
	if len(matches) >= 3 {
		return matches[1] + "/" + matches[2]
	}
	return ""
}

// createClickableLink creates a terminal hyperlink with ANSI escape sequences.
// Thread-safe as it only works with local variables.
func createClickableLink(url, text string) string {
	// ANSI escape sequence for hyperlink: OSC 8 ; params ; URL ST text OSC 8 ; ; ST
	// OSC = \033] and ST = \033\\ (or \007)
	return fmt.Sprintf("\033]8;;%s\033\\%s\033]8;;\033\\", url, text)
}

// extractRepoInfo extracts username, repo, and creates GitHub URLs.
// Thread-safe as it only works with local variables.
func extractRepoInfo(url string) (username, repo, repoUrl string) {
	re := regexp.MustCompile(`https://github\.com/([^/]+)/([^/]+?)(?:\.git)?(?:/|$)`)
	matches := re.FindStringSubmatch(url)
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

// createFileLink creates a clickable link to a specific file in the repository.
// Thread-safe as it only works with local variables.
func createFileLink(repoUrl, filePath, branch string) string {
	if branch == "" {
		branch = "main" // Default to main branch
	}
	// Handle different branch reference formats
	if strings.Contains(branch, "refs/heads/") {
		branch = strings.Replace(branch, "refs/heads/", "", 1)
	}
	
	// Clean up the file path - remove leading slashes and convert backslashes to forward slashes
	filePath = strings.TrimPrefix(filePath, "/")
	filePath = strings.TrimPrefix(filePath, "\\")
	filePath = strings.ReplaceAll(filePath, "\\", "/")
	
	fileUrl := fmt.Sprintf("%s/blob/%s/%s", repoUrl, branch, filePath)
	maskedText := fmt.Sprintf("📄 %s", filepath.Base(filePath))
	return createClickableLink(fileUrl, maskedText)
}

// getEnhancedLinkInfo returns enhanced repo info with user/repo display and file links.
// Thread-safe as it only works with local variables and calls thread-safe functions.
func getEnhancedLinkInfo(url, filePath, branch string) (userRepo string, repoLink string, fileLink string) {
	username, repo, repoUrl := extractRepoInfo(url)
	if username == "" || repo == "" {
		// Not a GitHub URL, return as-is
		return url, url, url
	}

	// Create prominent user/repo display
	userRepo = fmt.Sprintf("%s/%s", color.HiCyanString(username), color.HiMagentaString(repo))

	// Create clickable repo link
	repoLink = createClickableLink(repoUrl, userRepo)

	// Create file-specific link if filePath provided
	if filePath != "" {
		fileLink = createFileLink(repoUrl, filePath, branch)
	} else {
		fileLink = repoLink
	}

	return userRepo, repoLink, fileLink
}

// cosmicBox renders a centered 3-line bordered box (Even More Fancy / Cosmic preset).
func cosmicBox(borderColor func(...interface{}) string, titleColor func(...interface{}) string, title string) {
	const width = 62
	bar := strings.Repeat("═", width)
	pad := (width - len([]rune(title))) / 2
	padded := strings.Repeat(" ", pad) + title + strings.Repeat(" ", width-pad-len([]rune(title)))
	lockPrintln(borderColor("╔" + bar + "╗"))
	lockPrintln(borderColor("║") + titleColor(padded) + borderColor("║"))
	lockPrintln(borderColor("╚" + bar + "╝"))
}

func switchLogFormat() {}

func initLogFormat() {
	f := strings.ToLower(strings.TrimSpace(session.Config.LogFormat))
	setLogFormat(f)

	// Announce the active preset with its own style
	switch getLogFormat() {
	case LogFormatMinimal:
		lockPrintf("%s log preset -> %s\n\n", minInfo("◆"), minSearch(strings.ToUpper(getLogFormat())))
	case LogFormatFancy:
		lockPrintf("%s Log styling -> %s\n\n", fancyAccent("◆"), fancySearch(strings.ToUpper(getLogFormat())))
	case LogFormatUltraFancy:
		lockPrintf("%s %s\n\n", ultraBadge(" PRESET "), ultraAccent(strings.ToUpper(getLogFormat())))
	case LogFormatEvenMoreFancy:
		cosmicBox(cosmicBorder, cosmicTitle, "🎨  LOG PRESET: "+strings.ToUpper(getLogFormat())+"  🎨")
		lockPrintln()
	case LogFormatNeon:
		lockPrintf("%s %s\n\n",
			neonSearchLabel(" PRESET "),
			neonValue(strings.ToUpper(getLogFormat())))
	}
}

// startHotkeyListener is no longer needed — bubbletea handles keyboard input via the TUI.
func startHotkeyListener() {}

// ──────────────────────────────────────────────────────────────
//  LOG FUNCTIONS  (5 presets each)
// ──────────────────────────────────────────────────────────────

// logSearch logs a search-query match event.
func logSearch(count int, url, file, matches, branch string) {
	plural := core.Pluralize(count, "match", "matches")
	userRepo, _, fileLink := getEnhancedLinkInfo(url, file, branch)

	switch 	getLogFormat() {

	// ── 1. MINIMAL ────────────────────────────────────────────
	case LogFormatMinimal:
		lockPrintf("%s %s  %s %s  %s\n",
			minSearch("🔍"),
			minDim(fmt.Sprintf("%d %s", count, plural)),
			minDim("→"),
			minInfo(file),
			userRepo)
		lockPrintf("       %s  %s\n", minDim(matches), fileLink)

	// ── 2. FANCY ──────────────────────────────────────────────
	case LogFormatFancy:
		lockPrintf("%s %d %s in %s  %s\n",
			fancySearch("🔍 [SEARCH]"),
			count, plural,
			fancyFile(file),
			userRepo)
		lockPrintf("  %s %s  %s\n", fancyArrow("→"), matches, fileLink)

	// ── 3. ULTRA FANCY ────────────────────────────────────────
	case LogFormatUltraFancy:
		lockPrintf("%s  %s  %s %s  %s\n",
			ultraBadge(" 🔍 SEARCH "),
			ultraAccent(fmt.Sprintf("%d %s", count, plural)),
			ultraSearch("in"),
			ultraFile(file),
			userRepo)
		lockPrintf("  %s %s  %s\n", ultraAccent("✦ →"), matches, fileLink)

	// ── 4. EVEN MORE FANCY (Cosmic) ───────────────────────────
	case LogFormatEvenMoreFancy:
		cosmicBox(cosmicSearch, cosmicTitle, "🌌  🔍 SEARCH MATCH  🌌")
		lockPrintf("  %s  %d %s in %s  %s\n",
			cosmicSearch("◈"),
			count, plural,
			cosmicFile(file),
			userRepo)
		lockPrintf("  %s %s  %s\n", cosmicAccent("➜"), matches, fileLink)

	// ── 5. NEON ───────────────────────────────────────────────
	default:
		lockPrintf("%s %s %s  %s\n",
			neonSearchLabel(" 🔍 SEARCH "),
			neonValue(fmt.Sprintf("%d %s", count, plural)),
			neonDim("in "+file),
			userRepo)
		lockPrintf("  %s %s  %s\n", neonMatch("⟶"), matches, fileLink)
	}
}

// logSecret logs a signature/secret match event.
func logSecret(count int, url, sig, file, matches, branch string, stars int) {
	plural := core.Pluralize(count, "match", "matches")
	starStr := ""
	if stars > 0 {
		starStr = fmt.Sprintf("  ★ %d", stars)
	}

	userRepo, _, fileLink := getEnhancedLinkInfo(url, file, branch)

	// Hide preview for PEM keys and other large private keys
	shouldHidePreview := strings.Contains(matches, "-----BEGIN") ||
		strings.Contains(matches, "-----END") ||
		len(matches) > 200 // Hide very long keys
	
	displayMatches := matches
	if shouldHidePreview {
		displayMatches = "[REDACTED - Private key content hidden]"
	}

	switch 	getLogFormat() {

	// ── 1. MINIMAL ────────────────────────────────────────────
	case LogFormatMinimal:
		lockPrintf("%s %s  %s %s%s  %s\n",
			minSecret("🚨"),
			minDim(fmt.Sprintf("%d %s", count, plural)),
			minDim("→"),
			minInfo(file),
			minDim(starStr),
			userRepo)
		lockPrintf("       %s %s  %s\n", minEntropy(sig+":"), minDim(displayMatches), fileLink)

	// ── 2. FANCY ──────────────────────────────────────────────
	case LogFormatFancy:
		lockPrintf("%s %d %s  %s  in %s%s  %s\n",
			fancySecret("🚨 [SECRET]"),
			count, plural,
			fancyAccent(sig),
			fancyFile(file),
			color.HiBlueString(starStr),
			userRepo)
		lockPrintf("  %s %s  %s\n", fancyArrow("→"), displayMatches, fileLink)

	// ── 3. ULTRA FANCY ────────────────────────────────────────
	case LogFormatUltraFancy:
		starsFormatted := ""
		if stars > 0 {
			starsFormatted = "  " + ultraAccent(fmt.Sprintf("⭐ %d", stars))
		}
		lockPrintf("%s  %s  %s %s%s  %s\n",
			ultraDanger(" 🚨 SECRET "),
			ultraAccent(fmt.Sprintf("%d %s", count, plural)),
			ultraSecret(sig),
			ultraFile("→ "+file),
			starsFormatted,
			userRepo)
		lockPrintf("  %s %s  %s\n", ultraSecret("✦ →"), displayMatches, fileLink)

	// ── 4. EVEN MORE FANCY (Cosmic) ───────────────────────────
	case LogFormatEvenMoreFancy:
		cosmicBox(cosmicSecret, cosmicTitle, "🔥  🚨 LEGENDARY SECRET BREACH  🔥")
		if stars > 0 {
			lockPrintf("  %s ⭐ %d stars\n", cosmicSecret("║"), stars)
		}
		lockPrintf("  %s %d %s  sig: %s\n",
			cosmicSecret("║"),
			count, plural,
			cosmicAccent(sig))
		lockPrintf("  %s file: %s  %s\n", cosmicSecret("║"), cosmicFile(file), userRepo)
		lockPrintf("  %s %s  %s\n", cosmicAccent("➜"), displayMatches, fileLink)

	// ── 5. NEON ───────────────────────────────────────────────
	default:
		neonStars := ""
		if stars > 0 {
			neonStars = "  " + neonStar(fmt.Sprintf("⭐ %d", stars))
		}
		lockPrintf("%s %s  %s%s  %s\n",
			neonSecretLabel(" 🚨 SECRET "),
			neonValue(sig),
			neonDim(file),
			neonStars,
			userRepo)
		lockPrintf("  %s  %s %s  %s\n",
			neonDim(fmt.Sprintf("%d %s", count, plural)),
			neonMatch("⟶"),
			displayMatches,
			fileLink)
	}
}

// logFile logs a file-pattern match event.
func logFile(url, sig, file, branch string, stars int) {
	userRepo, _, fileLink := getEnhancedLinkInfo(url, file, branch)

	switch 	getLogFormat() {

	// ── 1. MINIMAL ────────────────────────────────────────────
	case LogFormatMinimal:
		lockPrintf("%s %s %s %s  %s\n",
			minFile("📄"),
			minDim(sig),
			minDim("→"),
			minInfo(file),
			userRepo)
		lockPrintf("       %s\n", fileLink)

	// ── 2. FANCY ──────────────────────────────────────────────
	case LogFormatFancy:
		lockPrintf("%s %s  matches  %s  %s\n",
			fancyFile("📄 [FILE]"),
			fancyFile(file),
			fancyAccent(sig),
			userRepo)
		lockPrintf("  %s\n", fileLink)

	// ── 3. ULTRA FANCY ────────────────────────────────────────
	case LogFormatUltraFancy:
		lockPrintf("%s  %s  %s %s  %s\n",
			ultraBadge(" 📄 FILE "),
			ultraFile(file),
			ultraSearch("matches"),
			ultraAccent(sig),
			userRepo)
		lockPrintf("  %s\n", fileLink)

	// ── 4. EVEN MORE FANCY (Cosmic) ───────────────────────────
	case LogFormatEvenMoreFancy:
		cosmicBox(cosmicFile, cosmicTitle, "📁  📄 EPIC FILE PATTERN MATCH  📁")
		lockPrintf("  %s %s %s %s  %s\n",
			cosmicFile("◈"),
			cosmicAccent(sig),
			cosmicFile("→"),
			cosmicFile(file),
			userRepo)
		lockPrintf("  %s\n", fileLink)
		if stars > 0 {
			lockPrintf("  %s ⭐ %d stars\n", cosmicFile("◈"), stars)
		}

	// ── 5. NEON ───────────────────────────────────────────────
	default:
		neonStars := ""
		if stars > 0 {
			neonStars = "  " + neonStar(fmt.Sprintf("⭐ %d", stars))
		}
		lockPrintf("%s %s %s %s%s  %s\n",
			neonFileLabel(" 📄 FILE "),
			neonValue(file),
			neonDim("→"),
			neonMatch(sig),
			neonStars,
			userRepo)
		lockPrintf("  %s\n", fileLink)
	}
}

// logEntropy logs a high-entropy string detection event.
func logEntropy(url, file, line, branch string, stars int) {
	userRepo, _, fileLink := getEnhancedLinkInfo(url, file, branch)

	switch 	getLogFormat() {

	// ── 1. MINIMAL ────────────────────────────────────────────
	case LogFormatMinimal:
		lockPrintf("%s %s  %s\n",
			minEntropy("⚡"),
			minInfo(file),
			userRepo)
		lockPrintf("         %s  %s\n", minDim(line), fileLink)

	// ── 2. FANCY ──────────────────────────────────────────────
	case LogFormatFancy:
		lockPrintf("%s potential secret in %s  %s\n",
			fancyEntropy("⚡ [ENTROPY]"),
			fancyFile(file),
			userRepo)
		lockPrintf("  %s %s  %s\n", fancyArrow("→"), line, fileLink)

	// ── 3. ULTRA FANCY ────────────────────────────────────────
	case LogFormatUltraFancy:
		lockPrintf("%s  %s %s  %s\n",
			ultraBadge(" ⚡ ENTROPY "),
			ultraEntropy("high-entropy string in"),
			ultraFile(file),
			userRepo)
		fmt.Printf("  %s %s  %s\n", ultraEntropy("✦ ->"), line, fileLink)

	// ── 4. EVEN MORE FANCY (Cosmic) ───────────────────────────
	case LogFormatEvenMoreFancy:
		cosmicBox(cosmicEntropy, cosmicTitle, "⚡  🌠 CHAOS ENTROPY BREACH  ⚡")
		lockPrintf("  %s file: %s  %s\n", cosmicEntropy("║"), cosmicFile(file), userRepo)
		lockPrintf("  %s %s  %s\n", cosmicAccent("➜"), line, fileLink)

	// ── 5. NEON ───────────────────────────────────────────────
	default:
		lockPrintf("%s %s  %s\n",
			neonEntropyLabel(" ⚡ ENTROPY "),
			neonValue(file),
			userRepo)
		lockPrintf("  %s %s  %s\n", neonMatch("⟶"), line, fileLink)
	}
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
	threadNum := *session.Options.Threads
	// Limit threads to prevent overwhelming
	if threadNum > 20 {
		threadNum = 20
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
		for repository := range session.Repositories {
			repoQueue <- repository
		}
	}()
}

func processRepository(repository core.GitResource, workerID int) {
	repo, err := core.GetRepository(session, repository.Id)
	if err != nil {
		session.Log.Warn("Failed to retrieve repository %d: %s", repository.Id, err)
		return
	}
	if repo.GetPermissions()["pull"] &&
		uint(repo.GetStargazersCount()) >= *session.Options.MinimumStars &&
		uint(repo.GetSize()) < *session.Options.MaximumRepositorySize {
		processRepositoryOrGist(repo.GetCloneURL(), repository.Ref, repo.GetStargazersCount(), core.GITHUB_SOURCE, workerID)
	}
}

func ProcessGists() {
	threadNum := *session.Options.Threads
	// Limit threads for gists to prevent rate limiting
	if threadNum > 5 {
		threadNum = 5
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
		for gistUrl := range session.Gists {
			gistQueue <- gistUrl
		}
	}()
}

func ProcessComments() {
	threadNum := *session.Options.Threads
	// Limit threads for comments to prevent rate limiting
	if threadNum > 3 {
		threadNum = 3
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
		for comment := range session.Comments {
			commentQueue <- comment
		}
	}()
}

func processComment(comment core.Comment) {
	dir := core.GetTempDir(core.GetHash(comment.Body))
	ioutil.WriteFile(filepath.Join(dir, "comment.ignore"), []byte(comment.Body), 0644)
	if !checkSignatures(dir, comment.Url, "", 0, core.GITHUB_COMMENT) {
		os.RemoveAll(dir)
	}

	// Mark directory as processed and trigger cleanup
	session.CleanupManager.MarkProcessed(dir)
	if err := session.CleanupManager.CleanupIfNeeded(); err != nil {
		session.Log.Debug("Cleanup error: %s", err.Error())
	}
}

func processRepositoryOrGist(url string, ref string, stars int, source core.GitResourceType, workerID int) {
	dir := core.GetTempDir(core.GetHash(url))

	activity.StartFetch(url)

	var gitProgress *core.GitProgressWriter

	_, err := core.CloneRepository(session, url, ref, dir, gitProgress)
	if err != nil {
		session.Log.Debug("[%s] Cloning failed: %s", url, err.Error())
		os.RemoveAll(dir)
		activity.FailRepo(url)
		return
	}
	activity.StartScan(url)
	session.Log.Debug("[%s] Cloning %s into %s",
		url, ref,
		strings.Replace(dir, *session.Options.TempDirectory, "", -1))
	core.ApplySandboxToRepo(dir)
	checkSignatures(dir, url, ref, stars, source)
	activity.FinishScan(url)

	// Mark directory as processed for cleanup tracking
	session.CleanupManager.MarkProcessed(dir)

	// Check if cleanup is needed
	if err := session.CleanupManager.CleanupIfNeeded(); err != nil {
		session.Log.Debug("Cleanup error: %s", err.Error())
	}
}

func checkSignatures(dir string, url string, ref string, stars int, source core.GitResourceType) (matchedAny bool) {
	for _, file := range core.GetMatchingFiles(dir) {
		var (
			matches          []string
			relativeFileName string
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

		if *session.Options.SearchQuery != "" {
			queryRegex := regexp.MustCompile(*session.Options.SearchQuery)
			for _, match := range queryRegex.FindAllSubmatch(file.Contents, -1) {
				matches = append(matches, string(match[0]))
			}
			if len(matches) > 0 {
				count := len(matches)
				m := strings.Join(matches, ", ")
				logSearch(count, url, relativeFileName, m, ref)
				session.WriteToCsv([]string{url, "Search Query", relativeFileName, m})
				session.LogMatch("Search Query", url, relativeFileName, matches)
			}
		} else {
			for _, signature := range session.Signatures {
				// Check if signature should be excluded in current mode
				currentMode := 	getLogFormat()
				if isSignatureExcludedInMode(signature, currentMode) {
					continue
				}

				if matched, part := signature.Match(file); matched {
					if part == core.PartContents {
						if matches = signature.GetContentsMatches(file); len(matches) > 0 {
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
							logSecret(count, url, signature.Name(), relativeFileName, m, ref, stars)
							session.WriteToCsv([]string{url, signature.Name(), relativeFileName, m})
							session.LogMatch(signature.Name(), url, relativeFileName, matches)

							// Add GitHub tokens to session for dynamic fetching
							if signature.IsTokenType() {
								for _, token := range matches {
									session.AddGitHubToken(token)
								}
							}

							// Test tokens if this is an AI token signature
							sigName := strings.ToLower(signature.Name())
							if isAITokenSignature(sigName) {
								session.TokenValidator.TestMatchedTokens(matches, signature.GetColor())
							}
						}
					} else {
						if *session.Options.PathChecks {
							publish(&MatchEvent{Source: source, Url: url, Matches: matches, Signature: signature.Name(), File: relativeFileName, Stars: stars, Priority: signature.GetPriority(), Color: signature.GetColor(), FileContent: string(file.Contents)})
							matchedAny = true
							logFile(url, signature.Name(), relativeFileName, ref, stars)
							session.WriteToCsv([]string{url, signature.Name(), relativeFileName, ""})
							session.LogMatch(signature.Name(), url, relativeFileName, []string{relativeFileName})
						}
						if *session.Options.EntropyThreshold > 0 && file.CanCheckEntropy() {
							scanner := bufio.NewScanner(bytes.NewReader(file.Contents))
							lineNo := 0
							for scanner.Scan() {
								lineNo++
								line := scanner.Text()
								if len(line) > 6 && len(line) < 100 {
									entropy := core.GetEntropy(line)
									if entropy >= *session.Options.EntropyThreshold {
										blacklistedMatch := false
										for _, blacklistedString := range session.Config.BlacklistedStrings {
											if strings.Contains(strings.ToLower(line), strings.ToLower(blacklistedString)) {
												blacklistedMatch = true
											}
										}
										if !blacklistedMatch {
											publish(&MatchEvent{Source: source, Url: url, Matches: []string{line}, Signature: "High entropy string", File: relativeFileName, Stars: stars, Priority: 0, Color: "", FileContent: string(file.Contents), Secret: line, SecretLine: lineNo})
											matchedAny = true
											logEntropy(url, relativeFileName, line, ref, stars)
											session.WriteToCsv([]string{url, "High entropy string", relativeFileName, line})
											session.LogMatch("High entropy string", url, relativeFileName, []string{line})
										}
									}
								}
							}
						}
					}
				}
			}
		}

		if !matchedAny && len(*session.Options.Local) <= 0 {
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

func publish(event *MatchEvent) {
	// Feed the in-process web dashboard (when running with --web).
	if webHub != nil {
		m := &Match{
			ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
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
		select {
		case webHub.broadcast <- m:
		default:
		}
		if event.FileContent != "" {
			storeMatchFile(m.ID, event.Url, event.File, event.FileContent, event.Secret, event.SecretLine)
		}
	}

	// Feed the TUI (when running with --tui).
	if tuiState != nil {
		AddTUIMatch(&TUIMatch{
			ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
			Timestamp: time.Now(),
			Source:    sourceName(event.Source),
			URL:       event.Url,
			File:      event.File,
			Signature: event.Signature,
			Matches:   event.Matches,
			Stars:     event.Stars,
			Priority:  event.Priority,
		})
	}

	if len(*session.Options.Live) > 0 {
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
		http.Post(*session.Options.Live, "application/json", bytes.NewBuffer(data))
	}

	// Send EVERY match to the main webhook.
	if len(session.Config.Webhook) > 0 {
		go core.SendMatchWebhook(
			session.Config.Webhook,
			session.Config.WebhookPayload,
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
	if len(session.Config.WebhookAITokens) > 0 && isAITokenSignature(event.Signature) {
		go core.SendMatchWebhook(
			session.Config.WebhookAITokens,
			session.Config.WebhookPayload,
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
	if len(session.Config.WebhookCrypto) > 0 && isCryptoSignature(event.Signature) {
		go core.SendMatchWebhook(
			session.Config.WebhookCrypto,
			session.Config.WebhookPayload,
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
	initLogFormat()

	// Print mode information
	fmt.Println()
	modeConfig.PrintModeInfo()
	fmt.Println()

	// Handle UI modes
	if modeConfig.Mode == ModeWeb {
		runWebMode()
		return
	}

	// --tui, --scanner and default all behave the same: normal terminal
	// scanner with logs printing to the terminal.
	runScannerCLI()
}

// exitScanner terminates a one-shot scanner flow. In web mode the scanner
// runs in a goroutine alongside the dashboard, so it must NOT kill the
// process; it just returns and the web server keeps serving results.
func exitScanner(code int) {
	if modeConfig != nil && modeConfig.Mode == ModeWeb {
		return
	}
	os.Exit(code)
}

// runWebMode starts the web dashboard and runs the scanner in-process,
// feeding matches and logs into the embedded dark-mode UI.
func runWebMode() {
	// In --web mode the dashboard is the interface: hide the console window
	// so only the browser UI is visible (scanner still runs in-process).
	hideConsole()

	// Initialize the web hub before the scanner starts so no matches are lost.
	ensureWebHub()
	webLogCapture = true

	// Capture scanner log output (Logger writes) into the web log buffer so
	// the dashboard's Logs tab shows everything.
	if session.Log != nil {
		session.Log.LogWriter = func(line string) {
			fmt.Print(line)
			appendLogLine(line)
		}
	}

	// Route token-validation results into the web "Tokens" view.
	if session.TokenValidator != nil {
		session.TokenValidator.OnTokenResult = func(token string, valid bool, provider string) {
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

	// Wire the AI review service (from config) before the web server starts.
	if err := InitAIReview(session.Config); err != nil {
		log.Printf("[web] AI review init failed: %v", err)
	}

	// Start web server (blocking)
	if err := modeConfig.SetupIntegratedUI(); err != nil {
		fmt.Printf("❌ Web server error: %v\n", err)
		os.Exit(1)
	}
}

// runScanner executes the scanner with UI reporting
func runScanner() {
	// Original banner output suppressed if UI is active
	if modeConfig.Mode == ModeScanner {
		// ── Banner (preset-aware) ──────────────────────────────────
		switch getLogFormat() {
		case LogFormatMinimal:
			lockPrintf("%s %s\n", minSearch("shhgit"), minDim(core.Author))

		case LogFormatFancy:
			lockPrintln(color.HiBlueString(core.Banner))
			lockPrintln("  " + color.HiCyanString(core.Author))

		case LogFormatUltraFancy:
			lockPrintln(color.HiBlueString(core.Banner))
			lockPrintf("  %s  %s\n",
				ultraBadge(" ULTRA FANCY "),
				ultraAccent(core.Author))

		case LogFormatEvenMoreFancy:
			cosmicBox(cosmicBorder, cosmicTitle, "✨  SHHGIT - EVEN MORE FANCY MODE  ✨")
			lockPrintf("  %s\n\n", cosmicAccent(core.Author))

		case LogFormatNeon:
			lockPrintln(color.HiMagentaString(core.Banner))
			lockPrintf("  %s %s\n",
				neonEntropyLabel(" NEON MODE "),
				neonValue(core.Author))
		}

		// ── Startup summary ───────────────────────────────────────
		switch getLogFormat() {
		case LogFormatMinimal:
			lockPrintf("%s sigs:%s  threads:%s  tmp:%s\n\n",
				minDim("◆"),
				minSearch(fmt.Sprintf("%d", len(session.Signatures))),
				minSearch(fmt.Sprintf("%d", *session.Options.Threads)),
				minDim(*session.Options.TempDirectory))
		case LogFormatNeon:
			lockPrintf("%s sigs %s  threads %s  tmp %s  style %s\n\n",
				neonDim("◆"),
				neonStar(fmt.Sprintf("%d", len(session.Signatures))),
				neonStar(fmt.Sprintf("%d", *session.Options.Threads)),
				neonDim(*session.Options.TempDirectory),
				neonSearchLabel(" "+strings.ToUpper(getLogFormat())+" "))
		default:
			lockPrintf("[*] Loaded %s signatures  .  %s threads  .  tmp: %s  .  style: %s\n\n",
				color.HiCyanString("%d", len(session.Signatures)),
				color.HiCyanString("%d", *session.Options.Threads),
				color.HiBlueString(*session.Options.TempDirectory),
				color.HiGreenString(strings.ToUpper(getLogFormat())))
		}
	}

	// Execute scanner logic
	executeScanner()
}

// runScannerCLI runs scanner with full CLI output
func runScannerCLI() {
	// ── Banner (preset-aware) ──────────────────────────────────
	switch getLogFormat() {
	case LogFormatMinimal:
		lockPrintf("%s %s\n", minSearch("shhgit"), minDim(core.Author))

	case LogFormatFancy:
		lockPrintln(color.HiBlueString(core.Banner))
		lockPrintln("  " + color.HiCyanString(core.Author))

	case LogFormatUltraFancy:
		lockPrintln(color.HiBlueString(core.Banner))
		lockPrintf("  %s  %s\n",
			ultraBadge(" ULTRA FANCY "),
			ultraAccent(core.Author))

	case LogFormatEvenMoreFancy:
		cosmicBox(cosmicBorder, cosmicTitle, "✨  SHHGIT - EVEN MORE FANCY MODE  ✨")
		lockPrintf("  %s\n\n", cosmicAccent(core.Author))

	case LogFormatNeon:
		lockPrintln(color.HiMagentaString(core.Banner))
		lockPrintf("  %s %s\n",
			neonEntropyLabel(" NEON MODE "),
			neonValue(core.Author))
	}

	// ── Startup summary ───────────────────────────────────────
	switch getLogFormat() {
	case LogFormatMinimal:
		lockPrintf("%s sigs:%s  threads:%s  tmp:%s\n\n",
			minDim("◆"),
			minSearch(fmt.Sprintf("%d", len(session.Signatures))),
			minSearch(fmt.Sprintf("%d", *session.Options.Threads)),
			minDim(*session.Options.TempDirectory))
	case LogFormatNeon:
		lockPrintf("%s sigs %s  threads %s  tmp %s  style %s\n\n",
			neonDim("◆"),
			neonStar(fmt.Sprintf("%d", len(session.Signatures))),
			neonStar(fmt.Sprintf("%d", *session.Options.Threads)),
			neonDim(*session.Options.TempDirectory),
			neonSearchLabel(" "+strings.ToUpper(getLogFormat())+" "))
	default:
		lockPrintf("[*] Loaded %s signatures  .  %s threads  .  tmp: %s  .  style: %s\n\n",
			color.HiCyanString("%d", len(session.Signatures)),
			color.HiCyanString("%d", *session.Options.Threads),
			color.HiBlueString(*session.Options.TempDirectory),
			color.HiGreenString(strings.ToUpper(getLogFormat())))
	}

	executeScanner()
}

// executeScanner runs the scanner core logic
func executeScanner() {
	// ── Dispatch ──────────────────────────────────────────────
	if *session.Options.TestTokens {
		validator := core.NewTokenValidator(session.Log)
		var tokensToTest []string

		// Collect tokens from all config sections and detect AI-related ones
		allTokens := make(map[string]string) // token -> detected provider

		// 1. Explicitly listed AI tokens
		for _, token := range session.Config.AITokens {
			allTokens[token] = validator.DetectProvider(token)
		}

		// 2. Check github_access_tokens for any AI tokens via provider detection
		for _, token := range session.Config.GitHubAccessTokens {
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
			session.Log.Warn("No AI tokens found in config")
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
	}

	if len(*session.Options.ScanKeys) > 0 {
		lockPrintf("[*] Scanning directory for API keys: %s\n", color.HiYellowString(*session.Options.ScanKeys))
		keyValidator := core.NewKeyValidator(session.Log)

		// Open log file for keys
		logFilename := fmt.Sprintf("found_keys_%d.txt", time.Now().Unix())
		if err := keyValidator.OpenLogFile(logFilename); err != nil {
			lockPrintf("[!] Error opening log file: %s\n", err)
		} else {
			lockPrintf("[*] Logging keys to: %s\n", logFilename)
			defer keyValidator.CloseLogFile()
		}

		validatedKeys := keyValidator.ScanDirectoryForKeys(*session.Options.ScanKeys, session)

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
	}

	if len(*session.Options.Local) > 0 {
		lockPrintf("[*] Scanning local: %s\n", color.HiYellowString(*session.Options.Local))
		rc := 0
		if checkSignatures(*session.Options.Local, *session.Options.Local, "", -1, core.LOCAL_SOURCE) {
			rc = 1
		} else {
			lockPrintf("[*] No secrets found in %s\n", color.HiBlueString(*session.Options.Local))
		}
		session.MatchLogger.Close()
		exitScanner(rc)
	}

	if *session.Options.SearchQuery != "" {
		lockPrintf("[*] Search query %s — only returning matching results.\n",
			color.HiYellowString(*session.Options.SearchQuery))
	}

	// Start background workers
	go core.GetRepositories(session)
	go ProcessRepositories()
	go ProcessComments()

	if *session.Options.ProcessGists {
		go core.GetGists(session)
		go ProcessGists()
	}

	// Block forever - workers run as goroutines
	select {}
}
