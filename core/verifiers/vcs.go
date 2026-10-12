package verifiers

import (
	"net/http"
	"strings"
)

func init() {
	add(Verifier{
		Name:        "github",
		Category:    "VCS",
		KeyPrefixes: []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.github.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/user", nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "token "+c.Key)
			req.Header.Set("Accept", "application/vnd.github.v3+json")
			req.Header.Set("User-Agent", "shhgit-verifiers")
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid GitHub token")
			var payload struct {
				Login string `json:"login"`
				Plan  struct {
					Name string `json:"name"`
				} `json:"plan"`
			}
			if decodeJSON(body, &payload) {
				out.Identity = payload.Login
				out.Tier = payload.Plan.Name
			}
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:        "gitlab",
		Category:    "VCS",
		KeyPrefixes: []string{"glpat-"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://gitlab.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/api/v4/user", nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("PRIVATE-TOKEN", c.Key)
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid GitLab token")
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
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:     "bitbucket",
		Category: "VCS",
		Needs:    []string{"Username", "Password"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.bitbucket.org"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/2.0/user", nil)
			if err != nil {
				return nil, err
			}
			basicAuth(req, c.Username, c.Password)
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid Bitbucket credentials")
			var payload struct {
				DisplayName string `json:"display_name"`
				Username    string `json:"username"`
			}
			if decodeJSON(body, &payload) {
				out.Identity = payload.DisplayName
				if out.Identity == "" {
					out.Identity = payload.Username
				}
			}
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:     "circleci",
		Category: "VCS",
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://circleci.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/api/v2/me", nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Circle-Token", c.Key)
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid CircleCI token")
			var payload struct {
				Login string `json:"login"`
				Name  string `json:"name"`
			}
			if decodeJSON(body, &payload) {
				out.Identity = payload.Login
				if out.Identity == "" {
					out.Identity = payload.Name
				}
			}
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:     "travis",
		Category: "VCS",
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.travis-ci.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/user", nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "token "+c.Key)
			req.Header.Set("Travis-API-Version", "3")
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid Travis CI token")
			var payload struct {
				Login string `json:"login"`
			}
			if decodeJSON(body, &payload) {
				out.Identity = payload.Login
			}
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:        "dockerhub",
		Category:    "VCS",
		KeyPrefixes: []string{"dckr_pat_"},
		Needs:       []string{"Username", "Password"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://hub.docker.com"
			}
			body := strings.NewReader(`{"username":` + jsonString(c.Username) + `,"password":` + jsonString(c.Password) + `}`)
			req, err := http.NewRequest(http.MethodPost, base+"/v2/users/login", body)
			if err != nil {
				return nil, err
			}
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
			var payload struct {
				Token string `json:"token"`
			}
			if decodeJSON(body, &payload) && payload.Token == "" {
				return Outcome{Status: Invalid, Detail: "Docker Hub returned no token"}
			}
			out := validOutcome("valid Docker Hub credentials")
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:        "npm",
		Category:    "VCS",
		KeyPrefixes: []string{"npm_"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://registry.npmjs.org"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/-/whoami", nil)
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
			out := validOutcome("valid NPM token")
			var payload struct {
				Username string `json:"username"`
			}
			if decodeJSON(body, &payload) {
				out.Identity = payload.Username
			}
			out.HighValue = true
			return out
		},
	})
}
