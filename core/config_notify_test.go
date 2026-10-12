package core

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trebor048/shhgot/core/notify"
)

func notifyBoolPtr(b bool) *bool { return &b }

// With no channels configured NotifySetup reports ok=false and no dispatcher,
// so the caller can skip notification entirely.
func TestNotifySetupNoChannels(t *testing.T) {
	d, _, ok := (&Config{}).NotifySetup()
	if ok || d != nil {
		t.Fatalf("NotifySetup with no channels = (%v, %v), want (nil, false)", d, ok)
	}
}

// A configured Slack channel is wired to its webhook: an injected transport
// reaches an httptest sink carrying the finding and its severity.
func TestNotifySetupSlackDelivery(t *testing.T) {
	var (
		mu   sync.Mutex
		hits int
		got  []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		hits++
		got = body
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	cfg := &Config{Notify: NotifyConfig{Channels: []ChannelConfig{{
		Type: "slack",
		URL:  srv.URL,
	}}}}
	d, _, ok := cfg.NotifySetup()
	if !ok || d == nil {
		t.Fatalf("NotifySetup = (%v, %v), want a dispatcher", d, ok)
	}
	d.SetDoer(notify.HTTPDoer{Client: srv.Client()})

	results := d.Deliver(notify.Event{
		Signature: "OpenAI API Key",
		Severity:  notify.SevCritical,
		Repo:      "acme/app",
		File:      "config.py",
		Line:      12,
		Secret:    "sk-abcdef0123456789",
	})
	if len(results) != 1 || results[0].Err != nil || results[0].Skipped {
		t.Fatalf("delivery result = %+v, want one successful channel", results)
	}

	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Fatalf("slack sink saw %d requests, want 1", hits)
	}
	body := string(got)
	if !strings.Contains(body, "OpenAI API Key") {
		t.Fatalf("body missing the finding signature: %s", body)
	}
	if !strings.Contains(body, "critical") {
		t.Fatalf("body missing the severity: %s", body)
	}
	if strings.Contains(body, "sk-abcdef0123456789") {
		t.Fatalf("body leaked the raw secret: %s", body)
	}
	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("slack body is not valid JSON: %v", err)
	}
}

// Quiet hours from notify.quiet_hours must reach the policy: a medium finding
// is suppressed inside the window while a critical one bypasses it.
func TestNotifySetupQuietHours(t *testing.T) {
	cfg := &Config{Notify: NotifyConfig{
		QuietHours: QuietHoursConfig{Enabled: notifyBoolPtr(true), Start: "22:00", End: "07:00"},
		Channels:   []ChannelConfig{{Type: "slack", URL: "https://hooks.slack.example/T/B"}},
	}}
	_, policy, ok := cfg.NotifySetup()
	if !ok {
		t.Fatal("NotifySetup reported no channels")
	}
	if !policy.QuietHours || policy.QuietStart != "22:00" || policy.QuietEnd != "07:00" {
		t.Fatalf("policy = %+v, want quiet hours wired from config", policy)
	}
	policy.Now = func() time.Time { return time.Date(2026, 9, 29, 23, 30, 0, 0, time.UTC) }

	ch := cfg.NotifyChannels()[0]
	if policy.Allow(ch, notify.Event{Severity: notify.SevMedium}) {
		t.Fatal("quiet hours must suppress a medium finding")
	}
	if !policy.Allow(ch, notify.Event{Severity: notify.SevCritical}) {
		t.Fatal("quiet hours must not suppress a critical finding")
	}
}

// Per-channel min_severity is carried on the channel so the dispatcher skips a
// finding below it.
func TestNotifySetupChannelMinSeverity(t *testing.T) {
	cfg := &Config{Notify: NotifyConfig{Channels: []ChannelConfig{{
		Type: "slack", URL: "https://hooks.slack.example/T/B", MinSeverity: "high",
	}}}}
	_, policy, ok := cfg.NotifySetup()
	if !ok {
		t.Fatal("NotifySetup reported no channels")
	}
	ch := cfg.NotifyChannels()[0]
	if ch.MinSeverity() != notify.SevHigh {
		t.Fatalf("channel floor = %v, want high", ch.MinSeverity())
	}
	if policy.Allow(ch, notify.Event{Severity: notify.SevMedium}) {
		t.Fatal("a high-floor channel must skip a medium finding")
	}
	if !policy.Allow(ch, notify.Event{Severity: notify.SevHigh}) {
		t.Fatal("a high-floor channel must accept a high finding")
	}
}

// An absent min_severity defaults to low.
func TestNotifySetupDefaultMinSeverityIsLow(t *testing.T) {
	cfg := &Config{Notify: NotifyConfig{Channels: []ChannelConfig{{
		Type: "discord", URL: "https://discord.example/api/webhooks/1/x",
	}}}}
	if _, _, ok := cfg.NotifySetup(); !ok {
		t.Fatal("NotifySetup reported no channels")
	}
	if got := cfg.NotifyChannels()[0].MinSeverity(); got != notify.SevLow {
		t.Fatalf("default floor = %v, want low", got)
	}
}

// Disabled and unknown-type entries are skipped; a listed sound channel remains.
func TestNotifyChannelsSkipsDisabledAndUnknown(t *testing.T) {
	cfg := &Config{Notify: NotifyConfig{Channels: []ChannelConfig{
		{Type: "slack", URL: "https://hooks.slack.example/T/B", Enabled: notifyBoolPtr(false)},
		{Type: "no-such-channel", URL: "https://example.invalid/"},
		{Type: "sound"},
	}}}
	channels := cfg.NotifyChannels()
	if len(channels) != 1 || channels[0].Name() != "sound" {
		t.Fatalf("channels = %+v, want exactly the sound channel", channels)
	}
}

// An email channel's smtp:// url carries the server, credentials, sender and
// recipients; a malformed value is skipped rather than registered broken.
func TestNotifyChannelsEmailParsing(t *testing.T) {
	cfg := &Config{Notify: NotifyConfig{Channels: []ChannelConfig{{
		Type: "email",
		URL:  "smtp://user:pass@smtp.example:587/shhgit@example.com/ops@example.com,sec@example.com",
	}}}}
	channels := cfg.NotifyChannels()
	if len(channels) != 1 {
		t.Fatalf("got %d channels, want 1", len(channels))
	}
	email, ok := channels[0].(notify.Email)
	if !ok {
		t.Fatalf("channel type = %T, want notify.Email", channels[0])
	}
	if email.Addr != "smtp.example:587" || email.From != "shhgit@example.com" {
		t.Fatalf("email = %+v, want the parsed server and sender", email)
	}
	if len(email.To) != 2 || email.To[0] != "ops@example.com" || email.To[1] != "sec@example.com" {
		t.Fatalf("recipients = %v, want two", email.To)
	}
	if email.Username != "user" || email.Password != "pass" {
		t.Fatalf("credentials = %q/%q, want user/pass", email.Username, email.Password)
	}

	bad := &Config{Notify: NotifyConfig{Channels: []ChannelConfig{{Type: "email", URL: "not a url"}}}}
	if got := bad.NotifyChannels(); len(got) != 0 {
		t.Fatalf("malformed email url produced %d channels, want 0", len(got))
	}
}

// Every new webhook/key channel type builds from notify.channels with the
// documented URL/secret format, and each channel's Build succeeds.
func TestNotifyChannelsBuildNewTypes(t *testing.T) {
	cfg := &Config{Notify: NotifyConfig{Channels: []ChannelConfig{
		{Type: "feishu", URL: "https://open.feishu.cn/open-apis/bot/v2/hook/abc"},
		{Type: "dingtalk", URL: "https://oapi.dingtalk.com/robot/send?access_token=abc", Secret: "SECx"},
		{Type: "bark", URL: "https://api.day.app/devicekey123"},
		{Type: "serverchan", URL: "SCTkey123"},
		{Type: "pushplus", URL: "pp-token"},
		{Type: "wxpusher", URL: "AT_abc|UID_a,UID_b"},
	}}}
	channels := cfg.NotifyChannels()
	if len(channels) != 6 {
		t.Fatalf("built %d channels, want 6", len(channels))
	}

	feishu, ok := channels[0].(notify.Feishu)
	if !ok || feishu.WebhookURL != "https://open.feishu.cn/open-apis/bot/v2/hook/abc" {
		t.Fatalf("feishu = %#v", channels[0])
	}
	dingtalk, ok := channels[1].(notify.DingTalk)
	if !ok || dingtalk.Secret != "SECx" || dingtalk.WebhookURL == "" {
		t.Fatalf("dingtalk = %#v", channels[1])
	}
	bark, ok := channels[2].(notify.Bark)
	if !ok || bark.BaseURL != "https://api.day.app" || bark.DeviceKey != "devicekey123" {
		t.Fatalf("bark = %#v", channels[2])
	}
	sc, ok := channels[3].(notify.ServerChan)
	if !ok || sc.SendKey != "SCTkey123" {
		t.Fatalf("serverchan = %#v", channels[3])
	}
	pp, ok := channels[4].(notify.PushPlus)
	if !ok || pp.Token != "pp-token" {
		t.Fatalf("pushplus = %#v", channels[4])
	}
	wx, ok := channels[5].(notify.WxPusher)
	if !ok || wx.AppToken != "AT_abc" || len(wx.UIDs) != 2 || wx.UIDs[1] != "UID_b" {
		t.Fatalf("wxpusher = %#v", channels[5])
	}

	ev := notify.Event{Signature: "OpenAI API Key", Severity: notify.SevHigh}
	for _, ch := range channels {
		if _, err := ch.Build(ev); err != nil {
			t.Fatalf("channel %s Build: %v", ch.Name(), err)
		}
	}
}

// The bare-key and full-URL alternatives parse too: a Bark device key alone
// keeps the public relay, a ServerChan ".../<key>.send" URL yields the key, and
// WxPusher accepts the recipients in the secret field.
func TestNotifyChannelTargetParsers(t *testing.T) {
	if base, key := parseBarkTarget("devicekey123"); base != "" || key != "devicekey123" {
		t.Fatalf("bare bark target = (%q, %q)", base, key)
	}
	if got := parseServerChanKey("https://sctapi.ftqq.com/SCTxyz.send"); got != "SCTxyz" {
		t.Fatalf("serverchan key = %q, want SCTxyz", got)
	}

	cfg := &Config{Notify: NotifyConfig{Channels: []ChannelConfig{
		{Type: "wxpusher", URL: "AT_only", Secret: "UID_1, UID_2"},
	}}}
	wx, ok := cfg.NotifyChannels()[0].(notify.WxPusher)
	if !ok || wx.AppToken != "AT_only" || len(wx.UIDs) != 2 || wx.UIDs[0] != "UID_1" || wx.UIDs[1] != "UID_2" {
		t.Fatalf("wxpusher from secret = %#v", wx)
	}
}
