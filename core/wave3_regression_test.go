package core

import "testing"

// TestResolveBitbucketNextRejectsArbitraryPort verifies an allowlisted host
// cannot be steered to a non-default port (SSRF to another service on that host).
func TestResolveBitbucketNextRejectsArbitraryPort(t *testing.T) {
	base := "https://api.bitbucket.org"
	if got := resolveBitbucketNext("https://api.bitbucket.org:6379/2.0/repositories", base); got != "" {
		t.Fatalf("non-default port accepted: %q", got)
	}
	if got := resolveBitbucketNext("https://api.bitbucket.org/2.0/repositories?page=2", base); got == "" {
		t.Fatal("default-port canonical link rejected")
	}
}
