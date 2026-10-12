package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// wxPusherEndpoint is the WxPusher message API root.
const wxPusherEndpoint = "https://wxpusher.zjiecode.com/api/send/message"

// WxPusher pushes a text message to WeChat through WxPusher.
type WxPusher struct {
	// AppToken is the application's appToken.
	AppToken string
	// UIDs are the target subscriber UIDs.
	UIDs []string
	// MinSev is the channel's built-in minimum severity.
	MinSev Severity
}

// Name implements Channel.
func (w WxPusher) Name() string { return "wxpusher" }

// MinSeverity implements Channel.
func (w WxPusher) MinSeverity() Severity { return w.MinSev }

type wxPusherMessage struct {
	AppToken    string   `json:"appToken"`
	Content     string   `json:"content"`
	ContentType int      `json:"contentType"`
	UIDs        []string `json:"uids"`
}

// Build implements Channel.
func (w WxPusher) Build(ev Event) (Payload, error) {
	if w.AppToken == "" {
		return Payload{}, errors.New("wxpusher: app token is empty")
	}
	if len(w.UIDs) == 0 {
		return Payload{}, errors.New("wxpusher: no recipients")
	}

	body, err := json.Marshal(wxPusherMessage{
		AppToken:    w.AppToken,
		Content:     truncate(strings.Join(detailLines(ev), "\n"), 4096),
		ContentType: 1,
		UIDs:        w.UIDs,
	})
	if err != nil {
		return Payload{}, fmt.Errorf("wxpusher: marshal message: %w", err)
	}
	return Payload{
		Method:  http.MethodPost,
		URL:     wxPusherEndpoint,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
	}, nil
}
