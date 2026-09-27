package core

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// prefilterAllows mirrors PatternSignature.prefilterMiss: it reports whether the
// prefilter would let the regex run.
func prefilterAllows(pf [][]byte, hay []byte) bool {
	if len(pf) == 0 {
		return true
	}
	for _, l := range pf {
		if bytes.Contains(hay, l) {
			return true
		}
	}
	return false
}

// TestRequiredLiteralsNeverDropMatches is the soundness check for the prefilter:
// for every pattern, every input the regex matches must still pass the
// prefilter. A rejection here would mean a real secret is silently skipped, so
// this is the test that guards the optimisation.
func TestRequiredLiteralsNeverDropMatches(t *testing.T) {
	patterns := []string{
		`AKIA[0-9A-Z]{16}`,
		`sk-[a-zA-Z0-9]{48}`,
		`[A-Za-z0-9]{32}`,
		`(admin_token|admintoken|admin-token)\s*[:=]\s*["']([^"']{20,})["']`,
		`(?i)(openai|gpt)[\s_-]*(api[\s_-]*key|token)\s*[:=]\s*["']?(sk-[A-Za-z0-9]{48})["']?`,
		`\b(function|class|const|let|var|import|export|return)\b`,
		`(?i)(anthropic|claude)[\s_-]*(api[\s_-]*key|token)\s*[:=]\s*["']?(sk-ant-[A-Za-z0-9_-]{95,})["']?`,
		`(?:(?:sk-or-v1-[A-Za-z0-9]{40,})|(?:openrouter_[A-Za-z0-9]{40}))`,
		`^\.?env\.(production|development|local)`,
		`(?i)(huggingface|hf)[\s_-]*(api[\s_-]*key|token)\s*[:=]\s*["']?(hf_[A-Za-z0-9]{34})["']?`,
	}

	inputs := []string{
		`AKIAIOSFODNN7EXAMPLE`,
		`sk-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKL`,
		`01234567890123456789012345678901`,
		`admin_token = "0123456789012345678901"`,
		`OPENAI_API_KEY=sk-abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOP`,
		`const x = 1; export default x`,
		`CLAUDE_API_KEY = "sk-ant-` + strings.Repeat("a", 100) + `"`,
		`openrouter_abcdefghijklmnopqrstuvwxyz0123456789ABCD`,
		`sk-or-v1-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOP`,
		`.env.production`,
		`HF_TOKEN=hf_abcdefghijklmnopqrstuvwxyz0123456789AB`,
		`nothing to see here`,
	}

	for _, p := range patterns {
		re := regexp.MustCompile(p)
		pf, fold := requiredLiterals(p)
		for _, in := range inputs {
			if !re.MatchString(in) {
				continue
			}
			hay := []byte(in)
			if fold {
				hay = bytes.ToLower(hay)
			}
			if !prefilterAllows(pf, hay) {
				t.Errorf("prefilter rejected an input the regex matches\n  pattern: %s\n  input:   %s\n  filters: %q", p, in, pf)
			}
		}
	}
}

// TestRequiredLiteralsDerived pins the shape of the derived filters so a
// regression that silently drops one (disabling the optimisation) is caught.
func TestRequiredLiteralsDerived(t *testing.T) {
	cases := []struct {
		pattern  string
		wantLits int
		wantFold bool
	}{
		{`AKIA[0-9A-Z]{16}`, 1, false},
		{`(?i)(openai|gpt)\s*key`, 2, true},
		// Simplify() factors the common "admin" prefix, which is still sound.
		{`(admin_token|admintoken|admin-token)\s*[:=]`, 1, false},
		{`\b(function|class)\b`, 2, false},
		{`[A-Za-z0-9]{32}`, 0, false}, // no literal: no prefilter, regex always runs
	}
	for _, c := range cases {
		pf, fold := requiredLiterals(c.pattern)
		if len(pf) != c.wantLits {
			t.Errorf("%s: got %d literals (%q), want %d", c.pattern, len(pf), pf, c.wantLits)
		}
		if len(pf) > 0 && fold != c.wantFold {
			t.Errorf("%s: fold=%v, want %v", c.pattern, fold, c.wantFold)
		}
	}
}
