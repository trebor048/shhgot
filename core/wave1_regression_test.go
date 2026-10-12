package core

import (
	"errors"
	"runtime"
	"testing"
	"testing/iotest"
)

// TestNonRoutableHostAnchoring verifies the non-routable-host guard does not
// suppress findings on public hosts that merely contain a private-IP or
// documentation-domain substring.
func TestNonRoutableHostAnchoring(t *testing.T) {
	nonRoutable := []string{
		`DATABASE_URL=postgresql://app:pw@localhost:5432/app`,
		`DATABASE_URL=postgresql://app:pw@127.0.0.1:5432/app`,
		`DATABASE_URL=postgresql://app:pw@10.0.0.5:5432/app`,
		`DATABASE_URL=postgresql://app:pw@172.16.0.5:5432/app`,
		`DATABASE_URL=postgresql://app:pw@192.168.1.5:5432/app`,
		`DATABASE_URL=postgresql://app:pw@example.com:5432/app`,
		`DATABASE_URL=mysql://root:pw@[::1]:3306/app`,
		`host = db.local`,
	}
	for _, in := range nonRoutable {
		if !isNonRoutableMatch(in) {
			t.Errorf("isNonRoutableMatch(%q) = false, want true", in)
		}
	}

	routable := []string{
		// 110.x and 100.x are public; the guard must not read the embedded
		// private-IP / all-zero substrings.
		`DATABASE_URL=postgresql://app:pw@110.0.0.1:5432/app`,
		`DATABASE_URL=postgresql://app:pw@100.0.0.0:5432/app`,
		// myexample.com is a different (registrable) domain, not example.com.
		`DATABASE_URL=postgresql://app:pw@myexample.com:5432/app`,
		// A host label that starts with an IP is still routable.
		`DATABASE_URL=postgresql://app:pw@10.0.0.5.evil.com:5432/app`,
		`DATABASE_URL=postgresql://app_rw:pw@db.prod.internal:5432/app`,
	}
	for _, in := range routable {
		if isNonRoutableMatch(in) {
			t.Errorf("isNonRoutableMatch(%q) = true, want false (false-negative suppression)", in)
		}
	}
}

// TestPlaceholderWordBoundary verifies the bare placeholder words only match as
// whole tokens, so a real secret containing e.g. "sample" or "example" as a
// substring is not discarded.
func TestPlaceholderWordBoundary(t *testing.T) {
	placeholders := []string{
		`API_KEY=example`,
		`token = dummy`,
		`secret: sample`,
		`value = todo`,
		`password=fake`,
	}
	for _, in := range placeholders {
		if !isPlaceholderMatch(in) {
			t.Errorf("isPlaceholderMatch(%q) = false, want true", in)
		}
	}

	real := []string{
		`API_KEY=myexamplekey`,
		`token=sampledprod`,
		`secret=xexamplex`,
		`key=dummysite`,
		`pass=fauxvalue`,
	}
	for _, in := range real {
		if isPlaceholderMatch(in) {
			t.Errorf("isPlaceholderMatch(%q) = true, want false (false-negative suppression)", in)
		}
	}
}

// TestMatchPathFragmentBoundary verifies a fragment blacklist rule matches a
// whole component run, not an arbitrary substring of a path.
func TestMatchPathFragmentBoundary(t *testing.T) {
	if !matchPathFragment("a/vendor/autoload.php", "vendor/autoload.php") {
		t.Error("expected a/vendor/autoload.php to match vendor/autoload.php")
	}
	if !matchPathFragment("vendor/autoload.php", "vendor/autoload.php") {
		t.Error("expected a leading fragment to match")
	}
	if matchPathFragment("a/myvendor/autoload.php", "vendor/autoload.php") {
		t.Error("myvendor/autoload.php must not match vendor/autoload.php")
	}
	if matchPathFragment("a/vendor/autoload.phpx", "vendor/autoload.php") {
		t.Error("a longer trailing component must not match")
	}
	// A trailing-slash rule is classified with the slash trimmed, so it matches
	// a nested path (vendor/bundle/ruby/...) rather than never matching.
	rule, ok := classifyBlacklistPath("vendor/bundle/")
	if !ok || rule.kind != pathRuleFragment || rule.pattern != "vendor/bundle" {
		t.Fatalf("classifyBlacklistPath(vendor/bundle/) = %+v, want fragment pattern vendor/bundle", rule)
	}
	if !matchPathFragment("app/vendor/bundle/ruby/gems/x.rb", rule.pattern) {
		t.Error("vendor/bundle fragment did not match a nested path")
	}
	if matchPathFragment("app/myvendor/bundle/x.rb", rule.pattern) {
		t.Error("myvendor/bundle must not match vendor/bundle")
	}
}

// TestResolveBitbucketNextRejectsForeignHost verifies the pagination link is
// constrained to Bitbucket hosts, closing the SSRF path.
func TestResolveBitbucketNextRejectsForeignHost(t *testing.T) {
	base := "https://api.bitbucket.org"
	if got := resolveBitbucketNext("http://169.254.169.254/latest/meta-data/", base); got != "" {
		t.Fatalf("foreign host accepted: %q", got)
	}
	if got := resolveBitbucketNext("https://evil.example.com/next", base); got != "" {
		t.Fatalf("foreign host accepted: %q", got)
	}
	if got := resolveBitbucketNext("https://api.bitbucket.org/2.0/repositories?page=2", base); got == "" {
		t.Fatal("canonical Bitbucket host rejected")
	}
	if got := resolveBitbucketNext("/2.0/repositories?page=2", base); got == "" {
		t.Fatal("relative link resolved against the base host rejected")
	}
}

// TestHTTPClientConfigExplicitZeroRetries verifies `retries: 0` disables
// retries instead of silently reverting to the default of 3.
func TestHTTPClientConfigExplicitZeroRetries(t *testing.T) {
	zero := 0
	got := (&Config{HTTP: HTTPConfig{Retries: &zero}}).HTTPClientConfig()
	if got.Retries != 0 {
		t.Fatalf("Retries = %d, want 0 when explicitly configured", got.Retries)
	}
}

// TestInitThreadsClampsNegative verifies a negative thread count is normalised
// rather than reaching InitGitHubClients as a negative channel capacity.
func TestInitThreadsClampsNegative(t *testing.T) {
	old := runtime.GOMAXPROCS(0)
	defer runtime.GOMAXPROCS(old)

	neg := -1
	s := &Session{Options: &Options{Threads: &neg}}
	s.InitThreads()
	if s.Options.Threads == nil || *s.Options.Threads < 1 {
		t.Fatalf("Threads = %v, want >= 1", s.Options.Threads)
	}
}

// TestParseSourcegraphStreamPropagatesScannerError verifies a failing stream is
// surfaced as an error instead of a silent partial result.
func TestParseSourcegraphStreamPropagatesScannerError(t *testing.T) {
	_, err := parseSourcegraphStream(iotest.ErrReader(errors.New("boom")), 0)
	if err == nil {
		t.Fatal("parseSourcegraphStream returned nil error for a failing reader")
	}
}
