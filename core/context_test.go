package core

import (
	"strings"
	"testing"
	"unicode/utf8"
)

const contextTestMaxBytes = 1 << 20

func TestContextWindowBounds(t *testing.T) {
	contents := []byte("l1\nl2\nl3\nl4\nl5\nl6\nl7\nl8\nl9\nl10")
	cases := []struct {
		name    string
		line    int
		context int
		want    string
	}{
		{"middle", 5, 2, "l3\nl4\nl5\nl6\nl7"},
		{"clamped start", 1, 2, "l1\nl2\nl3"},
		{"clamped end", 10, 2, "l8\nl9\nl10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractContext(contents, tc.line, tc.context, contextTestMaxBytes)
			if got != tc.want {
				t.Fatalf("ExtractContext(line=%d, context=%d) = %q, want %q", tc.line, tc.context, got, tc.want)
			}
			if n := strings.Count(got, "\n") + 1; n > 2*tc.context+1 {
				t.Fatalf("window has %d lines, want at most %d", n, 2*tc.context+1)
			}
		})
	}
}

func TestContextDisabled(t *testing.T) {
	contents := []byte("a\nb\nc")
	for _, n := range []int{0, -1, -100} {
		if got := ExtractContext(contents, 2, n, contextTestMaxBytes); got != "" {
			t.Errorf("contextLines=%d: got %q, want empty", n, got)
		}
	}
	if got := ExtractContext(nil, 1, 1, contextTestMaxBytes); got != "" {
		t.Errorf("empty contents: got %q, want empty", got)
	}
	if got := ExtractContext(contents, 99, 1, contextTestMaxBytes); got != "" {
		t.Errorf("line out of range: got %q, want empty", got)
	}
}

func TestContextMultibyteSafe(t *testing.T) {
	line := "世世世世世"
	contents := []byte(line)

	const maxBytes = 4
	got := ExtractContext(contents, 1, 1, maxBytes)
	if !utf8.ValidString(got) {
		t.Fatalf("capped result %q is not valid UTF-8", got)
	}
	if len(got) > maxBytes {
		t.Fatalf("capped result %q is %d bytes, want <= %d", got, len(got), maxBytes)
	}
	if got != "世" {
		t.Fatalf("capped result = %q, want %q", got, "世")
	}

	if full := ExtractContext(contents, 1, 1, contextTestMaxBytes); full != line {
		t.Fatalf("uncapped result = %q, want %q", full, line)
	}
}

func TestContextURLRanking(t *testing.T) {
	contents := []byte(strings.Join([]string{
		"key = sk-test",
		"plain = https://localhost/alpha",
		"relay = https://localhost/relay",
		"best = https://localhost/one-api/relay",
	}, "\n"))

	got, azure := ExtractBaseURL(contents)
	if azure {
		t.Fatalf("azure = true, want false")
	}
	if got != "https://localhost/one-api/relay" {
		t.Fatalf("ExtractBaseURL = %q, want %q", got, "https://localhost/one-api/relay")
	}
}

func TestContextRejectsPrivateURL(t *testing.T) {
	contents := []byte(strings.Join([]string{
		"rfc1918 = http://10.0.0.1/v1",
		"metadata = http://169.254.169.254/latest/meta-data/",
		"plain http public = http://example.com/v1",
	}, "\n"))

	got, azure := ExtractBaseURL(contents)
	if got != "" {
		t.Fatalf("ExtractBaseURL = %q, want empty", got)
	}
	if azure {
		t.Fatalf("azure = true, want false")
	}
}

func TestContextAzureEndpointDetected(t *testing.T) {
	contents := []byte(`AZURE_OPENAI_ENDPOINT = "https://localhost/openai/v1"`)
	got, azure := ExtractBaseURL(contents)
	if !azure {
		t.Fatalf("azure = false, want true")
	}
	if got != "https://localhost/openai" {
		t.Fatalf("ExtractBaseURL = %q, want %q", got, "https://localhost/openai")
	}

	// The host form is detected even though it cannot be resolved offline.
	if !isAzureContents([]byte("endpoint: https://my-resource.openai.azure.com/")) {
		t.Fatalf("openai.azure.com host not detected")
	}
	if isAzureContents([]byte("https://example.com/v1")) {
		t.Fatalf("non-Azure contents classified as Azure")
	}
}

func TestContextTrimsPunctuation(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"trailing comma and version", "endpoint = https://localhost/relay/v1,", "https://localhost/relay"},
		{"surrounding quotes", `"https://localhost/relay/v1"`, "https://localhost/relay"},
		{"markdown paren", "(https://localhost/relay).", "https://localhost/relay"},
		{"trailing slash", "https://localhost/relay/", "https://localhost/relay"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := ExtractBaseURL([]byte(tc.in))
			if got != tc.want {
				t.Fatalf("ExtractBaseURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
