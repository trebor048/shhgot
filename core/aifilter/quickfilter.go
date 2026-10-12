// Package aifilter holds the opt-in false-positive pre-filter for shhgit
// findings. It is intentionally self-contained: it never imports core, so the
// AI filter can be reasoned about and tested in isolation.
package aifilter

import (
	"math"
	"regexp"
)

// QuickResult is the deterministic verdict for a single candidate secret.
// A zero value means "keep": the candidate is not an obvious false positive.
type QuickResult struct {
	Suppress bool
	Reason   string
}

const (
	reasonPlaceholder = "placeholder-pattern"
	reasonDocPath     = "doc-or-test-path"
	reasonContext     = "context-marker"
	reasonLowEntropy  = "low-entropy"
	reasonRepeating   = "repeating-pattern"
)

// placeholderUnion is the union of obvious placeholder shapes. It is kept as a
// single expression so the rule stays easy to extend by hand. The bare `test`
// token is word-bounded so an unrelated high-entropy secret containing the
// substring "test" is not suppressed; repetition runs (x{3,}/a{3,}/1{3,}) stay
// because the truth-table pins them as placeholder-shaped.
const placeholderUnion = `\btest\b|demo|example|sample|fake|dummy|mock|placeholder|your[_-]?api[_-]?key|insert[_-]?here|replace[_-]?me|x{3,}|a{3,}|1{3,}|abc{2,}|\*{3,}|<[^>]+>|\$\{[^}]+\}|\{\{[^}]+\}\}`

// PlaceholderRegex matches the placeholder union against a candidate secret.
var PlaceholderRegex = regexp.MustCompile(`(?i)(?:` + placeholderUnion + `)`)

// docPathRegex matches documentation and test paths whose contents are unlikely
// to hold a live credential. The directory names are anchored to whole path
// components (start/end of path or "/") so a substring such as "contest/",
// "attestation/" or "latest/" is not mistaken for a test/docs directory, and
// the bare documentation extensions are matched on their own.
var docPathRegex = regexp.MustCompile(`(?i)(?:^|/)(?:readme|tests?|mocks?|samples?|demos?|examples?|docs?)(?:/|$)|\.(?:md|rst|txt)$`)

// contextMarkerRegex matches comments that flag a value as a documentation or
// replacement marker.
var contextMarkerRegex = regexp.MustCompile(`(?i)# example|// example|# replace|TODO:|FIXME:`)

// QuickFilter applies the ordered deterministic rules to one candidate. The
// first matching rule wins and its reason is returned. No rule matching means
// the candidate is kept.
func QuickFilter(candidate, context, filePath string) QuickResult {
	if PlaceholderRegex.MatchString(candidate) {
		return QuickResult{Suppress: true, Reason: reasonPlaceholder}
	}
	if docPathRegex.MatchString(filePath) {
		return QuickResult{Suppress: true, Reason: reasonDocPath}
	}
	if contextMarkerRegex.MatchString(context) {
		return QuickResult{Suppress: true, Reason: reasonContext}
	}
	if shannonEntropy(candidate) < 3.0 {
		return QuickResult{Suppress: true, Reason: reasonLowEntropy}
	}
	if hasRepeatingPattern(candidate) {
		return QuickResult{Suppress: true, Reason: reasonRepeating}
	}
	return QuickResult{}
}

// shannonEntropy returns the per-symbol Shannon entropy of s in bits, computed
// over runes so multi-byte secrets are measured correctly.
func shannonEntropy(s string) float64 {
	if s == "" {
		return 0
	}
	counts := make(map[rune]int)
	total := 0
	for _, r := range s {
		counts[r]++
		total++
	}
	var entropy float64
	for _, count := range counts {
		p := float64(count) / float64(total)
		entropy -= p * math.Log2(p)
	}
	return entropy
}

// hasRepeatingPattern reports whether s contains three identical consecutive
// bytes, or a 2-6 byte prefix repeated three times.
func hasRepeatingPattern(s string) bool {
	for i := 0; i+2 < len(s); i++ {
		if s[i] == s[i+1] && s[i+1] == s[i+2] {
			return true
		}
	}
	for n := 2; n <= 6; n++ {
		if len(s) < 3*n {
			continue
		}
		prefix := s[:n]
		if s[n:2*n] == prefix && s[2*n:3*n] == prefix {
			return true
		}
	}
	return false
}
