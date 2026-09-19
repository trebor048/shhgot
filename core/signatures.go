package core

import (
	"regexp"
	"strings"
)

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
		matchPart = PartPath
	case PartExtension:
		haystack = &file.Extension
		matchPart = PartPath
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

	return s.match.MatchString(*haystack), matchPart
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
	for _, signature := range s.Config.Signatures {
		if signature.Match != "" {
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
			if re, err := regexp.Compile(signature.Regex); err == nil {
				signatures = append(signatures, PatternSignature{
					name:           signature.Name,
					part:           signature.Part,
					match:          re,
					verifier:       signature.Verifier,
					priority:       signature.Priority,
					color:          signature.Color,
					excludeInModes: signature.ExcludeInModes,
				})
			}
		}
	}

	return signatures
}
