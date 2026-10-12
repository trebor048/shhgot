package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	defaultSearchBaseURL    = "https://api.github.com"
	defaultSearchRawBaseURL = "https://raw.githubusercontent.com"
	defaultSearchPerPage    = 100
	defaultSearchMaxPages   = 10
	defaultSearchMaxResults = 1000
	// maxSearchResponseBytes bounds a single search response page so a hostile
	// endpoint cannot stream unbounded JSON.
	maxSearchResponseBytes = 16 << 20

	// searchMaxPerMinute is GitHub's authenticated code-search request limit.
	searchMaxPerMinute = 30
	searchWindow       = time.Minute
	// searchResetRetries is how many times a 403/429 page carrying a usable
	// Retry-After is reparked before the status is surfaced as an error.
	searchResetRetries = 2
)

// CodeSearchHit is one file returned by GitHub code search.
type CodeSearchHit struct {
	Repo     string
	Path     string
	HTMLURL  string
	RawURL   string
	SHA      string
	Language string
	Stars    int
}

// CodeSearchOptions configures SearchCode. Zero values select the documented
// defaults: PerPage 100, MaxPages 10, MaxResults 1000 and the public GitHub
// API.
type CodeSearchOptions struct {
	PerPage    int
	MaxPages   int
	MaxResults int
	SleepMS    int
	Token      string
	BaseURL    string
}

// searchNow and searchSleep are the timing seams for the search rate limiter.
// They are variables so tests can install a clock that never blocks.
var (
	searchNow   = time.Now
	searchSleep = time.Sleep
)

// searchGate enforces a fixed 30-requests-per-minute window across every
// SearchCode call. It is safe for concurrent use.
type searchGate struct {
	mu          sync.Mutex
	windowStart time.Time
	count       int
}

var searchLimiter searchGate

// wait blocks until another search request may be issued this minute.
func (g *searchGate) wait() {
	g.mu.Lock()
	defer g.mu.Unlock()

	for {
		now := searchNow()
		if g.windowStart.IsZero() || now.Sub(g.windowStart) >= searchWindow {
			g.windowStart = now
			g.count = 0
		}
		if g.count < searchMaxPerMinute {
			g.count++
			return
		}
		wake := g.windowStart.Add(searchWindow)
		delay := wake.Sub(now)
		// Release the lock while sleeping so concurrent callers are not blocked
		// for the whole delay.
		g.mu.Unlock()
		if delay > 0 {
			searchSleep(delay)
		}
		g.mu.Lock()
		// After waking, reset the window so the next iteration can issue the
		// request that was waited for.
		g.windowStart = wake
		g.count = 0
	}
}

// SearchCode runs query against GET {base}/search/code, paginating up to
// MaxPages pages and MaxResults hits (hard-capped at 1000). Requests are paced
// to at most 30 per minute; 403/429 pages are parked using Retry-After and
// retried. Partial hits collected before an error are returned alongside it.
func SearchCode(doer Doer, opts CodeSearchOptions, query string) ([]CodeSearchHit, error) {
	if doer == nil {
		return nil, errors.New("core: SearchCode: nil doer")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("core: SearchCode: empty query")
	}

	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		base = defaultSearchBaseURL
	}
	perPage := opts.PerPage
	if perPage <= 0 || perPage > 100 {
		perPage = defaultSearchPerPage
	}
	maxPages := opts.MaxPages
	if maxPages <= 0 {
		maxPages = defaultSearchMaxPages
	}
	maxResults := opts.MaxResults
	if maxResults <= 0 || maxResults > defaultSearchMaxResults {
		maxResults = defaultSearchMaxResults
	}

	var hits []CodeSearchHit
	for page := 1; page <= maxPages; page++ {
		resp, err := searchPage(doer, base, opts.Token, query, perPage, page)
		if err != nil {
			return hits, err
		}
		if resp.StatusCode != http.StatusOK {
			status := resp.StatusCode
			closeResponse(resp)
			return hits, fmt.Errorf("core: code search: server status %d", status)
		}
		decoded, err := decodeSearchPage(resp.Body)
		closeResponse(resp)
		if err != nil {
			return hits, err
		}

		for _, item := range decoded.Items {
			hits = append(hits, item.toHit())
		}
		if len(hits) >= maxResults {
			hits = hits[:maxResults]
			break
		}
		// A short page is the last page; an incomplete result set is not
		// trustworthy enough to page deeper.
		if len(decoded.Items) < perPage || decoded.IncompleteResults {
			break
		}
		if opts.SleepMS > 0 && page < maxPages {
			searchSleep(time.Duration(opts.SleepMS) * time.Millisecond)
		}
	}
	return hits, nil
}

// searchPage performs one GET for page, parking and retrying a bounded number
// of times on a 403/429 rate-limit answer.
func searchPage(doer Doer, base, token, query string, perPage, page int) (*http.Response, error) {
	rawURL, err := searchPageURL(base, query, perPage, page)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("core: search request: %w", err)
	}
	req.Header.Set("User-Agent", "shhgit2")
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	for attempt := 0; ; attempt++ {
		searchLimiter.wait()
		resp, err := doer.Do(req)
		if err != nil {
			return nil, fmt.Errorf("core: search request: %w", err)
		}
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}
		if attempt >= searchResetRetries {
			return resp, nil
		}
		// Park and retry only a genuine rate-limit answer. A 403 without any
		// Retry-After is an auth/permissions failure (bad token): returning it
		// surfaces the problem at once instead of stalling ~2 minutes on the
		// blind 1-minute fallback, matching every sibling client.
		delay, ok := retryAfterDelayOK(resp, searchNow())
		if !ok {
			return resp, nil
		}
		closeResponse(resp)
		searchSleep(delay)
	}
}

// searchPageURL builds the paginated code-search URL.
func searchPageURL(base, query string, perPage, page int) (string, error) {
	u, err := url.Parse(base + "/search/code")
	if err != nil {
		return "", fmt.Errorf("core: search base URL: %w", err)
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("per_page", strconv.Itoa(perPage))
	q.Set("page", strconv.Itoa(page))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// decodeSearchPage parses one JSON page, bounded to maxSearchResponseBytes.
func decodeSearchPage(body io.Reader) (*codeSearchResponse, error) {
	var out codeSearchResponse
	dec := json.NewDecoder(io.LimitReader(body, maxSearchResponseBytes))
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("core: decoding search response: %w", err)
	}
	return &out, nil
}

// codeSearchResponse mirrors the fields SearchCode consumes from the API.
type codeSearchResponse struct {
	TotalCount        int              `json:"total_count"`
	IncompleteResults bool             `json:"incomplete_results"`
	Items             []codeSearchItem `json:"items"`
}

type codeSearchItem struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	SHA        string `json:"sha"`
	HTMLURL    string `json:"html_url"`
	RawURL     string `json:"raw_url"`
	Language   string `json:"language"`
	Repository struct {
		FullName        string `json:"full_name"`
		DefaultBranch   string `json:"default_branch"`
		Language        string `json:"language"`
		StargazersCount int    `json:"stargazers_count"`
	} `json:"repository"`
}

// toHit converts an API item to the public hit shape, deriving a raw download
// URL when the API supplied none.
func (it codeSearchItem) toHit() CodeSearchHit {
	repo := it.Repository.FullName
	raw := it.RawURL
	if raw == "" && repo != "" && it.Path != "" {
		branch := it.Repository.DefaultBranch
		if branch == "" {
			branch = "HEAD"
		}
		raw = defaultSearchRawBaseURL + "/" + repo + "/" + branch + "/" + escapePath(it.Path)
	}
	lang := it.Language
	if lang == "" {
		lang = it.Repository.Language
	}
	return CodeSearchHit{
		Repo:     repo,
		Path:     it.Path,
		HTMLURL:  it.HTMLURL,
		RawURL:   raw,
		SHA:      it.SHA,
		Language: lang,
		Stars:    it.Repository.StargazersCount,
	}
}

// escapePath URL-escapes each path segment while preserving separators.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

// genericDorkAnchors are too common to be high-signal and are skipped.
var genericDorkAnchors = map[string]bool{
	"api_key":  true,
	"apikey":   true,
	"key":      true,
	"token":    true,
	"secret":   true,
	"password": true,
}

// dorkSuffix narrows a GitHub code-search query away from test fixtures.
const dorkSuffix = " in:file NOT test NOT example NOT sample NOT mock"

// SignatureAnchors derives code-search anchors from a loaded signature set:
// the mandatory literals of every contents pattern, exactly the literals the
// scan prefilter (requiredLiterals) proves a match must contain. Filename and
// path rules are skipped - they name files, not code-search terms - as are
// patterns with no derivable literal. Generic and too-short anchors are left to
// BuildDorks, which is the ranking and filtering stage.
func SignatureAnchors(signatures []Signature) []string {
	var anchors []string
	for _, sig := range signatures {
		if sig == nil || sig.GetPart() != PartContents {
			continue
		}
		re := sig.GetRegex()
		if re == nil {
			continue
		}
		lits, _ := requiredLiterals(re.String())
		for _, l := range lits {
			anchors = append(anchors, string(l))
		}
	}
	return anchors
}

// NewMatchFileFromContents builds a MatchFile whose contents are supplied in
// memory rather than read from disk. The matching engine loads a MatchFile's
// body lazily through GetContents, which the unexported loaded flag short
// circuits; this constructor sets it so the same signatures, prefilters and
// suppression rules apply to a downloaded or otherwise in-memory body. It is
// the seam that lets the code-search source reuse cmd's on-disk scan engine.
func NewMatchFileFromContents(path string, contents []byte) MatchFile {
	path = filepath.ToSlash(path)
	_, filename := filepath.Split(path)
	return MatchFile{
		Path:      path,
		Filename:  filename,
		Extension: filepath.Ext(path),
		Contents:  contents,
		Size:      int64(len(contents)),
		loaded:    true,
	}
}

// BuildDorks turns signature anchors into ranked GitHub code-search queries,
// skipping empty, too-short and generic anchors and de-duplicating the rest.
func BuildDorks(signatureAnchors []string) []string {
	seen := make(map[string]bool, len(signatureAnchors))
	out := make([]string, 0, len(signatureAnchors))
	for _, anchor := range signatureAnchors {
		anchor = strings.TrimSpace(anchor)
		if utf8.RuneCountInString(anchor) < 4 || strings.ContainsRune(anchor, '"') {
			continue
		}
		if genericDorkAnchors[strings.ToLower(anchor)] || seen[anchor] {
			continue
		}
		seen[anchor] = true
		out = append(out, `"`+anchor+`"`+dorkSuffix)
	}
	return out
}
