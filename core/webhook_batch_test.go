package core

import (
	"encoding/json"
	"testing"
)

func item(url, payload string) *WebhookQueueItem {
	return &WebhookQueueItem{URL: url, Payload: payload, Hash: payload, MaxRetries: 3}
}

// TestMergeDiscordPayloads covers the batching decision: embed payloads merge up
// to Discord's cap, and anything unexpected declines the merge so the caller
// sends one at a time.
func TestMergeDiscordPayloads(t *testing.T) {
	embed := func(n int) string {
		b, _ := json.Marshal(map[string]any{
			"embeds": []map[string]any{{"title": string(rune('a' + n))}},
		})
		return string(b)
	}

	t.Run("merges two", func(t *testing.T) {
		merged, ok := mergeDiscordPayloads([]*WebhookQueueItem{
			item("https://discord.com/api/webhooks/1/x", embed(0)),
			item("https://discord.com/api/webhooks/1/x", embed(1)),
		})
		if !ok {
			t.Fatal("expected a merge")
		}
		var doc struct {
			Embeds []map[string]any `json:"embeds"`
		}
		if err := json.Unmarshal([]byte(merged), &doc); err != nil {
			t.Fatalf("merged payload is not valid JSON: %v", err)
		}
		if len(doc.Embeds) != 2 {
			t.Fatalf("merged %d embeds, want 2", len(doc.Embeds))
		}
	})

	t.Run("declines a non-embed payload", func(t *testing.T) {
		if _, ok := mergeDiscordPayloads([]*WebhookQueueItem{
			item("https://hooks.slack.com/services/T/B/X", `{"text":"hi"}`),
			item("https://hooks.slack.com/services/T/B/X", `{"text":"ho"}`),
		}); ok {
			t.Fatal("a non-embed payload must not merge")
		}
	})

	t.Run("declines past the embed cap", func(t *testing.T) {
		var batch []*WebhookQueueItem
		for i := 0; i < maxDiscordEmbeds+1; i++ {
			batch = append(batch, item("https://discord.com/api/webhooks/1/x",
				`{"embeds":[{"title":"x"}]}`))
		}
		if _, ok := mergeDiscordPayloads(batch); ok {
			t.Fatalf("merging %d embeds must exceed Discord's cap and be declined", len(batch))
		}
	})

	t.Run("declines a single item", func(t *testing.T) {
		if _, ok := mergeDiscordPayloads([]*WebhookQueueItem{item("u", embed(0))}); ok {
			t.Fatal("a single item is not a merge")
		}
	})
}
