package verifiers

import (
	"fmt"
	"net/http"
	"strings"
)

// modelsVerifier builds a verifier for the OpenAI-compatible "GET /v1/models"
// endpoint that most inference providers expose.
func modelsVerifier(name, category, defaultBase, path string, prefixes []string) Verifier {
	return Verifier{
		Name:        name,
		Category:    category,
		KeyPrefixes: prefixes,
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = defaultBase
			}
			req, err := http.NewRequest(http.MethodGet, base+path, nil)
			if err != nil {
				return nil, err
			}
			bearer(req, c.Key)
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome(name + ": valid key")
			var payload struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if decodeJSON(body, &payload) && len(payload.Data) > 0 {
				out.Detail = fmt.Sprintf("%s: %d models available", name, len(payload.Data))
			}
			return out
		},
	}
}

// anthropicTier maps the model id returned by the Anthropic API onto the tier
// name core.severity treats as top-tier. Without it no verifier ever set a
// Claude Tier, so TopTierModels' Claude entries were unreachable and a verified
// Opus/Sonnet key could only ever classify medium.
func anthropicTier(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "opus"):
		return "Claude-3-Opus"
	case strings.Contains(m, "sonnet"):
		return "Claude-3.5-Sonnet"
	default:
		return ""
	}
}

func init() {
	add(Verifier{
		Name:        "openai",
		Category:    "AI",
		KeyPrefixes: []string{"sk-proj-", "sk-svcacct-", "sk-"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.openai.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
			if err != nil {
				return nil, err
			}
			bearer(req, c.Key)
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid OpenAI key")
			var payload struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if decodeJSON(body, &payload) {
				out.Detail = fmt.Sprintf("%d models available", len(payload.Data))
				for _, m := range payload.Data {
					if strings.HasPrefix(m.ID, "gpt-4") {
						out.Tier = "GPT-4"
						break
					}
				}
			}
			out.RPM = intHeader(resp, "x-ratelimit-limit-requests")
			return out
		},
	})

	add(Verifier{
		Name:        "anthropic",
		Category:    "AI",
		KeyPrefixes: []string{"sk-ant-"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.anthropic.com"
			}
			body := strings.NewReader(`{"model":"claude-3-haiku-20240307","max_tokens":1,"messages":[{"role":"user","content":"Hi"}]}`)
			req, err := http.NewRequest(http.MethodPost, base+"/v1/messages", body)
			if err != nil {
				return nil, err
			}
			req.Header.Set("x-api-key", c.Key)
			req.Header.Set("anthropic-version", "2023-06-01")
			req.Header.Set("Content-Type", "application/json")
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid Anthropic key")
			var payload struct {
				Model string `json:"model"`
			}
			if decodeJSON(body, &payload) {
				out.Tier = anthropicTier(payload.Model)
			}
			return out
		},
	})

	add(Verifier{
		Name:        "google",
		Category:    "AI",
		KeyPrefixes: []string{"AIza"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://generativelanguage.googleapis.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/v1beta/models?key="+c.Key, nil)
			if err != nil {
				return nil, err
			}
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			// A valid key that is not enabled for the Generative Language API
			// answers 403 SERVICE_DISABLED/PERMISSION_DENIED. That is not proof
			// the credential is invalid, so do not map it to Invalid.
			if resp.StatusCode == http.StatusForbidden {
				lower := strings.ToLower(string(body))
				if strings.Contains(lower, "service_disabled") || strings.Contains(lower, "permission_denied") || strings.Contains(lower, "api_key_service_blocked") {
					return Outcome{Status: Unverified, Detail: "key not authorized for the Generative Language API"}
				}
			}
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if resp.StatusCode == http.StatusBadRequest {
				return Outcome{Status: Invalid, Detail: "invalid Google API key"}
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			return validOutcome("valid Google/Gemini key")
		},
	})

	add(modelsVerifier("xai", "AI", "https://api.x.ai", "/v1/models", []string{"xai-"}))

	add(Verifier{
		Name:        "openrouter",
		Category:    "AI",
		KeyPrefixes: []string{"sk-or-v1-"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://openrouter.ai"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/api/v1/auth/key", nil)
			if err != nil {
				return nil, err
			}
			bearer(req, c.Key)
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid OpenRouter key")
			var payload struct {
				Data struct {
					Label    string  `json:"label"`
					Credits  float64 `json:"credits"`
					IsActive bool    `json:"is_active"`
				} `json:"data"`
			}
			if decodeJSON(body, &payload) {
				out.Identity = payload.Data.Label
				out.Balance = fmt.Sprintf("%.2f credits", payload.Data.Credits)
			}
			return out
		},
	})

	add(Verifier{
		Name:        "huggingface",
		Category:    "AI",
		KeyPrefixes: []string{"hf_"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://huggingface.co"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/api/whoami", nil)
			if err != nil {
				return nil, err
			}
			bearer(req, c.Key)
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid HuggingFace token")
			var payload struct {
				Name  string `json:"name"`
				Email string `json:"email"`
			}
			if decodeJSON(body, &payload) {
				out.Identity = payload.Name
				if out.Identity == "" {
					out.Identity = payload.Email
				}
			}
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:        "replicate",
		Category:    "AI",
		KeyPrefixes: []string{"r8_"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.replicate.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/v1/account", nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Token "+c.Key)
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid Replicate token")
			var payload struct {
				Username string `json:"username"`
				Name     string `json:"name"`
			}
			if decodeJSON(body, &payload) {
				out.Identity = payload.Username
				if out.Identity == "" {
					out.Identity = payload.Name
				}
			}
			return out
		},
	})

	add(Verifier{
		Name:        "elevenlabs",
		Category:    "AI",
		KeyPrefixes: []string{"sk_"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.elevenlabs.io"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/v1/user", nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("xi-api-key", c.Key)
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid ElevenLabs key")
			var payload struct {
				Subscription struct {
					Tier           string `json:"tier"`
					CharacterCount int    `json:"character_count"`
					CharacterLimit int    `json:"character_limit"`
					Status         string `json:"status"`
				} `json:"subscription"`
			}
			if decodeJSON(body, &payload) {
				out.Tier = payload.Subscription.Tier
				if payload.Subscription.CharacterLimit > 0 {
					out.Balance = fmt.Sprintf("%d/%d", payload.Subscription.CharacterCount, payload.Subscription.CharacterLimit)
				}
			}
			return out
		},
	})

	add(modelsVerifier("deepseek", "AI", "https://api.deepseek.com", "/v1/models", nil))
	add(modelsVerifier("groq", "AI", "https://api.groq.com", "/openai/v1/models", []string{"gsk_"}))
	add(modelsVerifier("mistral", "AI", "https://api.mistral.ai", "/v1/models", nil))
	add(modelsVerifier("perplexity", "AI", "https://api.perplexity.ai", "/models", []string{"pplx-"}))
	add(modelsVerifier("together", "AI", "https://api.together.xyz", "/v1/models", nil))
	add(modelsVerifier("fireworks", "AI", "https://api.fireworks.ai", "/v1/models", nil))
	add(modelsVerifier("moonshot", "AI", "https://api.moonshot.cn", "/v1/models", nil))
	add(modelsVerifier("dashscope", "AI", "https://dashscope.aliyuncs.com", "/compatible-mode/v1/models", nil))

	add(Verifier{
		Name:     "cohere",
		Category: "AI",
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.cohere.ai"
			}
			req, err := http.NewRequest(http.MethodPost, base+"/v2/check-api-key", nil)
			if err != nil {
				return nil, err
			}
			bearer(req, c.Key)
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			var payload struct {
				Valid bool `json:"valid"`
			}
			if decodeJSON(body, &payload) && !payload.Valid {
				return Outcome{Status: Invalid, Detail: "Cohere key reported invalid"}
			}
			return validOutcome("valid Cohere key")
		},
	})
}
