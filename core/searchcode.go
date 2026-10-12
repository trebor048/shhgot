package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/trebor048/shhgot/core/httpclient"
)

const (
	// defaultSearchCodeBaseURL is the public searchcode.com host.
	defaultSearchCodeBaseURL = "https://searchcode.com"
	// defaultSearchCodePerPage is the page size the public API documents.
	defaultSearchCodePerPage = 100
	// searchCodePageDelay paces SearchKeyword pages so the unauthenticated
	// endpoint is not hammered.
	searchCodePageDelay = time.Second
	// searchCodeResetRetries bounds how many times a 429/403 answer is parked
	// and retried before the status is surfaced as an error.
	searchCodeResetRetries = 2
)

// SearchCodeResult is one file returned by searchcode.com's code search.
type SearchCodeResult struct {
	ID       string
	Filename string
	Repo     string
	URL      string
	Lines    map[string]string
}

// Content joins the result's line texts in ascending numeric-line order. The
// API returns the scannable body as a map of line number to text, so ordering
// has to be restored here before the content is handed to the scan engine.
// Non-numeric line keys are ignored.
func (r SearchCodeResult) Content() string {
	if len(r.Lines) == 0 {
		return ""
	}
	numbers := make([]int, 0, len(r.Lines))
	text := make(map[int]string, len(r.Lines))
	for key, value := range r.Lines {
		n, err := strconv.Atoi(strings.TrimSpace(key))
		if err != nil {
			continue
		}
		numbers = append(numbers, n)
		text[n] = value
	}
	if len(numbers) == 0 {
		return ""
	}
	sort.Ints(numbers)
	var b strings.Builder
	for i, n := range numbers {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text[n])
	}
	return b.String()
}

// SearchCodeClient talks to the searchcode.com code-search API. The Doer,
// Sleep and Now seams are injectable so tests run without real network or
// wall-clock waits.
type SearchCodeClient struct {
	Doer interface {
		Do(*http.Request) (*http.Response, error)
	}
	BaseURL string
	Sleep   func(time.Duration)
	Now     func() time.Time
	PerPage int
}

// NewSearchCodeClient builds a client bound to base (default
// https://searchcode.com) and per_page 100.
func NewSearchCodeClient(doer Doer, base string) *SearchCodeClient {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = defaultSearchCodeBaseURL
	}
	return &SearchCodeClient{
		Doer:    doer,
		BaseURL: base,
		Sleep:   time.Sleep,
		Now:     time.Now,
		PerPage: defaultSearchCodePerPage,
	}
}

// Search performs one GET {base}/api/codesearch_I/ for keyword page. A non-200
// answer is an error; results whose id is empty are dropped. A 429/403 answer
// that advertises Retry-After is parked (bounded by searchCodeResetRetries)
// and retried.
func (c *SearchCodeClient) Search(keyword string, page int) ([]SearchCodeResult, error) {
	if c == nil || c.Doer == nil {
		return nil, errors.New("core: searchcode: nil doer")
	}
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return nil, errors.New("core: searchcode: empty keyword")
	}
	if page < 0 {
		page = 0
	}

	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = defaultSearchCodeBaseURL
	}
	perPage := c.PerPage
	if perPage <= 0 {
		perPage = defaultSearchCodePerPage
	}

	rawURL, err := searchCodeURL(base, keyword, page, perPage)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("core: searchcode: request: %w", err)
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
		return nil, fmt.Errorf("core: searchcode: server status %d", status)
	}
	defer closeResponse(resp)

	var payload searchCodeResponse
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxSearchResponseBytes))
	if err := dec.Decode(&payload); err != nil {
		return nil, fmt.Errorf("core: searchcode: decoding response: %w", err)
	}

	out := make([]SearchCodeResult, 0, len(payload.Results))
	for _, wire := range payload.Results {
		id := wire.resultID()
		if id == "" {
			continue
		}
		out = append(out, SearchCodeResult{
			ID:       id,
			Filename: wire.Filename,
			Repo:     wire.Repo,
			URL:      wire.URL,
			Lines:    wire.Lines,
		})
	}
	return out, nil
}

// SearchKeyword walks pages 0..maxPages-1, sleeping searchCodePageDelay between
// requests, stopping at the first empty page and de-duplicating results by id.
func (c *SearchCodeClient) SearchKeyword(keyword string, maxPages int) ([]SearchCodeResult, error) {
	if maxPages <= 0 {
		return nil, nil
	}
	var out []SearchCodeResult
	seen := make(map[string]bool)
	for page := 0; page < maxPages; page++ {
		if page > 0 {
			c.sleep(searchCodePageDelay)
		}
		results, err := c.Search(keyword, page)
		if err != nil {
			return out, err
		}
		if len(results) == 0 {
			break
		}
		for _, result := range results {
			if seen[result.ID] {
				continue
			}
			seen[result.ID] = true
			out = append(out, result)
		}
	}
	return out, nil
}

// do issues req, parking and retrying a bounded number of 429/403 answers that
// carry a usable Retry-After.
func (c *SearchCodeClient) do(req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := c.Doer.Do(req)
		if err != nil {
			return nil, fmt.Errorf("core: searchcode: request: %w", err)
		}
		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusForbidden {
			return resp, nil
		}
		if attempt >= searchCodeResetRetries {
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
func (c *SearchCodeClient) sleep(d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(d)
		return
	}
	time.Sleep(d)
}

// now reports the current time using the injected clock when present.
func (c *SearchCodeClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// searchCodeURL builds the paginated codesearch_I URL.
func searchCodeURL(base, keyword string, page, perPage int) (string, error) {
	u, err := url.Parse(base + "/api/codesearch_I/")
	if err != nil {
		return "", fmt.Errorf("core: searchcode: base URL: %w", err)
	}
	q := u.Query()
	q.Set("q", keyword)
	q.Set("p", strconv.Itoa(page))
	q.Set("per_page", strconv.Itoa(perPage))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// searchCodeResponse is the envelope the codesearch_I endpoint returns.
type searchCodeResponse struct {
	Results []searchCodeWireResult `json:"results"`
}

// searchCodeWireResult decodes one result. The id arrives as a JSON number but
// is kept raw so the public shape can expose it as a string.
type searchCodeWireResult struct {
	ID       json.RawMessage   `json:"id"`
	Filename string            `json:"filename"`
	Repo     string            `json:"repo"`
	URL      string            `json:"url"`
	Lines    map[string]string `json:"lines"`
}

// resultID normalises a raw JSON id (number or string) to a plain string.
func (w searchCodeWireResult) resultID() string {
	raw := strings.TrimSpace(string(w.ID))
	if raw == "" || raw == "null" {
		return ""
	}
	if unquoted, err := strconv.Unquote(raw); err == nil {
		return strings.TrimSpace(unquoted)
	}
	return raw
}

// SearchCodeKeywords are the reference query terms the discovery source
// iterates over.
var SearchCodeKeywords = []string{
	"OPENAI_API_KEY",
	"sk-proj-",
	"ANTHROPIC_API_KEY",
	"sk-ant-api",
	"GEMINI_API_KEY",
	"AIzaSy",
	"HUGGINGFACE_TOKEN",
	"hf_",
	"GROQ_API_KEY",
	"gsk_",
	"DEEPSEEK_API_KEY",
	"AWS_SECRET_ACCESS_KEY",
	"STRIPE_SECRET_KEY",
	"sk_live_",
}
