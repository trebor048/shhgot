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
)

const (
	// defaultBitbucketBaseURL is the public Bitbucket Cloud API root used when
	// the client is built without an explicit base.
	defaultBitbucketBaseURL = "https://api.bitbucket.org"
	// bitbucketPageLen is the largest page size the 2.0 API accepts; the client
	// always requests it to minimise round trips.
	bitbucketPageLen = 100
	// maxBitbucketResponseBytes bounds a single listing page so a hostile
	// endpoint cannot stream unbounded JSON.
	maxBitbucketResponseBytes = 8 << 20
	// maxBitbucketFileBytes bounds a downloaded file body to 2 MiB.
	maxBitbucketFileBytes = 2 << 20
	// bitbucketRateLimitRetries is how many times a 403/429 page is parked
	// using an advertised Retry-After and retried before the status is
	// surfaced as an error.
	bitbucketRateLimitRetries = 2
)

// bitbucketCanonicalHosts are the fixed hosts a Bitbucket raw download may use,
// in addition to this client's BaseURL host.
var bitbucketCanonicalHosts = []string{"api.bitbucket.org", "bitbucket.org"}

// BitbucketRepo is one repository returned by the Bitbucket API.
type BitbucketRepo struct {
	FullName   string
	Slug       string
	MainBranch string
	HTMLURL    string
	Private    bool
}

// BitbucketFile is one source file listed in a repository.
type BitbucketFile struct {
	Path   string
	Size   int
	RawURL string
}

// BitbucketClient reads the Bitbucket Cloud 2.0 API without authentication.
// Bitbucket has no global public-repository listing or code search, so the
// caller supplies the workspaces to walk. Every network and timing seam is
// injectable so tests never touch the network or block.
type BitbucketClient struct {
	// Doer performs the HTTP request. Required. It has the same method set as
	// the package-level Doer and is satisfied by *http.Client.
	Doer interface {
		Do(*http.Request) (*http.Response, error)
	}
	// BaseURL is the API root; empty uses https://api.bitbucket.org.
	BaseURL string
	// Sleep is the injected sleep seam; nil uses time.Sleep.
	Sleep func(time.Duration)
	// Now is the injected clock used to resolve an HTTP-date Retry-After; nil
	// uses time.Now.
	Now func() time.Time
}

// NewBitbucketClient builds a client around doer. An empty base selects the
// public Bitbucket Cloud API root.
func NewBitbucketClient(doer Doer, base string) *BitbucketClient {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = defaultBitbucketBaseURL
	}
	return &BitbucketClient{Doer: doer, BaseURL: base}
}

// ListRepos walks GET {base}/2.0/repositories/{workspace} and returns the
// non-private repositories, following the "next" links. A maxRepos greater
// than zero stops the walk early and truncates the result; a non-200 page
// returns an error.
func (c *BitbucketClient) ListRepos(workspace string, maxRepos int) ([]BitbucketRepo, error) {
	if c == nil || c.Doer == nil {
		return nil, errors.New("core: BitbucketClient.Doer is nil")
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil, errors.New("core: Bitbucket workspace is empty")
	}

	next, err := repositoriesURL(c.baseURL(), workspace)
	if err != nil {
		return nil, err
	}

	var out []BitbucketRepo
	seen := map[string]bool{}
	for next != "" {
		if seen[next] {
			break
		}
		seen[next] = true

		resp, err := c.get(next)
		if err != nil {
			return out, err
		}
		if resp.StatusCode != http.StatusOK {
			status := resp.StatusCode
			closeResponse(resp)
			return out, fmt.Errorf("core: Bitbucket repositories: server status %d", status)
		}
		page, err := decodeBitbucketPage[bitbucketRepoPage](resp.Body, "repositories")
		closeResponse(resp)
		if err != nil {
			return out, err
		}

		for _, it := range page.Values {
			if it.IsPrivate {
				continue
			}
			out = append(out, it.toRepo())
			if maxRepos > 0 && len(out) >= maxRepos {
				return out, nil
			}
		}
		next = resolveBitbucketNext(page.Next, c.baseURL())
	}
	return out, nil
}

// ListFiles walks the recursive source listing of one repository and returns
// the commit_file entries, following the "next" links. A maxFiles greater than
// zero stops the walk early and truncates the result; a non-200 page returns
// an error.
func (c *BitbucketClient) ListFiles(workspace, slug, branch string, maxFiles int) ([]BitbucketFile, error) {
	if c == nil || c.Doer == nil {
		return nil, errors.New("core: BitbucketClient.Doer is nil")
	}
	workspace = strings.TrimSpace(workspace)
	slug = strings.TrimSpace(slug)
	branch = strings.TrimSpace(branch)
	if workspace == "" || slug == "" {
		return nil, errors.New("core: Bitbucket workspace and repository slug are required")
	}

	next, err := filesURL(c.baseURL(), workspace, slug, branch)
	if err != nil {
		return nil, err
	}

	var out []BitbucketFile
	seen := map[string]bool{}
	for next != "" {
		if seen[next] {
			break
		}
		seen[next] = true

		resp, err := c.get(next)
		if err != nil {
			return out, err
		}
		if resp.StatusCode != http.StatusOK {
			status := resp.StatusCode
			closeResponse(resp)
			return out, fmt.Errorf("core: Bitbucket files: server status %d", status)
		}
		page, err := decodeBitbucketPage[bitbucketFilePage](resp.Body, "files")
		closeResponse(resp)
		if err != nil {
			return out, err
		}

		for _, it := range page.Values {
			if it.Type != bitbucketCommitFile {
				continue
			}
			out = append(out, BitbucketFile{
				Path:   it.Path,
				Size:   it.Size,
				RawURL: it.Links.Self.Href,
			})
			if maxFiles > 0 && len(out) >= maxFiles {
				return out, nil
			}
		}
		next = resolveBitbucketNext(page.Next, c.baseURL())
	}
	return out, nil
}

// FileContent downloads the raw body of one file through the shared Fetcher,
// which enforces the host allowlist and the size cap. The host must be
// api.bitbucket.org, bitbucket.org, or this client's BaseURL host; anything
// else is refused before a request is issued.
func (c *BitbucketClient) FileContent(rawURL string) ([]byte, error) {
	if c == nil || c.Doer == nil {
		return nil, errors.New("core: BitbucketClient.Doer is nil")
	}
	f := &Fetcher{
		Doer:         c.Doer,
		AllowedHosts: c.allowedHosts(),
		MaxFileSize:  maxBitbucketFileBytes,
		MaxRetries:   bitbucketRateLimitRetries,
		Now:          c.Now,
		Sleep:        c.Sleep,
	}
	return f.Fetch(rawURL)
}

// get performs one listing GET, parking and retrying a bounded number of times
// on a 403/429 rate-limit answer.
func (c *BitbucketClient) get(rawURL string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("core: Bitbucket request: %w", err)
	}
	req.Header.Set("User-Agent", "shhgit2")
	req.Header.Set("Accept", "application/json")

	for attempt := 0; ; attempt++ {
		resp, err := c.Doer.Do(req)
		if err != nil {
			return nil, fmt.Errorf("core: Bitbucket request: %w", err)
		}
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}
		if attempt >= bitbucketRateLimitRetries {
			return resp, nil
		}
		delay, ok := retryAfterDelayOK(resp, c.now())
		if !ok {
			return resp, nil
		}
		closeResponse(resp)
		c.sleep(delay)
	}
}

// repositoriesURL builds the paginated repository-listing URL.
func repositoriesURL(base, workspace string) (string, error) {
	u, err := url.Parse(base + "/2.0/repositories/" + url.PathEscape(workspace))
	if err != nil {
		return "", fmt.Errorf("core: Bitbucket base URL: %w", err)
	}
	q := u.Query()
	q.Set("pagelen", strconv.Itoa(bitbucketPageLen))
	q.Set("sort", "-updated_on")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// filesURL builds the recursive source-listing URL. An empty branch lists the
// repository's default branch root.
func filesURL(base, workspace, slug, branch string) (string, error) {
	raw := base + "/2.0/repositories/" + url.PathEscape(workspace) + "/" + url.PathEscape(slug) + "/src/"
	if branch != "" {
		raw += url.PathEscape(branch) + "/"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("core: Bitbucket base URL: %w", err)
	}
	q := u.Query()
	q.Set("pagelen", strconv.Itoa(bitbucketPageLen))
	q.Set("recursive", "true")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// bitbucketCommitFile is the entry type of a regular file in a source listing;
// directories carry "commit_directory" and are skipped.
const bitbucketCommitFile = "commit_file"

// bitbucketRepoPage is one JSON page of the repository listing.
type bitbucketRepoPage struct {
	Values []bitbucketRepoItem `json:"values"`
	Next   string              `json:"next"`
}

// bitbucketFilePage is one JSON page of the recursive source listing.
type bitbucketFilePage struct {
	Values []bitbucketFileItem `json:"values"`
	Next   string              `json:"next"`
}

// bitbucketRepoItem mirrors the fields this client consumes from a repository.
type bitbucketRepoItem struct {
	FullName  string `json:"full_name"`
	Slug      string `json:"slug"`
	IsPrivate bool   `json:"is_private"`
	// MainBranch is a pointer because the API can return null.
	MainBranch *struct {
		Name string `json:"name"`
	} `json:"mainbranch"`
	Links struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
}

// toRepo converts an API item into a BitbucketRepo.
func (it bitbucketRepoItem) toRepo() BitbucketRepo {
	r := BitbucketRepo{
		FullName: it.FullName,
		Slug:     it.Slug,
		HTMLURL:  it.Links.HTML.Href,
		Private:  it.IsPrivate,
	}
	if it.MainBranch != nil {
		r.MainBranch = it.MainBranch.Name
	}
	return r
}

// bitbucketFileItem mirrors the fields this client consumes from a source
// listing entry. links.self.href returns the raw file body.
type bitbucketFileItem struct {
	Path  string `json:"path"`
	Type  string `json:"type"`
	Size  int    `json:"size"`
	Links struct {
		Self struct {
			Href string `json:"href"`
		} `json:"self"`
	} `json:"links"`
}

// decodeBitbucketPage parses one JSON listing page, bounded to
// maxBitbucketResponseBytes. The page type is a type parameter so the shared
// decode logic serves both the repository and file listings.
func decodeBitbucketPage[T any](body io.Reader, what string) (T, error) {
	var page T
	dec := json.NewDecoder(io.LimitReader(body, maxBitbucketResponseBytes))
	if err := dec.Decode(&page); err != nil {
		var zero T
		return zero, fmt.Errorf("core: decoding Bitbucket %s: %w", what, err)
	}
	return page, nil
}

// resolveBitbucketNext normalises a "next" link. It returns "" for an absent,
// malformed, non-http(s) or host-less link so pagination ends cleanly rather
// than issuing a request to garbage. A relative link is resolved against base.
func resolveBitbucketNext(next, base string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return ""
	}
	u, err := url.Parse(next)
	if err != nil {
		return ""
	}
	if !u.IsAbs() {
		b, err := url.Parse(base)
		if err != nil {
			return ""
		}
		u = b.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	if u.Host == "" {
		return ""
	}
	// The "next" link comes from the API response, so a hostile or compromised
	// server could point pagination at an internal host (SSRF). Only follow a
	// link whose host is a canonical Bitbucket host or the configured base host;
	// relative links were already resolved against base above.
	host := canonicalHost(u.Hostname())
	if !hostAllowed(host, bitbucketCanonicalHosts) {
		baseHost := ""
		if b, err := url.Parse(base); err == nil {
			baseHost = canonicalHost(b.Hostname())
		}
		if baseHost == "" || host != baseHost {
			return ""
		}
	}
	// Reject a non-default port unless it matches the configured base, so an
	// allowlisted host cannot be steered to an arbitrary service on that host.
	defPort := "443"
	if u.Scheme == "http" {
		defPort = "80"
	}
	if p := u.Port(); p != "" && p != defPort {
		basePort := ""
		if b, err := url.Parse(base); err == nil {
			basePort = b.Port()
		}
		if p != basePort {
			return ""
		}
	}
	return u.String()
}

// baseURL returns the configured API root, or the public default.
func (c *BitbucketClient) baseURL() string {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return defaultBitbucketBaseURL
	}
	return base
}

// allowedHosts returns the canonical Bitbucket hosts plus the BaseURL host,
// de-duplicated. An explicit base port is preserved (host:port entry) so
// hostPortAllowed can scope custom-port instances without opening every port.
func (c *BitbucketClient) allowedHosts() []string {
	hosts := append([]string(nil), bitbucketCanonicalHosts...)
	if u, err := url.Parse(c.baseURL()); err == nil {
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

// now returns the injected clock, or the real one.
func (c *BitbucketClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// sleep waits using the injected seam, or really sleeps.
func (c *BitbucketClient) sleep(d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(d)
		return
	}
	time.Sleep(d)
}
