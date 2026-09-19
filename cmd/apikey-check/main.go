package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/term"
)

// ─── ANSI / Color Constants ───────────────────────────────────────────

const (
	reset       = "\033[0m"
	bold        = "\033[1m"
	dim         = "\033[2m"
	red         = "\033[31m"
	green       = "\033[32m"
	yellow      = "\033[33m"
	blue        = "\033[34m"
	magenta     = "\033[35m"
	cyan        = "\033[36m"
	white       = "\033[37m"
	brightRed   = "\033[91m"
	brightGreen = "\033[92m"
	brightCyan  = "\033[96m"
	bgRed       = "\033[41m"
	bgGreen     = "\033[42m"
	bgYellow    = "\033[43m"

	iconCheck   = "✅"
	iconCross   = "❌"
	iconWarn    = "⚠️"
	iconKey     = "🔑"
	iconMoney   = "💰"
	iconClock   = "⏳"
	iconGlobe   = "🌐"
	iconLock    = "🔒"
	iconSparkle = "✨"
	iconRocket  = "🚀"
	iconStar    = "⭐"
	iconZap     = "⚡"
	iconFire    = "🔥"
	iconPackage = "📦"
	iconSearch  = "🔍"
)

// ─── Provider Definition ──────────────────────────────────────────────

type AuthType int

const (
	AuthBearer AuthType = iota
	AuthAPIKey
	AuthXAPIKey
	AuthBasic
	AuthToken
	AuthURLParam
	AuthGitHubToken
	AuthGitLabToken
	AuthOpenAIProject
	AuthAnthropicKey
)

type CheckResult struct {
	Provider    string
	DisplayName string
	Status      string // "valid", "invalid", "error", "skipped", "no_key"
	Balance     string
	Credits     string
	Usage       string
	Tier        string
	Detail      string
	Latency     time.Duration
	Endpoint    string
}

type Provider struct {
	ID              string   // internal ID
	DisplayName     string   // pretty name
	Category        string   // "AI/LLM", "Cloud", "DevTools", etc.
	BaseURL         string   // API base URL
	CheckPath       string   // endpoint to check key validity
	CheckMethod     string   // GET, POST, etc.
	AuthType        AuthType // how to authenticate
	AuthHeader      string   // header name for auth
	AuthPrefix      string   // "Bearer ", "token ", etc.
	EnvVars         []string // environment variable names
	KeyPrefixes     []string // known key prefix patterns
	DocsURL         string   // provider docs link
	CanCheckBalance bool     // whether balance/usage can be queried
	BalancePath     string   // path for balance/usage info
}

// ─── Provider Registry ─────────────────────────────────────────────────

var providers = []Provider{
	// ═══════════ MAJOR AI PROVIDERS ═══════════
	{
		ID: "openai", DisplayName: "OpenAI", Category: "AI/LLM",
		BaseURL: "https://api.openai.com", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars:         []string{"OPENAI_API_KEY", "OPENAI_KEY", "OPENAI_SECRET_KEY"},
		KeyPrefixes:     []string{"sk-"},
		CanCheckBalance: true, BalancePath: "/v1/organization/usage?date=",
	},
	{
		ID: "anthropic", DisplayName: "Anthropic", Category: "AI/LLM",
		BaseURL: "https://api.anthropic.com", CheckPath: "/v1/messages", CheckMethod: "POST",
		AuthType: AuthAnthropicKey, AuthHeader: "x-api-key", AuthPrefix: "",
		EnvVars:         []string{"ANTHROPIC_API_KEY", "CLAUDE_API_KEY"},
		KeyPrefixes:     []string{"sk-ant-"},
		CanCheckBalance: true, BalancePath: "/v1/messages?beta=true",
	},
	{
		ID: "google", DisplayName: "Google/Gemini", Category: "AI/LLM",
		BaseURL: "https://generativelanguage.googleapis.com", CheckPath: "/v1beta/models", CheckMethod: "GET",
		AuthType: AuthURLParam, AuthHeader: "", AuthPrefix: "",
		EnvVars:     []string{"GOOGLE_API_KEY", "GEMINI_API_KEY", "GOOGLE_AI_STUDIO_KEY"},
		KeyPrefixes: []string{"AIza"},
	},
	{
		ID: "xai", DisplayName: "xAI / Grok", Category: "AI/LLM",
		BaseURL: "https://api.x.ai", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars:     []string{"XAI_API_KEY", "GROK_API_KEY"},
		KeyPrefixes: []string{"xai-"},
	},
	{
		ID: "deepseek", DisplayName: "DeepSeek", Category: "AI/LLM",
		BaseURL: "https://api.deepseek.com", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars:     []string{"DEEPSEEK_API_KEY", "DEEPSEEK_KEY"},
		KeyPrefixes: []string{"sk-"},
	},
	{
		ID: "openrouter", DisplayName: "OpenRouter", Category: "AI/LLM",
		BaseURL: "https://openrouter.ai", CheckPath: "/api/v1/auth/key", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars:         []string{"OPENROUTER_API_KEY", "OPENROUTER_KEY"},
		KeyPrefixes:     []string{"sk-or-v1-"},
		CanCheckBalance: true, BalancePath: "/api/v1/auth/key",
	},

	// ═══════════ INFERENCE / HOSTING PROVIDERS ═══════════
	{
		ID: "groq", DisplayName: "Groq", Category: "AI/LLM",
		BaseURL: "https://api.groq.com", CheckPath: "/openai/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars:     []string{"GROQ_API_KEY", "GROQ_KEY"},
		KeyPrefixes: []string{"gsk_"},
	},
	{
		ID: "together", DisplayName: "Together AI", Category: "AI/LLM",
		BaseURL: "https://api.together.xyz", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars:     []string{"TOGETHER_API_KEY", "TOGETHER_KEY"},
		KeyPrefixes: []string{"sk-"},
	},
	{
		ID: "fireworks", DisplayName: "Fireworks AI", Category: "AI/LLM",
		BaseURL: "https://api.fireworks.ai", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"FIREWORKS_API_KEY", "FIREWORKS_KEY"},
	},
	{
		ID: "deepinfra", DisplayName: "Deep Infra", Category: "AI/LLM",
		BaseURL: "https://api.deepinfra.com", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"DEEPINFRA_API_KEY", "DEEPINFRA_KEY"},
	},
	{
		ID: "cerebras", DisplayName: "Cerebras", Category: "AI/LLM",
		BaseURL: "https://api.cerebras.ai", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"CEREBRAS_API_KEY", "CEREBRAS_KEY"},
	},
	{
		ID: "baseten", DisplayName: "Baseten", Category: "AI/LLM",
		BaseURL: "https://api.baseten.co", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthAPIKey, AuthHeader: "Authorization", AuthPrefix: "Api-Key ",
		EnvVars: []string{"BASETEN_API_KEY", "BASETEN_API_KEY"},
	},
	{
		ID: "nvidia", DisplayName: "NVIDIA NIM", Category: "AI/LLM",
		BaseURL: "https://integrate.api.nvidia.com", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"NVIDIA_API_KEY", "NVIDIA_NIM_API_KEY"},
	},

	// ═══════════ CHINESE PROVIDERS ═══════════
	{
		ID: "alibaba", DisplayName: "Alibaba / Qwen", Category: "AI/LLM",
		BaseURL: "https://dashscope.aliyuncs.com/compatible-mode", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars:     []string{"DASHSCOPE_API_KEY", "ALIBABA_API_KEY", "QWEN_API_KEY"},
		KeyPrefixes: []string{"sk-"},
	},
	{
		ID: "kimi", DisplayName: "Kimi / Moonshot", Category: "AI/LLM",
		BaseURL: "https://api.moonshot.cn", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars:     []string{"MOONSHOT_API_KEY", "KIMI_API_KEY"},
		KeyPrefixes: []string{"sk-"},
	},
	{
		ID: "minimax", DisplayName: "MiniMax", Category: "AI/LLM",
		BaseURL: "https://api.minimax.chat", CheckPath: "/v1/text/chatcompletion_v2", CheckMethod: "POST",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"MINIMAX_API_KEY", "MINIMAX_KEY"},
	},
	{
		ID: "zhipu", DisplayName: "Zhipu AI / GLM", Category: "AI/LLM",
		BaseURL: "https://open.bigmodel.cn", CheckPath: "/api/paas/v4/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"ZHIPUAI_API_KEY", "GLM_API_KEY"},
	},

	// ═══════════ OTHER AI PROVIDERS ═══════════
	{
		ID: "perplexity", DisplayName: "Perplexity", Category: "AI/LLM",
		BaseURL: "https://api.perplexity.ai", CheckPath: "/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars:     []string{"PERPLEXITY_API_KEY", "PERPLEXITY_KEY", "PPLX_API_KEY"},
		KeyPrefixes: []string{"pplx-"},
	},
	{
		ID: "replicate", DisplayName: "Replicate", Category: "AI/LLM",
		BaseURL: "https://api.replicate.com", CheckPath: "/v1/account", CheckMethod: "GET",
		AuthType: AuthToken, AuthHeader: "Authorization", AuthPrefix: "Token ",
		EnvVars:     []string{"REPLICATE_API_KEY", "REPLICATE_API_TOKEN"},
		KeyPrefixes: []string{"r8_"},
	},
	{
		ID: "mistral", DisplayName: "Mistral AI", Category: "AI/LLM",
		BaseURL: "https://api.mistral.ai", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"MISTRAL_API_KEY", "MISTRAL_KEY"},
	},
	{
		ID: "cohere", DisplayName: "Cohere", Category: "AI/LLM",
		BaseURL: "https://api.cohere.ai", CheckPath: "/v2/check-api-key", CheckMethod: "POST",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"COHERE_API_KEY", "COHERE_KEY"},
	},
	{
		ID: "ai21", DisplayName: "AI21 Labs", Category: "AI/LLM",
		BaseURL: "https://api.ai21.com", CheckPath: "/studio/v1/account", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"AI21_API_KEY", "AI21_KEY"},
	},
	{
		ID: "huggingface", DisplayName: "Hugging Face", Category: "AI/LLM",
		BaseURL: "https://huggingface.co", CheckPath: "/api/whoami", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars:     []string{"HF_API_KEY", "HUGGINGFACE_API_KEY", "HF_TOKEN", "HUGGINGFACE_TOKEN"},
		KeyPrefixes: []string{"hf_"},
	},
	{
		ID: "elevenlabs", DisplayName: "ElevenLabs", Category: "AI/LLM",
		BaseURL: "https://api.elevenlabs.io", CheckPath: "/v1/user", CheckMethod: "GET",
		AuthType: AuthXAPIKey, AuthHeader: "xi-api-key", AuthPrefix: "",
		EnvVars: []string{"ELEVENLABS_API_KEY", "ELEVEN_API_KEY"},
	},
	{
		ID: "stability", DisplayName: "Stability AI", Category: "AI/LLM",
		BaseURL: "https://api.stability.ai", CheckPath: "/v1/user/account", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars:     []string{"STABILITY_API_KEY", "STABILITY_KEY"},
		KeyPrefixes: []string{"sk-"},
	},

	// ═══════════ CLOUD & DEVOPS ═══════════
	{
		ID: "azure", DisplayName: "Azure OpenAI", Category: "Cloud",
		BaseURL: "https://RESOURCE.openai.azure.com", CheckPath: "/openai/deployments", CheckMethod: "GET",
		AuthType: AuthAPIKey, AuthHeader: "api-key", AuthPrefix: "",
		EnvVars: []string{"AZURE_OPENAI_API_KEY", "AZURE_API_KEY", "AZURE_OPENAI_KEY"},
	},
	{
		ID: "digitalocean", DisplayName: "DigitalOcean", Category: "Cloud",
		BaseURL: "https://api.digitalocean.com", CheckPath: "/v2/account", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars:     []string{"DIGITALOCEAN_ACCESS_TOKEN", "DO_API_TOKEN", "DO_PAT"},
		KeyPrefixes: []string{"dop_v1_"},
	},
	{
		ID: "cloudflare", DisplayName: "Cloudflare", Category: "Cloud",
		BaseURL: "https://api.cloudflare.com/client/v4", CheckPath: "/user/tokens/verify", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"CLOUDFLARE_API_TOKEN", "CLOUDFLARE_API_KEY", "CF_API_TOKEN"},
	},
	{
		ID: "vercel", DisplayName: "Vercel", Category: "Cloud",
		BaseURL: "https://api.vercel.com", CheckPath: "/v2/user", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"VERCEL_API_TOKEN", "VERCEL_TOKEN"},
	},

	// ═══════════ DEV PLATFORMS ═══════════
	{
		ID: "github", DisplayName: "GitHub", Category: "DevTools",
		BaseURL: "https://api.github.com", CheckPath: "/user", CheckMethod: "GET",
		AuthType: AuthGitHubToken, AuthHeader: "Authorization", AuthPrefix: "token ",
		EnvVars:     []string{"GITHUB_TOKEN", "GITHUB_ACCESS_TOKEN", "GH_TOKEN"},
		KeyPrefixes: []string{"ghp_", "gho_", "github_pat_"},
	},
	{
		ID: "copilot", DisplayName: "GitHub Copilot", Category: "DevTools",
		BaseURL: "https://api.github.com", CheckPath: "/copilot/chat/diagnostics", CheckMethod: "GET",
		AuthType: AuthGitHubToken, AuthHeader: "Authorization", AuthPrefix: "token ",
		EnvVars:     []string{"COPILOT_TOKEN", "GITHUB_COPILOT_TOKEN", "GH_COPILOT_TOKEN"},
		KeyPrefixes: []string{"ghu_", "gho_"},
	},
	{
		ID: "gitlab", DisplayName: "GitLab", Category: "DevTools",
		BaseURL: "https://gitlab.com", CheckPath: "/api/v4/user", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars:     []string{"GITLAB_TOKEN", "GITLAB_ACCESS_TOKEN"},
		KeyPrefixes: []string{"glpat-"},
	},

	// ═══════════ API GATEWAYS / AGGREGATORS ═══════════
	{
		ID: "helicone", DisplayName: "Helicone", Category: "AI/LLM",
		BaseURL: "https://ai-gateway.helicone.ai", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"HELICONE_API_KEY", "HELICONE_KEY"},
	},
	{
		ID: "portkey", DisplayName: "Portkey", Category: "AI/LLM",
		BaseURL: "https://api.portkey.ai", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "x-portkey-api-key", AuthPrefix: "",
		EnvVars: []string{"PORTKEY_API_KEY"},
	},

	// ═══════════ MORE AI PROVIDERS ═══════════
	{
		ID: "sambanova", DisplayName: "SambaNova", Category: "AI/LLM",
		BaseURL: "https://api.sambanova.ai", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"SAMBANOVA_API_KEY", "SAMBA_API_KEY"},
	},
	{
		ID: "anyscale", DisplayName: "Anyscale", Category: "AI/LLM",
		BaseURL: "https://api.endpoints.anyscale.com", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"ANYSCALE_API_KEY", "ANYSCALE_ENDPOINT_API_KEY"},
	},
	{
		ID: "togetherai", DisplayName: "TogetherCompute", Category: "AI/LLM",
		BaseURL: "https://api.together.xyz", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"TOGETHER_API_KEY", "TOGETHER_KEY"},
	},
	{
		ID: "jina", DisplayName: "Jina AI", Category: "AI/LLM",
		BaseURL: "https://api.jina.ai", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"JINA_API_KEY"},
	},
	{
		ID: "voyage", DisplayName: "Voyage AI", Category: "AI/LLM",
		BaseURL: "https://api.voyageai.com", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"VOYAGE_API_KEY"},
	},

	// ═══════════ OPCODE / SPECIAL ═══════════
	{
		ID: "opencode", DisplayName: "OpenCode Zen", Category: "AI/LLM",
		BaseURL: "https://api.opencode.ai", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"OPENCODE_API_KEY", "OPENCODE_ZEN_KEY"},
	},
	{
		ID: "opencode_go", DisplayName: "OpenCode Go", Category: "AI/LLM",
		BaseURL: "https://api.opencode.ai", CheckPath: "/go/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"OPENCODE_GO_KEY"},
	},

	// ═══════════ LOCAL / SELF-HOSTED ═══════════
	{
		ID: "ollama", DisplayName: "Ollama (local)", Category: "Local",
		BaseURL: "http://localhost:11434", CheckPath: "/api/tags", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"OLLAMA_HOST", "OLLAMA_API_KEY"},
	},
	{
		ID: "lmstudio", DisplayName: "LM Studio (local)", Category: "Local",
		BaseURL: "http://localhost:1234", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"LMSTUDIO_API_KEY"},
	},
	{
		ID: "vllm", DisplayName: "vLLM (local)", Category: "Local",
		BaseURL: "http://localhost:8000", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"VLLM_API_KEY"},
	},

	// ═══════════ GATEWAYS ═══════════
	{
		ID: "litellm", DisplayName: "LiteLLM Proxy", Category: "AI/LLM",
		BaseURL: "http://localhost:4000", CheckPath: "/v1/models", CheckMethod: "GET",
		AuthType: AuthBearer, AuthHeader: "Authorization", AuthPrefix: "Bearer ",
		EnvVars: []string{"LITELLM_API_KEY", "LITELLM_MASTER_KEY"},
	},
}

// ─── Key Discovery ─────────────────────────────────────────────────────

func discoverKeys() map[string][]string {
	keys := make(map[string][]string)

	for _, p := range providers {
		for _, env := range p.EnvVars {
			if val := os.Getenv(env); val != "" {
				keys[p.ID] = appendIfUnique(keys[p.ID], val)
			}
		}
		if vars := envLike(os.Getenv(p.ID + "_API_KEY")); vars != "" {
			keys[p.ID] = appendIfUnique(keys[p.ID], vars)
		}
	}

	// Also check generic env vars
	genericKeys := os.Getenv("API_KEYS")
	if genericKeys != "" {
		for _, k := range strings.Split(genericKeys, ",") {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			matched := false
			for _, p := range providers {
				for _, prefix := range p.KeyPrefixes {
					if strings.HasPrefix(k, prefix) {
						keys[p.ID] = appendIfUnique(keys[p.ID], k)
						matched = true
						break
					}
				}
				if matched {
					break
				}
			}
			if !matched {
				keys["unknown"] = appendIfUnique(keys["unknown"], k)
			}
		}
	}

	// Check .env files
	envFiles := []string{".env", ".env.local", ".env.development", "config.env"}
	home, _ := os.UserHomeDir()
	if home != "" {
		envFiles = append(envFiles, filepath.Join(home, ".env"))
	}
	for _, f := range envFiles {
		if data, err := os.ReadFile(f); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				parts := strings.SplitN(line, "=", 2)
				if len(parts) != 2 {
					continue
				}
				key := strings.TrimSpace(parts[0])
				val := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
				if val == "" {
					continue
				}
				for _, p := range providers {
					for _, env := range p.EnvVars {
						if strings.EqualFold(key, env) {
							keys[p.ID] = appendIfUnique(keys[p.ID], val)
							break
						}
					}
				}
			}
		}
	}

	// Check opencode auth.json
	authPaths := []string{}
	if home != "" {
		if runtime.GOOS == "windows" {
			authPaths = append(authPaths, filepath.Join(home, "AppData", "Local", "opencode", "auth.json"))
		} else {
			authPaths = append(authPaths, filepath.Join(home, ".local", "share", "opencode", "auth.json"))
		}
	}
	for _, ap := range authPaths {
		data, err := os.ReadFile(ap)
		if err != nil {
			continue
		}
		var authData map[string]interface{}
		if err := json.Unmarshal(data, &authData); err != nil {
			continue
		}
		for provName, provData := range authData {
			provMap, ok := provData.(map[string]interface{})
			if !ok {
				continue
			}
			if apiKey, ok := provMap["api_key"].(string); ok && apiKey != "" {
				for _, p := range providers {
					if strings.EqualFold(p.ID, provName) || strings.EqualFold(p.DisplayName, provName) ||
						strings.Contains(strings.ToLower(p.DisplayName), strings.ToLower(provName)) {
						keys[p.ID] = appendIfUnique(keys[p.ID], apiKey)
					}
				}
			}
		}
	}

	return keys
}

func envLike(val string) string {
	if val == "" {
		return ""
	}
	return val
}

func appendIfUnique(slice []string, item string) []string {
	for _, s := range slice {
		if s == item {
			return slice
		}
	}
	return append(slice, item)
}

// ─── Key Checker ───────────────────────────────────────────────────────

type Checker struct {
	client  *http.Client
	results []CheckResult
	mu      sync.Mutex
	checked atomic.Int64
	total   atomic.Int64
	found   atomic.Int64
	errors  atomic.Int64
	valid   atomic.Int64
	invalid atomic.Int64
}

func NewChecker() *Checker {
	return &Checker{
		client: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:    50,
				IdleConnTimeout: 30 * time.Second,
			},
		},
	}
}

func maskKey(k string) string {
	if len(k) <= 12 {
		return strings.Repeat("*", len(k))
	}
	return k[:8] + "..." + k[len(k)-4:]
}

func (c *Checker) checkProvider(ctx context.Context, p Provider, keys []string, sem chan struct{}) {
	if len(keys) == 0 {
		c.mu.Lock()
		c.results = append(c.results, CheckResult{
			Provider: p.ID, DisplayName: p.DisplayName,
			Status: "no_key", Detail: "no API key found",
		})
		c.mu.Unlock()
		c.total.Add(1)
		c.checked.Add(1)
		return
	}

	c.total.Add(int64(len(keys)))
	c.found.Add(int64(len(keys)))

	var keyWg sync.WaitGroup
	for _, key := range keys {
		keyWg.Add(1)
		sem <- struct{}{} // acquire slot
		go func(k string) {
			defer func() { <-sem; keyWg.Done() }() // release slot

			result := c.checkKey(ctx, p, k)
			c.checked.Add(1)

			switch result.Status {
			case "valid":
				c.valid.Add(1)
			case "invalid":
				c.invalid.Add(1)
			case "error":
				c.errors.Add(1)
			}

			c.mu.Lock()
			c.results = append(c.results, result)
			c.mu.Unlock()
		}(key)
	}
	keyWg.Wait() // wait for all per-key goroutines before returning
}

func (c *Checker) checkKey(ctx context.Context, p Provider, key string) CheckResult {
	result := CheckResult{
		Provider: p.ID, DisplayName: p.DisplayName,
		Endpoint: p.BaseURL + p.CheckPath,
		Status:   "error",
	}

	start := time.Now()

	url := p.BaseURL + p.CheckPath
	if p.AuthType == AuthURLParam {
		if strings.Contains(url, "?") {
			url += "&key=" + key
		} else {
			url += "?key=" + key
		}
	}

	var body io.Reader
	if p.CheckMethod == "POST" && p.ID == "anthropic" {
		body = strings.NewReader(`{"model":"claude-3-haiku-20240307","max_tokens":1,"messages":[{"role":"user","content":"Hi"}]}`)
	}

	req, err := http.NewRequestWithContext(ctx, p.CheckMethod, url, body)
	if err != nil {
		result.Detail = "request creation failed: " + err.Error()
		result.Latency = time.Since(start)
		return result
	}

	req.Header.Set("User-Agent", "apikey-check/1.0")

	switch p.AuthType {
	case AuthBearer:
		req.Header.Set(p.AuthHeader, p.AuthPrefix+key)
	case AuthAPIKey:
		req.Header.Set(p.AuthHeader, key)
	case AuthXAPIKey:
		req.Header.Set(p.AuthHeader, key)
	case AuthToken:
		req.Header.Set(p.AuthHeader, p.AuthPrefix+key)
	case AuthGitHubToken:
		req.Header.Set("Authorization", "token "+key)
		req.Header.Set("Accept", "application/vnd.github.v3+json")
	case AuthGitLabToken:
		req.Header.Set("PRIVATE-TOKEN", key)
	case AuthAnthropicKey:
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("Content-Type", "application/json")
	case AuthURLParam:
		// key is already in URL
	}

	resp, err := c.client.Do(req)
	if err != nil {
		result.Detail = "connection error: " + err.Error()
		result.Latency = time.Since(start)
		return result
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	result.Latency = time.Since(start)

	// Determine validity
	statusValid := resp.StatusCode >= 200 && resp.StatusCode < 300
	statusAuth := resp.StatusCode == 401 || resp.StatusCode == 403

	// Specific provider checks
	switch p.ID {
	case "openrouter":
		if statusValid {
			var kr struct {
				Data struct {
					Credits  float64 `json:"credits"`
					IsActive bool    `json:"is_active"`
				} `json:"data"`
			}
			if json.Unmarshal(respBody, &kr) == nil && kr.Data.IsActive {
				result.Status = "valid"
				result.Credits = fmt.Sprintf("%.2f credits", kr.Data.Credits)
				result.Detail = fmt.Sprintf("credits remaining: %.2f", kr.Data.Credits)
			} else {
				result.Status = "valid"
			}
		} else if statusAuth {
			result.Status = "invalid"
		} else {
			result.Status = "error"
			result.Detail = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 100))
		}

	case "google":
		if statusValid {
			result.Status = "valid"
			result.Detail = "Gemini API key is active"
		} else if statusAuth {
			result.Status = "invalid"
		} else {
			result.Status = "error"
			result.Detail = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 100))
		}

	case "github", "copilot":
		if statusValid {
			result.Status = "valid"
			// Try to extract username from GitHub response
			var gu struct {
				Login string `json:"login"`
				Plan  struct {
					Name string `json:"name"`
				} `json:"plan"`
			}
			if json.Unmarshal(respBody, &gu) == nil {
				result.Detail = fmt.Sprintf("authenticated as @%s", gu.Login)
				if gu.Plan.Name != "" {
					result.Tier = gu.Plan.Name
				}
			}
		} else if statusAuth {
			result.Status = "invalid"
		} else {
			result.Status = "error"
			result.Detail = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 100))
		}

	case "huggingface":
		if statusValid {
			result.Status = "valid"
			var hf struct {
				Name  string `json:"name"`
				Email string `json:"email"`
			}
			if json.Unmarshal(respBody, &hf) == nil {
				result.Detail = fmt.Sprintf("authenticated as %s", hf.Name)
			}
		} else if statusAuth {
			result.Status = "invalid"
		} else {
			result.Status = "error"
			result.Detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}

	case "replicate":
		if statusValid {
			result.Status = "valid"
			var ra struct {
				Username string `json:"username"`
			}
			if json.Unmarshal(respBody, &ra) == nil {
				result.Detail = fmt.Sprintf("authenticated as @%s", ra.Username)
			}
		} else if statusAuth {
			result.Status = "invalid"
		} else {
			result.Status = "error"
			result.Detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}

	default:
		// OpenAI-compatible providers return model list on success
		if statusValid {
			result.Status = "valid"
			// Try to extract model count for OpenAI-compatible endpoints
			var models struct {
				Data []interface{} `json:"data"`
			}
			if json.Unmarshal(respBody, &models) == nil && len(models.Data) > 0 {
				result.Detail = fmt.Sprintf("key valid (%d models available)", len(models.Data))
			} else {
				result.Detail = "key is valid"
			}
		} else if statusAuth {
			result.Status = "invalid"
			result.Detail = "invalid API key"
		} else if resp.StatusCode == 429 {
			result.Status = "error"
			result.Detail = "rate limited - try again later"
		} else {
			result.Status = "error"
			result.Detail = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(string(respBody), 100))
		}
	}

	return result
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// ─── Display ───────────────────────────────────────────────────────────

type terminalUI struct {
	width      int
	isTerminal bool
	startTime  time.Time
}

func newTerminalUI() *terminalUI {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		w = 100
	}
	return &terminalUI{
		width:      w,
		isTerminal: term.IsTerminal(int(os.Stdout.Fd())),
		startTime:  time.Now(),
	}
}

func (ui *terminalUI) printBanner() {
	if !ui.isTerminal {
		return
	}
	fmt.Print("\033[2J\033[H") // clear screen
	fmt.Println(brightCyan + bold + "  ╔══════════════════════════════════════════════════════════════╗" + reset)
	fmt.Println(brightCyan + bold + "  ║" + reset + "  " + bold + white + iconKey + "  API KEY BALANCE & VALIDITY CHECKER" + reset + strings.Repeat(" ", 26) + brightCyan + bold + "║" + reset)
	fmt.Println(brightCyan + bold + "  ║" + reset + "  " + dim + "  " + iconGlobe + "  Supports 40+ LLM providers " + iconZap + " Auto-discovers from env" + reset + strings.Repeat(" ", 8) + brightCyan + bold + "║" + reset)
	fmt.Println(brightCyan + bold + "  ╚══════════════════════════════════════════════════════════════╝" + reset)
	fmt.Println()
}

func (ui *terminalUI) printProgress(checked, total int64) {
	if !ui.isTerminal {
		return
	}
	pct := 0
	if total > 0 {
		pct = int(checked * 100 / total)
	}
	barWidth := ui.width - 40
	if barWidth < 10 {
		barWidth = 10
	}
	filled := pct * barWidth / 100
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
	elapsed := time.Since(ui.startTime).Round(time.Second)

	fmt.Printf("\r\033[K  %s %s %s %3d%% %s %s[%d/%d]%s %s elapsed: %v %s",
		iconClock, brightCyan, bar, pct, reset,
		dim, checked, total, reset,
		brightCyan, elapsed, reset)
}

func (ui *terminalUI) printResults(results []CheckResult) {
	if !ui.isTerminal {
		ui.printTextResults(results)
		return
	}

	fmt.Print("\r\033[K") // clear progress line
	fmt.Println()
	fmt.Println()

	// Summary stats
	valid, invalid, errs, noKey, unknown := 0, 0, 0, 0, 0
	providerResults := make(map[string][]CheckResult)
	for _, r := range results {
		switch r.Status {
		case "valid":
			valid++
		case "invalid":
			invalid++
		case "error":
			errs++
		case "no_key":
			noKey++
		default:
			unknown++
		}
		providerResults[r.Provider] = append(providerResults[r.Provider], r)
	}

	// Summary bar
	fmt.Println("  " + bold + white + "═══ RESULTS SUMMARY ═══" + reset)
	fmt.Printf("  %s %sValid:%s %d  %s %sInvalid:%s %d  %s %sErrors:%s %d  %s %sNo Key:%s %d\n",
		brightGreen, bold, reset, valid,
		brightRed, bold, reset, invalid,
		yellow, bold, reset, errs,
		dim, bold, reset, noKey)
	fmt.Printf("  %s%d providers checked in %v%s\n\n", dim, len(results), time.Since(ui.startTime).Round(time.Millisecond), reset)

	// Grouped results by category
	categories := []string{"AI/LLM", "Cloud", "DevTools", "Local"}
	for _, cat := range categories {
		hasResults := false
		for _, r := range results {
			for _, p := range providers {
				if p.ID == r.Provider && p.Category == cat {
					hasResults = true
					break
				}
			}
			if hasResults {
				break
			}
		}
		if !hasResults {
			continue
		}

		// Category header
		switch cat {
		case "AI/LLM":
			fmt.Println("  " + bold + magenta + iconSparkle + "  AI / LLM PROVIDERS" + reset)
		case "Cloud":
			fmt.Println("  " + bold + blue + iconPackage + "  CLOUD & INFRASTRUCTURE" + reset)
		case "DevTools":
			fmt.Println("  " + bold + yellow + iconRocket + "  DEVELOPER PLATFORMS" + reset)
		case "Local":
			fmt.Println("  " + bold + dim + iconLock + "  LOCAL / SELF-HOSTED" + reset)
		}
		fmt.Println("  ──────────────────────────────────────────────────────────────")

		for _, r := range results {
			p := findProvider(r.Provider)
			if p == nil || p.Category != cat {
				continue
			}

			statusIcon, statusColor := ui.statusDisplay(r.Status)
			name := fmt.Sprintf("%-22s", r.DisplayName)
			detail := r.Detail
			if r.Tier != "" {
				detail = fmt.Sprintf("tier: %s | %s", r.Tier, detail)
			}
			if r.Credits != "" {
				detail = fmt.Sprintf("%s | %s", r.Credits, detail)
			}

			fmt.Printf("    %s %s%s %s %s%s%s\n",
				statusColor, statusIcon, reset,
				bold+name+reset,
				dim, detail, reset,
			)

			if r.Latency > 0 {
				fmt.Printf("      %s⏱ %v%s", dim, r.Latency.Round(time.Millisecond), reset)
				if r.Status == "no_key" {
					fmt.Printf("  %s(env: %s)%s", dim, strings.Join(p.EnvVars, ", "), reset)
				}
				fmt.Println()
			}
		}
		fmt.Println()
	}

	fmt.Println("  " + bold + white + "═══ END OF REPORT ═══" + reset)
	fmt.Printf("  %s%d valid%s | %s%d invalid%s | %s%d errors%s | %s%d no key configured%s\n",
		brightGreen, valid, reset,
		brightRed, invalid, reset,
		yellow, errs, reset,
		dim, noKey, reset)
	fmt.Println()
}

func (ui *terminalUI) printTextResults(results []CheckResult) {
	for _, r := range results {
		icon, _ := ui.statusDisplay(r.Status)
		fmt.Printf("[%s] %-22s | %s | %s\n", r.Status, r.DisplayName, r.Detail, icon)
	}
}

func (ui *terminalUI) statusDisplay(status string) (string, string) {
	switch status {
	case "valid":
		return iconCheck, green
	case "invalid":
		return iconCross, red
	case "error":
		return iconWarn, yellow
	case "no_key":
		return "  ", dim
	default:
		return "?", dim
	}
}

func findProvider(id string) *Provider {
	for i := range providers {
		if providers[i].ID == id {
			return &providers[i]
		}
	}
	return nil
}

// ─── Main ──────────────────────────────────────────────────────────────

func main() {
	ui := newTerminalUI()
	ui.printBanner()

	fmt.Printf("  %s%s Discovering API keys...%s\n", dim, iconSearch, reset)

	// Discover keys
	keys := discoverKeys()
	foundCount := 0
	for _, v := range keys {
		foundCount += len(v)
	}

	if foundCount == 0 {
		fmt.Println()
		fmt.Printf("  %s %sNo API keys found in environment variables or config files.%s\n", yellow, iconWarn, reset)
		fmt.Printf("  %sSet API keys as environment variables (e.g. OPENAI_API_KEY, ANTHROPIC_API_KEY)%s\n", dim, reset)
		fmt.Println()
		fmt.Println("  Supported env vars per provider:")
		for _, p := range providers {
			if len(p.EnvVars) > 0 {
				fmt.Printf("    %s%-20s%s %s\n", dim, p.DisplayName, reset, strings.Join(p.EnvVars, ", "))
			}
		}
		return
	}

	fmt.Printf("  %sFound %d key(s) across %d providers%s\n\n", green, foundCount, len(keys), reset)

	// Start checking
	checker := NewChecker()
	sem := make(chan struct{}, 8) // max 8 concurrent checks
	totalKeys := 0
	for _, k := range keys {
		totalKeys += len(k)
	}
	checker.total.Store(0)
	checker.checked.Store(0)
	checker.found.Store(int64(foundCount))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle Ctrl+C gracefully
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\n\n  " + yellow + iconWarn + " Cancelled by user" + reset)
		cancel()
	}()

	// Launch checks
	var wg sync.WaitGroup
	for _, p := range providers {
		if ks, ok := keys[p.ID]; ok {
			wg.Add(1)
			go func(prov Provider, keyList []string) {
				defer wg.Done()
				checker.checkProvider(ctx, prov, keyList, sem)
			}(p, ks)
		}
	}

	// Wait for no-key providers too
	for _, p := range providers {
		if _, ok := keys[p.ID]; !ok {
			wg.Add(1)
			go func(prov Provider) {
				defer wg.Done()
				checker.checkProvider(ctx, prov, nil, sem)
			}(p)
		}
	}

	// Progress display
	if ui.isTerminal {
		go func() {
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					ui.printProgress(checker.checked.Load(), checker.total.Load())
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	wg.Wait()
	cancel() // stop ticker

	// Print results
	ui.printResults(checker.results)

	// Exit with non-zero if any invalid keys found
	invalidCount := 0
	for _, r := range checker.results {
		if r.Status == "invalid" {
			invalidCount++
		}
	}
	if invalidCount > 0 {
		os.Exit(1)
	}
}
