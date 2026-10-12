package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/trebor048/shhgot/core/httpclient"
)

const (
	// defaultGitHubSearchBaseURL is the public GitHub REST host.
	defaultGitHubSearchBaseURL = "https://api.github.com"
	// defaultGitHubSearchPerPage is the page size the search endpoints accept.
	defaultGitHubSearchPerPage = 100
	// gitHubSearchResetRetries bounds how many times a 403/429 answer is parked
	// and retried before the status is surfaced as an error.
	gitHubSearchResetRetries = 2

	gitHubCommitsEndpoint = "/search/commits"
	gitHubIssuesEndpoint  = "/search/issues"
)

// GitHubCommitHit is one commit returned by GitHub commit search.
type GitHubCommitHit struct {
	SHA     string
	Repo    string
	Message string
	HTMLURL string
	Date    string
}

// GitHubIssueHit is one issue or pull request returned by GitHub issue search.
// The issue search endpoint also matches issue and PR comments.
type GitHubIssueHit struct {
	HTMLURL   string
	Title     string
	Body      string
	Repo      string
	Author    string
	CreatedAt string
}

// GitHubSearchClient talks to GitHub's commit and issue search endpoints. The
// Doer, Sleep and Now seams are injectable so tests run without real network or
// wall-clock waits.
type GitHubSearchClient struct {
	Doer interface {
		Do(*http.Request) (*http.Response, error)
	}
	BaseURL string
	Token   string
	Sleep   func(time.Duration)
	Now     func() time.Time
}

// NewGitHubSearchClient builds a client bound to base (default
// https://api.github.com) authenticating with token.
func NewGitHubSearchClient(doer Doer, base, token string) *GitHubSearchClient {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = defaultGitHubSearchBaseURL
	}
	return &GitHubSearchClient{
		Doer:    doer,
		BaseURL: base,
		Token:   strings.TrimSpace(token),
		Sleep:   time.Sleep,
		Now:     time.Now,
	}
}

// SearchCommits runs query against GET {base}/search/commits, paginating up to
// maxResults commits (100 per page) and stopping at the first empty page or an
// incomplete result set. A 403/429 page advertising Retry-After is parked and
// retried (bounded). Hits collected before an error are returned alongside it.
func (c *GitHubSearchClient) SearchCommits(query string, maxResults int) ([]GitHubCommitHit, error) {
	query = strings.TrimSpace(query)
	if err := c.ready(query); err != nil {
		return nil, err
	}
	items, err := gitHubSearchPages[gitHubCommitItem](c, gitHubCommitsEndpoint, query, maxResults)
	hits := make([]GitHubCommitHit, 0, len(items))
	for _, item := range items {
		hits = append(hits, item.toHit())
	}
	return hits, err
}

// SearchIssues runs query against GET {base}/search/issues, paginating up to
// maxResults issues, pull requests or comments (100 per page) and stopping at
// the first empty page or an incomplete result set. A 403/429 page advertising
// Retry-After is parked and retried (bounded). Hits collected before an error
// are returned alongside it.
func (c *GitHubSearchClient) SearchIssues(query string, maxResults int) ([]GitHubIssueHit, error) {
	query = strings.TrimSpace(query)
	if err := c.ready(query); err != nil {
		return nil, err
	}
	items, err := gitHubSearchPages[gitHubIssueItem](c, gitHubIssuesEndpoint, query, maxResults)
	hits := make([]GitHubIssueHit, 0, len(items))
	for _, item := range items {
		hits = append(hits, item.toHit())
	}
	return hits, err
}

// ready rejects a client that cannot make an authenticated request before any
// request is issued.
func (c *GitHubSearchClient) ready(query string) error {
	if c == nil || c.Doer == nil {
		return errors.New("core: github search: nil doer")
	}
	if strings.TrimSpace(c.Token) == "" {
		return errors.New("core: github search: missing token")
	}
	if query == "" {
		return errors.New("core: github search: empty query")
	}
	return nil
}

// gitHubSearchPages walks pages 1.. until maxResults items are collected, an
// empty page is returned or the result set is reported incomplete.
func gitHubSearchPages[T any](c *GitHubSearchClient, endpoint, query string, maxResults int) ([]T, error) {
	var out []T
	for page := 1; len(out) < maxResults; page++ {
		resp, err := c.getPage(endpoint, query, page)
		if err != nil {
			return out, err
		}
		if resp.StatusCode != http.StatusOK {
			status := resp.StatusCode
			closeResponse(resp)
			return out, fmt.Errorf("core: github search: server status %d", status)
		}
		decoded, err := decodeGitHubSearchPage[T](resp.Body)
		closeResponse(resp)
		if err != nil {
			return out, err
		}
		if len(decoded.Items) == 0 {
			break
		}
		out = append(out, decoded.Items...)
		if decoded.IncompleteResults {
			break
		}
		if len(decoded.Items) < defaultGitHubSearchPerPage {
			break
		}
	}
	if len(out) > maxResults {
		out = out[:maxResults]
	}
	return out, nil
}

// getPage issues one search page, parking and retrying a bounded number of
// 403/429 answers that carry a usable Retry-After.
func (c *GitHubSearchClient) getPage(endpoint, query string, page int) (*http.Response, error) {
	rawURL, err := gitHubSearchURL(c.BaseURL, endpoint, query, page)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("core: github search: request: %w", err)
	}
	req.Header.Set("User-Agent", "shhgit2")
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+c.Token)

	for attempt := 0; ; attempt++ {
		resp, err := c.Doer.Do(req)
		if err != nil {
			return nil, fmt.Errorf("core: github search: request: %w", err)
		}
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}
		if attempt >= gitHubSearchResetRetries {
			return resp, nil
		}
		delay, ok := httpclient.ParseRetryAfter(resp.Header.Get("Retry-After"), c.now())
		if !ok {
			return resp, nil
		}
		closeResponse(resp)
		c.sleep(delay)
	}
}

// sleep waits d using the injected sleeper when present.
func (c *GitHubSearchClient) sleep(d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(d)
		return
	}
	time.Sleep(d)
}

// now reports the current time using the injected clock when present.
func (c *GitHubSearchClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// gitHubSearchURL builds the paginated search URL for endpoint.
func gitHubSearchURL(base, endpoint, query string, page int) (string, error) {
	u, err := url.Parse(strings.TrimRight(base, "/") + endpoint)
	if err != nil {
		return "", fmt.Errorf("core: github search: base URL: %w", err)
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("per_page", strconv.Itoa(defaultGitHubSearchPerPage))
	q.Set("page", strconv.Itoa(page))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// decodeGitHubSearchPage parses one JSON page, bounded to
// maxSearchResponseBytes.
func decodeGitHubSearchPage[T any](body io.Reader) (*gitHubSearchPage[T], error) {
	var out gitHubSearchPage[T]
	dec := json.NewDecoder(io.LimitReader(body, maxSearchResponseBytes))
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("core: github search: decoding response: %w", err)
	}
	return &out, nil
}

// gitHubSearchPage is the envelope both search endpoints return.
type gitHubSearchPage[T any] struct {
	TotalCount        int  `json:"total_count"`
	IncompleteResults bool `json:"incomplete_results"`
	Items             []T  `json:"items"`
}

// gitHubCommitItem mirrors the commit fields SearchCommits consumes.
type gitHubCommitItem struct {
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	Commit  struct {
		Message string `json:"message"`
		Author  struct {
			Date string `json:"date"`
		} `json:"author"`
	} `json:"commit"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// toHit converts an API commit item to the public hit shape.
func (it gitHubCommitItem) toHit() GitHubCommitHit {
	return GitHubCommitHit{
		SHA:     it.SHA,
		Repo:    it.Repository.FullName,
		Message: it.Commit.Message,
		HTMLURL: it.HTMLURL,
		Date:    it.Commit.Author.Date,
	}
}

// gitHubIssueItem mirrors the issue/PR fields SearchIssues consumes.
type gitHubIssueItem struct {
	HTMLURL       string `json:"html_url"`
	Title         string `json:"title"`
	Body          string `json:"body"`
	RepositoryURL string `json:"repository_url"`
	User          struct {
		Login string `json:"login"`
	} `json:"user"`
	CreatedAt string `json:"created_at"`
}

// toHit converts an API issue item to the public hit shape, deriving repo from
// the repository API URL.
func (it gitHubIssueItem) toHit() GitHubIssueHit {
	return GitHubIssueHit{
		HTMLURL:   it.HTMLURL,
		Title:     it.Title,
		Body:      it.Body,
		Repo:      repoFromRepositoryURL(it.RepositoryURL),
		Author:    it.User.Login,
		CreatedAt: it.CreatedAt,
	}
}

// repoFromRepositoryURL extracts "owner/name" from a GitHub repository API URL
// such as https://api.github.com/repos/owner/name.
func repoFromRepositoryURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == "repos" {
			return parts[i+1] + "/" + parts[i+2]
		}
	}
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return ""
}
