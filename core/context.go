package core

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/trebor048/shhgot/core/httpclient"
)

// RelayKeywords are the host or path fragments of well-known OpenAI relay
// services. ExtractBaseURL ranks a candidate carrying any of these above a
// candidate that carries none, so a leaked key is reported with the endpoint an
// operator actually needs. The list is deliberately small and literal: a
// fragment that does not occur in the wild would silently reorder results.
var RelayKeywords = []string{
	"one-api",
	"new-api",
	"openai-proxy",
	"api2d",
	"closeai",
	"aiproxy",
	"gptapi",
	"openai-sb",
	"fastgpt",
	"relay",
}

var (
	// urlCandidateRE finds http(s) URLs embedded in arbitrary text. Quotes,
	// brackets, whitespace, '#' and backslashes terminate a candidate so a URL
	// in JSON, markdown or a comment is captured without its delimiters, and a
	// trailing fragment is not included.
	urlCandidateRE = regexp.MustCompile(`https?://[^\s"'` + "`" + `<()\[\]{}|\\^#]+`)

	// azureHostRE matches an Azure OpenAI resource endpoint host.
	azureHostRE = regexp.MustCompile(`(?i)[a-z0-9-]+\.openai\.azure\.com`)

	// azureEnvRE matches the documented AZURE_OPENAI_ENDPOINT environment
	// variable wherever it is referenced (a value, os.Getenv, a template).
	azureEnvRE = regexp.MustCompile(`(?i)\bAZURE_OPENAI_ENDPOINT\b`)
)

// ExtractContext returns the lines from line-contextLines to line+contextLines
// inclusive (line is 1-based), capped to maxBytes on a rune boundary. It returns
// the empty string when contextLines is not positive, when line falls outside
// the contents, or when contents is empty. A maxBytes of zero or less means no
// cap.
func ExtractContext(contents []byte, line, contextLines, maxBytes int) string {
	if contextLines <= 0 || line < 1 || len(contents) == 0 {
		return ""
	}

	lines := strings.Split(string(contents), "\n")
	if line > len(lines) {
		return ""
	}

	start := line - contextLines
	if start < 1 {
		start = 1
	}
	end := line + contextLines
	if end > len(lines) {
		end = len(lines)
	}

	window := make([]string, 0, end-start+1)
	for _, l := range lines[start-1 : end] {
		window = append(window, strings.TrimSuffix(l, "\r"))
	}
	return truncateToRuneBoundary(strings.Join(window, "\n"), maxBytes)
}

// truncateToRuneBoundary cuts s to at most maxBytes without leaving a partial
// UTF-8 rune at the end. A maxBytes of zero or less returns s unchanged.
func truncateToRuneBoundary(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	s = s[:maxBytes]
	for len(s) > 0 {
		r, size := utf8.DecodeLastRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			s = s[:len(s)-1]
			continue
		}
		break
	}
	return s
}

// ExtractBaseURL selects the most likely API base URL from file contents. It
// finds every http(s) candidate, normalises it, ranks candidates carrying a
// RelayKeywords fragment first, and returns the first candidate that passes the
// SSRF guard in httpclient.SafeURL. The second result reports whether the
// contents look like an Azure OpenAI endpoint (an openai.azure.com host or a
// reference to AZURE_OPENAI_ENDPOINT).
func ExtractBaseURL(contents []byte) (baseURL string, azure bool) {
	azure = isAzureContents(contents)
	if len(contents) == 0 {
		return "", azure
	}

	type candidate struct {
		url   string
		score int
	}
	candidates := make([]candidate, 0, 4)
	for _, raw := range urlCandidateRE.FindAll(contents, -1) {
		u, ok := normaliseCandidate(string(raw))
		if !ok {
			continue
		}
		candidates = append(candidates, candidate{url: u, score: relayScore(u)})
	}

	// SliceStable keeps document order among equal scores.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})
	for _, c := range candidates {
		if httpclient.SafeURL(c.url) == nil {
			return c.url, azure
		}
	}
	return "", azure
}

// normaliseCandidate strips the punctuation that commonly wraps a URL and a
// trailing /v1 version suffix, then reports whether the result is still an
// absolute http(s) URL.
func normaliseCandidate(raw string) (string, bool) {
	u := strings.TrimSpace(raw)
	u = strings.TrimRight(u, ".,;:!?\"'`>)]}*")
	u = strings.TrimRight(u, "/")
	if i := strings.IndexByte(u, '#'); i >= 0 {
		u = u[:i]
	}
	if strings.HasSuffix(u, "/v1") {
		u = strings.TrimSuffix(u, "/v1")
		u = strings.TrimRight(u, "/")
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return "", false
	}
	if len(u) <= len("https://") {
		return "", false
	}
	return u, true
}

// relayScore counts how many relay keywords occur in u; a higher score is a
// stronger relay-service signal.
func relayScore(u string) int {
	lower := strings.ToLower(u)
	score := 0
	for _, kw := range RelayKeywords {
		if strings.Contains(lower, kw) {
			score++
		}
	}
	return score
}

// isAzureContents reports whether contents reference an Azure OpenAI endpoint.
func isAzureContents(contents []byte) bool {
	return azureHostRE.Match(contents) || azureEnvRE.Match(contents)
}
