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
	defaultGitLabBaseURL = "https://gitlab.com"
	defaultGitLabPerPage = 20
	maxGitLabPerPage     = 100
	// maxGitLabResponseBytes bounds a single snippet-list page so a hostile
	// endpoint cannot stream unbounded JSON.
	maxGitLabResponseBytes = 8 << 20
	// gitlabRateLimitRetries is how many times a 403/429 page is parked using
	// an advertised Retry-After and retried before the status is surfaced.
	gitlabRateLimitRetries = 2
	// gitLabSearchPath is the global search endpoint used with scope=blobs.
	gitLabSearchPath = "/api/v4/search"
	// gitLabTierProbeBytes bounds how much of a 403 body is read to recognise
	// the advanced-search tier refusal before it is thrown away.
	gitLabTierProbeBytes = 8 << 10
	// maxGitLabSnippetPages bounds the public-snippets walk when the server
	// omits X-Next-Page: without it a server that always returns a full page
	// would loop forever and grow the result slice without limit.
	maxGitLabSnippetPages = 1000
)

// ErrGitLabSearchUnavailable reports that the GitLab instance or its tier does
// not offer the blobs search scope. That scope needs advanced (exact code)
// search, a Premium/Ultimate feature, so the caller should warn and disable
// GitLab blob search rather than keep retrying.
var ErrGitLabSearchUnavailable = errors.New("core: GitLab blob search unavailable (requires advanced search / Premium or Ultimate)")

// gitlabSearchTierMarkers are case-insensitive phrases GitLab uses to reject a
// blobs search because the tier does not include advanced code search.
var gitlabSearchTierMarkers = []string{
	"advanced search",
	"exact code search",
	"premium",
	"ultimate",
	"search_api",
}

// GitLabBlob is one blob match returned by the GitLab API. Data is the matching
// content excerpt.
type GitLabBlob struct {
	Path      string
	Filename  string
	Ref       string
	Data      string
	StartLine int
	ProjectID int
}

// GitLabSnippet is one public snippet returned by the GitLab API.
type GitLabSnippet struct {
	ID       int
	Title    string
	FileName string
	RawURL   string
	WebURL   string
}

// GitLabClient reads the public GitLab snippets API and the authenticated blob
// search API. Every network seam is injectable so tests never touch the network
// or block.
type GitLabClient struct {
	// Doer performs the HTTP request. Required.
	Doer interface {
		Do(*http.Request) (*http.Response, error)
	}
	// BaseURL is the GitLab instance root; empty uses https://gitlab.com.
	BaseURL string
	// PerPage is the page size clamped to 1..100; out-of-range uses 20.
	PerPage int
	// Token is the GitLab personal access token sent as the PRIVATE-TOKEN
	// header on search requests; empty sends no credential.
	Token string
	// Sleep is the injected sleep seam; nil uses time.Sleep.
	Sleep func(time.Duration)
	// Now is the injected clock used to resolve an HTTP-date Retry-After; nil
	// uses time.Now.
	Now func() time.Time
}

// PublicSnippets pages through GET {base}/api/v4/snippets/public and returns
// the snippets. A limit greater than zero stops the walk early and truncates
// the result. A non-200 page (including a rate-limit answer that survived the
// bounded retries) ends pagination cleanly, returning what was collected.
func (g *GitLabClient) PublicSnippets(limit int) ([]GitLabSnippet, error) {
	if g.Doer == nil {
		return nil, errors.New("core: GitLabClient.Doer is nil")
	}
	base := g.baseURL()
	perPage := g.perPage()

	var out []GitLabSnippet
	for page := 1; page <= maxGitLabSnippetPages; {
		resp, err := g.snippetsPage(base, perPage, page)
		if err != nil {
			return out, err
		}
		if resp.StatusCode != http.StatusOK {
			closeResponse(resp)
			break
		}
		next := strings.TrimSpace(resp.Header.Get("X-Next-Page"))
		items, err := decodeGitLabSnippets(resp.Body, base)
		closeResponse(resp)
		if err != nil {
			return out, err
		}

		out = append(out, items...)
		if limit > 0 && len(out) >= limit {
			out = out[:limit]
			break
		}
		if len(items) == 0 {
			break
		}
		if next != "" {
			n, convErr := strconv.Atoi(next)
			if convErr != nil || n <= page {
				break
			}
			page = n
			continue
		}
		// No X-Next-Page header: fall back to a page loop that stops on a
		// short page or when a bonus page yields nothing.
		if len(items) < perPage {
			break
		}
		page++
	}
	return out, nil
}

// SearchBlobs runs one GET {base}/api/v4/search?scope=blobs page and returns
// the matching blobs. The PRIVATE-TOKEN header is set when Token is non-empty
// (the blobs scope always requires authentication). A 403/429 answer is parked
// and retried at most gitlabRateLimitRetries times using an advertised
// Retry-After; a 403 carrying GitLab's advanced-search tier refusal is surfaced
// as ErrGitLabSearchUnavailable instead of being retried. Any other non-200
// status, or a 403/429 without a usable Retry-After, is returned as an error.
func (g *GitLabClient) SearchBlobs(query string, perPage, page int) ([]GitLabBlob, error) {
	if g.Doer == nil {
		return nil, errors.New("core: GitLabClient.Doer is nil")
	}
	base := g.baseURL()
	if perPage <= 0 || perPage > maxGitLabPerPage {
		perPage = g.perPage()
	}
	rawURL, err := gitLabBlobSearchURL(base, query, perPage, page)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("core: GitLab search request: %w", err)
	}
	req.Header.Set("User-Agent", "shhgit2")
	req.Header.Set("Accept", "application/json")
	if g.Token != "" {
		req.Header.Set("PRIVATE-TOKEN", g.Token)
	}

	resp, err := g.searchDo(req)
	if err != nil {
		return nil, err
	}
	defer closeResponse(resp)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("core: GitLab blob search: server status %d", resp.StatusCode)
	}
	return decodeGitLabBlobs(resp.Body)
}

// SearchBlobsAll pages 1..maxPages with SearchBlobs, stopping at the first empty
// page and de-duplicating by path+ref. A non-positive maxPages returns nothing.
func (g *GitLabClient) SearchBlobsAll(query string, maxPages int) ([]GitLabBlob, error) {
	if maxPages <= 0 {
		return nil, nil
	}
	perPage := g.perPage()
	var out []GitLabBlob
	seen := make(map[string]bool)
	for page := 1; page <= maxPages; page++ {
		blobs, err := g.SearchBlobs(query, perPage, page)
		if err != nil {
			return out, err
		}
		if len(blobs) == 0 {
			break
		}
		for _, b := range blobs {
			key := b.Path + "\x00" + b.Ref
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, b)
		}
	}
	return out, nil
}

// SnippetContent downloads the raw body of a snippet. The host must be on the
// allowlist: gitlab.com plus the host of this client's BaseURL (so an injected
// base in tests is honoured) are permitted, and anything else is refused before
// a request is issued.
func (g *GitLabClient) SnippetContent(rawURL string) ([]byte, error) {
	f := &Fetcher{
		Doer:         g.Doer,
		AllowedHosts: g.allowedHosts(),
		MaxRetries:   gitlabRateLimitRetries,
		Now:          g.Now,
		Sleep:        g.Sleep,
	}
	return f.Fetch(rawURL)
}

// allowedHosts returns gitlab.com plus the BaseURL host, de-duplicated. An
// explicit base port is preserved (host:port entry) so hostPortAllowed can
// scope custom-port instances without opening every port on that host.
func (g *GitLabClient) allowedHosts() []string {
	hosts := []string{"gitlab.com"}
	base := g.baseURL()
	if u, err := url.Parse(base); err == nil {
		if h, p := entryHostPort(u.Host); h != "" {
			entry := h
			if p != "" {
				entry += ":" + p
			}
			if !entryListed(entry, hosts) {
				hosts = append(hosts, entry)
			}
		}
	}
	return hosts
}

// snippetsPage performs one snippet-list GET, parking and retrying a bounded
// number of times on a 403/429 rate-limit answer.
func (g *GitLabClient) snippetsPage(base string, perPage, page int) (*http.Response, error) {
	rawURL, err := gitLabSnippetsURL(base, perPage, page)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("core: GitLab request: %w", err)
	}
	req.Header.Set("User-Agent", "shhgit2")
	req.Header.Set("Accept", "application/json")

	for attempt := 0; ; attempt++ {
		resp, err := g.Doer.Do(req)
		if err != nil {
			return nil, fmt.Errorf("core: GitLab request: %w", err)
		}
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}
		if attempt >= gitlabRateLimitRetries {
			return resp, nil
		}
		delay, ok := retryAfterDelayOK(resp, g.now())
		if !ok {
			return resp, nil
		}
		closeResponse(resp)
		g.sleep(delay)
	}
}

// searchDo performs one blob-search request, parking and retrying a 403/429
// rate-limit answer at most gitlabRateLimitRetries times when it advertises a
// usable Retry-After. A 403 that carries GitLab's advanced-search/tier refusal
// is surfaced immediately as ErrGitLabSearchUnavailable rather than retried,
// and a 403/429 without a Retry-After is returned to the caller unchanged so it
// cannot loop forever.
func (g *GitLabClient) searchDo(req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := g.Doer.Do(req)
		if err != nil {
			return nil, fmt.Errorf("core: GitLab search request: %w", err)
		}
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}
		if resp.StatusCode == http.StatusForbidden {
			if msg := gitlabTierRefusal(resp); msg != "" {
				closeResponse(resp)
				return nil, fmt.Errorf("%w: %s", ErrGitLabSearchUnavailable, msg)
			}
		}
		if attempt >= gitlabRateLimitRetries {
			return resp, nil
		}
		delay, ok := httpclient.ParseRetryAfter(resp.Header.Get("Retry-After"), g.now())
		if !ok {
			delay, ok = httpclient.ParseRetryAfter(resp.Header.Get("X-RateLimit-Reset-After"), g.now())
		}
		if !ok {
			return resp, nil
		}
		closeResponse(resp)
		g.sleep(delay)
	}
}

// gitlabTierRefusal reads a bounded slice of a 403 body and reports whether it
// is GitLab's advanced-search/tier refusal, returning the message (trimmed) so
// the caller can surface it. Any other body, or a read error, returns "".
func gitlabTierRefusal(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, gitLabTierProbeBytes))
	if err != nil {
		return ""
	}
	msg := strings.TrimSpace(string(body))
	lower := strings.ToLower(msg)
	for _, marker := range gitlabSearchTierMarkers {
		if strings.Contains(lower, marker) {
			return msg
		}
	}
	return ""
}

// gitLabSnippetsURL builds the paginated public-snippets URL.
func gitLabSnippetsURL(base string, perPage, page int) (string, error) {
	u, err := url.Parse(base + "/api/v4/snippets/public")
	if err != nil {
		return "", fmt.Errorf("core: GitLab base URL: %w", err)
	}
	q := u.Query()
	q.Set("per_page", strconv.Itoa(perPage))
	q.Set("page", strconv.Itoa(page))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// gitLabBlobSearchURL builds the paginated blob-search URL.
func gitLabBlobSearchURL(base, query string, perPage, page int) (string, error) {
	u, err := url.Parse(base + gitLabSearchPath)
	if err != nil {
		return "", fmt.Errorf("core: GitLab base URL: %w", err)
	}
	q := u.Query()
	q.Set("scope", "blobs")
	q.Set("search", query)
	q.Set("per_page", strconv.Itoa(perPage))
	q.Set("page", strconv.Itoa(page))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// decodeGitLabSnippets parses one JSON page, bounded to maxGitLabResponseBytes.
func decodeGitLabSnippets(body io.Reader, base string) ([]GitLabSnippet, error) {
	var items []gitLabSnippetItem
	dec := json.NewDecoder(io.LimitReader(body, maxGitLabResponseBytes))
	if err := dec.Decode(&items); err != nil {
		return nil, fmt.Errorf("core: decoding GitLab snippets: %w", err)
	}
	out := make([]GitLabSnippet, 0, len(items))
	for _, it := range items {
		out = append(out, it.toSnippet(base))
	}
	return out, nil
}

// decodeGitLabBlobs parses one blob-search page, bounded to maxGitLabResponseBytes.
func decodeGitLabBlobs(body io.Reader) ([]GitLabBlob, error) {
	var items []gitLabBlobItem
	dec := json.NewDecoder(io.LimitReader(body, maxGitLabResponseBytes))
	if err := dec.Decode(&items); err != nil {
		return nil, fmt.Errorf("core: decoding GitLab blobs: %w", err)
	}
	out := make([]GitLabBlob, 0, len(items))
	for _, it := range items {
		out = append(out, GitLabBlob(it))
	}
	return out, nil
}

// gitLabSnippetItem mirrors the fields this client consumes from the API.
type gitLabSnippetItem struct {
	ID       int    `json:"id"`
	Title    string `json:"title"`
	FileName string `json:"file_name"`
	RawURL   string `json:"raw_url"`
	WebURL   string `json:"web_url"`
}

// toSnippet converts an API item, deriving the raw download URL when absent.
func (it gitLabSnippetItem) toSnippet(base string) GitLabSnippet {
	raw := it.RawURL
	if raw == "" && it.ID != 0 {
		raw = base + "/api/v4/snippets/" + strconv.Itoa(it.ID) + "/raw"
	}
	return GitLabSnippet{
		ID:       it.ID,
		Title:    it.Title,
		FileName: it.FileName,
		RawURL:   raw,
		WebURL:   it.WebURL,
	}
}

// gitLabBlobItem mirrors the blob-search fields this client consumes.
type gitLabBlobItem struct {
	Path      string `json:"path"`
	Filename  string `json:"filename"`
	Ref       string `json:"ref"`
	Data      string `json:"data"`
	StartLine int    `json:"startline"`
	ProjectID int    `json:"project_id"`
}

// baseURL returns the configured instance root with any trailing slash
// stripped, defaulting to gitlab.com.
func (g *GitLabClient) baseURL() string {
	base := strings.TrimRight(strings.TrimSpace(g.BaseURL), "/")
	if base == "" {
		base = defaultGitLabBaseURL
	}
	return base
}

// perPage returns the effective page size, clamped to 1..maxGitLabPerPage.
func (g *GitLabClient) perPage() int {
	if g.PerPage <= 0 || g.PerPage > maxGitLabPerPage {
		return defaultGitLabPerPage
	}
	return g.PerPage
}

// now returns the injected clock, or the real one.
func (g *GitLabClient) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// sleep waits using the injected seam, or really sleeps.
func (g *GitLabClient) sleep(d time.Duration) {
	if g.Sleep != nil {
		g.Sleep(d)
		return
	}
	time.Sleep(d)
}
