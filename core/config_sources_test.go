package core

import "testing"

func gitLabBoolPtr(b bool) *bool { return &b }
func gitLabIntPtr(n int) *int    { return &n }

// An absent sources.gitlab section keeps the documented defaults and leaves the
// source disabled, so an existing operator's scan output is unchanged.
func TestGitLabValuesDefaults(t *testing.T) {
	c := &Config{}
	perPage, interval := c.GitLabValues()
	if perPage != 50 {
		t.Fatalf("perPage = %d, want 50", perPage)
	}
	if interval != 300 {
		t.Fatalf("interval = %d, want 300", interval)
	}
	if c.GitLabEnabled() {
		t.Fatal("GitLab source must be off by default")
	}
}

// An explicit positive value wins over the default.
func TestGitLabValuesOverride(t *testing.T) {
	c := &Config{
		Sources: SourcesConfig{GitLab: GitLabSourceConfig{
			Enabled:  gitLabBoolPtr(true),
			PerPage:  gitLabIntPtr(10),
			Interval: gitLabIntPtr(45),
		}},
	}
	perPage, interval := c.GitLabValues()
	if perPage != 10 || interval != 45 {
		t.Fatalf("GitLabValues = (%d, %d), want (10, 45)", perPage, interval)
	}
	if !c.GitLabEnabled() {
		t.Fatal("GitLabEnabled must honour enabled: true")
	}
}

// A non-positive explicit value is treated as absent, keeping the default.
func TestGitLabValuesNonPositiveFallsBack(t *testing.T) {
	c := &Config{Sources: SourcesConfig{GitLab: GitLabSourceConfig{
		PerPage:  gitLabIntPtr(0),
		Interval: gitLabIntPtr(-5),
	}}}
	perPage, interval := c.GitLabValues()
	if perPage != 50 || interval != 300 {
		t.Fatalf("GitLabValues = (%d, %d), want the defaults (50, 300)", perPage, interval)
	}
}

// A nil config must not panic and reports the safe defaults.
func TestGitLabValuesNilConfig(t *testing.T) {
	var c *Config
	if c.GitLabEnabled() {
		t.Fatal("nil config must report disabled")
	}
	perPage, interval := c.GitLabValues()
	if perPage != 50 || interval != 300 {
		t.Fatalf("nil GitLabValues = (%d, %d), want (50, 300)", perPage, interval)
	}
}

// An absent sources.pastebin section keeps the documented defaults and leaves
// the source disabled.
func TestPastebinValuesDefaults(t *testing.T) {
	c := &Config{}
	apiKey, limit, interval := c.PastebinValues()
	if apiKey != "" {
		t.Fatalf("apiKey = %q, want empty", apiKey)
	}
	if limit != 100 || interval != 30 {
		t.Fatalf("PastebinValues = (%q, %d, %d), want (\"\", 100, 30)", apiKey, limit, interval)
	}
	if c.PastebinEnabled() {
		t.Fatal("Pastebin source must be off by default")
	}
}

// Explicit values win over the defaults; the api key is trimmed.
func TestPastebinValuesOverride(t *testing.T) {
	c := &Config{Sources: SourcesConfig{Pastebin: PastebinSourceConfig{
		Enabled:  gitLabBoolPtr(true),
		APIKey:   "  k  ",
		Limit:    gitLabIntPtr(5),
		Interval: gitLabIntPtr(10),
	}}}
	apiKey, limit, interval := c.PastebinValues()
	if apiKey != "k" || limit != 5 || interval != 10 {
		t.Fatalf("PastebinValues = (%q, %d, %d), want (\"k\", 5, 10)", apiKey, limit, interval)
	}
	if !c.PastebinEnabled() {
		t.Fatal("PastebinEnabled must honour enabled: true")
	}
}

// An absent sources.searchcode section keeps the documented defaults and leaves
// queries nil so the caller can fall back to core.SearchCodeKeywords.
func TestSearchCodeValuesDefaults(t *testing.T) {
	c := &Config{}
	queries, perPage, maxPages, interval := c.SearchCodeValues()
	if queries != nil {
		t.Fatalf("queries = %v, want nil", queries)
	}
	if perPage != 100 || maxPages != 3 || interval != 300 {
		t.Fatalf("SearchCodeValues = (%d, %d, %d), want (100, 3, 300)", perPage, maxPages, interval)
	}
	if c.SearchCodeEnabled() {
		t.Fatal("searchcode source must be off by default")
	}
}

// Explicit searchcode values win over the defaults.
func TestSearchCodeValuesOverride(t *testing.T) {
	c := &Config{Sources: SourcesConfig{SearchCode: SearchCodeSourceConfig{
		Enabled:  gitLabBoolPtr(true),
		Queries:  []string{"q1", "q2"},
		PerPage:  gitLabIntPtr(7),
		MaxPages: gitLabIntPtr(2),
		Interval: gitLabIntPtr(60),
	}}}
	queries, perPage, maxPages, interval := c.SearchCodeValues()
	if len(queries) != 2 || queries[0] != "q1" || queries[1] != "q2" {
		t.Fatalf("queries = %v, want [q1 q2]", queries)
	}
	if perPage != 7 || maxPages != 2 || interval != 60 {
		t.Fatalf("SearchCodeValues = (%d, %d, %d), want (7, 2, 60)", perPage, maxPages, interval)
	}
	if !c.SearchCodeEnabled() {
		t.Fatal("SearchCodeEnabled must honour enabled: true")
	}
}

// An absent sources.sourcegraph section keeps the documented defaults and leaves
// the source disabled.
func TestSourcegraphValuesDefaults(t *testing.T) {
	c := &Config{}
	queries, maxResults, interval, token := c.SourcegraphValues()
	if queries != nil || token != "" {
		t.Fatalf("queries = %v, token = %q, want nil and empty", queries, token)
	}
	if maxResults != 100 || interval != 300 {
		t.Fatalf("SourcegraphValues = (%d, %d), want (100, 300)", maxResults, interval)
	}
	if c.SourcegraphEnabled() {
		t.Fatal("Sourcegraph source must be off by default")
	}
}

// Explicit sourcegraph values win over the defaults; the token is trimmed.
func TestSourcegraphValuesOverride(t *testing.T) {
	c := &Config{Sources: SourcesConfig{Sourcegraph: SourcegraphSourceConfig{
		Enabled:    gitLabBoolPtr(true),
		Queries:    []string{"q"},
		MaxResults: gitLabIntPtr(11),
		Interval:   gitLabIntPtr(45),
		Token:      "  sgp_x  ",
	}}}
	queries, maxResults, interval, token := c.SourcegraphValues()
	if len(queries) != 1 || queries[0] != "q" {
		t.Fatalf("queries = %v, want [q]", queries)
	}
	if maxResults != 11 || interval != 45 || token != "sgp_x" {
		t.Fatalf("SourcegraphValues = (%d, %d, %q), want (11, 45, sgp_x)", maxResults, interval, token)
	}
	if !c.SourcegraphEnabled() {
		t.Fatal("SourcegraphEnabled must honour enabled: true")
	}
}

// A nil config must not panic and reports the safe defaults for every new
// source.
func TestNewSourceValuesNilConfig(t *testing.T) {
	var c *Config
	if c.PastebinEnabled() || c.SearchCodeEnabled() || c.SourcegraphEnabled() {
		t.Fatal("nil config must report every source disabled")
	}
	if _, limit, interval := c.PastebinValues(); limit != 100 || interval != 30 {
		t.Fatalf("nil PastebinValues = (%d, %d), want (100, 30)", limit, interval)
	}
	if _, perPage, maxPages, interval := c.SearchCodeValues(); perPage != 100 || maxPages != 3 || interval != 300 {
		t.Fatalf("nil SearchCodeValues = (%d, %d, %d), want (100, 3, 300)", perPage, maxPages, interval)
	}
	if _, maxResults, interval, _ := c.SourcegraphValues(); maxResults != 100 || interval != 300 {
		t.Fatalf("nil SourcegraphValues = (%d, %d), want (100, 300)", maxResults, interval)
	}
	if c.GitHubCommitsEnabled() || c.GitHubIssuesEnabled() || c.GitLabBlobsEnabled() ||
		c.GrepAppEnabled() || c.PsbdmpEnabled() || c.BitbucketEnabled() {
		t.Fatal("nil config must report every new source disabled")
	}
	if _, maxResults, interval := c.GitHubCommitsValues(); maxResults != 100 || interval != 300 {
		t.Fatalf("nil GitHubCommitsValues = (%d, %d), want (100, 300)", maxResults, interval)
	}
	if _, maxResults, interval := c.GitHubIssuesValues(); maxResults != 100 || interval != 300 {
		t.Fatalf("nil GitHubIssuesValues = (%d, %d), want (100, 300)", maxResults, interval)
	}
	if _, maxResults, interval := c.GrepAppValues(); maxResults != 100 || interval != 300 {
		t.Fatalf("nil GrepAppValues = (%d, %d), want (100, 300)", maxResults, interval)
	}
	if _, maxResults, interval := c.PsbdmpValues(); maxResults != 100 || interval != 300 {
		t.Fatalf("nil PsbdmpValues = (%d, %d), want (100, 300)", maxResults, interval)
	}
	if token, interval := c.GitLabBlobValues(); token != "" || interval != 300 {
		t.Fatalf("nil GitLabBlobValues = (%q, %d), want (\"\", 300)", token, interval)
	}
	if workspaces, maxRepos, maxFiles, interval := c.BitbucketValues(); workspaces != nil || maxRepos != 50 || maxFiles != 500 || interval != 300 {
		t.Fatalf("nil BitbucketValues = (%v, %d, %d, %d), want (nil, 50, 500, 300)", workspaces, maxRepos, maxFiles, interval)
	}
}

// The query-driven sources keep the documented defaults and stay off until
// enabled; explicit values win.
func TestQuerySourceValues(t *testing.T) {
	c := &Config{}
	if c.GitHubCommitsEnabled() || c.GitHubIssuesEnabled() || c.GrepAppEnabled() || c.PsbdmpEnabled() {
		t.Fatal("query-driven sources must be off by default")
	}
	if _, maxResults, interval := c.GitHubCommitsValues(); maxResults != 100 || interval != 300 {
		t.Fatalf("default GitHubCommitsValues = (%d, %d), want (100, 300)", maxResults, interval)
	}

	c = &Config{Sources: SourcesConfig{
		GitHubCommits: QuerySourceConfig{Enabled: gitLabBoolPtr(true), Queries: []string{"q"}, MaxResults: gitLabIntPtr(7), Interval: gitLabIntPtr(45)},
		GitHubIssues:  QuerySourceConfig{Enabled: gitLabBoolPtr(true), Queries: []string{"r"}, MaxResults: gitLabIntPtr(8), Interval: gitLabIntPtr(60)},
		GrepApp:       QuerySourceConfig{Enabled: gitLabBoolPtr(true), Queries: []string{"s"}, MaxResults: gitLabIntPtr(9), Interval: gitLabIntPtr(90)},
		Psbdmp:        QuerySourceConfig{Enabled: gitLabBoolPtr(true), Queries: []string{"t"}, MaxResults: gitLabIntPtr(10), Interval: gitLabIntPtr(120)},
	}}
	if queries, maxResults, interval := c.GitHubCommitsValues(); len(queries) != 1 || queries[0] != "q" || maxResults != 7 || interval != 45 {
		t.Fatalf("GitHubCommitsValues = (%v, %d, %d), want ([q], 7, 45)", queries, maxResults, interval)
	}
	if queries, maxResults, interval := c.GitHubIssuesValues(); len(queries) != 1 || queries[0] != "r" || maxResults != 8 || interval != 60 {
		t.Fatalf("GitHubIssuesValues = (%v, %d, %d), want ([r], 8, 60)", queries, maxResults, interval)
	}
	if queries, maxResults, interval := c.GrepAppValues(); len(queries) != 1 || queries[0] != "s" || maxResults != 9 || interval != 90 {
		t.Fatalf("GrepAppValues = (%v, %d, %d), want ([s], 9, 90)", queries, maxResults, interval)
	}
	if queries, maxResults, interval := c.PsbdmpValues(); len(queries) != 1 || queries[0] != "t" || maxResults != 10 || interval != 120 {
		t.Fatalf("PsbdmpValues = (%v, %d, %d), want ([t], 10, 120)", queries, maxResults, interval)
	}
}

// The extended GitLab blob fields are gated independently of the snippet source.
func TestGitLabBlobValues(t *testing.T) {
	c := &Config{Sources: SourcesConfig{GitLab: GitLabSourceConfig{
		Enabled:       gitLabBoolPtr(true),
		SearchEnabled: gitLabBoolPtr(true),
		Token:         "  glpat-x  ",
		Interval:      gitLabIntPtr(120),
	}}}
	if !c.GitLabBlobsEnabled() {
		t.Fatal("GitLabBlobsEnabled must honour search_enabled: true")
	}
	if token, interval := c.GitLabBlobValues(); token != "glpat-x" || interval != 120 {
		t.Fatalf("GitLabBlobValues = (%q, %d), want (glpat-x, 120)", token, interval)
	}
	if !c.GitLabEnabled() {
		t.Fatal("the snippet source must stay independently gated")
	}
}

// Bitbucket keeps its documented defaults and stays off until enabled.
func TestBitbucketValues(t *testing.T) {
	c := &Config{}
	if c.BitbucketEnabled() {
		t.Fatal("Bitbucket source must be off by default")
	}
	if _, maxRepos, maxFiles, interval := c.BitbucketValues(); maxRepos != 50 || maxFiles != 500 || interval != 300 {
		t.Fatalf("default BitbucketValues = (%d, %d, %d), want (50, 500, 300)", maxRepos, maxFiles, interval)
	}

	c = &Config{Sources: SourcesConfig{Bitbucket: BitbucketSourceConfig{
		Enabled:    gitLabBoolPtr(true),
		Workspaces: []string{"acme", "beta"},
		MaxRepos:   gitLabIntPtr(3),
		MaxFiles:   gitLabIntPtr(4),
		Interval:   gitLabIntPtr(30),
	}}}
	workspaces, maxRepos, maxFiles, interval := c.BitbucketValues()
	if len(workspaces) != 2 || workspaces[0] != "acme" || workspaces[1] != "beta" {
		t.Fatalf("workspaces = %v, want [acme beta]", workspaces)
	}
	if maxRepos != 3 || maxFiles != 4 || interval != 30 {
		t.Fatalf("BitbucketValues = (%d, %d, %d), want (3, 4, 30)", maxRepos, maxFiles, interval)
	}
}

// DefaultSourceQueries keeps the longest anchors, drops short and duplicate
// ones, and does not quote what it keeps.
func TestDefaultSourceQueries(t *testing.T) {
	got := DefaultSourceQueries([]string{
		"sk-",
		"AWS_SECRET_ACCESS_KEY",
		"ab",
		"aws_secret_access_key",
		`has"quote`,
		"OPENAI_API_KEY",
	}, 0)
	want := []string{"AWS_SECRET_ACCESS_KEY", "OPENAI_API_KEY"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}

	capped := DefaultSourceQueries([]string{"OPENAI_API_KEY", "AWS_SECRET_ACCESS_KEY"}, 1)
	if len(capped) != 1 || capped[0] != "AWS_SECRET_ACCESS_KEY" {
		t.Fatalf("capped = %v, want the longest anchor only", capped)
	}

	if out := DefaultSourceQueries(nil, 0); len(out) != 0 {
		t.Fatalf("nil anchors = %v, want empty", out)
	}
}

// DefaultSourcegraphQueries keeps the longest (most specific) anchors, drops
// short and duplicate ones, quotes what it keeps and honours the cap.
func TestDefaultSourcegraphQueries(t *testing.T) {
	got := DefaultSourcegraphQueries([]string{
		"sk-proj-",
		"AWS_SECRET_ACCESS_KEY",
		"ab",
		"aws_secret_access_key",
		`has"quote`,
		"OPENAI_API_KEY",
	}, 0)
	want := []string{`"AWS_SECRET_ACCESS_KEY"`, `"OPENAI_API_KEY"`, `"sk-proj-"`}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}

	capped := DefaultSourcegraphQueries([]string{"OPENAI_API_KEY", "AWS_SECRET_ACCESS_KEY"}, 1)
	if len(capped) != 1 || capped[0] != `"AWS_SECRET_ACCESS_KEY"` {
		t.Fatalf("capped = %v, want the longest anchor only", capped)
	}

	if out := DefaultSourcegraphQueries(nil, 0); len(out) != 0 {
		t.Fatalf("nil anchors = %v, want empty", out)
	}
}
