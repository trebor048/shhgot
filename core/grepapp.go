package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/trebor048/shhgot/core/httpclient"
)

const (
	defaultGrepAppBaseURL   = "https://grep.app"
	grepAppSearchPath       = "/api/search"
	maxGrepAppResponseBytes = 16 << 20
	grepAppRateLimitRetries = 2
	// maxGrepAppPages bounds the pagination walk: without it a server that
	// keeps returning non-empty pages loops forever and grows the result
	// without limit when the caller passes maxResults <= 0.
	maxGrepAppPages = 50
)

var (
	grepAppLineRe = regexp.MustCompile(`(?i)data-line\s*=\s*["'](\d+)["']`)
	grepAppPreRe  = regexp.MustCompile(`(?is)<pre[^>]*>(.*?)</pre>`)
	grepAppTagRe  = regexp.MustCompile(`(?s)<[^>]*>`)
)

type GrepAppHit struct {
	Repo   string
	Branch string
	Path   string
	Code   string
	Lines  []int
}

type GrepAppClient struct {
	Doer interface {
		Do(*http.Request) (*http.Response, error)
	}
	BaseURL string
	Sleep   func(time.Duration)
	Now     func() time.Time
}

func NewGrepAppClient(doer Doer, base string) *GrepAppClient {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = defaultGrepAppBaseURL
	}
	return &GrepAppClient{
		Doer:    doer,
		BaseURL: base,
		Sleep:   time.Sleep,
		Now:     time.Now,
	}
}

func (c *GrepAppClient) Search(query string, maxResults int) ([]GrepAppHit, error) {
	if c == nil || c.Doer == nil {
		return nil, errors.New("core: GrepAppClient.Doer is nil")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("core: grep.app search: empty query")
	}

	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = defaultGrepAppBaseURL
	}

	var out []GrepAppHit
	for page := 1; page <= maxGrepAppPages; page++ {
		hits, err := c.searchPage(base, query, page)
		if err != nil {
			return out, err
		}
		if len(hits) == 0 {
			break
		}
		out = append(out, hits...)
		if maxResults > 0 && len(out) >= maxResults {
			out = out[:maxResults]
			break
		}
	}
	return out, nil
}

func (c *GrepAppClient) searchPage(base, query string, page int) ([]GrepAppHit, error) {
	rawURL, err := grepAppSearchURL(base, query, page)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("core: grep.app request: %w", err)
	}
	req.Header.Set("User-Agent", "shhgit2")
	req.Header.Set("Accept", "application/json")

	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		status := resp.StatusCode
		closeResponse(resp)
		return nil, fmt.Errorf("core: grep.app search: server status %d", status)
	}
	defer closeResponse(resp)

	var payload grepAppResponse
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxGrepAppResponseBytes))
	if err := dec.Decode(&payload); err != nil {
		return nil, fmt.Errorf("core: grep.app: decoding response: %w", err)
	}

	out := make([]GrepAppHit, 0, len(payload.Hits.Hits))
	for _, wire := range payload.Hits.Hits {
		repo := strings.TrimSpace(wire.Repo)
		if repo == "" || strings.TrimSpace(wire.Path) == "" {
			continue
		}
		code, lines := extractGrepAppSnippet(wire.Content.Snippet)
		out = append(out, GrepAppHit{
			Repo:   repo,
			Branch: wire.Branch,
			Path:   wire.Path,
			Code:   code,
			Lines:  lines,
		})
	}
	return out, nil
}

func (c *GrepAppClient) do(req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := c.Doer.Do(req)
		if err != nil {
			return nil, fmt.Errorf("core: grep.app request: %w", err)
		}
		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusForbidden {
			return resp, nil
		}
		if attempt >= grepAppRateLimitRetries {
			return resp, nil
		}
		delay, ok := grepAppRetryAfter(resp, c.now())
		if !ok {
			return resp, nil
		}
		closeResponse(resp)
		c.sleep(delay)
	}
}

func grepAppRetryAfter(resp *http.Response, now time.Time) (time.Duration, bool) {
	if resp != nil {
		if d, ok := httpclient.ParseRetryAfter(resp.Header.Get("Retry-After"), now); ok {
			return d, true
		}
	}
	return 0, false
}

func grepAppSearchURL(base, query string, page int) (string, error) {
	u, err := url.Parse(strings.TrimRight(base, "/") + grepAppSearchPath)
	if err != nil {
		return "", fmt.Errorf("core: grep.app base URL: %w", err)
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("page", strconv.Itoa(page))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (c *GrepAppClient) BlobURL(h GrepAppHit) string {
	repo := strings.Trim(strings.TrimSpace(h.Repo), "/")
	branch := strings.Trim(strings.TrimSpace(h.Branch), "/")
	path := strings.TrimLeft(strings.TrimSpace(h.Path), "/")
	return "https://github.com/" + repo + "/blob/" + branch + "/" + path
}

func extractGrepAppSnippet(snippet string) (string, []int) {
	if snippet == "" {
		return "", nil
	}

	matches := grepAppLineRe.FindAllStringSubmatchIndex(snippet, -1)
	if len(matches) == 0 {
		text := grepAppPlainText(snippet)
		if text == "" {
			return "", nil
		}
		return text, nil
	}

	codes := make([]string, 0, len(matches))
	lines := make([]int, 0, len(matches))
	for i, m := range matches {
		lineNo, err := strconv.Atoi(snippet[m[2]:m[3]])
		if err != nil {
			continue
		}
		end := len(snippet)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		codes = append(codes, grepAppPlainText(snippet[m[1]:end]))
		lines = append(lines, lineNo)
	}
	if len(codes) == 0 {
		return "", nil
	}
	return strings.Join(codes, "\n"), lines
}

func grepAppPlainText(segment string) string {
	if m := grepAppPreRe.FindStringSubmatch(segment); m != nil {
		segment = m[1]
	}
	segment = grepAppTagRe.ReplaceAllString(segment, "")
	segment = html.UnescapeString(segment)
	return strings.TrimSpace(segment)
}

type grepAppResponse struct {
	Hits grepAppHits `json:"hits"`
}

type grepAppHits struct {
	Total int              `json:"total"`
	Hits  []grepAppWireHit `json:"hits"`
}

type grepAppWireHit struct {
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	Path    string `json:"path"`
	Content struct {
		Snippet string `json:"snippet"`
	} `json:"content"`
}

func (c *GrepAppClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *GrepAppClient) sleep(d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(d)
		return
	}
	time.Sleep(d)
}
