package core

import (
	"bufio"
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
	// defaultSourcegraphBaseURL is the public Sourcegraph instance used when the
	// client is built without an explicit base.
	defaultSourcegraphBaseURL = "https://sourcegraph.com"
	// sourcegraphStreamPath is the documented public stream-search endpoint.
	sourcegraphStreamPath = "/.api/search/stream"
	// sourcegraphAPIVersion is the documented v parameter value.
	sourcegraphAPIVersion = "V3"
	// sourcegraphSearchType is the documented t parameter value.
	sourcegraphSearchType = "standard"
	// sourcegraphEventMatches is the SSE event whose data carries content
	// matches; every other event type is ignored.
	sourcegraphEventMatches = "matches"
	// defaultSourcegraphDisplay is the fallback display hint sent when no result
	// cap is supplied.
	defaultSourcegraphDisplay = 30
	// sourcegraphRateLimitRetries is how many times a 403/429 answer is parked
	// using an advertised Retry-After and retried before the status is surfaced.
	sourcegraphRateLimitRetries = 2
	// maxSourcegraphFrameBytes bounds a single SSE line so a hostile endpoint
	// cannot force unbounded buffering while the stream is scanned.
	maxSourcegraphFrameBytes = 8 << 20
)

// SourcegraphMatch is one matched line of one file returned by stream search.
type SourcegraphMatch struct {
	Repository string
	Path       string
	Preview    string
	Line       int
}

// SourcegraphClient queries Sourcegraph's public stream-search API. Anonymous
// access works but is rate-limited; a Token raises the limit. Every network and
// timing seam is injectable so tests never touch the network or block.
type SourcegraphClient struct {
	// Doer performs the HTTP request. Required. It has the same method set as
	// the package-level Doer and is satisfied by *http.Client.
	Doer interface {
		Do(*http.Request) (*http.Response, error)
	}
	// BaseURL is the Sourcegraph root; empty uses https://sourcegraph.com.
	BaseURL string
	// Token, when set, is sent as "Authorization: token <Token>".
	Token string
	// Sleep is the injected sleep seam; nil uses time.Sleep.
	Sleep func(time.Duration)
	// Now is the injected clock used to resolve an HTTP-date Retry-After; nil
	// uses time.Now.
	Now func() time.Time
}

// NewSourcegraphClient builds a client. An empty base selects the public
// https://sourcegraph.com instance.
func NewSourcegraphClient(doer Doer, base string) *SourcegraphClient {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = defaultSourcegraphBaseURL
	}
	return &SourcegraphClient{Doer: doer, BaseURL: base}
}

// Search runs query against the stream endpoint and returns up to maxResults
// matched lines. Each content match contributes one SourcegraphMatch per
// lineMatches entry; frames that are not well-formed "matches" frames are
// skipped. A non-200 answer returns an error; a 403/429 answer is parked with
// Retry-After (or a fixed fallback) and retried a bounded number of times.
func (c *SourcegraphClient) Search(query string, maxResults int) ([]SourcegraphMatch, error) {
	if c == nil || c.Doer == nil {
		return nil, errors.New("core: SourcegraphClient.Doer is nil")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("core: Sourcegraph search: empty query")
	}

	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = defaultSourcegraphBaseURL
	}
	rawURL, err := sourcegraphStreamURL(base, query, maxResults)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("core: sourcegraph request: %w", err)
	}
	req.Header.Set("User-Agent", "shhgit2")
	req.Header.Set("Accept", "text/event-stream")
	if c.Token != "" {
		req.Header.Set("Authorization", "token "+c.Token)
	}

	resp, err := c.doWithRateLimitRetry(req)
	if err != nil {
		return nil, err
	}
	defer closeResponse(resp)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("core: sourcegraph search: server status %d", resp.StatusCode)
	}
	return parseSourcegraphStream(resp.Body, maxResults)
}

// doWithRateLimitRetry performs the request, parking and retrying a bounded
// number of times on a 403/429 rate-limit answer.
func (c *SourcegraphClient) doWithRateLimitRetry(req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := c.Doer.Do(req)
		if err != nil {
			return nil, fmt.Errorf("core: sourcegraph request: %w", err)
		}
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}
		if attempt >= sourcegraphRateLimitRetries {
			return resp, nil
		}
		delay, ok := c.retryDelay(resp)
		if !ok {
			return resp, nil
		}
		closeResponse(resp)
		c.sleep(delay)
	}
}

// retryDelay returns the advertised Retry-After delay and whether one was
// present. A 403/429 without a usable header is a hard refusal, not a rate
// limit, so the caller returns it immediately instead of parking.
func (c *SourcegraphClient) retryDelay(resp *http.Response) (time.Duration, bool) {
	if resp != nil {
		if d, ok := httpclient.ParseRetryAfter(resp.Header.Get("Retry-After"), c.now()); ok {
			return d, true
		}
	}
	return 0, false
}

// sourcegraphStreamURL builds the documented stream-search URL. The result cap
// doubles as the display hint so the server does not truncate below it.
func sourcegraphStreamURL(base, query string, maxResults int) (string, error) {
	u, err := url.Parse(base + sourcegraphStreamPath)
	if err != nil {
		return "", fmt.Errorf("core: sourcegraph base URL: %w", err)
	}
	display := maxResults
	if display <= 0 {
		display = defaultSourcegraphDisplay
	}
	q := u.Query()
	q.Set("q", query)
	q.Set("v", sourcegraphAPIVersion)
	q.Set("t", sourcegraphSearchType)
	q.Set("display", strconv.Itoa(display))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// parseSourcegraphStream reads a Server-Sent-Events body and returns the
// content matches carried by "matches" frames, capped at maxResults when
// positive. Progress, filters, alert and done frames are ignored, and any frame
// whose data is not a JSON array of content matches is skipped rather than
// failing the whole search. The scanner error is returned so a frame larger
// than maxSourcegraphFrameBytes surfaces as a truncated stream rather than a
// short-but-successful result set.
func parseSourcegraphStream(r io.Reader, maxResults int) ([]SourcegraphMatch, error) {
	var out []SourcegraphMatch
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), maxSourcegraphFrameBytes)
	event := ""
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		switch {
		case line == "":
			event = ""
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			if event != sourcegraphEventMatches {
				continue
			}
			out = appendSourcegraphMatches(out, strings.TrimSpace(strings.TrimPrefix(line, "data:")), maxResults)
			if maxResults > 0 && len(out) >= maxResults {
				return out, nil
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("core: sourcegraph stream: %w", err)
	}
	return out, nil
}

// appendSourcegraphMatches decodes one "matches" data payload and expands each
// content match's lineMatches into a SourcegraphMatch, stopping at maxResults.
// A payload that is not a JSON array, and any entry missing a repository or
// path, is skipped.
func appendSourcegraphMatches(dst []SourcegraphMatch, data string, maxResults int) []SourcegraphMatch {
	if data == "" {
		return dst
	}
	var matches []sourcegraphStreamMatch
	if err := json.Unmarshal([]byte(data), &matches); err != nil {
		return dst
	}
	for _, m := range matches {
		if m.Repository == "" || m.Path == "" {
			continue
		}
		for _, lm := range m.LineMatches {
			dst = append(dst, SourcegraphMatch{
				Repository: m.Repository,
				Path:       m.Path,
				Preview:    lm.Preview,
				Line:       lm.LineNumber,
			})
			if maxResults > 0 && len(dst) >= maxResults {
				return dst
			}
		}
	}
	return dst
}

// sourcegraphStreamMatch mirrors the subset of the documented content match
// this client consumes. Other match types (path, repository, commit, symbol)
// carry no lineMatches and decode to zero values, so they are skipped.
type sourcegraphStreamMatch struct {
	Repository  string                 `json:"repository"`
	Path        string                 `json:"path"`
	LineMatches []sourcegraphLineMatch `json:"lineMatches"`
}

// sourcegraphLineMatch mirrors one matched line. offsetAndLengths is documented
// but deliberately ignored: this source only needs the preview and line number.
type sourcegraphLineMatch struct {
	Preview    string `json:"preview"`
	LineNumber int    `json:"lineNumber"`
}

// now returns the injected clock, or the real one.
func (c *SourcegraphClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// sleep waits using the injected seam, or really sleeps.
func (c *SourcegraphClient) sleep(d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(d)
		return
	}
	time.Sleep(d)
}
