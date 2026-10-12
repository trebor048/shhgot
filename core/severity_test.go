package core

import (
	"net/http"
	"testing"

	"github.com/trebor048/shhgot/core/verifiers"
)

func TestSeverityString(t *testing.T) {
	cases := []struct {
		sev  Severity
		want string
	}{
		{SevInfo, "info"},
		{SevLow, "low"},
		{SevMedium, "medium"},
		{SevHigh, "high"},
		{SevCritical, "critical"},
	}
	for _, c := range cases {
		if got := c.sev.String(); got != c.want {
			t.Errorf("Severity(%d).String() = %q, want %q", c.sev, got, c.want)
		}
	}
}

func TestSeverityClassify(t *testing.T) {
	outcome := func(status verifiers.Status, tier, balance string) *verifiers.Outcome {
		return &verifiers.Outcome{Status: status, Tier: tier, Balance: balance}
	}

	cases := []struct {
		name     string
		priority int
		outcome  *verifiers.Outcome
		want     Severity
	}{
		{"nil outcome priority 0", 0, nil, SevLow},
		{"nil outcome priority 1", 1, nil, SevMedium},
		{"nil outcome priority 2", 2, nil, SevHigh},
		{"nil outcome priority 3", 3, nil, SevCritical},

		{"funded top-tier critical", 0, outcome(verifiers.Valid, "GPT-4", "$150.00"), SevCritical},
		{"comma balance critical", 0, outcome(verifiers.Valid, "GPT-4o", "$1,234.56"), SevCritical},
		{"unlimited balance critical", 0, outcome(verifiers.Valid, "Claude-3-Opus", "unlimited"), SevCritical},
		{"unfunded top-tier high", 0, outcome(verifiers.Valid, "Claude-3.5-Sonnet", "$0.00"), SevHigh},
		{"valid balance at high bound", 0, outcome(verifiers.Valid, "", "$10.00"), SevHigh},
		{"valid high value high", 0, &verifiers.Outcome{Status: verifiers.Valid, HighValue: true}, SevHigh},
		{"valid unfunded medium", 0, outcome(verifiers.Valid, "", ""), SevMedium},

		{"invalid low", 0, outcome(verifiers.Invalid, "GPT-4", "$150.00"), SevLow},
		{"dead low", 0, outcome(verifiers.Dead, "GPT-4", "$150.00"), SevLow},

		{"unverified priority 0 low", 0, outcome(verifiers.Unverified, "", ""), SevLow},
		{"unverified priority 3 critical", 3, outcome(verifiers.Unverified, "", ""), SevCritical},

		{"quota-limited priority 0 low", 0, outcome(verifiers.QuotaLimited, "", ""), SevLow},
		{"quota-limited priority 1 medium", 1, outcome(verifiers.QuotaLimited, "", ""), SevMedium},
		{"quota-limited priority 3 critical", 3, outcome(verifiers.QuotaLimited, "", ""), SevCritical},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.priority, c.outcome); got != c.want {
				t.Errorf("Classify(%d, %+v) = %s, want %s", c.priority, c.outcome, got, c.want)
			}
		})
	}
}

// TestIsTopTierCaseInsensitive proves a raw mixed-case tier still matches: a
// funded top-tier key must not under-severitise because of capitalisation.
func TestIsTopTierCaseInsensitive(t *testing.T) {
	for _, tier := range []string{"gpt-4o-mini", "CLAUDE-3-OPUS", "Claude-3.5-sonnet"} {
		if !isTopTier(tier) {
			t.Errorf("isTopTier(%q) = false, want true", tier)
		}
	}
	if isTopTier("gpt-3.5-turbo") {
		t.Error(`isTopTier("gpt-3.5-turbo") = true, want false`)
	}
}

// TestAnthropicVerifiedOpusClassifies proves a verified Opus key is actually
// top-tier: before the Anthropic verifier set Tier from the response model, no
// verifier ever produced a Claude tier, so the TopTierModels Claude entries were
// unreachable and a funded Opus key could not classify critical. The verifier's
// real Test hook is exercised so this fails if the tier is dropped again.
func TestAnthropicVerifiedOpusClassifies(t *testing.T) {
	v, ok := verifiers.Lookup("anthropic")
	if !ok {
		t.Fatal("anthropic verifier not registered")
	}
	out := v.Test(&http.Response{StatusCode: http.StatusOK, Header: make(http.Header)},
		[]byte(`{"id":"msg_1","model":"claude-3-opus-20240229"}`))
	if out.Status != verifiers.Valid {
		t.Fatalf("status = %s, want valid", out.Status)
	}
	if got := Classify(0, &out); got != SevHigh {
		t.Errorf("unfunded opus = %s, want high (tier %q)", got, out.Tier)
	}
	funded := out
	funded.Balance = "$150.00"
	if got := Classify(0, &funded); got != SevCritical {
		t.Errorf("funded opus = %s, want critical", got)
	}
}

func TestSeverityParseBalance(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"$150.00", 150},
		{"$1,234.56", 1234.56},
		{"10 credits", 10},
		{"unlimited", 0},
		{"", 0},
	}
	for _, c := range cases {
		if got := parseBalance(c.in); got != c.want {
			t.Errorf("parseBalance(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
