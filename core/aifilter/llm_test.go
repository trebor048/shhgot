package aifilter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGenerator records every call and is safe for use under -race.
type fakeGenerator struct {
	mu      sync.Mutex
	calls   int
	prompts []string
	reply   func(prompt string) (string, error)
}

func newFakeGenerator(reply func(prompt string) (string, error)) *fakeGenerator {
	return &fakeGenerator{reply: reply}
}

func (f *fakeGenerator) generate(prompt string) (string, error) {
	f.mu.Lock()
	f.calls++
	f.prompts = append(f.prompts, prompt)
	f.mu.Unlock()
	if f.reply == nil {
		return "", nil
	}
	return f.reply(prompt)
}

func (f *fakeGenerator) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeGenerator) lastPrompt() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.prompts) == 0 {
		return ""
	}
	return f.prompts[len(f.prompts)-1]
}

// llmReply returns a strict-JSON response for a confidence/key_type pair.
func llmReply(confidence, keyType string) func(string) (string, error) {
	return func(string) (string, error) {
		return fmt.Sprintf(
			`{"confidence":%q,"key_type":%q,"provider":"openai","reasoning":"looks like a fixture","is_likely_real":false}`,
			confidence, keyType), nil
	}
}

func TestClassifySuppressionRule(t *testing.T) {
	cases := []struct {
		name     string
		response func(string) (string, error)
		suppress bool
	}{
		{"test high", llmReply("HIGH", "TEST"), true},
		{"example medium", llmReply("MEDIUM", "EXAMPLE"), true},
		{"placeholder high", llmReply("HIGH", "PLACEHOLDER"), true},
		{"fake high", llmReply("HIGH", "FAKE"), true},
		{"lowercase labels", llmReply("high", "placeholder"), true},
		{"real high is kept", llmReply("HIGH", "REAL"), false},
		{"placeholder low is kept", llmReply("LOW", "PLACEHOLDER"), false},
		{"test low is kept", llmReply("LOW", "TEST"), false},
		{"unknown key type is kept", llmReply("HIGH", "UNKNOWN"), false},
		{"missing confidence is kept", func(string) (string, error) {
			return `{"key_type":"PLACEHOLDER"}`, nil
		}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gen := newFakeGenerator(tc.response)
			c := NewClassifier(LLMConfig{Enabled: true}, gen.generate)

			decision := c.Classify("sk-test-your-api-key", "OPENAI_API_KEY=...", "config.py")
			if decision.Suppress != tc.suppress {
				t.Fatalf("Suppress = %v, want %v (decision %+v)", decision.Suppress, tc.suppress, decision)
			}
			if decision.Err != nil {
				t.Fatalf("unexpected Err: %v", decision.Err)
			}
			if gen.callCount() != 1 {
				t.Fatalf("generate calls = %d, want 1", gen.callCount())
			}
		})
	}
}

func TestClassifyPopulatesMetadata(t *testing.T) {
	gen := newFakeGenerator(func(string) (string, error) {
		return `{"confidence":"medium","key_type":"example","provider":"openai","reasoning":"a fixture value","is_likely_real":false}`, nil
	})
	c := NewClassifier(LLMConfig{Enabled: true}, gen.generate)

	decision := c.Classify("sk-example", "", "config.py")
	if !decision.Suppress {
		t.Fatalf("expected suppression, got %+v", decision)
	}
	if decision.Confidence != "MEDIUM" {
		t.Errorf("Confidence = %q, want MEDIUM", decision.Confidence)
	}
	if decision.KeyType != "EXAMPLE" {
		t.Errorf("KeyType = %q, want EXAMPLE", decision.KeyType)
	}
	if decision.Provider != "openai" {
		t.Errorf("Provider = %q, want openai", decision.Provider)
	}
	if decision.Reasoning != "a fixture value" {
		t.Errorf("Reasoning = %q, want %q", decision.Reasoning, "a fixture value")
	}
}

func TestClassifyFailsOpen(t *testing.T) {
	cases := []struct {
		name  string
		reply func(string) (string, error)
	}{
		{"generate error", func(string) (string, error) {
			return "", errors.New("connection refused")
		}},
		{"timeout", func(string) (string, error) {
			return "", fmt.Errorf("chat: %w", context.DeadlineExceeded)
		}},
		{"empty response", func(string) (string, error) {
			return "   \n", nil
		}},
		{"prose with no JSON", func(string) (string, error) {
			return "I cannot help with that.", nil
		}},
		{"truncated JSON", func(string) (string, error) {
			return `{"confidence":"HIGH","key_type":"PLACEHOL`, nil
		}},
		{"malformed JSON object", func(string) (string, error) {
			return `{oops}`, nil
		}},
		{"JSON array is not an object", func(string) (string, error) {
			return `[1,2,3]`, nil
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gen := newFakeGenerator(tc.reply)
			c := NewClassifier(LLMConfig{Enabled: true}, gen.generate)

			decision := c.Classify("sk-test", "", "config.py")
			if decision.Suppress {
				t.Fatalf("expected keep, got suppression: %+v", decision)
			}
			if decision.Err == nil {
				t.Fatalf("expected Err to record the failure, got nil")
			}
		})
	}
}

func TestClassifyDisabledKeepsWithoutCallingGenerate(t *testing.T) {
	gen := newFakeGenerator(llmReply("HIGH", "PLACEHOLDER"))
	c := NewClassifier(LLMConfig{Enabled: false}, gen.generate)

	decision := c.Classify("sk-test", "", "config.py")
	if decision.Suppress {
		t.Fatalf("disabled classifier suppressed a finding: %+v", decision)
	}
	if decision.Err != nil {
		t.Fatalf("disabled classifier returned Err: %v", decision.Err)
	}
	if gen.callCount() != 0 {
		t.Fatalf("generate calls = %d, want 0", gen.callCount())
	}
}

func TestClassifyNilGeneratorKeeps(t *testing.T) {
	c := NewClassifier(LLMConfig{Enabled: true}, nil)

	decision := c.Classify("sk-test", "", "config.py")
	if decision.Suppress || decision.Err == nil {
		t.Fatalf("expected keep with an error, got %+v", decision)
	}
}

func TestClassifyCacheHitAvoidsSecondGenerateCall(t *testing.T) {
	gen := newFakeGenerator(llmReply("HIGH", "PLACEHOLDER"))
	c := NewClassifier(LLMConfig{
		Enabled:      true,
		CacheEntries: 8,
		CacheTTL:     time.Minute,
		Now:          func() time.Time { return time.Unix(1000, 0) },
	}, gen.generate)

	first := c.Classify("sk-test-key", "ctx", "config.py")
	second := c.Classify("sk-test-key", "ctx", "config.py")

	if gen.callCount() != 1 {
		t.Fatalf("generate calls = %d, want 1 (cache hit)", gen.callCount())
	}
	if !first.Suppress || !second.Suppress {
		t.Fatalf("expected both decisions to suppress: first=%+v second=%+v", first, second)
	}
	if first.KeyType != second.KeyType || first.Confidence != second.Confidence {
		t.Fatalf("cached decision changed: first=%+v second=%+v", first, second)
	}
}

func TestClassifyCacheKeyIgnoresContext(t *testing.T) {
	gen := newFakeGenerator(llmReply("HIGH", "PLACEHOLDER"))
	c := NewClassifier(LLMConfig{Enabled: true, CacheEntries: 8, CacheTTL: time.Minute}, gen.generate)

	c.Classify("sk-test-key", "first context", "config.py")
	c.Classify("sk-test-key", "second context", "config.py")

	if gen.callCount() != 1 {
		t.Fatalf("generate calls = %d, want 1 (context is not part of the key)", gen.callCount())
	}
}

func TestClassifyCacheKeyIncludesFilePath(t *testing.T) {
	gen := newFakeGenerator(llmReply("HIGH", "PLACEHOLDER"))
	c := NewClassifier(LLMConfig{Enabled: true, CacheEntries: 8, CacheTTL: time.Minute}, gen.generate)

	c.Classify("sk-test-key", "", "a/config.py")
	c.Classify("sk-test-key", "", "b/config.py")

	if gen.callCount() != 2 {
		t.Fatalf("generate calls = %d, want 2 (different file paths)", gen.callCount())
	}
}

func TestClassifyCacheExpiresAfterTTL(t *testing.T) {
	now := time.Unix(1000, 0)
	gen := newFakeGenerator(llmReply("HIGH", "PLACEHOLDER"))
	c := NewClassifier(LLMConfig{
		Enabled:      true,
		CacheEntries: 8,
		CacheTTL:     time.Minute,
		Now:          func() time.Time { return now },
	}, gen.generate)

	c.Classify("sk-test-key", "", "config.py")
	now = now.Add(30 * time.Second)
	c.Classify("sk-test-key", "", "config.py")
	if gen.callCount() != 1 {
		t.Fatalf("within TTL: generate calls = %d, want 1", gen.callCount())
	}

	now = now.Add(31 * time.Second)
	c.Classify("sk-test-key", "", "config.py")
	if gen.callCount() != 2 {
		t.Fatalf("past TTL: generate calls = %d, want 2", gen.callCount())
	}
}

func TestClassifyCacheIsBoundedLRU(t *testing.T) {
	gen := newFakeGenerator(llmReply("HIGH", "PLACEHOLDER"))
	c := NewClassifier(LLMConfig{
		Enabled:      true,
		CacheEntries: 2,
		CacheTTL:     time.Minute,
		Now:          func() time.Time { return time.Unix(1000, 0) },
	}, gen.generate)

	c.Classify("key-a", "", "file-1")
	c.Classify("key-b", "", "file-2")
	c.Classify("key-c", "", "file-3") // evicts key-a
	if got := len(c.entries); got != 2 {
		t.Fatalf("cache size = %d, want 2", got)
	}

	c.Classify("key-a", "", "file-1") // key-a was evicted, so this calls again
	if gen.callCount() != 4 {
		t.Fatalf("generate calls = %d, want 4", gen.callCount())
	}
	if got := len(c.entries); got != 2 {
		t.Fatalf("cache size after reinsert = %d, want 2", got)
	}
}

func TestClassifyPromptIsStrictJSONSchema(t *testing.T) {
	gen := newFakeGenerator(llmReply("HIGH", "REAL"))
	c := NewClassifier(LLMConfig{Enabled: true}, gen.generate)

	c.Classify("sk-proj-K7mQ2vNp9Rt4Wx6Lz3Bd8Gh5Jc1Fa", "apiKey = ...", "src/config.py")

	prompt := gen.lastPrompt()
	for _, want := range []string{
		`"confidence"`,
		`"key_type"`,
		`"provider"`,
		`"reasoning"`,
		`"is_likely_real"`,
		"Example 1",
		"Example 2",
		"src/config.py",
		"sk-proj-K7mQ2vNp9Rt4Wx6Lz3Bd8Gh5Jc1Fa",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestClassifyClassifiesWithFencedJSON(t *testing.T) {
	gen := newFakeGenerator(func(string) (string, error) {
		return "```json\n{\"confidence\":\"HIGH\",\"key_type\":\"FAKE\",\"reasoning\":\"r\"}\n```", nil
	})
	c := NewClassifier(LLMConfig{Enabled: true}, gen.generate)

	decision := c.Classify("sk-fake", "", "config.py")
	if !decision.Suppress {
		t.Fatalf("expected fenced JSON to parse and suppress, got %+v", decision)
	}
	if decision.Err != nil {
		t.Fatalf("unexpected Err: %v", decision.Err)
	}
}
