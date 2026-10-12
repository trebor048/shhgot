package verifiers

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func init() {
	add(Verifier{
		Name:     "discord",
		Category: "Messaging",
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://discord.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/api/v10/users/@me", nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bot "+c.Key)
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
			out := validOutcome("valid Discord bot token")
			var payload struct {
				ID            string `json:"id"`
				Username      string `json:"username"`
				Discriminator string `json:"discriminator"`
			}
			if decodeJSON(body, &payload) {
				out.Identity = payload.Username
				if payload.Discriminator != "" && payload.Discriminator != "0" {
					out.Identity = payload.Username + "#" + payload.Discriminator
				}
				out.Extra = map[string]string{"id": payload.ID}
			}
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:        "slack",
		Category:    "Messaging",
		KeyPrefixes: []string{"xoxb-", "xoxp-", "xoxa-", "xoxr-"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://slack.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/api/auth.test", nil)
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
				OK    bool   `json:"ok"`
				Error string `json:"error"`
				Team  string `json:"team"`
				User  string `json:"user"`
				URL   string `json:"url"`
			}
			if !decodeJSON(body, &payload) {
				return validOutcome("valid Slack token")
			}
			if !payload.OK {
				if payload.Error == "token_expired" {
					return Outcome{Status: QuotaLimited, Detail: "Slack token expired"}
				}
				return Outcome{Status: Invalid, Detail: "Slack: " + payload.Error}
			}
			out := validOutcome("valid Slack token")
			out.Identity = payload.User
			out.Extra = map[string]string{"team": payload.Team, "url": payload.URL}
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:     "telegram",
		Category: "Messaging",
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.telegram.org"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/bot"+c.Key+"/getMe", nil)
			if err != nil {
				return nil, err
			}
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
				OK     bool `json:"ok"`
				Result struct {
					ID        int64  `json:"id"`
					Username  string `json:"username"`
					FirstName string `json:"first_name"`
				} `json:"result"`
			}
			if !decodeJSON(body, &payload) {
				return validOutcome("valid Telegram bot token")
			}
			if !payload.OK {
				return Outcome{Status: Invalid, Detail: "Telegram reported not ok"}
			}
			out := validOutcome("valid Telegram bot token")
			out.Identity = payload.Result.Username
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:     "twilio",
		Category: "Messaging",
		Needs:    []string{"SID", "Password"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.twilio.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/2010-04-01/Accounts/"+c.SID+".json", nil)
			if err != nil {
				return nil, err
			}
			basicAuth(req, c.SID, c.Password)
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid Twilio credentials")
			var payload struct {
				Status       string `json:"status"`
				FriendlyName string `json:"friendly_name"`
			}
			if decodeJSON(body, &payload) {
				out.Detail = "valid Twilio credentials (status: " + payload.Status + ")"
				out.Identity = payload.FriendlyName
			}
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:        "sendgrid",
		Category:    "Messaging",
		KeyPrefixes: []string{"SG."},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.sendgrid.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/v3/user/profile", nil)
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
			out := validOutcome("valid SendGrid API key")
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

	add(Verifier{
		Name:        "mailgun",
		Category:    "Messaging",
		KeyPrefixes: []string{"key-"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.mailgun.net"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/v3/domains", nil)
			if err != nil {
				return nil, err
			}
			basicAuth(req, "api", c.Key)
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid Mailgun API key")
			var payload struct {
				Items []struct {
					Name string `json:"name"`
				} `json:"items"`
			}
			if decodeJSON(body, &payload) {
				out.Detail = fmt.Sprintf("valid Mailgun API key (%d domains)", len(payload.Items))
				out.HighValue = len(payload.Items) > 0
			}
			return out
		},
	})

	add(Verifier{
		Name:     "mailchimp",
		Category: "Messaging",
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				dc := "us1"
				if i := strings.LastIndex(c.Key, "-"); i >= 0 && i+1 < len(c.Key) {
					dc = c.Key[i+1:]
				}
				base = "https://" + dc + ".api.mailchimp.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/3.0/", nil)
			if err != nil {
				return nil, err
			}
			basicAuth(req, "anystring", c.Key)
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid Mailchimp API key")
			var payload struct {
				AccountName string `json:"account_name"`
				Email       string `json:"email"`
			}
			if decodeJSON(body, &payload) {
				out.Identity = payload.AccountName
				if out.Identity == "" {
					out.Identity = payload.Email
				}
			}
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:        "stripe",
		Category:    "Messaging",
		KeyPrefixes: []string{"sk_live_", "sk_test_", "rk_live_", "rk_test_", "pk_live_", "pk_test_"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.stripe.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/v1/balance", nil)
			if err != nil {
				return nil, err
			}
			basicAuth(req, c.Key, "")
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid Stripe key")
			var payload struct {
				Available []struct {
					Amount   int64  `json:"amount"`
					Currency string `json:"currency"`
				} `json:"available"`
			}
			if decodeJSON(body, &payload) && len(payload.Available) > 0 {
				first := payload.Available[0]
				out.Balance = fmt.Sprintf("%d %s", first.Amount, strings.ToUpper(first.Currency))
			}
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:        "square",
		Category:    "Messaging",
		KeyPrefixes: []string{"sq0"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://connect.squareup.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/v2/me", nil)
			if err != nil {
				return nil, err
			}
			bearer(req, c.Key)
			req.Header.Set("Square-Version", "2023-12-13")
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid Square token")
			var payload struct {
				Merchant struct {
					BusinessName string `json:"business_name"`
					Country      string `json:"country"`
				} `json:"merchant"`
			}
			if decodeJSON(body, &payload) {
				out.Identity = payload.Merchant.BusinessName
			}
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:     "paypal",
		Category: "Messaging",
		Needs:    []string{"Username", "Password"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api-m.paypal.com"
			}
			form := url.Values{"grant_type": {"client_credentials"}}
			req, err := http.NewRequest(http.MethodPost, base+"/v1/oauth2/token", strings.NewReader(form.Encode()))
			if err != nil {
				return nil, err
			}
			basicAuth(req, c.Username, c.Password)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
				AccessToken string `json:"access_token"`
			}
			if decodeJSON(body, &payload) && payload.AccessToken == "" {
				return Outcome{Status: Invalid, Detail: "PayPal returned no access token"}
			}
			out := validOutcome("valid PayPal credentials")
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:        "notion",
		Category:    "Collaboration",
		KeyPrefixes: []string{"ntn_", "secret_"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.notion.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/v1/users/me", nil)
			if err != nil {
				return nil, err
			}
			bearer(req, c.Key)
			req.Header.Set("Notion-Version", "2022-06-28")
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid Notion integration token")
			var payload struct {
				Name string `json:"name"`
				Type string `json:"type"`
			}
			if decodeJSON(body, &payload) {
				out.Identity = payload.Name
				out.Tier = payload.Type
			}
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:        "linear",
		Category:    "Collaboration",
		KeyPrefixes: []string{"lin_api_"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.linear.app"
			}
			body := strings.NewReader(`{"query":"{ viewer { id name email } }"}`)
			req, err := http.NewRequest(http.MethodPost, base+"/graphql", body)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", c.Key)
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
				Data struct {
					Viewer struct {
						Name  string `json:"name"`
						Email string `json:"email"`
					} `json:"viewer"`
				} `json:"data"`
				Errors []struct {
					Message string `json:"message"`
				} `json:"errors"`
			}
			if decodeJSON(body, &payload) && payload.Data.Viewer.Name == "" {
				if len(payload.Errors) > 0 {
					return Outcome{Status: Invalid, Detail: "Linear: " + payload.Errors[0].Message}
				}
				return Outcome{Status: Invalid, Detail: "Linear returned no viewer"}
			}
			out := validOutcome("valid Linear API key")
			out.Identity = payload.Data.Viewer.Name
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:     "asana",
		Category: "Collaboration",
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://app.asana.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/api/1.0/users/me", nil)
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
			out := validOutcome("valid Asana token")
			var payload struct {
				Data struct {
					Name  string `json:"name"`
					Email string `json:"email"`
				} `json:"data"`
			}
			if decodeJSON(body, &payload) {
				out.Identity = payload.Data.Name
				if out.Identity == "" {
					out.Identity = payload.Data.Email
				}
			}
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:     "airtable",
		Category: "Collaboration",
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.airtable.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/v0/meta/whoami", nil)
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
			out := validOutcome("valid Airtable API key")
			var payload struct {
				ID    string `json:"id"`
				Email string `json:"email"`
			}
			if decodeJSON(body, &payload) {
				out.Identity = payload.ID
				if out.Identity == "" {
					out.Identity = payload.Email
				}
			}
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:     "atlassian",
		Category: "Collaboration",
		Needs:    []string{"Email", "Password", "Domain"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://" + c.Domain + ".atlassian.net"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/rest/api/3/myself", nil)
			if err != nil {
				return nil, err
			}
			basicAuth(req, c.Email, c.Password)
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid Atlassian credentials")
			var payload struct {
				DisplayName  string `json:"displayName"`
				EmailAddress string `json:"emailAddress"`
			}
			if decodeJSON(body, &payload) {
				out.Identity = payload.DisplayName
				if out.Identity == "" {
					out.Identity = payload.EmailAddress
				}
			}
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:     "segment",
		Category: "Analytics",
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.segment.io"
			}
			body := strings.NewReader(`{"userId":"shhgit","event":"validation","properties":{}}`)
			req, err := http.NewRequest(http.MethodPost, base+"/v1/track", body)
			if err != nil {
				return nil, err
			}
			basicAuth(req, c.Key, "")
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
			out := validOutcome("valid Segment write key")
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:     "mixpanel",
		Category: "Analytics",
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.mixpanel.com"
			}
			payload := base64.StdEncoding.EncodeToString([]byte(`{"event":"validation","properties":{"token":"` + c.Key + `"}}`))
			form := url.Values{"data": {payload}}
			req, err := http.NewRequest(http.MethodPost, base+"/track", strings.NewReader(form.Encode()))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			if strings.TrimSpace(string(body)) == "1" {
				out := validOutcome("valid Mixpanel project token")
				out.HighValue = true
				return out
			}
			return Outcome{Status: Invalid, Detail: "Mixpanel rejected the token"}
		},
	})

	add(Verifier{
		Name:     "amplitude",
		Category: "Analytics",
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api2.amplitude.com"
			}
			body := strings.NewReader(`{"api_key":` + jsonString(c.Key) + `,"events":[{"user_id":"shhgit","event_type":"validation"}]}`)
			req, err := http.NewRequest(http.MethodPost, base+"/2/httpapi", body)
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
				Code int `json:"code"`
			}
			if decodeJSON(body, &payload) && payload.Code != 200 {
				return Outcome{Status: Invalid, Detail: fmt.Sprintf("Amplitude code %d", payload.Code)}
			}
			out := validOutcome("valid Amplitude API key")
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:     "algolia",
		Category: "Analytics",
		Needs:    []string{"AppID"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://" + c.AppID + "-dsn.algolia.net"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/1/keys/"+c.Key, nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("X-Algolia-Application-Id", c.AppID)
			req.Header.Set("X-Algolia-API-Key", c.Key)
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid Algolia key")
			var payload struct {
				ACL []string `json:"acl"`
			}
			if decodeJSON(body, &payload) {
				out.Detail = fmt.Sprintf("valid Algolia key (%d permissions)", len(payload.ACL))
				for _, a := range payload.ACL {
					if strings.EqualFold(a, "admin") {
						out.HighValue = true
					}
				}
			}
			return out
		},
	})

	add(Verifier{
		Name:     "zendesk",
		Category: "CRM",
		Needs:    []string{"Subdomain", "Email", "Password"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://" + c.Subdomain + ".zendesk.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/api/v2/users/me.json", nil)
			if err != nil {
				return nil, err
			}
			basicAuth(req, c.Email+"/token", c.Password)
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid Zendesk API token")
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

	add(Verifier{
		Name:     "intercom",
		Category: "CRM",
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.intercom.io"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/me", nil)
			if err != nil {
				return nil, err
			}
			bearer(req, c.Key)
			req.Header.Set("Accept", "application/json")
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid Intercom access token")
			var payload struct {
				Name  string `json:"name"`
				Email string `json:"email"`
				App   struct {
					IDCode string `json:"id_code"`
				} `json:"app"`
			}
			if decodeJSON(body, &payload) {
				out.Identity = payload.Name
				if out.Identity == "" {
					out.Identity = payload.Email
				}
				out.Extra = map[string]string{"app": payload.App.IDCode}
			}
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:     "freshdesk",
		Category: "CRM",
		Needs:    []string{"Subdomain", "Password"},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://" + c.Subdomain + ".freshdesk.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/api/v2/agents/me", nil)
			if err != nil {
				return nil, err
			}
			basicAuth(req, c.Password, "X")
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid Freshdesk API key")
			var payload struct {
				Contact struct {
					Name  string `json:"name"`
					Email string `json:"email"`
				} `json:"contact"`
			}
			if decodeJSON(body, &payload) {
				out.Identity = payload.Contact.Name
				if out.Identity == "" {
					out.Identity = payload.Contact.Email
				}
			}
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:        "mapbox",
		Category:    "Maps",
		KeyPrefixes: []string{"pk.", "sk."},
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://api.mapbox.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/tokens/v2?access_token="+url.QueryEscape(c.Key), nil)
			if err != nil {
				return nil, err
			}
			return req, nil
		},
		Test: func(resp *http.Response, body []byte) Outcome {
			if o, ok := mapStatus(resp); ok {
				return o
			}
			if !succeeded(resp) {
				return unexpected(resp)
			}
			out := validOutcome("valid Mapbox access token")
			out.HighValue = true
			return out
		},
	})

	add(Verifier{
		Name:     "google_maps",
		Category: "Maps",
		Build: func(c Credential, base string) (*http.Request, error) {
			if base == "" {
				base = "https://maps.googleapis.com"
			}
			req, err := http.NewRequest(http.MethodGet, base+"/maps/api/geocode/json?address=test&key="+url.QueryEscape(c.Key), nil)
			if err != nil {
				return nil, err
			}
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
				Status       string `json:"status"`
				ErrorMessage string `json:"error_message"`
			}
			if !decodeJSON(body, &payload) {
				return validOutcome("valid Google Maps key")
			}
			switch payload.Status {
			case "OK", "ZERO_RESULTS":
				out := validOutcome("valid Google Maps key")
				out.HighValue = true
				return out
			case "REQUEST_DENIED":
				detail := "Google Maps key denied"
				if payload.ErrorMessage != "" {
					detail = "Google Maps: " + payload.ErrorMessage
				}
				return Outcome{Status: Invalid, Detail: detail}
			default:
				return Outcome{Status: Unverified, Detail: "Google Maps status: " + payload.Status}
			}
		},
	})
}
