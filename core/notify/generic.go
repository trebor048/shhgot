package notify

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// Generic posts a templated body to an arbitrary HTTP endpoint. With no
// template it sends the shhgit default {"text": "..."} shape; with a template
// the placeholders below are substituted verbatim (the operator owns escaping).
//
// Placeholders: {message} {signature} {severity} {priority} {repo} {file}
// {line} {url} {secret} {provider} {tier} {balance} {identity}.
type Generic struct {
	// URL is the destination endpoint.
	URL string
	// Method defaults to POST.
	Method string
	// Template is the request body with placeholders. Empty uses the default.
	Template string
	// Headers are merged over the default Content-Type.
	Headers map[string]string
	// MinSev is the channel's built-in minimum severity.
	MinSev Severity
}

// Name implements Channel.
func (g Generic) Name() string { return "generic" }

// MinSeverity implements Channel.
func (g Generic) MinSeverity() Severity { return g.MinSev }

// Build implements Channel.
func (g Generic) Build(ev Event) (Payload, error) {
	if g.URL == "" {
		return Payload{}, errors.New("generic: URL is empty")
	}
	if err := checkBaseURL("generic", g.URL); err != nil {
		return Payload{}, err
	}

	headers := map[string]string{"Content-Type": "application/json"}
	for k, v := range g.Headers {
		headers[k] = v
	}

	var body []byte
	if strings.TrimSpace(g.Template) == "" {
		encoded, err := json.Marshal(map[string]string{"text": eventSummary(ev)})
		if err != nil {
			return Payload{}, err
		}
		body = encoded
	} else {
		body = []byte(expandTemplate(g.Template, ev))
	}

	method := g.Method
	if method == "" {
		method = http.MethodPost
	}
	return Payload{
		Method:  strings.ToUpper(method),
		URL:     g.URL,
		Headers: headers,
		Body:    body,
	}, nil
}

// expandTemplate substitutes the documented placeholders. Every value is
// substituted exactly once; a placeholder that is a prefix of another is not an
// issue because the braces delimit each name.
func expandTemplate(tmpl string, ev Event) string {
	replacer := strings.NewReplacer(
		"{message}", eventSummary(ev),
		"{signature}", ev.Signature,
		"{severity}", ev.Severity.String(),
		"{priority}", strconv.Itoa(ev.Priority),
		"{repo}", ev.Repo,
		"{file}", ev.File,
		"{line}", strconv.Itoa(ev.Line),
		"{url}", ev.URL,
		"{secret}", maskSecret(ev.Secret),
		"{provider}", ev.Provider,
		"{tier}", ev.Tier,
		"{balance}", ev.Balance,
		"{identity}", ev.Identity,
	)
	return replacer.Replace(tmpl)
}
