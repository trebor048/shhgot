package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// defaultPastebinBaseURL is the Pastebin Scraping API host. The Scraping
	// API needs a Pastebin Pro account with the caller's IP whitelisted; it is
	// not key-authenticated.
	defaultPastebinBaseURL = "https://scrape.pastebin.com"
	// defaultPastebinScrapeItemURL downloads a paste body through the Scraping
	// API; the paste key is appended to it.
	defaultPastebinScrapeItemURL = "https://scrape.pastebin.com/api_scrape_item.php?i="
	// defaultPastebinPublicRawURL downloads a paste body through the public raw
	// endpoint, which works without Scraping API access.
	defaultPastebinPublicRawURL = "https://pastebin.com/raw/"
	// defaultPastebinWebURL is the human-facing paste page prefix.
	defaultPastebinWebURL = "https://pastebin.com/"
	// defaultPastebinLimit is the scrape-list size used when a caller passes a
	// non-positive limit.
	defaultPastebinLimit = 100
	// maxPastebinResponseBytes bounds the scrape-list JSON.
	maxPastebinResponseBytes = 8 << 20
	// maxPastebinContentBytes caps a single downloaded paste at 2 MiB.
	maxPastebinContentBytes = 2 << 20
	// pastebinRateLimitRetries is how many times a 403/429 answer is parked
	// using an advertised Retry-After and retried before the status surfaces.
	pastebinRateLimitRetries = 2
)

// pasteKeyRe constrains paste keys to bare alphanumeric tokens: PasteContent
// concatenates the key onto the endpoint URL, so ? # / and .. must be
// rejected rather than fetched.
var pasteKeyRe = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)

// PastebinPaste is one entry from the Pastebin Scraping API list.
type PastebinPaste struct {
	Key    string
	Title  string
	Syntax string
	Date   string
	Size   int
	URL    string
}

// PastebinClient reads the Pastebin Scraping API. Every network seam is
// injectable so tests never touch the network or block.
type PastebinClient struct {
	// Doer performs the HTTP request. Required.
	Doer interface {
		Do(*http.Request) (*http.Response, error)
	}
	// BaseURL is the scrape API root; empty uses https://scrape.pastebin.com.
	BaseURL string
	// RawBase prefixes a paste key for the Scraping API body endpoint; empty
	// uses the scrape.pastebin.com item URL.
	RawBase string
	// PublicRaw prefixes a paste key for the public raw endpoint; empty uses
	// https://pastebin.com/raw/.
	PublicRaw string
	// Sleep is the injected sleep seam; nil uses time.Sleep.
	Sleep func(time.Duration)
	// Now is the injected clock used to resolve an HTTP-date Retry-After; nil
	// uses time.Now.
	Now func() time.Time
}

// NewPastebinClient returns a client for the given scrape API base, filling the
// documented endpoint defaults. An empty base uses the Pastebin Scraping API
// host.
func NewPastebinClient(doer Doer, base string) *PastebinClient {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = defaultPastebinBaseURL
	}
	return &PastebinClient{
		Doer:      doer,
		BaseURL:   base,
		RawBase:   defaultPastebinScrapeItemURL,
		PublicRaw: defaultPastebinPublicRawURL,
	}
}

// ListRecent fetches the most recent public pastes from
// GET {BaseURL}/api_scraping.php?limit=N. Entries with an empty key are
// skipped. A non-positive limit uses defaultPastebinLimit. A 403/429 answer is
// parked using an advertised Retry-After and retried a bounded number of times.
func (c *PastebinClient) ListRecent(limit int) ([]PastebinPaste, error) {
	if c.Doer == nil {
		return nil, errors.New("core: PastebinClient.Doer is nil")
	}
	if limit <= 0 {
		limit = defaultPastebinLimit
	}
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = defaultPastebinBaseURL
	}

	resp, err := c.scrapeList(base, limit)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		status := resp.StatusCode
		closeResponse(resp)
		return nil, fmt.Errorf("core: pastebin scrape list: server status %d", status)
	}
	items, err := decodePastebinList(resp.Body)
	closeResponse(resp)
	if err != nil {
		return nil, err
	}
	return items, nil
}

// PasteContent downloads the body of one paste. With useScrapeAPI the Scraping
// API item endpoint is used; otherwise the public raw endpoint. The host must
// be on the allowlist (scrape.pastebin.com, pastebin.com and the BaseURL host),
// and the body is capped at maxPastebinContentBytes.
func (c *PastebinClient) PasteContent(key string, useScrapeAPI bool) ([]byte, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errors.New("core: pastebin paste key is empty")
	}
	// The key is concatenated onto the endpoint URL, so it must be a bare key:
	// anything else (? # / ..) would inject query, fragment or path elements.
	if !pasteKeyRe.MatchString(key) {
		return nil, fmt.Errorf("core: pastebin paste key %q is not a bare key", key)
	}
	if c.Doer == nil {
		return nil, errors.New("core: PastebinClient.Doer is nil")
	}

	base := strings.TrimSpace(c.PublicRaw)
	if useScrapeAPI {
		base = strings.TrimSpace(c.RawBase)
	}
	if base == "" {
		if useScrapeAPI {
			base = defaultPastebinScrapeItemURL
		} else {
			base = defaultPastebinPublicRawURL
		}
	}

	f := &Fetcher{
		Doer:         c.Doer,
		AllowedHosts: c.allowedHosts(),
		MaxFileSize:  maxPastebinContentBytes,
		MaxRetries:   pastebinRateLimitRetries,
		Now:          c.Now,
		Sleep:        c.Sleep,
	}
	return f.Fetch(base + key)
}

// scrapeList performs one scrape-list GET, parking and retrying a bounded
// number of times on a 403/429 rate-limit answer.
func (c *PastebinClient) scrapeList(base string, limit int) (*http.Response, error) {
	rawURL, err := pastebinListURL(base, limit)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("core: pastebin request: %w", err)
	}
	req.Header.Set("User-Agent", "shhgit2")
	req.Header.Set("Accept", "application/json")

	for attempt := 0; ; attempt++ {
		resp, err := c.Doer.Do(req)
		if err != nil {
			return nil, fmt.Errorf("core: pastebin request: %w", err)
		}
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}
		if attempt >= pastebinRateLimitRetries {
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

// pastebinListURL builds GET {base}/api_scraping.php?limit=N.
func pastebinListURL(base string, limit int) (string, error) {
	u, err := url.Parse(base + "/api_scraping.php")
	if err != nil {
		return "", fmt.Errorf("core: pastebin base URL: %w", err)
	}
	q := u.Query()
	q.Set("limit", strconv.Itoa(limit))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// decodePastebinList parses the scrape-list JSON, bounded to
// maxPastebinResponseBytes, skipping entries with an empty key.
func decodePastebinList(body io.Reader) ([]PastebinPaste, error) {
	var items []pastebinItem
	dec := json.NewDecoder(io.LimitReader(body, maxPastebinResponseBytes))
	if err := dec.Decode(&items); err != nil {
		return nil, fmt.Errorf("core: decoding pastebin list: %w", err)
	}
	out := make([]PastebinPaste, 0, len(items))
	for _, it := range items {
		key := strings.TrimSpace(it.Key)
		if key == "" {
			continue
		}
		out = append(out, PastebinPaste{
			Key:    key,
			Title:  it.Title,
			Syntax: it.Syntax,
			Date:   it.Date,
			Size:   it.Size,
			URL:    defaultPastebinWebURL + key,
		})
	}
	return out, nil
}

// pastebinItem mirrors the fields this client consumes from the scrape list.
type pastebinItem struct {
	Key    string `json:"key"`
	Title  string `json:"title"`
	Syntax string `json:"syntax"`
	Size   int    `json:"size"`
	Date   string `json:"date"`
}

// allowedHosts returns the canonical Pastebin hosts plus the BaseURL host,
// de-duplicated. An explicit base port is preserved (host:port entry) so
// hostPortAllowed can scope custom-port instances without opening every port.
func (c *PastebinClient) allowedHosts() []string {
	hosts := []string{"scrape.pastebin.com", "pastebin.com"}
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = defaultPastebinBaseURL
	}
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

// now returns the injected clock, or the real one.
func (c *PastebinClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// sleep waits using the injected seam, or really sleeps.
func (c *PastebinClient) sleep(d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(d)
		return
	}
	time.Sleep(d)
}
