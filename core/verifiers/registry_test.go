package verifiers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// wantRegistryCount pins the exact number of registered verifiers. Update it
// deliberately when adding or removing a provider; the count is asserted so an
// accidental deletion cannot silently shrink the registry.
const wantRegistryCount = 56

func TestRegistryCount(t *testing.T) {
	if got := len(All()); got != wantRegistryCount {
		t.Fatalf("registry has %d verifiers, want %d", got, wantRegistryCount)
	}
}

func TestRegistryShape(t *testing.T) {
	for _, v := range All() {
		if v.Name == "" {
			t.Errorf("verifier with empty name")
		}
		if v.Category == "" {
			t.Errorf("%s: empty category", v.Name)
		}
		if v.Build == nil {
			t.Errorf("%s: nil Build", v.Name)
		}
		if v.Test == nil {
			t.Errorf("%s: nil Test", v.Name)
		}
	}
}

func TestLookupAndDetect(t *testing.T) {
	if _, ok := Lookup("openai"); !ok {
		t.Fatalf("Lookup(openai) not found")
	}
	if _, ok := Lookup("does-not-exist"); ok {
		t.Fatalf("Lookup(does-not-exist) unexpectedly found")
	}

	cases := map[string]string{
		"ghp_abc":      "github",
		"github_pat_x": "github",
		"glpat-abc":    "gitlab",
		"sk-ant-api03": "anthropic",
		"sk-or-v1-abc": "openrouter",
		"AIzaSyabc":    "google",
		"hf_abc":       "huggingface",
		"r8_abc":       "replicate",
		"gsk_abc":      "groq",
		"pplx-abc":     "perplexity",
		"xai-abc":      "xai",
		"dop_v1_abc":   "digitalocean",
		"dckr_pat_abc": "dockerhub",
		"npm_abc":      "npm",
		"xoxb-abc":     "slack",
		"sk_live_abc":  "stripe",
		"sq0atp-abc":   "square",
		"SG.abc.def":   "sendgrid",
		"key-abc":      "mailgun",
	}
	for input, want := range cases {
		got, ok := Detect(input)
		if !ok {
			t.Errorf("Detect(%q) found nothing, want %s", input, want)
			continue
		}
		if got.Name != want {
			t.Errorf("Detect(%q) = %s, want %s", input, got.Name, want)
		}
	}
	if _, ok := Detect(""); ok {
		t.Errorf("Detect(\"\") should not match")
	}
}

type fixture struct {
	cred         Credential
	status       int
	body         string
	headers      map[string]string
	want         Status
	wantIdentity string
}

func runFixture(t *testing.T, v Verifier, f fixture) Outcome {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, val := range f.headers {
			w.Header().Set(k, val)
		}
		if f.status != 0 {
			w.WriteHeader(f.status)
		}
		_, _ = io.WriteString(w, f.body)
	}))
	defer srv.Close()
	out, err := v.Verify(srv.Client(), f.cred, srv.URL)
	if err != nil {
		t.Fatalf("%s: Verify returned error: %v", v.Name, err)
	}
	return out
}

// fixtures provides one success fixture per registered verifier. Every entry
// must use the exact registered Name; the table is asserted to cover All().
var fixtures = map[string]fixture{
	// AI
	"openai":      {cred: Credential{Key: "sk-test"}, body: `{"data":[{"id":"gpt-4o"},{"id":"gpt-3.5-turbo"}]}`, headers: map[string]string{"x-ratelimit-limit-requests": "500"}, want: Valid},
	"anthropic":   {cred: Credential{Key: "sk-ant-x"}, body: `{"id":"msg_1"}`, want: Valid},
	"google":      {cred: Credential{Key: "AIzaSyExample"}, body: `{"models":[]}`, want: Valid},
	"xai":         {cred: Credential{Key: "xai-x"}, body: `{"data":[{"id":"grok-2"}]}`, want: Valid},
	"openrouter":  {cred: Credential{Key: "sk-or-v1-x"}, body: `{"data":{"label":"proj","credits":42.5,"is_active":true}}`, want: Valid},
	"huggingface": {cred: Credential{Key: "hf_x"}, body: `{"name":"alice","email":"a@b.c"}`, want: Valid, wantIdentity: "alice"},
	"replicate":   {cred: Credential{Key: "r8_x"}, body: `{"username":"bob","name":"Bob"}`, want: Valid, wantIdentity: "bob"},
	"elevenlabs":  {cred: Credential{Key: "sk_x"}, body: `{"subscription":{"tier":"free","character_count":1,"character_limit":10}}`, want: Valid},
	"deepseek":    {cred: Credential{Key: "sk-x"}, body: `{"data":[{"id":"deepseek-chat"}]}`, want: Valid},
	"groq":        {cred: Credential{Key: "gsk_x"}, body: `{"data":[{"id":"llama"}]}`, want: Valid},
	"mistral":     {cred: Credential{Key: "m-x"}, body: `{"data":[{"id":"mistral-large"}]}`, want: Valid},
	"perplexity":  {cred: Credential{Key: "pplx-x"}, body: `{"data":[{"id":"sonar"}]}`, want: Valid},
	"together":    {cred: Credential{Key: "t-x"}, body: `{"data":[{"id":"x"}]}`, want: Valid},
	"fireworks":   {cred: Credential{Key: "f-x"}, body: `{"data":[{"id":"x"}]}`, want: Valid},
	"moonshot":    {cred: Credential{Key: "sk-x"}, body: `{"data":[{"id":"kimi"}]}`, want: Valid},
	"dashscope":   {cred: Credential{Key: "sk-x"}, body: `{"data":[{"id":"qwen"}]}`, want: Valid},
	"cohere":      {cred: Credential{Key: "co-x"}, body: `{"valid":true}`, want: Valid},

	// VCS / CI
	"github":    {cred: Credential{Key: "ghp_x"}, body: `{"login":"octocat","plan":{"name":"pro"}}`, want: Valid, wantIdentity: "octocat"},
	"gitlab":    {cred: Credential{Key: "glpat-x"}, body: `{"username":"gl","name":"GL"}`, want: Valid, wantIdentity: "gl"},
	"bitbucket": {cred: Credential{Username: "u", Password: "p"}, body: `{"display_name":"BB","username":"bb"}`, want: Valid, wantIdentity: "BB"},
	"circleci":  {cred: Credential{Key: "ci"}, body: `{"login":"ci","name":"CI"}`, want: Valid, wantIdentity: "ci"},
	"travis":    {cred: Credential{Key: "tv"}, body: `{"login":"tv"}`, want: Valid, wantIdentity: "tv"},
	"dockerhub": {cred: Credential{Username: "u", Password: "p"}, body: `{"token":"jwt"}`, want: Valid},
	"npm":       {cred: Credential{Key: "npm_x"}, body: `{"username":"np"}`, want: Valid, wantIdentity: "np"},

	// Cloud
	"digitalocean": {cred: Credential{Key: "dop_v1_x"}, body: `{"account":{"email":"do@x.com","droplet_limit":10}}`, want: Valid, wantIdentity: "do@x.com"},
	"cloudflare":   {cred: Credential{Key: "cf"}, body: `{"success":true,"result":{"status":"active"}}`, want: Valid},
	"vercel":       {cred: Credential{Key: "v"}, body: `{"user":{"username":"vercel"}}`, want: Valid, wantIdentity: "vercel"},
	"heroku":       {cred: Credential{Key: "h"}, body: `{"email":"h@x.com"}`, want: Valid, wantIdentity: "h@x.com"},

	// Monitoring
	"datadog":   {cred: Credential{Key: "dd"}, body: `{"valid":true}`, want: Valid},
	"sentry":    {cred: Credential{Key: "s"}, body: `{"user":{"username":"sentry"}}`, want: Valid, wantIdentity: "sentry"},
	"newrelic":  {cred: Credential{Key: "nr"}, body: `{"data":{"actor":{"user":{"name":"NR","email":"nr@x"}}}}`, want: Valid, wantIdentity: "NR"},
	"pagerduty": {cred: Credential{Key: "pd"}, body: `{"user":{"name":"PD"}}`, want: Valid, wantIdentity: "PD"},

	// Messaging / Payments / Collaboration / Analytics / CRM / Maps
	"discord":     {cred: Credential{Key: "tok"}, body: `{"id":"1","username":"bot","discriminator":"0"}`, want: Valid, wantIdentity: "bot"},
	"slack":       {cred: Credential{Key: "xoxb-x"}, body: `{"ok":true,"team":"T","user":"U"}`, want: Valid, wantIdentity: "U"},
	"telegram":    {cred: Credential{Key: "123:abc"}, body: `{"ok":true,"result":{"id":1,"username":"tb"}}`, want: Valid, wantIdentity: "tb"},
	"twilio":      {cred: Credential{SID: "AC1", Password: "tok"}, body: `{"status":"active","friendly_name":"Acme"}`, want: Valid, wantIdentity: "Acme"},
	"sendgrid":    {cred: Credential{Key: "SG.x.y"}, body: `{"email":"sg@x.com"}`, want: Valid, wantIdentity: "sg@x.com"},
	"mailgun":     {cred: Credential{Key: "key-x"}, body: `{"items":[{"name":"d1"}]}`, want: Valid},
	"mailchimp":   {cred: Credential{Key: "abc-us1"}, body: `{"account_name":"MC"}`, want: Valid, wantIdentity: "MC"},
	"stripe":      {cred: Credential{Key: "sk_live_x"}, body: `{"available":[{"amount":1500,"currency":"usd"}]}`, want: Valid},
	"square":      {cred: Credential{Key: "sq0atp-x"}, body: `{"merchant":{"business_name":"SQ","country":"US"}}`, want: Valid, wantIdentity: "SQ"},
	"paypal":      {cred: Credential{Username: "cid", Password: "sec"}, body: `{"access_token":"tok"}`, want: Valid},
	"notion":      {cred: Credential{Key: "ntn_x"}, body: `{"name":"N","type":"bot"}`, want: Valid, wantIdentity: "N"},
	"linear":      {cred: Credential{Key: "lin_x"}, body: `{"data":{"viewer":{"name":"L","email":"l@x"}}}`, want: Valid, wantIdentity: "L"},
	"asana":       {cred: Credential{Key: "as_x"}, body: `{"data":{"name":"A","email":"a@x"}}`, want: Valid, wantIdentity: "A"},
	"airtable":    {cred: Credential{Key: "at_x"}, body: `{"id":"usr1","email":"at@x"}`, want: Valid, wantIdentity: "usr1"},
	"atlassian":   {cred: Credential{Email: "a@x", Password: "tok", Domain: "ex"}, body: `{"displayName":"At"}`, want: Valid, wantIdentity: "At"},
	"segment":     {cred: Credential{Key: "seg"}, body: `{"success":true}`, want: Valid},
	"mixpanel":    {cred: Credential{Key: "mp"}, body: `1`, want: Valid},
	"amplitude":   {cred: Credential{Key: "amp"}, body: `{"code":200}`, want: Valid},
	"algolia":     {cred: Credential{Key: "akey", AppID: "APP"}, body: `{"acl":["search"]}`, want: Valid},
	"zendesk":     {cred: Credential{Subdomain: "sub", Email: "a@x", Password: "tok"}, body: `{"user":{"name":"Z","email":"z@x"}}`, want: Valid, wantIdentity: "Z"},
	"intercom":    {cred: Credential{Key: "int"}, body: `{"name":"I","app":{"id_code":"app1"}}`, want: Valid, wantIdentity: "I"},
	"freshdesk":   {cred: Credential{Subdomain: "sub", Password: "key"}, body: `{"contact":{"name":"F","email":"f@x"}}`, want: Valid, wantIdentity: "F"},
	"mapbox":      {cred: Credential{Key: "pk.mapbox"}, body: `{}`, want: Valid},
	"google_maps": {cred: Credential{Key: "AIzaSyExample"}, body: `{"status":"OK"}`, want: Valid},
}

func TestFixturesCoverRegistry(t *testing.T) {
	all := All()
	for _, v := range all {
		if _, ok := fixtures[v.Name]; !ok {
			t.Fatalf("%s has no fixture", v.Name)
		}
	}
	if len(fixtures) != len(all) {
		t.Fatalf("fixture table has %d entries for %d verifiers (stale fixture?)", len(fixtures), len(all))
	}
}

func TestFixturesSucceed(t *testing.T) {
	for _, v := range All() {
		f, ok := fixtures[v.Name]
		if !ok {
			t.Fatalf("%s has no fixture", v.Name)
		}
		out := runFixture(t, v, f)
		if out.Status != f.want {
			t.Errorf("%s: status = %s, want %s (detail: %s)", v.Name, out.Status, f.want, out.Detail)
		}
		if f.wantIdentity != "" && out.Identity != f.wantIdentity {
			t.Errorf("%s: identity = %q, want %q", v.Name, out.Identity, f.wantIdentity)
		}
	}
}

func TestOpenAIMetadata(t *testing.T) {
	out := runFixture(t, mustLookup(t, "openai"), fixture{
		cred:    Credential{Key: "sk-x"},
		body:    `{"data":[{"id":"gpt-4-turbo"}]}`,
		headers: map[string]string{"x-ratelimit-limit-requests": "5000"},
		want:    Valid,
	})
	if out.Tier != "GPT-4" {
		t.Errorf("Tier = %q, want GPT-4", out.Tier)
	}
	if out.RPM != 5000 {
		t.Errorf("RPM = %d, want 5000", out.RPM)
	}
}

func TestStripeBalance(t *testing.T) {
	out := runFixture(t, mustLookup(t, "stripe"), fixture{
		cred: Credential{Key: "sk_live_x"},
		body: `{"available":[{"amount":1500,"currency":"usd"}]}`,
		want: Valid,
	})
	if out.Balance != "1500 USD" {
		t.Errorf("Balance = %q, want %q", out.Balance, "1500 USD")
	}
}

func TestSlackIdentityAndTeam(t *testing.T) {
	out := runFixture(t, mustLookup(t, "slack"), fixture{
		cred: Credential{Key: "xoxb-x"},
		body: `{"ok":true,"team":"Acme","user":"alice"}`,
		want: Valid,
	})
	if out.Identity != "alice" {
		t.Errorf("Identity = %q, want alice", out.Identity)
	}
	if out.Extra["team"] != "Acme" {
		t.Errorf("Extra[team] = %q, want Acme", out.Extra["team"])
	}
}

func TestCloudflareTokenAndGlobalKeyPaths(t *testing.T) {
	cf := mustLookup(t, "cloudflare")

	var tokenPath, globalPath bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/client/v4/user/tokens/verify":
			tokenPath = true
			if got := r.Header.Get("Authorization"); got != "Bearer tok" {
				t.Errorf("token path Authorization = %q", got)
			}
		case "/client/v4/user":
			globalPath = true
			if got := r.Header.Get("X-Auth-Key"); got != "key" {
				t.Errorf("global path X-Auth-Key = %q", got)
			}
			if got := r.Header.Get("X-Auth-Email"); got != "e@x.com" {
				t.Errorf("global path X-Auth-Email = %q", got)
			}
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"success":true}`)
	}))
	defer srv.Close()

	if out, err := cf.Verify(srv.Client(), Credential{Key: "tok"}, srv.URL); err != nil || out.Status != Valid {
		t.Fatalf("token path: status=%s err=%v", out.Status, err)
	}
	if out, err := cf.Verify(srv.Client(), Credential{Key: "key", Email: "e@x.com"}, srv.URL); err != nil || out.Status != Valid {
		t.Fatalf("global path: status=%s err=%v", out.Status, err)
	}
	if !tokenPath || !globalPath {
		t.Errorf("expected both paths, token=%v global=%v", tokenPath, globalPath)
	}
}

func TestNeedsCompanionMakesNoRequest(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	twilio := mustLookup(t, "twilio")
	out, err := twilio.Verify(srv.Client(), Credential{Key: "only-key"}, srv.URL)
	if err != nil {
		t.Fatalf("Verify returned error: %v", err)
	}
	if out.Status != Unverified {
		t.Errorf("status = %s, want unverified", out.Status)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("provider observed %d requests, want 0", n)
	}
}

func TestStatusMapping(t *testing.T) {
	openai := mustLookup(t, "openai")
	cases := []struct {
		status int
		want   Status
	}{
		{http.StatusTooManyRequests, QuotaLimited},
		{http.StatusUnauthorized, Invalid},
		{http.StatusForbidden, Invalid},
		{http.StatusInternalServerError, Unverified},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, `{"error":"x"}`)
		}))
		out, err := openai.Verify(srv.Client(), Credential{Key: "sk-x"}, srv.URL)
		srv.Close()
		if err != nil {
			t.Fatalf("status %d: Verify error: %v", tc.status, err)
		}
		if out.Status != tc.want {
			t.Errorf("status %d -> %s, want %s", tc.status, out.Status, tc.want)
		}
	}
}

func mustLookup(t *testing.T, name string) Verifier {
	t.Helper()
	v, ok := Lookup(name)
	if !ok {
		t.Fatalf("Lookup(%q) not found", name)
	}
	return v
}
