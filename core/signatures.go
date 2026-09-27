package core

import (
	"bytes"
	"fmt"
	"regexp"
	"regexp/syntax"
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

// credentialNameRe matches assignment targets that name a credential. A
// "NAME = VALUE" hit is only a finding when NAME looks like the sort of thing a
// secret is stored in. The name-keyed rules are built as
// "<provider>[\w.-]{0,20}\s*[:=]\s*<value>", and a provider's own name is not a
// credential: lambda, firebase, nexus, highlight and friends are ordinary words
// that appear all over source code (`lambda: _build_engine_and_rank`,
// `firebase:firebase-crashlytics`, `NEXUS_BASE_BRANCH=handoff-...`). Requiring
// a credential-ish token on the left-hand side is what separates
// `LAMBDA_API_KEY=<key>` from `lambda: <function name>`.
var credentialNameRe = regexp.MustCompile(
	`(?i)key|token|secret|password|passwd|pass|pwd|credential|auth|api|access|` +
		`client|signing|webhook|private|dsn|url|uri|connection|conn|session|` +
		`refresh|bearer|apikey|license|salt|hash|seed|mnemonic|wallet`,
)

// assignTargetRe recognises the left-hand side of an assignment: an identifier
// that may be dotted, dashed or quoted (a YAML/JSON key). Anything else - a URL
// scheme, a sentence - means the match is not a "NAME = VALUE" pair at all.
var assignTargetRe = regexp.MustCompile(`^["']?[A-Za-z_][A-Za-z0-9_.\-]*["']?$`)

// memberAccessRe matches a value that starts as a member-access chain - an
// identifier followed by a dot and another identifier (lease.accessToken,
// this.policy.bucket_name, tls_private_key.RSA-KEY...). Real credentials are
// opaque strings; code references look like this.
var memberAccessRe = regexp.MustCompile(`^["']?[A-Za-z_][A-Za-z0-9_]*\.[A-Za-z_][A-Za-z0-9_]*`)

// bareWordRe matches a value that is a single "word": letters, digits and
// underscores, optionally quoted, with no separators.
var bareWordRe = regexp.MustCompile(`^["']?[A-Za-z_][A-Za-z0-9_]*["']?$`)

// kebabSlugRe matches a lower-case hyphenated slug (firebase-crashlytics).
var kebabSlugRe = regexp.MustCompile(`^["']?[a-z][a-z0-9]*(?:-[a-z0-9]+)+["']?$`)

// tokenPrefixRe matches the leading characters of credential formats built only
// from letters, digits, underscores and hyphens. A digit-free value of one of
// these shapes is a token rather than an identifier, so the bare-word guard
// must not drop it (sk_live_..., ghp_..., AIza..., AKIA...).
var tokenPrefixRe = regexp.MustCompile(`(?i)^["']?(?:sk[_-]|pk[_-]|rk[_-]|gh[pousr]_|github_pat_|npm_|hf_|gsk_|r8_|dckr_pat_|pat_|secret_|ntn_|pscale_|sbp_|napi_|xau_|bkua_|tfp_|dop_v1_|nfp_|rnd_|aiza|akia|asia|scw|eyj)`)

// passwordNameRe matches assignment targets that hold a human-chosen password.
// Passwords are word-like by nature, so the identifier guard must not apply to
// them - only to machine-generated tokens. The boundaries keep "bypass" and
// "compass" out.
var passwordNameRe = regexp.MustCompile(`(?i)(?:^|[_-])pass(?:word|wd|phrase)?(?:$|[_-])|pwd`)

// looksLikeIdentifierValue reports whether a value is a name rather than a
// machine-generated credential. A credential is a random string: base62, base64
// and hex all carry a healthy share of digits, and a mixed-case encoding carries
// a heavy share of capitals. A camelCase/snake_case identifier - the shape the
// name-keyed rules kept matching - is one or two digits inside a run of words,
// so it fails both densities.
func looksLikeIdentifierValue(value string) bool {
	trimmed := strings.Trim(value, `"'`)
	if trimmed == "" {
		return false
	}
	if !bareWordRe.MatchString(value) && !kebabSlugRe.MatchString(value) {
		return false
	}
	if tokenPrefixRe.MatchString(value) {
		return false
	}
	var upper, digit int
	for _, r := range trimmed {
		switch {
		case r >= 'A' && r <= 'Z':
			upper++
		case r >= '0' && r <= '9':
			digit++
		}
	}
	n := len(trimmed)
	return float64(digit)/float64(n) < 0.10 && float64(upper)/float64(n) < 0.30
}

// splitCredentialAssignment splits a matched text at its first assignment
// operator ("=" or ":") that is not a URL scheme separator, returning the
// left-hand name and right-hand value. ok is false when the match carries no
// such operator or its left-hand side is not a plain name, so a bare connection
// string (`postgres://user:pass@host`) is never read as an assignment.
func splitCredentialAssignment(match string) (name, value string, ok bool) {
	for i := 0; i < len(match); i++ {
		c := match[i]
		if c != '=' && c != ':' {
			continue
		}
		if c == ':' && i+1 < len(match) && match[i+1] == '/' {
			continue // scheme separator: postgres://, https://
		}
		name = strings.TrimSpace(match[:i])
		value = strings.TrimSpace(match[i+1:])
		if name == "" || value == "" || !assignTargetRe.MatchString(name) {
			return "", "", false
		}
		return name, value, true
	}
	return "", "", false
}

// looksLikeCodeReference reports whether a matched text is source code rather
// than a credential. It rejects three shapes that are never secrets:
//
//  1. the assignment target does not name a credential - `lambda:` and
//     `firebase:` and `highlighted =` are code, not configuration;
//  2. the value is a member-access chain with no digit anywhere - a credential
//     is an opaque string, whereas `lease.accessToken` is a variable read;
//  3. the value is an identifier rather than a random string - see
//     looksLikeIdentifierValue.
//
// A JWT (eyJ...eyJ...), a hostname (o123.ingest.sentry.io), a UUID and a
// connection string all carry the density or the separator the guard expects
// and are left alone. Like placeholderRe this is a global guard, applied to
// every contents match regardless of which signature produced it. RE2 has no
// lookaround, so it is a plain function tested against the matched text.
func looksLikeCodeReference(match string) bool {
	name, value, ok := splitCredentialAssignment(match)
	if !ok {
		return false
	}
	// A connection string carries its credential inside the value, so the name
	// may be anything (`db = "postgres://user:pass@host"`).
	if strings.Contains(value, "://") {
		return false
	}
	if !credentialNameRe.MatchString(name) {
		return true
	}
	if !strings.ContainsAny(value, "0123456789") && memberAccessRe.MatchString(value) {
		return true
	}
	// A machine-generated credential is an opaque string, never a word. A
	// human-chosen password is the exception, so password targets are exempt.
	if !passwordNameRe.MatchString(name) && looksLikeIdentifierValue(value) {
		return true
	}
	return false
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

	// prefilter holds literals such that any match must contain at least one of
	// them; prefilterFold marks them case-insensitive. A pattern whose literals
	// are absent cannot match, so the (much slower) regex is skipped. It is nil
	// when no sound prefilter could be derived, in which case the regex always
	// runs. See requiredLiterals.
	prefilter     [][]byte
	prefilterFold bool
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

// requiredLiterals derives a sound prefilter for a pattern: literals such that
// any input the pattern can match must contain at least one of them. The second
// result marks them case-insensitive (the pattern folded their case). A nil
// result means no prefilter could be proven and the regex must always run.
//
// The derivation walks the parsed regex and keeps only mandatory literals: the
// first constrained element of a concatenation, the union of an alternation
// whose branches are all constrained, and the operand of a mandatory
// repetition. Optional, zero-width and unanalysable elements constrain nothing.
// A regex matches anywhere in the input, so "the match must contain X" implies
// "the input must contain X": skipping the regex when none of its literals is
// present can never drop a real match.
//
// This matters because RE2 has no literal fast-path for a pattern with no
// common prefix (an alternation of keywords, a leading (?i)), so every such
// signature rescans the whole file (milliseconds). The prefilter turns that
// into a handful of substring searches (microseconds).
func requiredLiterals(pattern string) ([][]byte, bool) {
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil, false
	}
	lits, fold, ok := collectRequired(parsed.Simplify())
	if !ok {
		return nil, false
	}
	seen := make(map[string]bool, len(lits))
	out := make([][]byte, 0, len(lits))
	for _, l := range lits {
		if l == "" || seen[l] || !literalIsASCII(l) {
			continue
		}
		seen[l] = true
		if fold {
			// bytes.ToLower folds ASCII only, which literalIsASCII guarantees.
			l = strings.ToLower(l)
		}
		out = append(out, []byte(l))
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, fold
}

// collectRequired implements the mandatory-literal walk described on
// requiredLiterals. ok is false when the node imposes no usable constraint.
func collectRequired(re *syntax.Regexp) (lits []string, fold bool, ok bool) {
	switch re.Op {
	case syntax.OpLiteral:
		if len(re.Rune) == 0 {
			return nil, false, false
		}
		return []string{string(re.Rune)}, re.Flags&syntax.FoldCase != 0, true
	case syntax.OpConcat:
		// Every element of a concatenation must match, so any element's
		// guaranteed literal is a valid prefilter. Keep the most selective one
		// (the longest shortest-literal), so a mandatory "admin" is preferred
		// over a mandatory "a".
		var best []string
		bestFold := false
		bestScore := -1
		for _, sub := range re.Sub {
			l, f, ok := collectRequired(sub)
			if !ok {
				continue
			}
			if score := minLiteralLen(l); score > bestScore {
				bestScore, best, bestFold = score, l, f
			}
		}
		if bestScore < 0 {
			return nil, false, false
		}
		return best, bestFold, true
	case syntax.OpAlternate:
		var out []string
		foldAny := false
		for _, sub := range re.Sub {
			l, f, ok := collectRequired(sub)
			if !ok {
				// One branch could match without a known literal.
				return nil, false, false
			}
			out = append(out, l...)
			foldAny = foldAny || f
		}
		return out, foldAny, len(out) > 0
	case syntax.OpCapture:
		if len(re.Sub) == 1 {
			return collectRequired(re.Sub[0])
		}
		return nil, false, false
	case syntax.OpPlus:
		if len(re.Sub) == 1 {
			return collectRequired(re.Sub[0])
		}
		return nil, false, false
	case syntax.OpRepeat:
		if len(re.Sub) == 1 && re.Min >= 1 {
			return collectRequired(re.Sub[0])
		}
		return nil, false, false
	default:
		return nil, false, false
	}
}

func literalIsASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// minLiteralLen is the selectivity of an any-of literal set: the shortest
// literal is the one most likely to appear by chance.
func minLiteralLen(lits []string) int {
	m := -1
	for _, l := range lits {
		if n := len(l); m < 0 || n < m {
			m = n
		}
	}
	return m
}

// prefilterMiss reports whether the prefilter proves the pattern cannot match
// contents, so the regex can be skipped.
func (s PatternSignature) prefilterMiss(file MatchFile, contents []byte) bool {
	if len(s.prefilter) == 0 {
		return false
	}
	hay := contents
	if s.prefilterFold {
		hay = file.LowerContents()
	}
	for _, l := range s.prefilter {
		if bytes.Contains(hay, l) {
			return false
		}
	}
	return true
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
		if s.prefilterMiss(file, contents) {
			return false, PartContents
		}
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
	if s.prefilterMiss(file, contents) {
		return nil
	}

	for _, match := range s.match.FindAllSubmatch(contents, -1) {
		match := string(match[0])
		blacklistedMatch := false

		for _, blacklistedString := range session.Config.BlacklistedStrings {
			// An empty entry would make strings.Contains true for every match
			// and silently disable the signature, so it is skipped rather than
			// honoured.
			if blacklistedString == "" {
				continue
			}
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

		// A value assigned to a name that does not look like a credential, or a
		// value that is a digit-free member-access chain, is source code rather
		// than a leak (accessToken = lease.accessToken, lambda: _build_engine_and_rank).
		if !blacklistedMatch && looksLikeCodeReference(match) {
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

	if aiTokenVerifiers[s.verifier] {
		// Route through TokenValidator so the provider's API is actually called.
		// Before this, verifier: openai / verifier: claude fell into the default
		// branch below and every match was accepted unverified.
		if session != nil && session.TokenValidator != nil {
			isValid = session.TokenValidator.TestAndLogToken(match, s.color)
			tokenType = session.TokenValidator.DetectProvider(match)
		} else {
			// No validator available (e.g. a unit test): fail open rather than
			// silently dropping the finding.
			isValid = true
			tokenType = "AI Token (unverified)"
		}
	} else {
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
		case "crypto_balance":
			isValid, tokenType = ValidateCryptoKey(match)
		default:
			// config.yaml validation rejects unknown verifiers at load time, so
			// this is only reached if a signature was built programmatically.
			isValid = true
			tokenType = "Unverified"
		}
	}

	// Send webhook notification if webhook is configured
	if session != nil && session.Config != nil && session.Config.Webhook != "" {
		// The scan loop attaches the repository to the file; fall back to
		// "unknown" only when no repo context was available.
		repository := file.Repo
		if repository == "" {
			repository = "unknown"
		}
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

// aiTokenVerifiers maps the verifier names accepted in config.yaml to the
// TokenValidator provider path. A signature carrying one of these is verified
// against the provider's live API rather than merely matching its shape.
var aiTokenVerifiers = map[string]bool{
	"openai": true, "openai_project": true,
	"anthropic": true, "claude": true,
	"gemini": true, "google": true,
	"xai": true, "grok": true,
	"openrouter": true, "huggingface": true, "replicate": true,
	"elevenlabs": true, "stability": true, "assemblyai": true, "deepgram": true,
	"deepseek": true, "groq": true, "mistral": true, "cohere": true,
	"perplexity": true, "together": true,
}

// knownVerifiers is every verifier name the scanner can actually execute. A
// config that names anything else is a typo that would otherwise silently accept
// every match; GetSignatures warns about each one.
func knownVerifier(name string) bool {
	if aiTokenVerifiers[name] {
		return true
	}
	switch name {
	case "discord", "telegram", "ssh", "crypto_balance":
		return true
	}
	return false
}

func GetSignatures(s *Session) []Signature {
	var signatures []Signature
	var unusable []string
	var incomplete []string
	var badVerifiers []string
	regexCount, matchCount := 0, 0
	for _, signature := range s.Config.Signatures {
		if signature.Match == "" && signature.Regex == "" {
			// An entry with neither a match string nor a regex is a typo, and a
			// costly one: it used to fall through to regexp.Compile(""), which
			// succeeds and matches every string. As a contents rule that emitted
			// an empty match for every position in every file, and as a
			// path/filename rule it matched every path - so one bad config line
			// buried the report in false positives.
			incomplete = append(incomplete, signature.Name)
			continue
		}
		if signature.Match != "" {
			matchCount++
			// A verifier only runs on content matches; on a path/filename rule
			// it can never execute, so flag it rather than let the operator
			// believe the rule is validated.
			if signature.Verifier != "" {
				badVerifiers = append(badVerifiers, fmt.Sprintf(
					"%s: verifier %q is ignored on a 'match' (filename/path) rule", signature.Name, signature.Verifier))
			}
			signatures = append(signatures, SimpleSignature{
				name:           signature.Name,
				part:           signature.Part,
				match:          signature.Match,
				priority:       signature.Priority,
				color:          signature.Color,
				excludeInModes: signature.ExcludeInModes,
			})
		} else {
			if signature.Verifier != "" && !knownVerifier(signature.Verifier) {
				badVerifiers = append(badVerifiers, fmt.Sprintf(
					"%s: unknown verifier %q (match would be accepted unverified)", signature.Name, signature.Verifier))
			}
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
			prefilter, prefilterFold := requiredLiterals(signature.Regex)
			signatures = append(signatures, PatternSignature{
				name:           signature.Name,
				part:           signature.Part,
				match:          re,
				notMatch:       notRe,
				verifier:       signature.Verifier,
				priority:       signature.Priority,
				color:          signature.Color,
				excludeInModes: signature.ExcludeInModes,
				prefilter:      prefilter,
				prefilterFold:  prefilterFold,
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
	if len(incomplete) > 0 && s.Log != nil {
		s.Log.Warn("%d configured signature(s) set neither 'match' nor 'regex' and were skipped "+
			"(an empty regex matches every file, which would flag everything):", len(incomplete))
		for _, name := range incomplete {
			s.Log.Warn("  - %s", name)
		}
	}
	if len(badVerifiers) > 0 && s.Log != nil {
		s.Log.Warn("%d configured signature(s) have a verifier that will not run; the rule still matches, but unverified:", len(badVerifiers))
		for _, b := range badVerifiers {
			s.Log.Warn("  - %s", b)
		}
	}
	if s.Log != nil && len(signatures) > 0 {
		s.Log.Info("Loaded %d signatures (%d regex, %d file/path)",
			len(signatures), regexCount, matchCount)
	}

	return signatures
}
