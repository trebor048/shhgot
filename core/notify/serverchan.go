package notify

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// serverChanEndpoint is the ServerChan Turbo API root.
const serverChanEndpoint = "https://sctapi.ftqq.com"

// ServerChan pushes a message through ServerChan (Server酱).
type ServerChan struct {
	// SendKey is the account's SCT send key.
	SendKey string
	// MinSev is the channel's built-in minimum severity.
	MinSev Severity
}

// Name implements Channel.
func (s ServerChan) Name() string { return "serverchan" }

// MinSeverity implements Channel.
func (s ServerChan) MinSeverity() Severity { return s.MinSev }

// Build implements Channel.
func (s ServerChan) Build(ev Event) (Payload, error) {
	if s.SendKey == "" {
		return Payload{}, errors.New("serverchan: send key is empty")
	}

	form := url.Values{}
	form.Set("title", truncate(eventSummary(ev), 256))
	form.Set("desp", truncate(strings.Join(detailLines(ev), "\n"), 4096))

	return Payload{
		Method:  http.MethodPost,
		URL:     serverChanEndpoint + "/" + url.PathEscape(s.SendKey) + ".send",
		Headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
		Body:    []byte(form.Encode()),
	}, nil
}
