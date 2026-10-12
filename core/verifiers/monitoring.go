package verifiers

import (
	"net/http"
	"strings"
)

func init() {
	add(Verifier{
		Name:     "datadog",
		Category: "Monitoring",
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.datadoghq.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/api/v1/validate", nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("DD-API-KEY", c.Key)
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
				return Outcome{Status: Invalid, Detail: "Datadog reported key invalid"}
			}
			out := validOutcome("valid Datadog API key")
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:     "sentry",
		Category: "Monitoring",
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://sentry.io"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/api/0/", nil)
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
			out := validOutcome("valid Sentry auth token")
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
		Name:        "newrelic",
		Category:    "Monitoring",
		KeyPrefixes: []string{"NRAK-"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.newrelic.com"
			}
			body := strings.NewReader(`{"query":"{ actor { user { email name } } }"}`)
			req, err := http.NewRequest(http.MethodPost, base+"/graphql", body)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Api-Key", c.Key)
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
			out := validOutcome("valid New Relic API key")
			var payload struct {
				Data struct {
					Actor struct {
						User struct {
							Name  string `json:"name"`
							Email string `json:"email"`
						} `json:"user"`
					} `json:"actor"`
				} `json:"data"`
				Errors []struct {
					Message string `json:"message"`
				} `json:"errors"`
			}
			if decodeJSON(body, &payload) {
				out.Identity = payload.Data.Actor.User.Name
				if out.Identity == "" {
					out.Identity = payload.Data.Actor.User.Email
				}
				if out.Identity == "" && len(payload.Errors) > 0 {
					return Outcome{Status: Invalid, Detail: "New Relic: " + payload.Errors[0].Message}
				}
			}
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:     "pagerduty",
		Category: "Monitoring",
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.pagerduty.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/users/me", nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Token token="+c.Key)
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid PagerDuty API key")
			var payload struct {
				User struct {
					Name  string `json:"name"`
					Email string `json:"email"`
				} `json:"user"`
			}
			if decodeJSON(body, &payload) {
				out.Identity = payload.User.Name
				if out.Identity == "" {
					out.Identity = payload.User.Email
				}
			}
			out.HighValue = true
			return out
		},
	})
}
