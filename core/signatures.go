package core

import (
	"fmt"
	"regexp"
	"strings"
)

// placeholderRe matches values that are templates, code references or
// environment lookups rather than credentials: "<password>", "${GH_TOKEN}",
// "{{ api_key }}", "YOUR_API_KEY", "$($discovery.token)", "%TEMP%",
// "REPLACE_WITH_...", "bp_dev_only", "os.environ['KEY']". Example configs, CI
// workflow definitions, deploy scripts and documentation are full of these, and
// none of them is a leaked secret. RE2 has no lookaround, so this is a plain
// alternation tested against the matched text.
var placeholderRe = regexp.MustCompile(
	`(?i)<[a-z0-9_.\- ]+>` + // <password>, <host>, <your-api-key>
		`|\$\{[^}]*\}` + // ${GH_TOKEN}
		`|\$\(` + // $(command), $($var) - shell and PowerShell interpolation
		`|\{\{[^}]*\}\}` + // {{ api_key }}
		`|%[a-z_]{2,}%` + // %WINDOWS_VAR%
		`|%[sdv]\b` + // printf-style templates
		`|your[_-][a-z0-9]` + // YOUR_API_KEY / your-secret
		`|replace[_-]?with` + // REPLACE_WITH_URL_SAFE_RANDOM_PASSWORD
		`|(?:dev|ci|staging|local|qa|sandbox)[_-]?only` + // bp_dev_only, bp_ci_only
		`|os\.environ|process\.env|getenv` + // language-level env lookups
		`|x{4,}` + // xxxx
		`|change[_-]?me|replace[_-]?me|placeholder|redacted`,
)

// isPlaceholderMatch reports whether a matched value is an obvious template.
func isPlaceholderMatch(match string) bool {
	return placeholderRe.MatchString(match)
}

const (
	TypeSimple  = "simple"
	TypePattern = "pattern"

	PartExtension = "extension"
	PartFilename  = "filename"
	PartPath      = "path"
	PartContents  = "contents"
)

type Signature interface {
	Name() string
	Match(file MatchFile) (bool, string)
	GetContentsMatches(file MatchFile) []string
	GetPriority() int
	GetColor() string
	GetExcludeInModes() []string
	IsTokenType() bool
	GetRegex() *regexp.Regexp
	GetPart() string
}

type SimpleSignature struct {
	part           string
	match          string
	name           string
	verifier       string
	priority       int
	color          string
	excludeInModes []string
}

type PatternSignature struct {
	part           string
	match          *regexp.Regexp
	notMatch       *regexp.Regexp
	name           string
	verifier       string
	priority       int
	color          string
	excludeInModes []string
}

func (s SimpleSignature) Match(file MatchFile) (bool, string) {
	var (
		haystack  *string
		matchPart = ""
	)

	switch s.part {
	case PartPath:
		haystack = &file.Path
		matchPart = PartPath
	case PartFilename:
		haystack = &file.Filename
		matchPart = PartFilename
	case PartExtension:
		haystack = &file.Extension
		matchPart = PartExtension
	default:
		return false, matchPart
	}

	return (s.match == *haystack), matchPart
}

func (s SimpleSignature) GetContentsMatches(file MatchFile) []string {
	return nil
}

func (s SimpleSignature) Name() string {
	return s.name
}

func (s SimpleSignature) GetPriority() int {
	return s.priority
}

func (s SimpleSignature) GetColor() string {
	return s.color
}

func (s SimpleSignature) GetExcludeInModes() []string {
	return s.excludeInModes
}

func (s SimpleSignature) IsTokenType() bool {
	return false
}

func (s SimpleSignature) GetRegex() *regexp.Regexp {
	// Simple signatures are exact-match strings, not regexes
	return nil
}

func (s SimpleSignature) GetPart() string {
	return s.part
}

func (s PatternSignature) Match(file MatchFile) (bool, string) {
	var (
		haystack  *string
		matchPart = ""
	)

	switch s.part {
	case PartPath:
		haystack = &file.Path
		matchPart = PartPath
	case PartFilename:
		haystack = &file.Filename
		matchPart = PartFilename
	case PartExtension:
		haystack = &file.Extension
		matchPart = PartExtension
	case PartContents:
		// Lazy load contents only when needed
		contents := (&file).GetContents()
		return s.match.Match(contents), PartContents
	default:
		return false, matchPart
	}

	if !s.match.MatchString(*haystack) {
		return false, matchPart
	}
	// A not_regex guard discards path-like candidates that are known
	// false positives (test fixtures, example paths). Contents rules are
	// filtered per matched string in GetContentsMatches instead: rejecting
	// on the whole file would drop real secrets sharing a file with an
	// example.
	if s.notMatch != nil && s.part != PartContents && s.notMatch.MatchString(*haystack) {
		return false, matchPart
	}
	return true, matchPart
}

func (s PatternSignature) GetContentsMatches(file MatchFile) []string {
	matches := make([]string, 0)

	// Lazy load contents only when needed
	contents := (&file).GetContents()

	for _, match := range s.match.FindAllSubmatch(contents, -1) {
		match := string(match[0])
		blacklistedMatch := false

		for _, blacklistedString := range session.Config.BlacklistedStrings {
			if strings.Contains(strings.ToLower(match), strings.ToLower(blacklistedString)) {
				blacklistedMatch = true
			}
		}

		// A not_regex guard discards individual matches that are known
		// false positives (placeholder and example values). It is tested
		// against the matched text, mirroring where a PCRE negative
		// lookahead would have sat in the pattern.
		if !blacklistedMatch && s.notMatch != nil && s.notMatch.MatchString(match) {
			continue
		}

		// A value that is a template - "<password>", "${GH_TOKEN}",
		// "{{ api_key }}", "YOUR_API_KEY" - is documentation, not a leak.
		// This catches the placeholders in .env.example files and CI
		// workflows regardless of which signature matched them.
		if !blacklistedMatch && isPlaceholderMatch(match) {
			continue
		}

		if !blacklistedMatch {
			// Apply verifier if specified
			if s.verifier != "" {
				if !s.applyVerifier(match, file) {
					continue
				}
			}
			matches = append(matches, match)
		}
	}

	return matches
}

func (s PatternSignature) Name() string {
	return s.name
}

func (s PatternSignature) GetPriority() int {
	return s.priority
}

func (s PatternSignature) GetColor() string {
	return s.color
}

func (s PatternSignature) GetExcludeInModes() []string {
	return s.excludeInModes
}

func (s PatternSignature) IsTokenType() bool {
	// Check if this is a GitHub token signature
	sigName := strings.ToLower(s.name)
	return strings.Contains(sigName, "github") && strings.Contains(sigName, "token")
}

func (s PatternSignature) GetRegex() *regexp.Regexp {
	return s.match
}

func (s PatternSignature) GetPart() string {
	return s.part
}

func (s PatternSignature) applyVerifier(match string, file MatchFile) bool {
	var isValid bool
	var tokenType string

	switch s.verifier {
	case "discord":
		isValid = ValidateDiscordToken(match)
		tokenType = "Discord"
	case "telegram":
		isValid = ValidateTelegramToken(match)
		tokenType = "Telegram"
	case "ssh":
		isValid = ValidateSSHKey(match)
		tokenType = "SSH Key"
	default:
		return true // No verifier or unknown verifier, accept the match
	}

	// Send webhook notification if webhook is configured
	if session != nil && session.Config != nil && session.Config.Webhook != "" {
		// Extract repository and filename from current context if available
		repository := "unknown"
		filename := file.Filename

		// Send validation webhook
		go SendValidationWebhook(
			session.Config.Webhook,
			session.Config.WebhookPayload,
			tokenType,
			match,
			repository,
			filename,
			isValid,
		)
	}

	return isValid
}

func GetSignatures(s *Session) []Signature {
	var signatures []Signature
	var unusable []string
	regexCount, matchCount := 0, 0
	for _, signature := range s.Config.Signatures {
		if signature.Match != "" {
			matchCount++
			signatures = append(signatures, SimpleSignature{
				name:           signature.Name,
				part:           signature.Part,
				match:          signature.Match,
				verifier:       signature.Verifier,
				priority:       signature.Priority,
				color:          signature.Color,
				excludeInModes: signature.ExcludeInModes,
			})
		} else {
			// regexp.Compile validates with Perl-compatible flags so patterns
			// using (?i), (?:...), lazy quantifiers, etc. are accepted. The
			// old syntax.Parse(..., syntax.FoldCase) check ran in POSIX mode
			// and silently dropped every such pattern.
			//
			// What it still cannot accept is PCRE-only syntax, most commonly a
			// lookaround ((?=), (?!), (?<=)) or a backreference (\1); Go's engine
			// is RE2 and has neither. Such a signature can never fire, so dropping
			// it without a word leaves the operator believing a detection rule is
			// active when it is not - six rules in the shipped example config were
			// dead this way, including the Ethereum private-key and sk- key rules.
			re, err := regexp.Compile(signature.Regex)
			if err != nil {
				unusable = append(unusable, fmt.Sprintf("%s: %v", signature.Name, err))
				continue
			}
			var notRe *regexp.Regexp
			if signature.NotRegex != "" {
				notRe, err = regexp.Compile(signature.NotRegex)
				if err != nil {
					unusable = append(unusable, fmt.Sprintf("%s (not_regex): %v", signature.Name, err))
					continue
				}
			}
			regexCount++
			signatures = append(signatures, PatternSignature{
				name:           signature.Name,
				part:           signature.Part,
				match:          re,
				notMatch:       notRe,
				verifier:       signature.Verifier,
				priority:       signature.Priority,
				color:          signature.Color,
				excludeInModes: signature.ExcludeInModes,
			})
		}
	}

	if len(unusable) > 0 && s.Log != nil {
		s.Log.Warn("%d configured signature(s) can never fire and were skipped: Go's regexp "+
			"engine is RE2, which supports neither lookaround nor backreferences.",
			len(unusable))
		for _, u := range unusable {
			s.Log.Warn("  - %s", u)
		}
	}
	if s.Log != nil && len(signatures) > 0 {
		s.Log.Info("Loaded %d signatures (%d regex, %d file/path)",
			len(signatures), regexCount, matchCount)
	}

	return signatures
}
