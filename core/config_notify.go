package core

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/trebor048/shhgot/core/notify"
)

// severityFromName maps a config severity name to notify.Severity. Unknown and
// empty values fall back to notify.SevLow, the documented default floor.
func severityFromName(s string) notify.Severity {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical":
		return notify.SevCritical
	case "high":
		return notify.SevHigh
	case "medium":
		return notify.SevMedium
	default:
		return notify.SevLow
	}
}

// NotifySetup builds the alert dispatcher and its delivery policy from the
// "notify:" config section. ok is false when no channel is enabled and usable,
// so the caller can skip notification entirely; the dispatcher is then nil.
//
// The policy carries the quiet-hours window as written; its clock is left nil
// so it uses the real wall clock. The dispatcher applies both the policy and
// each channel's own minimum severity on every delivery.
func (c *Config) NotifySetup() (*notify.Dispatcher, notify.Policy, bool) {
	policy := notify.Policy{}
	if c == nil {
		return nil, policy, false
	}
	policy = notify.Policy{
		QuietHours: boolOr(c.Notify.QuietHours.Enabled, false),
		QuietStart: c.Notify.QuietHours.Start,
		QuietEnd:   c.Notify.QuietHours.End,
	}

	channels := c.NotifyChannels()
	if len(channels) == 0 {
		return nil, policy, false
	}
	d := notify.NewDispatcher(policy)
	for _, ch := range channels {
		// Add may only raise a floor; the channel already carries its configured
		// minimum, so passing SevLow leaves that floor intact.
		d.Add(ch, notify.SevLow)
	}
	return d, policy, true
}

// NotifyChannels builds every enabled channel described by notify.channels, in
// order. A channel with the absent key defaults to enabled; an entry that is
// explicitly disabled or names an unknown type is skipped. A channel whose URL
// does not parse (email, telegram) is skipped too, because a channel that
// cannot be constructed cannot be delivered to or tested.
//
// It is separate from NotifySetup so the startup test delivery in cmd can name
// each channel it attempts.
func (c *Config) NotifyChannels() []notify.Channel {
	if c == nil {
		return nil
	}
	out := make([]notify.Channel, 0, len(c.Notify.Channels))
	for _, cc := range c.Notify.Channels {
		if !boolOr(cc.Enabled, true) {
			continue
		}
		ch, err := buildNotifyChannel(cc)
		if err != nil {
			continue
		}
		out = append(out, ch)
	}
	return out
}

// buildNotifyChannel constructs one channel. Only the type, url, secret and
// min_severity fields are available in config, so the URL carries whatever the
// channel needs beyond a plain endpoint. The per-type URL/secret formats are:
//
//	discord     url: the webhook URL
//	slack       url: the incoming-webhook URL
//	generic     url: any JSON endpoint
//	file        url: the output file path
//	sound       (no url)
//	telegram    url: "<bot-token>|<chat-id>"
//	email       url: smtp://[user[:pass]@]host[:port]/from/to1[,to2]
//	feishu      url: the custom-bot webhook URL (Lark)
//	dingtalk    url: the robot webhook URL; secret: the HMAC-SHA256 signing
//	                 secret (omit for unsigned delivery)
//	bark        url: the device key alone, or "https://<relay>/<device-key>"
//	serverchan  url: the SendKey alone, or "https://sctapi.ftqq.com/<key>.send"
//	pushplus    url: the account token
//	wxpusher    url: "<appToken>|<uid1>,<uid2>" (or appToken alone with the
//	                 recipient UIDs in secret as a comma-separated list)
func buildNotifyChannel(cc ChannelConfig) (notify.Channel, error) {
	min := severityFromName(cc.MinSeverity)
	target := strings.TrimSpace(cc.URL)
	switch strings.ToLower(strings.TrimSpace(cc.Type)) {
	case "discord":
		return notify.Discord{WebhookURL: target, MinSev: min}, nil
	case "slack":
		return notify.Slack{WebhookURL: target, MinSev: min}, nil
	case "generic":
		return notify.Generic{URL: target, MinSev: min}, nil
	case "file":
		return notify.File{Path: target, MinSev: min}, nil
	case "sound":
		return notify.Sound{MinSev: min}, nil
	case "telegram":
		token, chat := splitTelegramTarget(target)
		return notify.Telegram{BotToken: token, ChatID: chat, MinSev: min}, nil
	case "email":
		return parseEmailChannel(target, min)
	case "feishu", "lark":
		return notify.Feishu{WebhookURL: target, MinSev: min}, nil
	case "dingtalk", "dingding":
		return notify.DingTalk{WebhookURL: target, Secret: strings.TrimSpace(cc.Secret), MinSev: min}, nil
	case "bark":
		base, key := parseBarkTarget(target)
		return notify.Bark{BaseURL: base, DeviceKey: key, MinSev: min}, nil
	case "serverchan", "server_chan":
		return notify.ServerChan{SendKey: parseServerChanKey(target), MinSev: min}, nil
	case "pushplus":
		return notify.PushPlus{Token: target, MinSev: min}, nil
	case "wxpusher":
		appToken, uids := parseWxPusherTarget(target, cc.Secret)
		return notify.WxPusher{AppToken: appToken, UIDs: uids, MinSev: min}, nil
	default:
		return nil, fmt.Errorf("notify: unknown channel type %q", cc.Type)
	}
}

// parseBarkTarget splits a Bark target into the relay base URL and the device
// key. A bare device key keeps the public relay (Bark.BaseURL empty -> its
// default); a full URL contributes its scheme://host as the base and its path
// (without slashes) as the key.
func parseBarkTarget(raw string) (base, key string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
		return u.Scheme + "://" + u.Host, strings.Trim(strings.TrimSpace(u.Path), "/")
	}
	return "", raw
}

// parseServerChanKey extracts the SendKey from either a bare key or the
// documented ".../<key>.send" API URL.
func parseServerChanKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
		p := strings.Trim(strings.TrimSpace(u.Path), "/")
		return strings.TrimSuffix(p, ".send")
	}
	return raw
}

// parseWxPusherTarget parses "<appToken>|<uid1>,<uid2>" from the URL. When the
// pipe form is not used the whole URL is the appToken and the comma-separated
// recipient UIDs come from the secret field.
func parseWxPusherTarget(raw, secret string) (appToken string, uids []string) {
	raw = strings.TrimSpace(raw)
	parts := strings.SplitN(raw, "|", 2)
	appToken = strings.TrimSpace(parts[0])
	uidsRaw := strings.TrimSpace(secret)
	if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
		uidsRaw = parts[1]
	}
	for _, u := range strings.Split(uidsRaw, ",") {
		if u = strings.TrimSpace(u); u != "" {
			uids = append(uids, u)
		}
	}
	return appToken, uids
}

// splitTelegramTarget parses a Telegram channel's url as "<bot-token>|<chat-id>".
// A BotFather token already contains a colon ("123456:ABC-DEF"), so the pipe is
// the unambiguous separator. An entry without one is treated as the token alone;
// the channel then reports the missing chat id when it is built for delivery.
func splitTelegramTarget(raw string) (token, chat string) {
	raw = strings.TrimSpace(raw)
	if i := strings.LastIndex(raw, "|"); i >= 0 {
		return strings.TrimSpace(raw[:i]), strings.TrimSpace(raw[i+1:])
	}
	return raw, ""
}

// parseEmailChannel parses an email channel's url as
//
//	smtp://[user[:password]@]host[:port]/from@example.com/to1@example.com[,to2@example.com]
//
// The path's first segment is the From address and the remainder is the
// comma-separated recipient list. A missing port defaults to 25. The grammar is
// deliberately self-contained: config.go's ChannelConfig exposes only a single
// url field for every channel type.
func parseEmailChannel(raw string, min notify.Severity) (notify.Channel, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("notify: email channel needs an smtp:// URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("notify: email URL is not valid: %w", err)
	}
	if u.Scheme != "smtp" || u.Host == "" {
		return nil, errors.New("notify: email URL must be of the form smtp://host[:port]/from/to")
	}
	addr := u.Host
	if u.Port() == "" {
		addr = u.Host + ":25"
	}

	var username, password string
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
	}

	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segments) < 2 || strings.TrimSpace(segments[0]) == "" {
		return nil, errors.New("notify: email URL needs a from address and at least one recipient")
	}
	from := strings.TrimSpace(segments[0])
	var to []string
	for _, r := range strings.Split(strings.Join(segments[1:], "/"), ",") {
		if r = strings.TrimSpace(r); r != "" {
			to = append(to, r)
		}
	}
	if len(to) == 0 {
		return nil, errors.New("notify: email URL needs at least one recipient")
	}

	return notify.Email{
		Addr:     addr,
		From:     from,
		To:       to,
		Username: username,
		Password: password,
		MinSev:   min,
	}, nil
}
