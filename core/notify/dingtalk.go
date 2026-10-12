package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DingTalk posts a markdown message to a DingTalk custom-robot webhook. When a
// secret is configured the URL is signed with HMAC-SHA256 as DingTalk requires.
type DingTalk struct {
	// WebhookURL is the robot webhook endpoint, with its access_token query.
	WebhookURL string
	// Secret, when set, enables signed delivery.
	Secret string
	// Now supplies the signing timestamp; nil means time.Now.
	Now func() time.Time
	// MinSev is the channel's built-in minimum severity.
	MinSev Severity
}

// Name implements Channel.
func (d DingTalk) Name() string { return "dingtalk" }

// MinSeverity implements Channel.
func (d DingTalk) MinSeverity() Severity { return d.MinSev }

type dingTalkMarkdown struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

type dingTalkMessage struct {
	MsgType  string           `json:"msgtype"`
	Markdown dingTalkMarkdown `json:"markdown"`
}

// dingTalkSign returns the base64url HMAC-SHA256 of "<timestamp>\n<secret>"
// keyed by the secret, the signature DingTalk's custom robot expects.
func dingTalkSign(secret string, ts int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("\n"))
	mac.Write([]byte(secret))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Build implements Channel.
func (d DingTalk) Build(ev Event) (Payload, error) {
	if d.WebhookURL == "" {
		return Payload{}, errors.New("dingtalk: webhook URL is empty")
	}

	rawURL := d.WebhookURL
	if d.Secret != "" {
		now := time.Now
		if d.Now != nil {
			now = d.Now
		}
		ts := now().UnixMilli()
		sep := "?"
		if strings.Contains(rawURL, "?") {
			sep = "&"
		}
		rawURL = fmt.Sprintf("%s%stimestamp=%d&sign=%s", rawURL, sep, ts, dingTalkSign(d.Secret, ts))
	}

	body, err := json.Marshal(dingTalkMessage{
		MsgType: "markdown",
		Markdown: dingTalkMarkdown{
			Title: truncate(eventSummary(ev), 256),
			Text:  truncate(strings.Join(detailLines(ev), "\n"), 4096),
		},
	})
	if err != nil {
		return Payload{}, fmt.Errorf("dingtalk: marshal message: %w", err)
	}
	return Payload{
		Method:  http.MethodPost,
		URL:     rawURL,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
	}, nil
}
