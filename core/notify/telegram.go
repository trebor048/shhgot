package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// defaultTelegramBaseURL is the Telegram Bot API root.
const defaultTelegramBaseURL = "https://api.telegram.org"

// Telegram posts a sendMessage call to the Bot API.
type Telegram struct {
	// BotToken is the bot token from @BotFather.
	BotToken string
	// ChatID is the target chat.
	ChatID string
	// BaseURL overrides the API root; defaults to the real Telegram API. Tests
	// point it at a local server.
	BaseURL string
	// MinSev is the channel's built-in minimum severity.
	MinSev Severity
}

// Name implements Channel.
func (t Telegram) Name() string { return "telegram" }

// MinSeverity implements Channel.
func (t Telegram) MinSeverity() Severity { return t.MinSev }

type telegramMessage struct {
	ChatID                string `json:"chat_id"`
	Text                  string `json:"text"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview"`
}

// Build implements Channel.
func (t Telegram) Build(ev Event) (Payload, error) {
	if t.BotToken == "" {
		return Payload{}, errors.New("telegram: bot token is empty")
	}
	if t.ChatID == "" {
		return Payload{}, errors.New("telegram: chat id is empty")
	}

	text := eventSummary(ev)
	if lines := detailLines(ev); len(lines) > 0 {
		text = strings.Join(lines, "\n")
	}

	body, err := json.Marshal(telegramMessage{
		ChatID:                t.ChatID,
		Text:                  truncate(text, 4096),
		DisableWebPagePreview: true,
	})
	if err != nil {
		return Payload{}, fmt.Errorf("telegram: marshal message: %w", err)
	}

	base := t.BaseURL
	if base == "" {
		base = defaultTelegramBaseURL
	}
	if err := checkBaseURL("telegram", base); err != nil {
		return Payload{}, err
	}
	url := strings.TrimRight(base, "/") + "/bot" + t.BotToken + "/sendMessage"

	return Payload{
		Method:  http.MethodPost,
		URL:     url,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
	}, nil
}
