package verifiers

import (
	"net/http"
)

func init() {
	add(Verifier{
		Name:        "digitalocean",
		Category:    "Cloud",
		KeyPrefixes: []string{"dop_v1_"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.digitalocean.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/v2/account", nil)
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
			out := validOutcome("valid DigitalOcean token")
			var payload struct {
				Account struct {
					Email        string `json:"email"`
					DropletLimit int    `json:"droplet_limit"`
				} `json:"account"`
			}
			if decodeJSON(body, &payload) {
				out.Identity = payload.Account.Email
			}
			out.HighValue = true
			return out
		},
	})

	// Cloudflare accepts either a scoped API token (Bearer, sent to
	// /user/tokens/verify) or a global API key (X-Auth-Key + X-Auth-Email).
	// Build selects the global-key path when an email companion is supplied,
	// otherwise the token path.
	add(Verifier{
		Name:     "cloudflare",
		Category: "Cloud",
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.cloudflare.com"
			}
			if c.Email != "" {
				req, err := http.NewRequest(http.MethodGet, base+"/client/v4/user", nil)
				if err != nil {
					return nil, err
				}
				req.Header.Set("X-Auth-Key", c.Key)
				req.Header.Set("X-Auth-Email", c.Email)
				return req, nil
			}
			req, err := http.NewRequest(http.MethodGet, base+"/client/v4/user/tokens/verify", nil)
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
				Success bool `json:"success"`
			}
			if decodeJSON(body, &payload) && !payload.Success {
				return Outcome{Status: Invalid, Detail: "Cloudflare reported failure"}
			}
			out := validOutcome("valid Cloudflare credential")
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:     "vercel",
		Category: "Cloud",
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.vercel.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/v2/user", nil)
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
			out := validOutcome("valid Vercel token")
			var payload struct {
				User struct {
					Username string `json:"username"`
					Email    string `json:"email"`
				} `json:"user"`
			}
			if decodeJSON(body, &payload) {
				out.Identity = payload.User.Username
				if out.Identity == "" {
					out.Identity = payload.User.Email
				}
			}
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:     "heroku",
		Category: "Cloud",
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.heroku.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/account", nil)
			if err != nil {
				return nil, err
			}
			bearer(req, c.Key)
			req.Header.Set("Accept", "application/vnd.heroku+json; version=3")
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid Heroku API key")
			var payload struct {
				Email string `json:"email"`
			}
			if decodeJSON(body, &payload) {
				out.Identity = payload.Email
			}
			out.HighValue = true
			return out
		},
	})
}
