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

	// Security/Other
	ProviderShodan      = "Shodan"
	ProviderVirusTotal  = "VirusTotal"
	ProviderIPInfo      = "IPInfo"
	ProviderAbstractAPI = "AbstractAPI"
	ProviderJWT         = "JWT"
	ProviderGeneric     = "Generic API Key"
	ProviderUnknown     = "Unknown"
)

// TokenValidator handles validation of AI API tokens and other secrets
type TokenValidator struct {
	logger       *Logger
	testedTokens map[string]bool
	mutex        sync.Mutex
	httpClient   *http.Client

	// Rate Limiting
	rateLimitMutex sync.Mutex
	lastRequest    time.Time
	minInterval    time.Duration

	// OnTokenResult is called when a token validation completes (for TUI/web live updates).
	OnTokenResult func(token string, valid bool, provider string)
}

// NewTokenValidator creates a new token validator
func NewTokenValidator(logger *Logger) *TokenValidator {
	return &TokenValidator{
		logger:       logger,
		testedTokens: make(map[string]bool),
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		minInterval: 1200 * time.Millisecond, // ~50 requests/min safe limit
	}
}

// waitForRateLimit prevents API bans
func (tv *TokenValidator) waitForRateLimit() {
	tv.rateLimitMutex.Lock()
	defer tv.rateLimitMutex.Unlock()

	now := time.Now()
	timeSinceLast := now.Sub(tv.lastRequest)

	if timeSinceLast < tv.minInterval {
		sleepTime := tv.minInterval - timeSinceLast
		tv.rateLimitMutex.Unlock()
		time.Sleep(sleepTime)
		tv.rateLimitMutex.Lock()
	}

	tv.lastRequest = time.Now()
}

// HasBeenTested checks if a token has already been tested
func (tv *TokenValidator) HasBeenTested(token string) bool {
	tv.mutex.Lock()
	defer tv.mutex.Unlock()
	return tv.testedTokens[token]
}

// MarkAsTested marks a token as tested
func (tv *TokenValidator) MarkAsTested(token string) {
	tv.mutex.Lock()
	defer tv.mutex.Unlock()
	tv.testedTokens[token] = true
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
	cohereRegex     = regexp.MustCompile(`[A-Za-z0-9]{40}(_[A-Za-z0-9]{10,})?`)
	ai21Regex       = regexp.MustCompile(`[A-Za-z0-9]{32}`)
	elevenRegex     = regexp.MustCompile(`sk_[a-z0-9]{48}`)
	togetherRegex   = regexp.MustCompile(`sk-[a-zA-Z0-9]{86}`)
	sambaRegex      = regexp.MustCompile(`[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}`)
	fireworksRegex  = regexp.MustCompile(`[A-Za-z0-9]{48}`)
	pineconeRegex   = regexp.MustCompile(`[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}`)
	stabilityRegex  = regexp.MustCompile(`sk-[a-zA-Z0-9]{40,}`)
	assemblyRegex   = regexp.MustCompile(`[a-f0-9]{32}`)
	deepgramRegex   = regexp.MustCompile(`[a-f0-9]{40}`)
	azureRegex      = regexp.MustCompile(`[a-f0-9]{32}`)

	// Cloud Providers
	awsRegex       = regexp.MustCompile(`AKIA[0-9A-Z]{16}`)
	awsSecretRegex = regexp.MustCompile(`[A-Za-z0-9/+=]{40}`)
	doRegex        = regexp.MustCompile(`dop_v1_[a-f0-9]{64}`)
	herokuRegex    = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	cfRegex        = regexp.MustCompile(`[A-Za-z0-9_-]{40}`)
	vercelRegex    = regexp.MustCompile(`[A-Za-z0-9]{24}`)

	// Developer Platforms
	githubRegex = regexp.MustCompile(`(ghp_[a-zA-Z0-9_]{36}|gho_[a-zA-Z0-9_]{36}|github_pat_[a-zA-Z0-9_]{82})`)
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
	postmarkRegex = regexp.MustCompile(`[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}`)
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
		// Cohere keys are typically 40 chars
		if len(t) == 40 || strings.Contains(t, "_") {
			addUnique(t)
		}
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

	// 15. Together AI (sk-... with specific length)
	for _, t := range togetherRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 16. SambaNova
	for _, t := range sambaRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 17. Fireworks
	for _, t := range fireworksRegex.FindAllString(match, -1) {
		if len(t) == 48 && !isLikelyFalsePositive(t) {
			addUnique(t)
		}
	}

	// 18. Pinecone
	for _, t := range pineconeRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 19. Stability AI
	for _, t := range stabilityRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 20. AssemblyAI
	for _, t := range assemblyRegex.FindAllString(match, -1) {
		addUnique(t)
	}

	// 21. Deepgram
	for _, t := range deepgramRegex.FindAllString(match, -1) {
		if len(t) == 40 {
			addUnique(t)
		}
	}

	// 22. Azure OpenAI
	for _, t := range azureRegex.FindAllString(match, -1) {
		if len(t) == 32 {
			addUnique(t)
		}
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

	// 26. Heroku
	for _, t := range herokuRegex.FindAllString(match, -1) {
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

	// 40. Postmark
	for _, t := range postmarkRegex.FindAllString(match, -1) {
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
	case strings.HasPrefix(token, "sk-") && len(token) > 20:
		// Could be OpenAI, DeepSeek, Together, Stability, etc.
		if FastMatch("deepseek_key", token) {
			return ProviderDeepSeek
		}
		return ProviderOpenAI
	case strings.HasPrefix(token, "AIzaSy"):
		return ProviderGoogle
	case strings.HasPrefix(token, "xai-"):
		return ProviderXAI
	case strings.HasPrefix(token, "sk-or-v1-"):
		return ProviderOpenRouter
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
	case strings.HasPrefix(token, "ghp_") || strings.HasPrefix(token, "gho_") || strings.HasPrefix(token, "github_pat_"):
		return ProviderGithub
	case strings.HasPrefix(token, "glpat-"):
		return ProviderGitLab
	case strings.HasPrefix(token, "dckr_pat_"):
		return ProviderDockerHub
	case strings.HasPrefix(token, "xoxb-"), strings.HasPrefix(token, "xoxp-"), strings.HasPrefix(token, "xoxa-"):
		return ProviderSlack
	case strings.HasPrefix(token, "sk_live_"), strings.HasPrefix(token, "sk_test_"), strings.HasPrefix(token, "rk_live_"), strings.HasPrefix(token, "rk_test_"):
		return ProviderStripe
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
	return ProviderUnknown
}

// --- VALIDATORS ---

// ValidateOpenAIToken validates OpenAI, DeepSeek, Kimi, Qwen (OpenAI Compatible)
func (tv *TokenValidator) ValidateOpenAIToken(token string) (bool, string, string) {
	tv.waitForRateLimit()

	// Try OpenAI First
	valid, msg := tv.checkOpenAICompatible(token, "https://api.openai.com/v1/models", ProviderOpenAI)
	if valid {
		return true, ProviderOpenAI, msg
	}

	// DeepSeek Endpoint Check
	tv.waitForRateLimit()
	valid, msg = tv.checkOpenAICompatible(token, "https://api.deepseek.com/v1/models", ProviderDeepSeek)
	if valid {
		return true, ProviderDeepSeek, msg
	}

	// Kimi (Moonshot) Endpoint Check
	tv.waitForRateLimit()
	valid, msg = tv.checkOpenAICompatible(token, "https://api.moonshot.cn/v1/models", ProviderKimi)
	if valid {
		return true, ProviderKimi, msg
	}

	// Qwen (Alibaba) Endpoint Check
	tv.waitForRateLimit()
	valid, msg = tv.checkOpenAICompatible(token, "https://dashscope.aliyuncs.com/compatible-mode/v1/models", ProviderQwen)
	if valid {
		return true, ProviderQwen, msg
	}

	// Together AI Endpoint Check
	tv.waitForRateLimit()
	valid, msg = tv.checkOpenAICompatible(token, "https://api.together.xyz/v1/models", ProviderTogether)
	if valid {
		return true, ProviderTogether, msg
	}

	// Groq Endpoint Check
	tv.waitForRateLimit()
	valid, msg = tv.checkOpenAICompatible(token, "https://api.groq.com/openai/v1/models", ProviderGroq)
	if valid {
		return true, ProviderGroq, msg
	}

	// Perplexity Endpoint Check
	tv.waitForRateLimit()
	valid, msg = tv.checkOpenAICompatible(token, "https://api.perplexity.ai/models", ProviderPerplexity)
	if valid {
		return true, ProviderPerplexity, msg
	}

	// Fireworks Endpoint Check
	tv.waitForRateLimit()
	valid, msg = tv.checkOpenAICompatible(token, "https://api.fireworks.ai/v1/models", ProviderFireworks)
	if valid {
		return true, ProviderFireworks, msg
	}

	// Anyscale Endpoint Check
	tv.waitForRateLimit()
	valid, msg = tv.checkOpenAICompatible(token, "https://api.endpoints.anyscale.com/v1/models", ProviderAnyscale)
	if valid {
		return true, ProviderAnyscale, msg
	}

	return false, ProviderOpenAI, msg
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

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 200 {
		return true, fmt.Sprintf("Valid %s Token", provider)
	}
	return false, fmt.Sprintf("%s Invalid (%d) %s", provider, resp.StatusCode, string(body))
}

// ValidateAnthropicToken validates Claude
func (tv *TokenValidator) ValidateAnthropicToken(token string) (bool, string, string) {
	tv.waitForRateLimit()
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
	tv.waitForRateLimit()
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models?key=%s", token)

	resp, err := tv.httpClient.Get(url)
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
	tv.waitForRateLimit()
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
	tv.waitForRateLimit()
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
	tv.waitForRateLimit()
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
	tv.waitForRateLimit()
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
	tv.waitForRateLimit()
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

// ValidateStabilityToken validates Stability AI
func (tv *TokenValidator) ValidateStabilityToken(token string) (bool, string, string) {
	tv.waitForRateLimit()
	req, _ := http.NewRequest("GET", "https://api.stability.ai/v1/user/account", nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

	resp, err := tv.httpClient.Do(req)
	if err != nil {
		return false, ProviderStability, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return true, ProviderStability, "Valid Stability AI Token"
	}
	return false, ProviderStability, fmt.Sprintf("Invalid (%d)", resp.StatusCode)
}

// ValidateAssemblyAIToken validates AssemblyAI
func (tv *TokenValidator) ValidateAssemblyAIToken(token string) (bool, string, string) {
	tv.waitForRateLimit()
	req, _ := http.NewRequest("GET", "https://api.assemblyai.com/v2/account", nil)
	req.Header.Set("Authorization", token)

	resp, err := tv.httpClient.Do(req)
	if err != nil {
		return false, ProviderAssemblyAI, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return true, ProviderAssemblyAI, "Valid AssemblyAI Token"
	}
	return false, ProviderAssemblyAI, fmt.Sprintf("Invalid (%d)", resp.StatusCode)
}

// ValidateDeepgramToken validates Deepgram
func (tv *TokenValidator) ValidateDeepgramToken(token string) (bool, string, string) {
	tv.waitForRateLimit()
	req, _ := http.NewRequest("GET", "https://api.deepgram.com/v1/projects", nil)
	req.Header.Set("Authorization", fmt.Sprintf("Token %s", token))

	resp, err := tv.httpClient.Do(req)
	if err != nil {
		return false, ProviderDeepgram, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return true, ProviderDeepgram, "Valid Deepgram Token"
	}
	return false, ProviderDeepgram, fmt.Sprintf("Invalid (%d)", resp.StatusCode)
}

// ValidateGithubToken validates GitHub PAT
func (tv *TokenValidator) ValidateGithubToken(token string) (bool, string, string) {
	tv.waitForRateLimit()
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

// ValidateDiscordToken validates Discord Bot/User Token
func (tv *TokenValidator) ValidateDiscordToken(token string) (bool, string, string) {
	tv.waitForRateLimit()
	req, _ := http.NewRequest("GET", "https://discord.com/api/users/@me", nil)
	req.Header.Set("Authorization", token)

	resp, err := tv.httpClient.Do(req)
	if err != nil {
		return false, ProviderDiscord, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return true, ProviderDiscord, "Valid Discord Token"
	}
	return false, ProviderDiscord, fmt.Sprintf("Invalid (%d)", resp.StatusCode)
}

// ValidateSlackToken validates Slack Bot/User Token
func (tv *TokenValidator) ValidateSlackToken(token string) (bool, string, string) {
	tv.waitForRateLimit()
	req, _ := http.NewRequest("GET", "https://slack.com/api/auth.test", nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))

	resp, err := tv.httpClient.Do(req)
	if err != nil {
		return false, ProviderSlack, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	// Slack returns 200 OK even if auth fails, check body for "ok": false
	if resp.StatusCode == 200 && strings.Contains(string(body), `"ok":true`) {
		return true, ProviderSlack, "Valid Slack Token"
	}
	return false, ProviderSlack, fmt.Sprintf("Invalid (%s)", string(body))
}

// ValidateStripeToken validates Stripe Secret Key
func (tv *TokenValidator) ValidateStripeToken(token string) (bool, string, string) {
	tv.waitForRateLimit()
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
	tv.waitForRateLimit()
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
	tv.waitForRateLimit()
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getMe", token)

	resp, err := tv.httpClient.Get(url)
	if err != nil {
		return false, ProviderTelegram, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 200 && strings.Contains(string(body), `"ok":true`) {
		return true, ProviderTelegram, "Valid Telegram Bot Token"
	}
	return false, ProviderTelegram, fmt.Sprintf("Invalid (%s)", string(body))
}

// ValidateSquareToken validates Square Access Token
func (tv *TokenValidator) ValidateSquareToken(token string) (bool, string, string) {
	tv.waitForRateLimit()
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
	tv.waitForRateLimit()
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
	tv.waitForRateLimit()
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

// ValidateHerokuToken validates Heroku API Key
func (tv *TokenValidator) ValidateHerokuToken(token string) (bool, string, string) {
	tv.waitForRateLimit()
	req, _ := http.NewRequest("GET", "https://api.heroku.com/account", nil)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	req.Header.Set("Accept", "application/vnd.heroku+json; version=3")

	resp, err := tv.httpClient.Do(req)
	if err != nil {
		return false, ProviderHeroku, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return true, ProviderHeroku, "Valid Heroku Token"
	}
	return false, ProviderHeroku, fmt.Sprintf("Invalid (%d)", resp.StatusCode)
}

// ValidateShodanToken validates Shodan API Key
func (tv *TokenValidator) ValidateShodanToken(token string) (bool, string, string) {
	tv.waitForRateLimit()
	url := fmt.Sprintf("https://api.shodan.io/api-info?key=%s", token)

	resp, err := tv.httpClient.Get(url)
	if err != nil {
		return false, ProviderShodan, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return true, ProviderShodan, "Valid Shodan Key"
	}
	return false, ProviderShodan, fmt.Sprintf("Invalid (%d)", resp.StatusCode)
}

// ValidateIPInfoToken validates IPInfo API Key
func (tv *TokenValidator) ValidateIPInfoToken(token string) (bool, string, string) {
	tv.waitForRateLimit()
	url := fmt.Sprintf("https://ipinfo.io/me?token=%s", token)

	resp, err := tv.httpClient.Get(url)
	if err != nil {
		return false, ProviderIPInfo, fmt.Sprintf("Net Err: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return true, ProviderIPInfo, "Valid IPInfo Key"
	}
	return false, ProviderIPInfo, fmt.Sprintf("Invalid (%d)", resp.StatusCode)
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
	case ProviderStability:
		return tv.ValidateStabilityToken(token)
	case ProviderAssemblyAI:
		return tv.ValidateAssemblyAIToken(token)
	case ProviderDeepgram:
		return tv.ValidateDeepgramToken(token)
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
	case ProviderHeroku:
		return tv.ValidateHerokuToken(token)
	case ProviderShodan:
		return tv.ValidateShodanToken(token)
	case ProviderIPInfo:
		return tv.ValidateIPInfoToken(token)
	case ProviderJWT:
		return tv.ValidateJWT(token)
	default:
		// For unknown tokens, check if it's a valid JWT
		if strings.Count(token, ".") == 2 && strings.HasPrefix(token, "eyJ") {
			return tv.ValidateJWT(token)
		}
		return false, ProviderUnknown, "Unrecognized token format"
	}
}

// SaveValidToken saves a valid token to priority3.log
func (tv *TokenValidator) SaveValidToken(token string, provider string, response string) error {
	logDir := "logs"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return err
	}

	logFile := filepath.Join(logDir, "priority3.log")
	timestamp := time.Now().Format("2006-01-02 15:04:05")

	maskedToken := tv.maskToken(token)
	entry := fmt.Sprintf("[%s] Provider: %s | Token: %s | Response: %s\n",
		timestamp, provider, maskedToken, response)

	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString(entry)
	return err
}

// maskToken safely masks a token for logging
func (tv *TokenValidator) maskToken(token string) string {
	if len(token) <= 12 {
		return strings.Repeat("*", len(token))
	}
	return token[:8] + "..." + token[len(token)-4:]
}

// TestAndLogToken tests a single token and logs the result
func (tv *TokenValidator) TestAndLogToken(token string, sigColor string) {
	if tv.HasBeenTested(token) {
		return
	}
	tv.MarkAsTested(token)

	displayToken := token
	if len(token) > 20 {
		displayToken = token[:20] + "..."
	}

	// Pre-detect provider for logging
	providerHint := tv.DetectProvider(token)
	tv.logger.Info("🔍 Testing [%s]: %s", providerHint, displayToken)

	valid, provider, response := tv.ValidateToken(token)

	if valid {
		color := sigColor
		if color == "" {
			color = "#10A37F"
		}
		tv.logger.LogWithColor(color, "✓ %s token is VALID!", provider)
		tv.logger.Important("Response: %s", response)

		if err := tv.SaveValidToken(token, provider, response); err != nil {
			tv.logger.Error("Failed to save valid token: %v", err)
		} else {
			tv.logger.Important("✅ Valid token saved to logs/priority3.log")
		}
	} else {
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

// TestTokens tests a list of tokens (for manual testing)
func (tv *TokenValidator) TestTokens(tokens []string) {
	if len(tokens) == 0 {
		tv.logger.Warn("No tokens to test")
		return
	}

	tv.logger.Important("Testing %d token(s)...", len(tokens))

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
		tv.logger.Info("[%d/%d] Testing [%s]: %s...", i+1, len(tokens), providerHint, displayToken)

		valid, provider, response := tv.ValidateToken(token)

		if valid {
			tv.logger.LogWithColor("#10A37F", "✓ %s token is VALID", provider)
			tv.logger.Important("Response: %s", response)

			if err := tv.SaveValidToken(token, provider, response); err != nil {
				tv.logger.Error("Failed to save valid token: %v", err)
			}
		} else {
			tv.logger.LogWithColor("#FF0000", "✗ %s token is INVALID", provider)
			tv.logger.Warn("Response: %s", response)
		}
	}

	tv.logger.Important("Token validation complete.")
}
