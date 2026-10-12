package httpclient

import (
	"errors"
	"net"
	"testing"
)

// withLookup installs a deterministic resolver for the duration of a test and
// clears the resolution cache so a result from another test cannot leak in.
func withLookup(t *testing.T, fn func(string) ([]net.IP, error)) {
	t.Helper()
	original := lookupIP
	lookupIP = fn
	clearCache := func() {
		safeURLCache.mu.Lock()
		safeURLCache.entries = make(map[string]resolveEntry)
		safeURLCache.mu.Unlock()
	}
	clearCache()
	t.Cleanup(func() {
		lookupIP = original
		clearCache()
	})
}

func TestSafeURL(t *testing.T) {
	withLookup(t, func(host string) ([]net.IP, error) {
		switch host {
		case "example.com", "api.openai.com", "github.com":
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		case "internal.example.com":
			return []net.IP{net.ParseIP("10.0.0.5")}, nil
		case "mixed.example.com":
			return []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("10.0.0.5")}, nil
		default:
			return nil, errors.New("no such host")
		}
	})

	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"https public hostname", "https://example.com/v1/keys", false},
		{"https public api", "https://api.openai.com/v1/models", false},
		{"https public github", "https://github.com/org/repo", false},
		{"http localhost", "http://localhost:8080/health", false},
		{"https localhost", "https://localhost/health", false},
		{"http loopback ipv4", "http://127.0.0.1:3000", false},
		{"http loopback ipv6", "http://[::1]:3000", false},
		{"https public ipv4", "https://8.8.8.8/resolve", false},
		{"https public ipv6", "https://[2001:4860:4860::8888]", false},
		{"six-to-four with public embedded ipv4", "https://[2002:5db8:d822::]/", false},
		{"six-to-four embedding loopback", "https://[2002:7f00:1::]/", true},
		{"six-to-four embedding private", "https://[2002:0a00:1::]/", true},
		{"six-to-four embedding cgnat", "https://[2002:6440:1::]/", true},
		{"teredo", "https://[2001:0:4136:e378:8000:63bf:3fff:fdd2]/", true},
		{"nat64 embedding link-local", "https://[64:ff9b::a9fe:a9fe]/", true},
		{"nat64 embedding loopback", "https://[64:ff9b::7f00:1]/", true},
		{"nat64 local-use embedding link-local", "https://[64:ff9b:1::a9fe:a9fe]/", true},
		{"nat64 embedding public ipv4", "https://[64:ff9b::5db8:d822]/", false},
		{"ipv4-compatible loopback", "https://[::127.0.0.1]/", true},
		{"ipv4-compatible private", "https://[::a00:1]/", true},
		{"zero-slash-eight", "https://0.0.0.1/", true},

		{"plain http public host", "http://example.com/", true},
		{"plain http public ip", "http://8.8.8.8/", true},
		{"https loopback literal", "https://127.0.0.1/", true},
		{"ftp scheme", "ftp://example.com/file", true},
		{"file scheme", "file:///etc/passwd", true},
		{"gopher scheme", "gopher://example.com/", true},
		{"embedded credentials", "https://user:pass@example.com/", true},
		{"empty host", "https://", true},
		{"private 10/8", "https://10.0.0.1/", true},
		{"private 192.168/16", "https://192.168.1.1/", true},
		{"private 172.16/12", "https://172.16.5.4/", true},
		{"ipv6 ula", "https://[fc00::1]/", true},
		{"cloud metadata link-local", "https://169.254.169.254/latest/meta-data/", true},
		{"cgNAT low", "https://100.64.0.1/", true},
		{"cgNAT high", "https://100.127.255.254/", true},
		{"192.0.0.0/24", "https://192.0.0.10/", true},
		{"198.18.0.0/15", "https://198.18.0.1/", true},
		{"198.19.0.0/15", "https://198.19.255.1/", true},
		{"reserved 240/4", "https://240.0.0.1/", true},
		{"multicast", "https://224.0.0.1/", true},
		{"unspecified", "https://0.0.0.0/", true},
		{"dot local", "https://printer.local/", true},
		{"dot internal", "https://db.internal/", true},
		{"dot corp", "https://intranet.corp/", true},
		{"dot lan", "https://nas.lan/", true},
		{"dot home", "https://router.home/", true},
		{"resolves private", "https://internal.example.com/", true},
		{"resolves mixed public and private", "https://mixed.example.com/", true},
		{"unresolvable", "https://does-not-exist.example/", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := SafeURL(tt.url)
			if tt.wantErr && err == nil {
				t.Fatalf("SafeURL(%q) = nil, want error", tt.url)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("SafeURL(%q) = %v, want nil", tt.url, err)
			}
		})
	}
}

// TestSafeURLCachesResolutions asserts that repeated checks for the same host
// resolve only once: ExtractBaseURL ranks many candidates that often share one
// host, and each uncached net.LookupIP is a blocking call on the publish path.
func TestSafeURLCachesResolutions(t *testing.T) {
	var calls int
	withLookup(t, func(host string) ([]net.IP, error) {
		calls++
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	})

	for i := 0; i < 5; i++ {
		if err := SafeURL("https://cached.example/v1"); err != nil {
			t.Fatalf("SafeURL: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("resolver calls = %d, want 1 (a cached host resolves once)", calls)
	}
}
