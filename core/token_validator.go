package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/trebor048/shhgot/core/verifiers"
)

// --- Provider Constants ---
const (
	// AI/ML Providers
	ProviderOpenAI      = "OpenAI"
	ProviderAnthropic   = "Anthropic"
	ProviderGoogle      = "Google/Gemini"
	ProviderAzure       = "Azure"
	ProviderGroq        = "Groq"
	ProviderMistral     = "Mistral"
	ProviderCohere      = "Cohere"
	ProviderHuggingFace = "HuggingFace"
	ProviderPerplexity  = "Perplexity"
	ProviderTogether    = "Together AI"
	ProviderDeepSeek    = "DeepSeek"
	ProviderKimi        = "Kimi/Moonshot"
	ProviderQwen        = "Qwen/Alibaba"
	ProviderXAI         = "xAI/Grok"
	ProviderOpenRouter  = "OpenRouter"
	ProviderAI21        = "AI21"
	ProviderElevenLabs  = "ElevenLabs"
	ProviderReplicate   = "Replicate"
	ProviderStability   = "Stability AI"
	ProviderAssemblyAI  = "AssemblyAI"
	ProviderDeepgram    = "Deepgram"
	ProviderFireworks   = "Fireworks"
	ProviderPinecone    = "Pinecone"
	ProviderSambaNova   = "SambaNova"
	ProviderCerebras    = "Cerebras"
	ProviderAnyscale    = "Anyscale"
	ProviderNLPCloud    = "NLP Cloud"
	ProviderForefront   = "Forefront"
	ProviderJina        = "Jina AI"
	ProviderWriter      = "Writer"
	ProviderJasper      = "Jasper"
	ProviderCopyAI      = "Copy.ai"
	ProviderRunway      = "RunwayML"
	ProviderMidjourney  = "Midjourney"
	ProviderLambda      = "Lambda Labs"
	ProviderModal       = "Modal"
	ProviderBaseten     = "Baseten"
	ProviderAlephAlpha  = "Aleph Alpha"
	ProviderVLLM        = "vLLM"

	// Cloud Providers
	ProviderAWS          = "AWS"
	ProviderGCP          = "Google Cloud"
	ProviderDigitalOcean = "DigitalOcean"
	ProviderHeroku       = "Heroku"
	ProviderCloudflare   = "Cloudflare"
	ProviderVercel       = "Vercel"
	ProviderNetlify      = "Netlify"
	ProviderSupabase     = "Supabase"
	ProviderFirebase     = "Firebase"

	// Developer Platforms
	ProviderGithub    = "GitHub"
	ProviderGitLab    = "GitLab"
	ProviderBitbucket = "Bitbucket"
	ProviderDockerHub = "Docker Hub"
	ProviderNPM       = "NPM"
	ProviderPyPI      = "PyPI"

	// Communication
	ProviderDiscord     = "Discord"
	ProviderSlack       = "Slack"
	ProviderTelegram    = "Telegram"
	ProviderTwilio      = "Twilio"
	ProviderSendGrid    = "SendGrid"
	ProviderMailgun     = "Mailgun"
	ProviderPostmark    = "Postmark"
	ProviderPusher      = "Pusher"
	ProviderPubNub      = "PubNub"
	ProviderMessageBird = "MessageBird"
	ProviderVonage      = "Vonage"

	// Payment
	ProviderStripe    = "Stripe"
	ProviderSquare    = "Square"
	ProviderPayPal    = "PayPal"
	ProviderBraintree = "Braintree"
	ProviderRazorpay  = "Razorpay"
	ProviderPlaid     = "Plaid"

	// Social/Media
	ProviderTwitter   = "Twitter/X"
	ProviderReddit    = "Reddit"
	ProviderSpotify   = "Spotify"
	ProviderTwitch    = "Twitch"
	ProviderYouTube   = "YouTube"
	ProviderFacebook  = "Facebook"
	ProviderInstagram = "Instagram"
	ProviderTikTok    = "TikTok"
	ProviderLinkedIn  = "LinkedIn"

	// Blockchain/Web3
	ProviderAlchemy     = "Alchemy"
	ProviderInfura      = "Infura"
	ProviderQuickNode   = "QuickNode"
	ProviderMoralis     = "Moralis"
	ProviderEtherscan   = "Etherscan"
	ProviderBscScan     = "BscScan"
	ProviderPolygonScan = "PolygonScan"
	ProviderCoinbase    = "Coinbase"
	ProviderBinance     = "Binance"
	ProviderKraken      = "Kraken"
	ProviderBlockCypher = "BlockCypher"
	ProviderChainstack  = "Chainstack"

	// Collaboration / Maps / Monitoring (providers with documented key formats)
	ProviderNotion   = "Notion"
	ProviderLinear   = "Linear"
	ProviderMapbox   = "Mapbox"
	ProviderNewRelic = "New Relic"

	// Security/Other
	ProviderShodan      = "Shodan"
	ProviderVirusTotal  = "VirusTotal"
	ProviderIPInfo      = "IPInfo"
	ProviderAbstractAPI = "AbstractAPI"
	ProviderJWT         = "JWT"
	ProviderGeneric     = "Generic API Key"
	ProviderUnknown     = "Unknown"
)

// tokenTestResult is the cached outcome of validating one token. Its presence
// in TokenValidator.testedTokens is what "has been tested" means; the value
// carries the verdict so a later duplicate match can be accepted or dropped
// without a second network round-trip.
type tokenTestResult struct {
	valid    bool
	provider string
	// outcome is the verifier's richer verdict (status, tier, balance,
	// identity, high-value flag, RPM). It feeds severity classification and is
	// zero when the legacy validation path produced the verdict.
	outcome verifiers.Outcome

	// ready is non-nil while a validation is in flight. A duplicate that finds
	// a reserved entry waits on this channel and then reads the real verdict,
	// instead of being told "valid" before the provider has answered.
	ready chan struct{}
}

// TokenValidator handles validation of AI API tokens and other secrets
type TokenValidator struct {
	logger       *Logger
	testedTokens map[string]tokenTestResult
	mutex        sync.Mutex
	httpClient   verifiers.Doer

	// Cache is the opt-in validation result cache and per-host health tracker.
	// A nil cache disables both, so an operator who has not enabled
	// validators.cache_enabled sees the old behaviour.
	Cache *ValidationCache

	// sem bounds how many credential validations run concurrently, from
	// validators.max_concurrency. A nil sem is unbounded, which is what a
	// validator built directly (tests, --test-tokens) gets.
	sem chan struct{}

	// OnTokenResult is called when a token validation completes (for TUI/web live updates).
	OnTokenResult func(token string, valid bool, provider string)

	// OnOutcome is called after OnTokenResult with the verifier's Outcome, so
	// consumers can surface status, tier, balance and RPM without changing the
	// OnTokenResult signature.
	OnOutcome func(token string, provider string, o verifiers.Outcome)
}

// NewTokenValidator creates a new token validator. Validation requests go
// through the shared client wrapped in a validatorDoer, which applies
// validators.min_interval_ms as a per-call pacing override so credential
// validation is paced without pacing code search, GitLab, crypto or AI.
func NewTokenValidator(logger *Logger) *TokenValidator {
	return &TokenValidator{
		logger:       logger,
		testedTokens: make(map[string]tokenTestResult),
		httpClient:   validatorDoer{client: SharedHTTPClient()},
	}
}

// SetConcurrency bounds how many validations this validator runs at once, from
// validators.max_concurrency. A non-positive n leaves it unbounded. The value is
// read under the same mutex that guards testedTokens, so it is safe to call
// concurrently with validations, though callers normally set it once at startup.
func (tv *TokenValidator) SetConcurrency(n int) {
	tv.mutex.Lock()
	defer tv.mutex.Unlock()
	if n <= 0 {
		tv.sem = nil
		return
	}
	tv.sem = make(chan struct{}, n)
}

// acquireValidationSlot blocks until a validation slot is free and returns the
// function that releases it. When no limit is configured the release is a no-op.
func (tv *TokenValidator) acquireValidationSlot() func() {
	tv.mutex.Lock()
	sem := tv.sem
	tv.mutex.Unlock()
	if sem == nil {
		return func() {}
	}
	sem <- struct{}{}
	return func() { <-sem }
}

// CRITICAL PERFORMANCE FIX: Pre-compile all token extraction regexes at package init
// This avoids compiling 40+ regexes on EVERY match, which was causing massive slowdown
var (
	// AI/ML Tokens
	openaiProjRegex = regexp.MustCompile(`sk-proj-[a-zA-Z0-9_-]{20,}`)
	openaiSvcRegex  = regexp.MustCompile(`sk-svcacct-[a-zA-Z0-9_-]{20,}`)
	openaiRegex     = regexp.MustCompile(`sk-[a-zA-Z0-9_-]{20,}`)
	anthropicRegex  = regexp.MustCompile(`sk-ant-[a-zA-Z0-9_-]{20,}`)
	googleRegex     = regexp.MustCompile(`AIzaSy[A-Za-z0-9_-]{33}`)
	xaiRegex        = regexp.MustCompile(`xai-[A-Za-z0-9]{75,}`)
	openrouterRegex = regexp.MustCompile(`sk-or-v1-[a-z0-9]{60,}`)
	hfRegex         = regexp.MustCompile(`hf_[A-Za-z0-9]{30,}`)
	groqRegex       = regexp.MustCompile(`gsk_[A-Za-z0-9]{30,}`)
	perplexityRegex = regexp.MustCompile(`pplx-[A-Za-z0-9]{30,}`)
	replicateRegex  = regexp.MustCompile(`r8_[A-Za-z0-9]{30,}`)
	// The length-only detectors below are word-bounded: without the boundary a
	// longer hex/alnum secret (e.g. a 64-hex key) was split into fabricated
	// 32-char halves and truncated 40/48-char prefixes, each then probed against
	// the wrong provider's endpoint.
	cohereRegex    = regexp.MustCompile(`\b[A-Za-z0-9]{40}(_[A-Za-z0-9]{10,})?\b`)
	ai21Regex      = regexp.MustCompile(`\b[A-Za-z0-9]{32}\b`)
	elevenRegex    = regexp.MustCompile(`sk_[a-z0-9]{48}`)
	sambaRegex     = regexp.MustCompile(`[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}`)
	fireworksRegex = regexp.MustCompile(`\b[A-Za-z0-9]{48}\b`)
	assemblyRegex  = regexp.MustCompile(`\b[a-f0-9]{32}\b`)
	azureRegex     = regexp.MustCompile(`\b[a-f0-9]{32}\b`)

	// Cloud Providers
	awsRegex       = regexp.MustCompile(`AKIA[0-9A-Z]{16}`)
	awsSecretRegex = regexp.MustCompile(`[A-Za-z0-9/+=]{40}`)
	doRegex        = regexp.MustCompile(`dop_v1_[a-f0-9]{64}`)
	cfRegex        = regexp.MustCompile(`[A-Za-z0-9_-]{40}`)
	vercelRegex    = regexp.MustCompile(`[A-Za-z0-9]{24}`)

	// Developer Platforms
	githubRegex = regexp.MustCompile(`(ghp_[a-zA-Z0-9_]{36}|gho_[a-zA-Z0-9_]{36}|ghu_[a-zA-Z0-9_]{36}|ghs_[a-zA-Z0-9_]{36}|ghr_[a-zA-Z0-9_]{36}|github_pat_[a-zA-Z0-9_]{82})`)
	gitlabRegex = regexp.MustCompile(`glpat-[A-Za-z0-9_\-]{20,}`)
	dockerRegex = regexp.MustCompile(`dckr_pat_[A-Za-z0-9_\-]{20,}`)
	npmRegex    = regexp.MustCompile(`npm_[a-zA-Z0-9]{36}`)
	pypiRegex   = regexp.MustCompile(`pypi-[A-Za-z0-9_\-]{20,}`)

	// Communication
	discordRegex  = regexp.MustCompile(`([A-Za-z0-9_-]{24}\.[A-Za-z0-9_-]{6}\.[A-Za-z0-9_-]{27,})`)
	slackRegex    = regexp.MustCompile(`xox[bpatrs]-[a-zA-Z0-9-]+`)
	telegramRegex = regexp.MustCompile(`\d+:[A-Za-z0-9_-]{35}`)
	twilioRegex   = regexp.MustCompile(`SK[0-9a-f]{32}`)
	sendgridRegex = regexp.MustCompile(`SG\.[A-Za-z0-9_\-]{20,}\.[A-Za-z0-9_\-]{20,}`)
	mailgunRegex  = regexp.MustCompile(`key-[a-f0-9]{32}`)
	pusherRegex   = regexp.MustCompile(`[a-f0-9]{20}`)
	pubnubRegex   = regexp.MustCompile(`pub-c-[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}`)

	// Payment Processors
	stripeRegex    = regexp.MustCompile(`(sk|pk|rk)_(live|test)_[a-zA-Z0-9]{24,}`)
	squareRegex    = regexp.MustCompile(`sq0[atcp][a-z]-[a-zA-Z0-9_-]{22,}`)
	paypalRegex    = regexp.MustCompile(`[A-Za-z0-9]{80}`)
	braintreeRegex = regexp.MustCompile(`[a-z0-9]{32}`)
	razorpayRegex  = regexp.MustCompile(`rzp_(live|test)_[A-Za-z0-9]{20}`)
	plaidRegex     = regexp.MustCompile(`[a-f0-9]{24}`)

	// Social/Media
	twitterRegex   = regexp.MustCompile(`[A-Za-z0-9%]{100,}`)
	redditRegex    = regexp.MustCompile(`[A-Za-z0-9_-]{27}`)
	spotifyRegex   = regexp.MustCompile(`[A-Za-z0-9]{32}`)
	twitchRegex    = regexp.MustCompile(`[a-z0-9]{30}`)
	youtubeRegex   = regexp.MustCompile(`AIza[0-9A-Za-z_-]{35}`)
	linkedinRegex  = regexp.MustCompile(`[A-Za-z0-9]{80,}`)
	alchemyRegex   = regexp.MustCompile(`[a-zA-Z0-9_-]{32}`)
	infuraRegex    = regexp.MustCompile(`[a-f0-9]{32}`)
	etherscanRegex = regexp.MustCompile(`[A-Z0-9]{34}`)
	coinbaseRegex  = regexp.MustCompile(`[a-f0-9]{32}`)
	binanceRegex   = regexp.MustCompile(`[A-Za-z0-9]{64}`)
	moralisRegex   = regexp.MustCompile(`[A-Za-z0-9]{64}`)
	shodanRegex    = regexp.MustCompile(`[A-Za-z0-9]{32}`)
	vtRegex        = regexp.MustCompile(`[a-f0-9]{64}`)
	ipinfoRegex    = regexp.MustCompile(`[a-f0-9]{32}`)
	jwtRegex       = regexp.MustCompile(`eyJ[A-Za-z0-9_-]*\.eyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]*`)
	genericRegex   = regexp.MustCompile(`[A-Za-z0-9_-]{32,64}`)
)

// ExtractTokensFromMatch extracts tokens for ALL supported providers
func (tv *TokenValidator) ExtractTokensFromMatch(match string) []string {
	var tokens []string
	seen := make(map[string]bool)

	addUnique := func(t string) {
		t = strings.TrimSpace(t)
		if t != "" && !seen[t] {
			tokens = append(tokens, t)
			seen[t] = true
		}
	}

	// ═══════════════════════════════════════════════════════════════
	//  AI / ML TOKENS
	// ═══════════════════════════════════════════════════════════════

	// 1. OpenAI Project Keys (sk-proj-...)
	for _, t := range openaiProjRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 2. OpenAI Service Account Keys (sk-svcacct-...)
	for _, t := range openaiSvcRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 3. OpenAI Standard Keys (sk-...) - but filter out others
	for _, t := range openaiRegex.FindAllString(match, -1) {
		// Skip if it's a provider-specific prefix we handle separately
		if strings.HasPrefix(t, "sk-ant-") || strings.HasPrefix(t, "sk-or-") ||
			strings.HasPrefix(t, "sk-proj-") || strings.HasPrefix(t, "sk-svcacct-") {
			continue
		}
		addUnique(t)
	}

	// 4. Anthropic (sk-ant-...)
	for _, t := range anthropicRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 5. Google Gemini / MakerSuite (AIzaSy...)
	for _, t := range googleRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 6. xAI / Grok (xai-...)
	for _, t := range xaiRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 7. OpenRouter (sk-or-v1-...)
	for _, t := range openrouterRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 8. HuggingFace (hf_...)
	for _, t := range hfRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 9. Groq (gsk_...)
	for _, t := range groqRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 10. Perplexity (pplx-...)
	for _, t := range perplexityRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 11. Replicate (r8_...)
	for _, t := range replicateRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 12. Cohere
	for _, t := range cohereRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 13. AI21 / Mistral (32 alphanumeric)
	for _, t := range ai21Regex.FindAllString(match, -1) {
		// Avoid common false positives (hex colors, UUID fragments, etc.)
		if !isLikelyFalsePositive(t) {
			addUnique(t)
		}
	}

	// 14. ElevenLabs (sk_... or 32 lowercase hex)
	for _, t := range elevenRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 16. SambaNova
	for _, t := range sambaRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 17. Fireworks
	for _, t := range fireworksRegex.FindAllString(match, -1) {
		if !isLikelyFalsePositive(t) {
			addUnique(t)
		}
	}

	// 20. AssemblyAI
	for _, t := range assemblyRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 22. Azure OpenAI
	for _, t := range azureRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// ═══════════════════════════════════════════════════════════════
	//  CLOUD PROVIDERS
	// ═══════════════════════════════════════════════════════════════

	// 23. AWS Access Keys (AKIA...)
	for _, t := range awsRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 24. AWS Secret Keys (paired with access key)
	for _, t := range awsSecretRegex.FindAllString(match, -1) {
		if len(t) == 40 && strings.Contains(match, "AKIA") {
			addUnique(t)
		}
	}

	// 25. DigitalOcean
	for _, t := range doRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 27. Cloudflare
	for _, t := range cfRegex.FindAllString(match, -1) {
		if strings.Contains(strings.ToLower(match), "cloudflare") && len(t) == 40 {
			addUnique(t)
		}
	}

	// 28. Vercel
	for _, t := range vercelRegex.FindAllString(match, -1) {
		if strings.Contains(strings.ToLower(match), "vercel") && len(t) == 24 {
			addUnique(t)
		}
	}

	// ═══════════════════════════════════════════════════════════════
	//  DEVELOPER PLATFORMS
	// ═══════════════════════════════════════════════════════════════

	// 29. GitHub (ghp_..., gho_..., github_pat_...)
	for _, t := range githubRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 30. GitLab
	for _, t := range gitlabRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 31. Docker Hub
	for _, t := range dockerRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 32. NPM (npm_...)
	for _, t := range npmRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 33. PyPI
	for _, t := range pypiRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// ═══════════════════════════════════════════════════════════════
	//  COMMUNICATION
	// ═══════════════════════════════════════════════════════════════

	// 34. Discord (Bot/Discord tokens)
	for _, t := range discordRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 35. Slack (xoxb-..., xoxp-..., xoxa-...)
	for _, t := range slackRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 36. Telegram (Bot Token: 123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11)
	for _, t := range telegramRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 37. Twilio
	for _, t := range twilioRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 38. SendGrid
	for _, t := range sendgridRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 39. Mailgun
	for _, t := range mailgunRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 41. Pusher
	for _, t := range pusherRegex.FindAllString(match, -1) {
		if strings.Contains(strings.ToLower(match), "pusher") && len(t) == 20 {
			addUnique(t)
		}
	}

	// 42. PubNub
	for _, t := range pubnubRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// ═══════════════════════════════════════════════════════════════
	//  PAYMENT PROCESSORS
	// ═══════════════════════════════════════════════════════════════

	// 43. Stripe (sk_live_..., pk_live_..., rk_...)
	for _, t := range stripeRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 44. Square (sq0atp-..., sq0csp-...)
	for _, t := range squareRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 45. PayPal
	for _, t := range paypalRegex.FindAllString(match, -1) {
		if strings.Contains(strings.ToLower(match), "paypal") && len(t) == 80 {
			addUnique(t)
		}
	}

	// 46. Braintree
	for _, t := range braintreeRegex.FindAllString(match, -1) {
		if strings.Contains(strings.ToLower(match), "braintree") && len(t) == 32 {
			addUnique(t)
		}
	}

	// 47. Razorpay
	for _, t := range razorpayRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 48. Plaid
	for _, t := range plaidRegex.FindAllString(match, -1) {
		if strings.Contains(strings.ToLower(match), "plaid") && len(t) == 24 {
			addUnique(t)
		}
	}

	// ═══════════════════════════════════════════════════════════════
	//  SOCIAL / MEDIA
	// ═══════════════════════════════════════════════════════════════

	// 49. Twitter/X Bearer
	for _, t := range twitterRegex.FindAllString(match, -1) {
		if strings.Contains(strings.ToLower(match), "twitter") || strings.Contains(strings.ToLower(match), "x_api") {
			addUnique(t)
		}
	}

	// 50. Reddit
	for _, t := range redditRegex.FindAllString(match, -1) {
		if strings.Contains(strings.ToLower(match), "reddit") && len(t) == 27 {
			addUnique(t)
		}
	}

	// 51. Spotify
	for _, t := range spotifyRegex.FindAllString(match, -1) {
		if strings.Contains(strings.ToLower(match), "spotify") && len(t) == 32 {
			addUnique(t)
		}
	}

	// 52. Twitch
	for _, t := range twitchRegex.FindAllString(match, -1) {
		if strings.Contains(strings.ToLower(match), "twitch") && len(t) == 30 {
			addUnique(t)
		}
	}
	// 53. YouTube
	for _, t := range youtubeRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 54. LinkedIn
	for _, t := range linkedinRegex.FindAllString(match, -1) {
		if strings.Contains(strings.ToLower(match), "linkedin") && len(t) >= 80 {
			addUnique(t)
		}
	}

	// ═══════════════════════════════════════════════════════════════
	//  BLOCKCHAIN / WEB3
	// ═══════════════════════════════════════════════════════════════

	// 55. Alchemy
	for _, t := range alchemyRegex.FindAllString(match, -1) {
		if strings.Contains(strings.ToLower(match), "alchemy") && len(t) == 32 {
			addUnique(t)
		}
	}

	// 56. Infura
	for _, t := range infuraRegex.FindAllString(match, -1) {
		if strings.Contains(strings.ToLower(match), "infura") && len(t) == 32 {
			addUnique(t)
		}
	}

	// 57. Etherscan
	for _, t := range etherscanRegex.FindAllString(match, -1) {
		if strings.Contains(strings.ToLower(match), "etherscan") && len(t) == 34 {
			addUnique(t)
		}
	}

	// 58. Coinbase
	for _, t := range coinbaseRegex.FindAllString(match, -1) {
		if strings.Contains(strings.ToLower(match), "coinbase") && len(t) == 32 {
			addUnique(t)
		}
	}

	// 59. Binance
	for _, t := range binanceRegex.FindAllString(match, -1) {
		if strings.Contains(strings.ToLower(match), "binance") && len(t) == 64 {
			addUnique(t)
		}
	}

	// 60. Moralis
	for _, t := range moralisRegex.FindAllString(match, -1) {
		if strings.Contains(strings.ToLower(match), "moralis") && len(t) == 64 {
			addUnique(t)
		}
	}

	// ═══════════════════════════════════════════════════════════════
	//  SECURITY / SCANNING
	// ═══════════════════════════════════════════════════════════════

	// 61. Shodan
	for _, t := range shodanRegex.FindAllString(match, -1) {
		if strings.Contains(strings.ToLower(match), "shodan") && len(t) == 32 {
			addUnique(t)
		}
	}

	// 62. VirusTotal
	for _, t := range vtRegex.FindAllString(match, -1) {
		if strings.Contains(strings.ToLower(match), "virustotal") && len(t) == 64 {
			addUnique(t)
		}
	}

	// 63. IPInfo
	for _, t := range ipinfoRegex.FindAllString(match, -1) {
		if strings.Contains(strings.ToLower(match), "ipinfo") && len(t) == 32 {
			addUnique(t)
		}
	}

	// ═══════════════════════════════════════════════════════════════
	//  GENERIC / CATCH-ALL
	// ═══════════════════════════════════════════════════════════════

	// 64. JWT Tokens
	for _, t := range jwtRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 65. Generic high-entropy API keys (last resort)
	for _, t := range genericRegex.FindAllString(match, -1) {
		if !isLikelyFalsePositive(t) && len(t) >= 32 {
			addUnique(t)
		}
	}

	return tokens
}

// isLikelyFalsePositive filters out common non-secret patterns
func isLikelyFalsePositive(s string) bool {
	lower := strings.ToLower(s)
	commonFalsePositives := []string{
		"undefined", "function", "prototype", "constructor",
		"aaaaaaaa", "bbbbbbbb", "cccccccc", "dddddddd",
		"00000000", "11111111", "ffffffff", "12345678",
		"password", "username", "default", "example",
		"template", "placeholder", "sample", "testtest",
		"nullnull", "truetrue", "falsefal",
	}
	for _, fp := range commonFalsePositives {
		if strings.Contains(lower, fp) {
			return true
		}
	}
	// Check for repeated character sequences
	if isRepeatedSequence(s) {
		return true
	}
	return false
}

// isRepeatedSequence checks if a string is just repeated characters
func isRepeatedSequence(s string) bool {
	if len(s) < 8 {
		return false
	}
	first := s[0]
	allSame := true
	for i := 1; i < len(s); i++ {
		if s[i] != first {
			allSame = false
			break
		}
	}
	return allSame
}

// DetectProvider routes tokens to the correct validator
func (tv *TokenValidator) DetectProvider(token string) string {
	switch {
	case strings.HasPrefix(token, "sk-ant-api03-"):
		return ProviderAnthropic
	case strings.HasPrefix(token, "sk-ant-"):
		return ProviderAnthropic
	case strings.HasPrefix(token, "sk-proj-"):
		return ProviderOpenAI
	case strings.HasPrefix(token, "sk-svcacct-"):
		return ProviderOpenAI
	case strings.HasPrefix(token, "sk-or-v1-"):
		// Must precede the generic "sk-" case below: OpenRouter keys start with
		// "sk-" and are 64+ chars, so the generic case swallowed them and they
		// were validated against OpenAI endpoints (and always reported invalid).
		return ProviderOpenRouter
	case strings.HasPrefix(token, "sk_live_"), strings.HasPrefix(token, "sk_test_"), strings.HasPrefix(token, "rk_live_"), strings.HasPrefix(token, "rk_test_"):
		// Must precede the "sk_" case below: Stripe secret keys start with the
		// same "sk_" as ElevenLabs, so the shorter prefix swallowed them and a
		// Stripe secret was validated against api.elevenlabs.io (and reported
		// invalid). Longest/most-specific prefix wins, matching verifiers.Detect.
		return ProviderStripe
	case strings.HasPrefix(token, "sk_"):
		// ElevenLabs keys use "sk_" (underscore), distinct from OpenAI's "sk-".
		return ProviderElevenLabs
	case strings.HasPrefix(token, "pk."), strings.HasPrefix(token, "sk."):
		// Mapbox public ("pk.") and secret ("sk.") tokens. The period keeps
		// these distinct from Stripe's "pk_live_"/"sk_live_" and ElevenLabs'
		// "sk_", so the exact prefix routes to Mapbox.
		return ProviderMapbox
	case strings.HasPrefix(token, "sk-") && len(token) > 20:
		// Could be OpenAI, DeepSeek, Together, etc.
		if FastMatch("deepseek_key", token) {
			return ProviderDeepSeek
		}
		return ProviderOpenAI
	case strings.HasPrefix(token, "AIzaSy"):
		return ProviderGoogle
	case strings.HasPrefix(token, "xai-"):
		return ProviderXAI
	case strings.HasPrefix(token, "hf_"):
		return ProviderHuggingFace
	case strings.HasPrefix(token, "gsk_"):
		return ProviderGroq
	case strings.HasPrefix(token, "pplx-"):
		return ProviderPerplexity
	case strings.HasPrefix(token, "r8_"):
		return ProviderReplicate
	case strings.HasPrefix(token, "dop_v1_"):
		return ProviderDigitalOcean
	case strings.HasPrefix(token, "ghp_") || strings.HasPrefix(token, "gho_") || strings.HasPrefix(token, "ghu_") || strings.HasPrefix(token, "ghs_") || strings.HasPrefix(token, "ghr_") || strings.HasPrefix(token, "github_pat_"):
		return ProviderGithub
	case strings.HasPrefix(token, "glpat-"):
		return ProviderGitLab
	case strings.HasPrefix(token, "dckr_pat_"):
		return ProviderDockerHub
	case strings.HasPrefix(token, "xoxb-"), strings.HasPrefix(token, "xoxp-"), strings.HasPrefix(token, "xoxa-"):
		return ProviderSlack
	case strings.HasPrefix(token, "pk_live_"), strings.HasPrefix(token, "pk_test_"):
		return ProviderStripe
	case strings.HasPrefix(token, "sq0"):
		return ProviderSquare
	case strings.HasPrefix(token, "AKIA"):
		return ProviderAWS
	case strings.HasPrefix(token, "npm_"):
		return ProviderNPM
	case strings.HasPrefix(token, "pypi-"):
		return ProviderPyPI
	case strings.HasPrefix(token, "ntn_"), strings.HasPrefix(token, "secret_"):
		// Notion integration tokens ("ntn_") and the legacy "secret_" format.
		return ProviderNotion
	case strings.HasPrefix(token, "lin_api_"):
		return ProviderLinear
	case strings.HasPrefix(token, "NRAK-"):
		// New Relic user API keys.
		return ProviderNewRelic
	case strings.HasPrefix(token, "SG."):
		return ProviderSendGrid
	case strings.HasPrefix(token, "key-"):
		return ProviderMailgun
	case strings.HasPrefix(token, "rzp_"):
		return ProviderRazorpay
	case strings.HasPrefix(token, "shodan_"):
		return ProviderShodan
	case strings.Contains(token, ":") && len(strings.Split(token, ":")) == 2:
		// Potential Telegram bot token
		if FastMatch("telegram_bot", token) {
			return ProviderTelegram
		}
	case len(token) == 59 && strings.Count(token, ".") == 2:
		// Potential Discord token
		if FastMatch("discord_token", token) {
			return ProviderDiscord
		}
	case strings.HasPrefix(token, "eyJ") && strings.Count(token, ".") == 2:
		return ProviderJWT
	}

	// Fall back to the verifier registry's prefix table for any prefix the
	// switch above does not name (for example "ghu_", "ghs_" or "xoxr-"), so
	// detection and the registry cannot drift apart.
	if v, ok := verifiers.Detect(token); ok {
		if provider, ok := verifierProviderNames[v.Name]; ok {
			return provider
		}
	}
	return ProviderUnknown
}

// providerVerifierNames maps a detected provider to the verifier registry entry
// that can execute its check. A provider absent from this map (an AWS format
// check, a JWT shape check, an unknown token) keeps the legacy ValidateToken
// path. The registry is the single source of truth for the endpoint and
// response interpretation; this table only bridges the provider constants.
var providerVerifierNames = map[string]string{
	ProviderOpenAI:      "openai",
	ProviderAnthropic:   "anthropic",
	ProviderGoogle:      "google",
	ProviderXAI:         "xai",
	ProviderOpenRouter:  "openrouter",
	ProviderHuggingFace: "huggingface",
	ProviderReplicate:   "replicate",
	ProviderElevenLabs:  "elevenlabs",
	ProviderDeepSeek:    "deepseek",
	ProviderGroq:        "groq",
	ProviderPerplexity:  "perplexity",
	// Together, Fireworks, Kimi and Qwen have no detectable prefix of their own;
	// they are probed by ValidateOpenAIToken after a generic "sk-" key fails its
	// detected verifier, so the mapping keeps the dashboard able to resolve
	// their category. Mistral and Cohere have no such fallback and are not
	// mapped: they are only reachable through an explicit `verifier:` entry.
	ProviderTogether:  "together",
	ProviderFireworks: "fireworks",
	ProviderKimi:      "moonshot",
	ProviderQwen:      "dashscope",
	ProviderGithub:    "github",
	ProviderGitLab:    "gitlab",
	ProviderDockerHub: "dockerhub",
	ProviderNPM:       "npm",
	ProviderDiscord:   "discord",
	ProviderSlack:     "slack",
	ProviderTelegram:  "telegram",
	ProviderStripe:    "stripe",
	ProviderSquare:    "square",
	ProviderSendGrid:  "sendgrid",
	ProviderMailgun:   "mailgun",
	// Mapbox, Notion, Linear and New Relic have documented key prefixes and are
	// dispatched by DetectProvider above.
	ProviderMapbox:       "mapbox",
	ProviderNotion:       "notion",
	ProviderLinear:       "linear",
	ProviderNewRelic:     "newrelic",
	ProviderDigitalOcean: "digitalocean",
}

// verifierProviderNames is the inverse of providerVerifierNames. DetectProvider
// uses it to translate a registry match back into the provider constant callers
// expect, extending detection to prefixes its switch does not list.
var verifierProviderNames = func() map[string]string {
	m := make(map[string]string, len(providerVerifierNames))
	for provider, name := range providerVerifierNames {
		m[name] = provider
	}
	return m
}()

// VerifierNameForProvider returns the registry verifier name that executes a
// detected provider's check, and whether the provider has one. It lets callers
// outside core (the dashboard) resolve a provider display name to its verifier
// without duplicating the provider->verifier table.
func VerifierNameForProvider(provider string) (string, bool) {
	name, ok := providerVerifierNames[strings.TrimSpace(provider)]
	return name, ok
}

// validateToken resolves a token's verdict. When the detected provider has a
// registry verifier the verifier runs (through the cache and domain-health
// tracker); otherwise the legacy ValidateToken switch is used and its verdict
// is wrapped in an Outcome so every caller sees the same shape.
func (tv *TokenValidator) validateToken(token string) (bool, string, string, verifiers.Outcome) {
	provider := tv.DetectProvider(token)
	if name, ok := providerVerifierNames[provider]; ok {
		if v, found := verifiers.Lookup(name); found {
			valid, p, detail, outcome := tv.verifyWithRegistry(v, token, provider)
			if valid || !openAICompatibleSK(token) {
				return valid, p, detail, outcome
			}
			// The one registry verifier ran against the detected provider
			// (usually OpenAI) and did not accept the key. A generic "sk-" key
			// can belong to any OpenAI-compatible provider, so probe the ordered
			// compatible-endpoint list before concluding invalid; otherwise a
			// valid Moonshot/Qwen/Together key is reported invalid, downgraded
			// to low severity and cached with the wrong verdict.
			if fvalid, fprovider, fdetail := tv.ValidateOpenAIToken(token); fvalid {
				return true, fprovider, fdetail, verifiers.Outcome{Status: verifiers.Valid, Detail: fdetail}
			}
			return valid, p, detail, outcome
		}
	}
	valid, p, response := tv.ValidateToken(token)
	return valid, p, response, verdictOutcome(valid, response)
}

// openAICompatibleSK reports whether token is a generic "sk-" key whose provider
// is ambiguous. OpenAI, DeepSeek, Moonshot, Qwen, Together, Groq, Perplexity,
// Fireworks and Anyscale all mint "sk-" keys, so one registry verifier cannot by
// itself prove the key is invalid. Anthropic ("sk-ant-"), OpenRouter
// ("sk-or-v1-"), OpenAI project ("sk-proj-") and service-account
// ("sk-svcacct-") keys also start with "sk-" but have their own endpoints and
// are excluded from the fallback probe.
func openAICompatibleSK(token string) bool {
	if !strings.HasPrefix(token, "sk-") {
		return false
	}
	for _, prefix := range []string{"sk-ant-", "sk-or-v1-", "sk-proj-", "sk-svcacct-"} {
		if strings.HasPrefix(token, prefix) {
			return false
		}
	}
	return true
}

// verifyWithRegistry runs a registry verifier for token, consulting the
// validation cache and domain-health tracker when they are configured. A
// protected host (an AI endpoint) is never short-circuited.
func (tv *TokenValidator) verifyWithRegistry(v verifiers.Verifier, token, provider string) (bool, string, string, verifiers.Outcome) {
	cred := verifiers.Credential{Key: token}
	base, host := verifierEndpoint(v, cred)

	if cv, ok := tv.Cache.Get(token, base); ok {
		o := outcomeFromCached(cv)
		return outcomeValid(o), provider, o.Detail, o
	}
	if tv.Cache.DomainDead(host) {
		o := verifiers.Outcome{Status: verifiers.Dead, Detail: "connection error: host " + host + " is marked dead"}
		return false, provider, o.Detail, o
	}

	o, _ := v.Verify(tv.httpClient, cred, base)
	// Only a definitive provider answer proves the host healthy. Dead is a
	// failure; Unverified (for example a missing companion credential returned
	// before any request was issued) must not revive a dead host, so it leaves
	// the domain counters untouched.
	switch o.Status {
	case verifiers.Dead:
		tv.Cache.RecordDomainFailure(host)
	case verifiers.Valid, verifiers.Invalid, verifiers.QuotaLimited:
		tv.Cache.RecordDomainSuccess(host)
	}
	tv.Cache.Put(token, base, cachedFromOutcome(provider, o))
	return outcomeValid(o), provider, o.Detail, o
}

// verifierEndpoint derives the base URL and host a verifier will contact
// without issuing the request, so the cache key and domain health are keyed on
// the real endpoint.
func verifierEndpoint(v verifiers.Verifier, c verifiers.Credential) (base, host string) {
	req, err := v.Build(c, "")
	if err != nil || req == nil || req.URL == nil {
		return "", ""
	}
	return req.URL.Scheme + "://" + req.URL.Host, req.URL.Host
}

// outcomeValid reports whether an Outcome means the credential exists and the
// finding should be kept. A rate-limited credential (429) is real, just
// throttled, so it counts as valid for match acceptance.
func outcomeValid(o verifiers.Outcome) bool {
	return o.Status == verifiers.Valid || o.Status == verifiers.QuotaLimited
}

// verdictOutcome wraps a legacy verdict as an Outcome.
func verdictOutcome(valid bool, detail string) verifiers.Outcome {
	if valid {
		return verifiers.Outcome{Status: verifiers.Valid, Detail: detail}
	}
	return verifiers.Outcome{Status: verifiers.Invalid, Detail: detail}
}

// cachedFromOutcome projects an Outcome onto the subset stored by the cache.
func cachedFromOutcome(provider string, o verifiers.Outcome) CachedValidation {
	return CachedValidation{
		Status:    string(o.Status),
		Provider:  provider,
		Tier:      o.Tier,
		Balance:   o.Balance,
		Identity:  o.Identity,
		RPM:       o.RPM,
		HighValue: o.HighValue,
	}
}

// outcomeFromCached rebuilds an Outcome from a cached validation.
func outcomeFromCached(cv CachedValidation) verifiers.Outcome {
	return verifiers.Outcome{
		Status:    verifiers.Status(cv.Status),
		Detail:    cv.Provider + ": cached",
		Tier:      cv.Tier,
		Balance:   cv.Balance,
		Identity:  cv.Identity,
		RPM:       cv.RPM,
		HighValue: cv.HighValue,
	}
}

// --- VALIDATORS ---

// ValidateOpenAIToken validates OpenAI, DeepSeek, Kimi, Qwen (OpenAI Compatible)
func (tv *TokenValidator) ValidateOpenAIToken(token string) (bool, string, string) {

	// Try OpenAI First
	valid, openAIMsg := tv.checkOpenAICompatible(token, "https://api.openai.com/v1/models", ProviderOpenAI)
	if valid {
		return true, ProviderOpenAI, openAIMsg
	}

	// DeepSeek Endpoint Check
	valid, msg := tv.checkOpenAICompatible(token, "https://api.deepseek.com/v1/models", ProviderDeepSeek)
	if valid {
		return true, ProviderDeepSeek, msg
	}

	// Kimi (Moonshot) Endpoint Check
	valid, msg = tv.checkOpenAICompatible(token, "https://api.moonshot.cn/v1/models", ProviderKimi)
	if valid {
		return true, ProviderKimi, msg
	}

	// Qwen (Alibaba) Endpoint Check
	valid, msg = tv.checkOpenAICompatible(token, "https://dashscope.aliyuncs.com/compatible-mode/v1/models", ProviderQwen)
	if valid {
		return true, ProviderQwen, msg
	}

	// Together AI Endpoint Check
	valid, msg = tv.checkOpenAICompatible(token, "https://api.together.xyz/v1/models", ProviderTogether)
	if valid {
		return true, ProviderTogether, msg
	}

	// Groq Endpoint Check
	valid, msg = tv.checkOpenAICompatible(token, "https://api.groq.com/openai/v1/models", ProviderGroq)
	if valid {
		return true, ProviderGroq, msg
	}

	// Perplexity Endpoint Check
	valid, msg = tv.checkOpenAICompatible(token, "https://api.perplexity.ai/models", ProviderPerplexity)
	if valid {
		return true, ProviderPerplexity, msg
	}

	// Fireworks Endpoint Check
	valid, msg = tv.checkOpenAICompatible(token, "https://api.fireworks.ai/v1/models", ProviderFireworks)
	if valid {
		return true, ProviderFireworks, msg
	}

	// Anyscale Endpoint Check
	valid, msg = tv.checkOpenAICompatible(token, "https://api.endpoints.anyscale.com/v1/models", ProviderAnyscale)
	if valid {
		return true, ProviderAnyscale, msg
	}

	// The final provider label is OpenAI but the message must be OpenAI's own
	// verdict, not the last endpoint (Anyscale) that happened to be tried.
	return false, ProviderOpenAI, openAIMsg
}

// checkOpenAICompatible is a helper for any provider using the OpenAI SDK spec
func (tv *TokenValidator) checkOpenAICompatible(token, url, provider string) (bool, string) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false, fmt.Sprintf("Req Err: %v", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	req.Header.Set("User-Agent", "shhgit-validator")

	resp, err := tv.httpClient.Do(req)
	if err != nil {
		return false, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))

	if resp.StatusCode == 200 {
		return true, fmt.Sprintf("Valid %s Token", provider)
	}
	return false, fmt.Sprintf("%s Invalid (%d) %s", provider, resp.StatusCode, string(body))
}

// ValidateAnthropicToken validates Claude
func (tv *TokenValidator) ValidateAnthropicToken(token string) (bool, string, string) {
	payload := map[string]interface{}{
		"model":      "claude-3-haiku-20240307",
		"max_tokens": 5,
		"messages":   []map[string]string{{"role": "user", "content": "Hi"}},
	}
	jsonPayload, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewBuffer(jsonPayload))
	req.Header.Set("x-api-key", token)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	resp, err := tv.httpClient.Do(req)
	if err != nil {
		return false, ProviderAnthropic, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return true, ProviderAnthropic, "Valid Anthropic Token"
	}
	return false, ProviderAnthropic, fmt.Sprintf("Invalid (%d)", resp.StatusCode)
}

// ValidateGoogleToken validates Gemini/Google Cloud
func (tv *TokenValidator) ValidateGoogleToken(token string) (bool, string, string) {
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models?key=%s", token)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false, ProviderGoogle, fmt.Sprintf("Req Err: %v", err)
	}
	resp, err := tv.httpClient.Do(req)
	if err != nil {
		return false, ProviderGoogle, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return true, ProviderGoogle, "Valid Google/Gemini Key"
	}
	return false, ProviderGoogle, fmt.Sprintf("Invalid (%d)", resp.StatusCode)
}

// ValidateXAIToken validates xAI/Grok
func (tv *TokenValidator) ValidateXAIToken(token string) (bool, string, string) {
	req, _ := http.NewRequest("GET", "https://api.x.ai/v1/models", nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

	resp, err := tv.httpClient.Do(req)
	if err != nil {
		return false, ProviderXAI, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return true, ProviderXAI, "Valid xAI Token"
	}
	return false, ProviderXAI, fmt.Sprintf("Invalid (%d)", resp.StatusCode)
}

// ValidateOpenRouterToken validates OpenRouter
func (tv *TokenValidator) ValidateOpenRouterToken(token string) (bool, string, string) {
	req, _ := http.NewRequest("GET", "https://openrouter.ai/api/v1/models", nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

	resp, err := tv.httpClient.Do(req)
	if err != nil {
		return false, ProviderOpenRouter, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return true, ProviderOpenRouter, "Valid OpenRouter Token"
	}
	return false, ProviderOpenRouter, fmt.Sprintf("Invalid (%d)", resp.StatusCode)
}

// ValidateHuggingFaceToken validates HuggingFace
func (tv *TokenValidator) ValidateHuggingFaceToken(token string) (bool, string, string) {
	req, _ := http.NewRequest("GET", "https://huggingface.co/api/whoami", nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

	resp, err := tv.httpClient.Do(req)
	if err != nil {
		return false, ProviderHuggingFace, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return true, ProviderHuggingFace, "Valid HuggingFace Token"
	}
	return false, ProviderHuggingFace, fmt.Sprintf("Invalid (%d)", resp.StatusCode)
}

// ValidateReplicateToken validates Replicate
func (tv *TokenValidator) ValidateReplicateToken(token string) (bool, string, string) {
	req, _ := http.NewRequest("GET", "https://api.replicate.com/v1/models", nil)
	req.Header.Set("Authorization", fmt.Sprintf("Token %s", token))

	resp, err := tv.httpClient.Do(req)
	if err != nil {
		return false, ProviderReplicate, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return true, ProviderReplicate, "Valid Replicate Token"
	}
	return false, ProviderReplicate, fmt.Sprintf("Invalid (%d)", resp.StatusCode)
}

// ValidateElevenLabsToken validates ElevenLabs
func (tv *TokenValidator) ValidateElevenLabsToken(token string) (bool, string, string) {
	req, _ := http.NewRequest("GET", "https://api.elevenlabs.io/v1/user", nil)
	req.Header.Set("xi-api-key", token)

	resp, err := tv.httpClient.Do(req)
	if err != nil {
		return false, ProviderElevenLabs, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return true, ProviderElevenLabs, "Valid ElevenLabs Token"
	}
	return false, ProviderElevenLabs, fmt.Sprintf("Invalid (%d)", resp.StatusCode)
}

// ValidateGithubToken validates GitHub PAT
func (tv *TokenValidator) ValidateGithubToken(token string) (bool, string, string) {
	req, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
	req.Header.Set("Authorization", fmt.Sprintf("token %s", token))
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	// GitHub rejects requests without a User-Agent header (403) before even
	// looking at the token, so without this every token would report invalid.
	req.Header.Set("User-Agent", fmt.Sprintf("%s v%s", Name, Version))

	resp, err := tv.httpClient.Do(req)
	if err != nil {
		return false, ProviderGithub, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return true, ProviderGithub, "Valid GitHub Token"
	}
	return false, ProviderGithub, fmt.Sprintf("Invalid (%d)", resp.StatusCode)
}

// discordAPIBase is overridable so the validation flow can be exercised against
// a local test server.
var discordAPIBase = "https://discord.com/api/v10"

// telegramAPIBase is overridable so the validation flow can be exercised
// against a local test server.
var telegramAPIBase = "https://api.telegram.org"

// ValidateDiscordToken validates a Discord bot or user token.
//
// Discord bot tokens must be sent as "Bot <token>"; user (OAuth) tokens go in
// the header bare. This used to send the bare token for both, so every valid
// bot token came back 401 and was reported invalid. The two schemes are tried
// in turn, and only an auth failure (401/403) triggers the second attempt.
func (tv *TokenValidator) ValidateDiscordToken(token string) (bool, string, string) {
	schemes := []string{"Bot ", ""}
	lastMsg := "Invalid Discord Token"

	for _, prefix := range schemes {
		req, err := http.NewRequest("GET", discordAPIBase+"/users/@me", nil)
		if err != nil {
			return false, ProviderDiscord, fmt.Sprintf("Req Err: %v", err)
		}
		req.Header.Set("Authorization", prefix+token)
		req.Header.Set("User-Agent", fmt.Sprintf("%s v%s", Name, Version))

		resp, err := tv.httpClient.Do(req)
		if err != nil {
			return false, ProviderDiscord, fmt.Sprintf("Net Err: %v", err)
		}

		if resp.StatusCode == 200 {
			resp.Body.Close()
			return true, ProviderDiscord, "Valid Discord Token"
		}
		lastMsg = fmt.Sprintf("Invalid (%d)", resp.StatusCode)
		resp.Body.Close()

		// A non-auth failure (e.g. 429 or 5xx) will not be fixed by swapping the
		// scheme, so stop rather than burn a second request.
		if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
			break
		}
	}
	return false, ProviderDiscord, lastMsg
}

// ValidateSlackToken validates Slack Bot/User Token
func (tv *TokenValidator) ValidateSlackToken(token string) (bool, string, string) {
	req, _ := http.NewRequest("GET", "https://slack.com/api/auth.test", nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

	resp, err := tv.httpClient.Do(req)
	if err != nil {
		return false, ProviderSlack, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if err != nil {
		return false, ProviderSlack, fmt.Sprintf("Read Err: %v", err)
	}
	// Slack returns 200 OK even if auth fails, so decide on the parsed "ok"
	// field, not a substring: a pretty-printed body (`"ok": true`, extra
	// whitespace) never contains `"ok":true` and misclassified valid tokens.
	var result struct {
		Ok bool `json:"ok"`
	}
	if resp.StatusCode == 200 && json.Unmarshal(body, &result) == nil && result.Ok {
		return true, ProviderSlack, "Valid Slack Token"
	}
	return false, ProviderSlack, fmt.Sprintf("Invalid (%s)", string(body))
}

// ValidateStripeToken validates Stripe Secret Key
func (tv *TokenValidator) ValidateStripeToken(token string) (bool, string, string) {
	req, _ := http.NewRequest("GET", "https://api.stripe.com/v1/charges?limit=1", nil)
	req.SetBasicAuth(token, "")

	resp, err := tv.httpClient.Do(req)
	if err != nil {
		return false, ProviderStripe, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return true, ProviderStripe, "Valid Stripe Key"
	} else if resp.StatusCode == 401 {
		return false, ProviderStripe, "Invalid Stripe Key"
	}
	return false, ProviderStripe, fmt.Sprintf("Status %d", resp.StatusCode)
}

// ValidateAWSToken validates AWS Access Key ID (Format Check + Optional STS)
func (tv *TokenValidator) ValidateAWSToken(token string) (bool, string, string) {
	if !FastMatch("aws_key", token) {
		return false, ProviderAWS, "Invalid AWS Key Format"
	}
	return true, ProviderAWS, "Valid AWS Key Format (Secret Required for Full Auth)"
}

// ValidateNPMToken validates NPM Token
func (tv *TokenValidator) ValidateNPMToken(token string) (bool, string, string) {
	req, _ := http.NewRequest("GET", "https://registry.npmjs.org/-/whoami", nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

	resp, err := tv.httpClient.Do(req)
	if err != nil {
		return false, ProviderNPM, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return true, ProviderNPM, "Valid NPM Token"
	}
	return false, ProviderNPM, fmt.Sprintf("Invalid (%d)", resp.StatusCode)
}

// ValidateTelegramToken validates Telegram Bot Token
func (tv *TokenValidator) ValidateTelegramToken(token string) (bool, string, string) {
	url := fmt.Sprintf("%s/bot%s/getMe", telegramAPIBase, token)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false, ProviderTelegram, fmt.Sprintf("Req Err: %v", err)
	}
	resp, err := tv.httpClient.Do(req)
	if err != nil {
		return false, ProviderTelegram, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode == 200 && strings.Contains(string(body), `"ok":true`) {
		return true, ProviderTelegram, "Valid Telegram Bot Token"
	}
	return false, ProviderTelegram, fmt.Sprintf("Invalid (%s)", string(body))
}

// ValidateSquareToken validates Square Access Token
func (tv *TokenValidator) ValidateSquareToken(token string) (bool, string, string) {
	req, _ := http.NewRequest("GET", "https://connect.squareup.com/v2/me", nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	req.Header.Set("Square-Version", "2023-12-13")

	resp, err := tv.httpClient.Do(req)
	if err != nil {
		return false, ProviderSquare, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return true, ProviderSquare, "Valid Square Token"
	}
	return false, ProviderSquare, fmt.Sprintf("Invalid (%d)", resp.StatusCode)
}

// ValidateSendGridToken validates SendGrid API Key
func (tv *TokenValidator) ValidateSendGridToken(token string) (bool, string, string) {
	req, _ := http.NewRequest("GET", "https://api.sendgrid.com/v3/user/profile", nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

	resp, err := tv.httpClient.Do(req)
	if err != nil {
		return false, ProviderSendGrid, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return true, ProviderSendGrid, "Valid SendGrid Key"
	}
	return false, ProviderSendGrid, fmt.Sprintf("Invalid (%d)", resp.StatusCode)
}

// ValidateDigitalOceanToken validates DigitalOcean Token
func (tv *TokenValidator) ValidateDigitalOceanToken(token string) (bool, string, string) {
	req, _ := http.NewRequest("GET", "https://api.digitalocean.com/v2/account", nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

	resp, err := tv.httpClient.Do(req)
	if err != nil {
		return false, ProviderDigitalOcean, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return true, ProviderDigitalOcean, "Valid DigitalOcean Token"
	}
	return false, ProviderDigitalOcean, fmt.Sprintf("Invalid (%d)", resp.StatusCode)
}

// ValidateJWT checks if a string is a valid JWT format
func (tv *TokenValidator) ValidateJWT(token string) (bool, string, string) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false, ProviderJWT, "Invalid JWT format (need 3 parts)"
	}
	if len(parts[0]) == 0 || len(parts[1]) == 0 || len(parts[2]) == 0 {
		return false, ProviderJWT, "Invalid JWT format (empty parts)"
	}
	return true, ProviderJWT, "Valid JWT Format"
}

// ValidateToken is the main dispatcher
func (tv *TokenValidator) ValidateToken(token string) (bool, string, string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return false, ProviderUnknown, "Empty token"
	}

	provider := tv.DetectProvider(token)

	switch provider {
	case ProviderOpenAI, ProviderDeepSeek, ProviderKimi, ProviderQwen, ProviderTogether, ProviderGroq, ProviderPerplexity, ProviderFireworks, ProviderAnyscale:
		return tv.ValidateOpenAIToken(token)
	case ProviderAnthropic:
		return tv.ValidateAnthropicToken(token)
	case ProviderGoogle:
		return tv.ValidateGoogleToken(token)
	case ProviderXAI:
		return tv.ValidateXAIToken(token)
	case ProviderOpenRouter:
		return tv.ValidateOpenRouterToken(token)
	case ProviderHuggingFace:
		return tv.ValidateHuggingFaceToken(token)
	case ProviderReplicate:
		return tv.ValidateReplicateToken(token)
	case ProviderElevenLabs:
		return tv.ValidateElevenLabsToken(token)
	case ProviderGithub:
		return tv.ValidateGithubToken(token)
	case ProviderDiscord:
		return tv.ValidateDiscordToken(token)
	case ProviderSlack:
		return tv.ValidateSlackToken(token)
	case ProviderStripe:
		return tv.ValidateStripeToken(token)
	case ProviderAWS:
		return tv.ValidateAWSToken(token)
	case ProviderNPM:
		return tv.ValidateNPMToken(token)
	case ProviderTelegram:
		return tv.ValidateTelegramToken(token)
	case ProviderSquare:
		return tv.ValidateSquareToken(token)
	case ProviderSendGrid:
		return tv.ValidateSendGridToken(token)
	case ProviderDigitalOcean:
		return tv.ValidateDigitalOceanToken(token)
	case ProviderJWT:
		return tv.ValidateJWT(token)
	default:
		return false, ProviderUnknown, "Unrecognized token format"
	}
}

// SaveValidToken saves a valid token to priority3.log.
//
// The file is the operator's loot file: it stores the FULL token so a
// validated credential can actually be recovered and reused afterwards. The
// console output stays masked (callers only print short display prefixes), and
// the per-signature CSVs under logs/matches/ plus the --scan-keys key files
// already carry full secrets, so this matches the existing sinks. Writing only
// a masked prefix (the old behaviour) made validated tokens unrecoverable:
// they "validated" but never showed up usably in the logs.
func (tv *TokenValidator) SaveValidToken(token string, provider string, response string) error {
	logDir := "logs"
	// The loot file holds plaintext credentials, so the directory and file are
	// owner-only (like key_validator's key files and the review store), not
	// world-readable.
	if err := os.MkdirAll(logDir, 0700); err != nil {
		return err
	}

	logFile := filepath.Join(logDir, "priority3.log")
	timestamp := time.Now().Format("2006-01-02 15:04:05")

	entry := fmt.Sprintf("[%s] Provider: %s | Token: %s | Response: %s\n",
		timestamp, provider, token, response)

	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	// O_CREATE does not change the mode of a file an earlier release created
	// world-readable, so tighten it (best effort: never fail the save on it).
	_ = f.Chmod(0600)

	_, err = f.WriteString(entry)
	return err
}

// maxTestedTokens bounds the in-memory verdict cache so a long-running scan
// over attacker-controlled content cannot grow it without limit.
const maxTestedTokens = 50000

// TestAndLogToken tests a single token, logs the outcome and returns whether it
// is valid. The verdict is cached, so a repeated match for the same token is
// answered from the cache instead of re-hitting the provider.
func (tv *TokenValidator) TestAndLogToken(token string, sigColor string) bool {
	tv.mutex.Lock()
	if res, ok := tv.testedTokens[token]; ok {
		// A reserved entry means a validation for this token is already in
		// flight. Wait for its verdict and return that, rather than the
		// placeholder value (previously hard-coded true, which told a
		// concurrent duplicate the token was valid before the provider had
		// answered).
		if res.ready != nil {
			ready := res.ready
			tv.mutex.Unlock()
			<-ready
			tv.mutex.Lock()
			final := tv.testedTokens[token]
			tv.mutex.Unlock()
			return final.valid
		}
		tv.mutex.Unlock()
		return res.valid
	}
	// Reserve the token so a concurrent worker does not start a second call.
	// The channel is closed once the real verdict is stored, waking any waiter.
	ready := make(chan struct{})
	tv.testedTokens[token] = tokenTestResult{ready: ready}
	tv.mutex.Unlock()

	displayToken := token
	if len(token) > 20 {
		displayToken = token[:20] + "..."
	}

	// Pre-detect provider for logging
	providerHint := tv.DetectProvider(token)
	if tv.logger != nil {
		tv.logger.Info("🔍 Testing [%s]: %s", providerHint, displayToken)
	}

	release := tv.acquireValidationSlot()
	valid, provider, response, outcome := tv.validateToken(token)
	release()

	tv.mutex.Lock()
	// Bound the verdict cache so a long-running scan over attacker-controlled
	// content cannot grow it without limit. Completed entries are evicted once
	// the cap is reached; in-flight reservations (ready != nil) are left intact
	// so their waiters still receive a verdict.
	if len(tv.testedTokens) >= maxTestedTokens {
		for k, r := range tv.testedTokens {
			if r.ready == nil {
				delete(tv.testedTokens, k)
			}
		}
	}
	tv.testedTokens[token] = tokenTestResult{valid: valid, provider: provider, outcome: outcome}
	tv.mutex.Unlock()
	close(ready)

	if valid {
		color := sigColor
		if color == "" {
			color = "#10A37F"
		}
		if tv.logger != nil {
			tv.logger.LogWithColor(color, "✓ %s token is VALID!", provider)
			tv.logger.Important("Response: %s", response)

			if err := tv.SaveValidToken(token, provider, response); err != nil {
				tv.logger.Error("Failed to save valid token: %v", err)
			} else {
				tv.logger.Important("✅ Valid token saved to logs/priority3.log")
			}
		}
	} else if tv.logger != nil {
		tv.logger.LogWithColor("#FF0000", "✗ %s token is INVALID", provider)
		tv.logger.Warn("Response: %s", response)
	}

	// Notify TUI/web of live token validation result
	if tv.OnTokenResult != nil {
		masked := token
		if len(token) > 20 {
			masked = token[:17] + "..."
		}
		tv.OnTokenResult(masked, valid, provider)
	}

	// Second, non-breaking callback carrying the richer verifier outcome.
	if tv.OnOutcome != nil {
		masked := token
		if len(token) > 20 {
			masked = token[:17] + "..."
		}
		tv.OnOutcome(masked, provider, outcome)
	}

	return valid
}

// TestMatchedTokens extracts and tests tokens from a match
func (tv *TokenValidator) TestMatchedTokens(matches []string, sigColor string) {
	for _, match := range matches {
		tokens := tv.ExtractTokensFromMatch(match)
		for _, token := range tokens {
			tv.TestAndLogToken(token, sigColor)
		}
	}
}

// TestTokens tests a list of tokens (for manual testing). It tolerates a nil
// logger (NewTokenValidator(nil) is a supported construction) the same way
// TestAndLogToken does.
func (tv *TokenValidator) TestTokens(tokens []string) {
	if len(tokens) == 0 {
		if tv.logger != nil {
			tv.logger.Warn("No tokens to test")
		}
		return
	}

	if tv.logger != nil {
		tv.logger.Important("Testing %d token(s)...", len(tokens))
	}

	for i, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}

		displayToken := token
		if len(token) > 20 {
			displayToken = token[:20] + "..."
		}

		providerHint := tv.DetectProvider(token)
		if tv.logger != nil {
			tv.logger.Info("[%d/%d] Testing [%s]: %s...", i+1, len(tokens), providerHint, displayToken)
		}

		valid, provider, response := tv.ValidateToken(token)

		if valid {
			if tv.logger != nil {
				tv.logger.LogWithColor("#10A37F", "✓ %s token is VALID", provider)
				tv.logger.Important("Response: %s", response)
			}

			if err := tv.SaveValidToken(token, provider, response); err != nil {
				if tv.logger != nil {
					tv.logger.Error("Failed to save valid token: %v", err)
				}
			}
		} else if tv.logger != nil {
			tv.logger.LogWithColor("#FF0000", "✗ %s token is INVALID", provider)
			tv.logger.Warn("Response: %s", response)
		}
	}

	if tv.logger != nil {
		tv.logger.Important("Token validation complete.")
	}
}
