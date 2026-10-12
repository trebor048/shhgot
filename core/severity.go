package core

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/trebor048/shhgot/core/verifiers"
)

// Severity is the assessed impact of a finding, distinct from a signature's
// static rule priority. It is derived from the verifier outcome when one
// exists and falls back to the signature priority otherwise.
type Severity int

const (
	// SevInfo is informational (summaries and other non-finding output).
	SevInfo Severity = iota
	// SevLow is an invalid or unreachable credential.
	SevLow
	// SevMedium is an ordinary valid credential.
	SevMedium
	// SevHigh is a valid high-value or funded credential.
	SevHigh
	// SevCritical is a valid, funded top-tier model credential.
	SevCritical
)

// String renders the severity as its lowercase wire name.
func (s Severity) String() string {
	switch s {
	case SevInfo:
		return "info"
	case SevLow:
		return "low"
	case SevMedium:
		return "medium"
	case SevHigh:
		return "high"
	case SevCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// TopTierModels lists the model tiers whose funded credentials are critical.
// A tier matches when it contains one of these substrings.
var TopTierModels = []string{
	"GPT-4",
	"GPT-4o",
	"Claude-3-Opus",
	"Claude-3.5-Sonnet",
}

const (
	// CriticalBalance is the minimum parsed balance that makes a valid
	// top-tier key critical.
	CriticalBalance float64 = 100
	// HighBalance is the minimum parsed balance that makes a valid key high.
	HighBalance float64 = 10

	priorityMedium   = 1
	priorityHigh     = 2
	priorityCritical = 3
)

// balanceNumberRE matches the first number in a balance string, tolerating
// thousands separators and an optional fraction.
var balanceNumberRE = regexp.MustCompile(`[\d,]+(?:\.\d+)?`)

// Classify returns the severity for a finding. When o is nil or carries no
// usable verification result, the signature priority (0..3) is mapped to
// SevLow..SevCritical; otherwise the verifier metadata drives the result.
func Classify(sigPriority int, o *verifiers.Outcome) Severity {
	if o == nil {
		return prioritySeverity(sigPriority)
	}
	switch o.Status {
	case verifiers.Invalid, verifiers.Dead:
		return SevLow
	case verifiers.Valid:
		return classifyValid(o)
	default:
		// Unverified or QuotaLimited: the credential's validity is unknown,
		// so fall back to the signature priority.
		return prioritySeverity(sigPriority)
	}
}

// classifyValid derives severity from a verified-valid outcome.
func classifyValid(o *verifiers.Outcome) Severity {
	topTier := isTopTier(o.Tier)
	balance := parseBalance(o.Balance)
	unlimited := strings.Contains(strings.ToLower(o.Balance), "unlimited")
	switch {
	case topTier && (balance >= CriticalBalance || unlimited):
		return SevCritical
	case topTier || balance >= HighBalance || o.HighValue:
		return SevHigh
	default:
		return SevMedium
	}
}

// prioritySeverity maps a signature priority to a severity, clamping values
// outside 0..3 to the nearest bound.
func prioritySeverity(p int) Severity {
	switch {
	case p >= priorityCritical:
		return SevCritical
	case p == priorityHigh:
		return SevHigh
	case p == priorityMedium:
		return SevMedium
	default:
		return SevLow
	}
}

// isTopTier reports whether tier names a top-tier model. The comparison is
// case-insensitive: verifier tiers arrive in canonical case today, but a raw
// mixed-case tier (e.g. "gpt-4o-mini") must not silently under-severitise a
// funded key to High/Medium.
func isTopTier(tier string) bool {
	lowered := strings.ToLower(tier)
	for _, m := range TopTierModels {
		if strings.Contains(lowered, strings.ToLower(m)) {
			return true
		}
	}
	return false
}

// parseBalance extracts the first number from a balance string such as
// "$1,234.56", stripping thousands separators. It returns 0 when no number is
// present.
func parseBalance(s string) float64 {
	match := balanceNumberRE.FindString(s)
	if match == "" {
		return 0
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(match, ",", ""), 64)
	if err != nil {
		return 0
	}
	return v
}
