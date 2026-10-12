package core

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// Defaults for the "sources.gitlab", "sources.pastebin", "sources.searchcode"
// and "sources.sourcegraph" sections of config.yaml. They are consulted only
// when the corresponding key is absent; an explicit positive value wins.
const (
	defaultGitLabSourcePerPage  = 50
	defaultGitLabSourceInterval = 300

	defaultPastebinSourceLimit    = 100
	defaultPastebinSourceInterval = 30

	defaultSearchCodeSourcePerPage  = 100
	defaultSearchCodeSourceMaxPages = 3
	defaultSearchCodeSourceInterval = 300

	defaultSourcegraphSourceMaxResults = 100
	defaultSourcegraphSourceInterval   = 300

	// sourcegraphDefaultQueryCap bounds how many derived default queries the
	// Sourcegraph source issues when the operator configured none. Anonymous
	// stream search is rate-limited, so a handful of the most specific anchors
	// is the conservative default.
	sourcegraphDefaultQueryCap = 5

	// Defaults for the query-driven sources: github_commits, github_issues,
	// grepapp and psbdmp. Each caps a single pass at 100 results and polls
	// every 300 seconds unless configured otherwise.
	defaultQuerySourceMaxResults = 100
	defaultQuerySourceInterval   = 300

	// Defaults for sources.bitbucket. They bound one walk so a large workspace
	// cannot make a pass run unbounded.
	defaultBitbucketMaxRepos = 50
	defaultBitbucketMaxFiles = 500
	defaultBitbucketInterval = 300

	// defaultSourceQueryCap bounds how many derived default queries each
	// query-driven source issues when the operator configured none.
	defaultSourceQueryCap = 5
)

// GitLabEnabled reports whether the GitLab public-snippet source is enabled. It
// is off unless sources.gitlab.enabled is explicitly true, so an operator who
// has not opted in keeps the previous behaviour.
func (c *Config) GitLabEnabled() bool {
	if c == nil {
		return false
	}
	return boolOr(c.Sources.GitLab.Enabled, false)
}

// GitLabValues resolves the "sources.gitlab" section into the values the source
// loop consumes. Absent keys keep the documented defaults: perPage 50 and a
// 300-second polling interval.
func (c *Config) GitLabValues() (perPage, intervalSecs int) {
	if c == nil {
		return defaultGitLabSourcePerPage, defaultGitLabSourceInterval
	}
	return intOr(c.Sources.GitLab.PerPage, defaultGitLabSourcePerPage),
		intOr(c.Sources.GitLab.Interval, defaultGitLabSourceInterval)
}

// PastebinEnabled reports whether the Pastebin Scraping API source is enabled.
// It is off unless sources.pastebin.enabled is explicitly true.
func (c *Config) PastebinEnabled() bool {
	if c == nil {
		return false
	}
	return boolOr(c.Sources.Pastebin.Enabled, false)
}

// PastebinValues resolves the "sources.pastebin" section into the values the
// source loop consumes. apiKey only gates the on/off decision and selects the
// Scraping API over the public raw endpoint; the scrape endpoint itself is not
// key-authenticated. Absent keys keep the documented defaults: limit 100 and a
// 30-second polling interval.
func (c *Config) PastebinValues() (apiKey string, limit, intervalSecs int) {
	if c == nil {
		return "", defaultPastebinSourceLimit, defaultPastebinSourceInterval
	}
	return strings.TrimSpace(c.Sources.Pastebin.APIKey),
		intOr(c.Sources.Pastebin.Limit, defaultPastebinSourceLimit),
		intOr(c.Sources.Pastebin.Interval, defaultPastebinSourceInterval)
}

// SearchCodeEnabled reports whether the searchcode.com source is enabled. It is
// off unless sources.searchcode.enabled is explicitly true.
func (c *Config) SearchCodeEnabled() bool {
	if c == nil {
		return false
	}
	return boolOr(c.Sources.SearchCode.Enabled, false)
}

// SearchCodeValues resolves the "sources.searchcode" section into the values
// the source loop consumes. queries is returned as configured; an empty result
// means the caller falls back to core.SearchCodeKeywords. Absent keys keep the
// documented defaults: perPage 100, maxPages 3 and a 300-second polling
// interval.
func (c *Config) SearchCodeValues() (queries []string, perPage, maxPages, intervalSecs int) {
	if c == nil {
		return nil, defaultSearchCodeSourcePerPage, defaultSearchCodeSourceMaxPages,
			defaultSearchCodeSourceInterval
	}
	return c.Sources.SearchCode.Queries,
		intOr(c.Sources.SearchCode.PerPage, defaultSearchCodeSourcePerPage),
		intOr(c.Sources.SearchCode.MaxPages, defaultSearchCodeSourceMaxPages),
		intOr(c.Sources.SearchCode.Interval, defaultSearchCodeSourceInterval)
}

// SourcegraphEnabled reports whether the Sourcegraph stream-search source is
// enabled. It is off unless sources.sourcegraph.enabled is explicitly true.
func (c *Config) SourcegraphEnabled() bool {
	if c == nil {
		return false
	}
	return boolOr(c.Sources.Sourcegraph.Enabled, false)
}

// SourcegraphValues resolves the "sources.sourcegraph" section into the values
// the source loop consumes. queries is returned as configured; an empty result
// means the caller derives a small default list from the loaded signature
// anchors. Absent keys keep the documented defaults: maxResults 100 and a
// 300-second polling interval. token is optional and only raises the anonymous
// rate limit.
func (c *Config) SourcegraphValues() (queries []string, maxResults, intervalSecs int, token string) {
	if c == nil {
		return nil, defaultSourcegraphSourceMaxResults, defaultSourcegraphSourceInterval, ""
	}
	return c.Sources.Sourcegraph.Queries,
		intOr(c.Sources.Sourcegraph.MaxResults, defaultSourcegraphSourceMaxResults),
		intOr(c.Sources.Sourcegraph.Interval, defaultSourcegraphSourceInterval),
		strings.TrimSpace(c.Sources.Sourcegraph.Token)
}

// GitLabBlobsEnabled reports whether the GitLab blobs (code search) source is
// enabled. It is off unless sources.gitlab.search_enabled is explicitly true.
func (c *Config) GitLabBlobsEnabled() bool {
	if c == nil {
		return false
	}
	return boolOr(c.Sources.GitLab.SearchEnabled, false)
}

// GitLabBlobValues resolves the sources.gitlab blob-search fields: the access
// token and the polling interval. The per-page size is shared with the snippet
// source; the page depth is bounded by defaultGitLabBlobMaxPages.
func (c *Config) GitLabBlobValues() (token string, intervalSecs int) {
	if c == nil {
		return "", defaultGitLabSourceInterval
	}
	return strings.TrimSpace(c.Sources.GitLab.Token),
		intOr(c.Sources.GitLab.Interval, defaultGitLabSourceInterval)
}

// GitHubCommitsEnabled reports whether the GitHub commit-search source is
// enabled. It is off unless sources.github_commits.enabled is explicitly true.
func (c *Config) GitHubCommitsEnabled() bool {
	if c == nil {
		return false
	}
	return boolOr(c.Sources.GitHubCommits.Enabled, false)
}

// GitHubCommitsValues resolves sources.github_commits into the values the
// source loop consumes. queries is returned as configured; an empty result
// means the caller derives defaults from the loaded signature anchors.
func (c *Config) GitHubCommitsValues() (queries []string, maxResults, intervalSecs int) {
	if c == nil {
		return nil, defaultQuerySourceMaxResults, defaultQuerySourceInterval
	}
	return c.Sources.GitHubCommits.Queries,
		intOr(c.Sources.GitHubCommits.MaxResults, defaultQuerySourceMaxResults),
		intOr(c.Sources.GitHubCommits.Interval, defaultQuerySourceInterval)
}

// GitHubIssuesEnabled reports whether the GitHub issue-search source is
// enabled. It is off unless sources.github_issues.enabled is explicitly true.
func (c *Config) GitHubIssuesEnabled() bool {
	if c == nil {
		return false
	}
	return boolOr(c.Sources.GitHubIssues.Enabled, false)
}

// GitHubIssuesValues resolves sources.github_issues into the values the source
// loop consumes. queries is returned as configured; an empty result means the
// caller derives defaults from the loaded signature anchors.
func (c *Config) GitHubIssuesValues() (queries []string, maxResults, intervalSecs int) {
	if c == nil {
		return nil, defaultQuerySourceMaxResults, defaultQuerySourceInterval
	}
	return c.Sources.GitHubIssues.Queries,
		intOr(c.Sources.GitHubIssues.MaxResults, defaultQuerySourceMaxResults),
		intOr(c.Sources.GitHubIssues.Interval, defaultQuerySourceInterval)
}

// GrepAppEnabled reports whether the grep.app source is enabled. It is off
// unless sources.grepapp.enabled is explicitly true.
func (c *Config) GrepAppEnabled() bool {
	if c == nil {
		return false
	}
	return boolOr(c.Sources.GrepApp.Enabled, false)
}

// GrepAppValues resolves sources.grepapp into the values the source loop
// consumes. queries is returned as configured; an empty result means the caller
// derives defaults from the loaded signature anchors.
func (c *Config) GrepAppValues() (queries []string, maxResults, intervalSecs int) {
	if c == nil {
		return nil, defaultQuerySourceMaxResults, defaultQuerySourceInterval
	}
	return c.Sources.GrepApp.Queries,
		intOr(c.Sources.GrepApp.MaxResults, defaultQuerySourceMaxResults),
		intOr(c.Sources.GrepApp.Interval, defaultQuerySourceInterval)
}

// PsbdmpEnabled reports whether the psbdmp.ws source is enabled. It is off
// unless sources.psbdmp.enabled is explicitly true.
func (c *Config) PsbdmpEnabled() bool {
	if c == nil {
		return false
	}
	return boolOr(c.Sources.Psbdmp.Enabled, false)
}

// PsbdmpValues resolves sources.psbdmp into the values the source loop
// consumes. queries is returned as configured; an empty result means the caller
// derives defaults from the loaded signature anchors.
func (c *Config) PsbdmpValues() (queries []string, maxResults, intervalSecs int) {
	if c == nil {
		return nil, defaultQuerySourceMaxResults, defaultQuerySourceInterval
	}
	return c.Sources.Psbdmp.Queries,
		intOr(c.Sources.Psbdmp.MaxResults, defaultQuerySourceMaxResults),
		intOr(c.Sources.Psbdmp.Interval, defaultQuerySourceInterval)
}

// BitbucketEnabled reports whether the Bitbucket workspace source is enabled.
// It is off unless sources.bitbucket.enabled is explicitly true.
func (c *Config) BitbucketEnabled() bool {
	if c == nil {
		return false
	}
	return boolOr(c.Sources.Bitbucket.Enabled, false)
}

// BitbucketValues resolves sources.bitbucket into the values the source loop
// consumes: the workspaces to walk and the per-pass caps. An empty workspace
// list means the source does nothing.
func (c *Config) BitbucketValues() (workspaces []string, maxRepos, maxFiles, intervalSecs int) {
	if c == nil {
		return nil, defaultBitbucketMaxRepos, defaultBitbucketMaxFiles, defaultBitbucketInterval
	}
	return c.Sources.Bitbucket.Workspaces,
		intOr(c.Sources.Bitbucket.MaxRepos, defaultBitbucketMaxRepos),
		intOr(c.Sources.Bitbucket.MaxFiles, defaultBitbucketMaxFiles),
		intOr(c.Sources.Bitbucket.Interval, defaultBitbucketInterval)
}

// DefaultSourcegraphQueries turns signature anchors into a small set of quoted
// Sourcegraph queries. It keeps the most specific (longest) anchors, skips
// blanks and duplicates, ignores anchors too short to be useful, and caps the
// result at max (a non-positive max uses sourcegraphDefaultQueryCap). A quoted
// anchor is the high-signal default: it matches the literal the scan prefilter
// already proves a signature needs.
func DefaultSourcegraphQueries(anchors []string, max int) []string {
	if max <= 0 {
		max = sourcegraphDefaultQueryCap
	}
	seen := make(map[string]bool, len(anchors))
	unique := make([]string, 0, len(anchors))
	for _, anchor := range anchors {
		anchor = strings.TrimSpace(anchor)
		if utf8.RuneCountInString(anchor) < 4 || strings.ContainsRune(anchor, '"') {
			continue
		}
		key := strings.ToLower(anchor)
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, anchor)
	}
	sort.SliceStable(unique, func(i, j int) bool {
		return utf8.RuneCountInString(unique[i]) > utf8.RuneCountInString(unique[j])
	})
	if len(unique) > max {
		unique = unique[:max]
	}
	out := make([]string, 0, len(unique))
	for _, anchor := range unique {
		out = append(out, `"`+anchor+`"`)
	}
	return out
}

// DefaultSourceQueries turns signature anchors into a small, plain (unquoted)
// query list for the query-driven discovery sources whose API takes a bare
// search term: GitHub commit/issue search, grep.app, psbdmp and GitLab blob
// search. It keeps the longest (most specific) anchors, skips blanks, anchors
// too short to be useful and anchors containing a quote, de-duplicates
// case-insensitively and caps the result at max (a non-positive max uses
// defaultSourceQueryCap). It mirrors DefaultSourcegraphQueries but omits the
// quoting, which those backends would search for literally.
func DefaultSourceQueries(anchors []string, max int) []string {
	if max <= 0 {
		max = defaultSourceQueryCap
	}
	seen := make(map[string]bool, len(anchors))
	unique := make([]string, 0, len(anchors))
	for _, anchor := range anchors {
		anchor = strings.TrimSpace(anchor)
		if utf8.RuneCountInString(anchor) < 4 || strings.ContainsRune(anchor, '"') {
			continue
		}
		key := strings.ToLower(anchor)
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, anchor)
	}
	sort.SliceStable(unique, func(i, j int) bool {
		return utf8.RuneCountInString(unique[i]) > utf8.RuneCountInString(unique[j])
	})
	if len(unique) > max {
		unique = unique[:max]
	}
	return unique
}
