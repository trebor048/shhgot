package core

import (
	"bytes"
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
	// defaultPsbdmpBaseURL is the public psbdmp.ws host.
	defaultPsbdmpBaseURL = "https://psbdmp.ws"
	// defaultPsbdmpHost is the hostname the Dump download path is allowlisted
	// for.
	defaultPsbdmpHost = "psbdmp.ws"
	// psbdmpSearchPath is the documented search endpoint prefix; the URL-encoded
	// query is appended to it.
	psbdmpSearchPath = "/api/v3/search/"
	// psbdmpDumpPath is the documented single-paste endpoint prefix; the paste
	// id is appended to it.
	psbdmpDumpPath = "/api/v3/dump/"
	// psbdmpPasteWebURL prefixes a psbdmp id to form the human-facing Pastebin
	// page when the search entry carries no explicit url.
	psbdmpPasteWebURL = "https://pastebin.com/"
	// maxPsbdmpResponseBytes bounds the search JSON so a hostile endpoint cannot
	// force unbounded buffering.
	maxPsbdmpResponseBytes = 8 << 20
	// maxPsbdmpContentBytes caps a single dumped paste at 2 MiB.
	maxPsbdmpContentBytes = 2 << 20
	// psbdmpRateLimitRetries bounds how many times a 429/403 answer carrying a
	// usable Retry-After is parked and retried before the status is surfaced.
	psbdmpRateLimitRetries = 2
	// psbdmpEnvelopeDepth bounds how many nested data/results/hits envelopes the
	// permissive decoder unwraps before giving up.
	psbdmpEnvelopeDepth = 2
)

// PsbdmpPaste is one search hit returned by the psbdmp.ws API.
//
// The exact response shape of psbdmp.ws could not be verified from the build
// sandbox, so every field is treated as best-effort: the decoder accepts
// several plausible key spellings, skips anything it cannot recognise, and
// never errors on an unexpected shape. Callers must not assume that a paste's
// Text or ID is populated by the server.
type PsbdmpPaste struct {
	// ID identifies the paste (from id or key). Required for a usable hit.
	ID string
	// Text is the paste body, when the search response carried it inline.
	Text string
	// URL is the explicit url field, or https://pastebin.com/<ID> when absent.
	URL string
	// Tags are the paste's tags, from tags or tag, split on comma or whitespace.
	Tags []string
}

// PsbdmpClient talks to the psbdmp.ws paste-dump search API. Every network and
// timing seam is injectable so tests never touch the network or block. See
// PsbdmpPaste for the caveat that the remote response shape is unverified.
type PsbdmpClient struct {
	// Doer performs the HTTP request. Required.
	Doer interface {
		Do(*http.Request) (*http.Response, error)
	}
	// BaseURL is the psbdmp.ws root; empty uses https://psbdmp.ws.
	BaseURL string
	// Sleep is the injected sleep seam; nil uses time.Sleep.
	Sleep func(time.Duration)
	// Now is the injected clock used to resolve an HTTP-date Retry-After; nil
	// uses time.Now.
	Now func() time.Time
}

// NewPsbdmpClient returns a client for the given base, filling the documented
// default host. An empty base uses https://psbdmp.ws.
func NewPsbdmpClient(doer Doer, base string) *PsbdmpClient {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = defaultPsbdmpBaseURL
	}
	return &PsbdmpClient{Doer: doer, BaseURL: base}
}

// Search performs GET {base}/api/v3/search/<url-encoded query> and returns up
// to maxResults usable pastes. A non-positive maxResults applies no cap.
//
// The response is decoded permissively because the remote shape is unverified:
// a top-level array, or an object holding data/results/hits, is accepted; the
// text may arrive as text/content/body/data, the id as id/key, and tags as
// tags/tag (an array or a comma/space-separated string). An entry missing both
// an id and text is skipped, and an unrecognised shape yields no results rather
// than an error. A 429/403 answer that advertises Retry-After is parked with
// Sleep and retried a bounded number of times; any other non-200 status returns
// an error so the caller can warn and back off.
func (c *PsbdmpClient) Search(query string, maxResults int) ([]PsbdmpPaste, error) {
	if c == nil || c.Doer == nil {
		return nil, errors.New("core: PsbdmpClient.Doer is nil")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("core: psbdmp search: empty query")
	}

	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = defaultPsbdmpBaseURL
	}
	rawURL := base + psbdmpSearchPath + url.PathEscape(query)
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("core: psbdmp request: %w", err)
	}
	req.Header.Set("User-Agent", "shhgit2")
	req.Header.Set("Accept", "application/json")

	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer closeResponse(resp)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("core: psbdmp search: server status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPsbdmpResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("core: psbdmp search: reading response: %w", err)
	}
	return decodePsbdmpSearch(data, maxResults), nil
}

// Dump downloads the raw body of one paste through the documented dump
// endpoint. It is used only when a search entry carried no inline text. The
// request host must be allowlisted (psbdmp.ws plus the BaseURL host) and the
// body is capped at maxPsbdmpContentBytes.
func (c *PsbdmpClient) Dump(id string) ([]byte, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("core: psbdmp dump: empty id")
	}
	if c == nil || c.Doer == nil {
		return nil, errors.New("core: PsbdmpClient.Doer is nil")
	}

	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = defaultPsbdmpBaseURL
	}
	f := &Fetcher{
		Doer:         c.Doer,
		AllowedHosts: c.allowedHosts(),
		MaxFileSize:  maxPsbdmpContentBytes,
		MaxRetries:   psbdmpRateLimitRetries,
		Now:          c.Now,
		Sleep:        c.Sleep,
	}
	return f.Fetch(base + psbdmpDumpPath + url.PathEscape(id))
}

// do issues req, parking and retrying a bounded number of 429/403 answers that
// carry a usable Retry-After. A rate-limit answer without a usable header is
// returned unchanged so the caller surfaces it as an error.
func (c *PsbdmpClient) do(req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := c.Doer.Do(req)
		if err != nil {
			return nil, fmt.Errorf("core: psbdmp request: %w", err)
		}
		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusForbidden {
			return resp, nil
		}
		if attempt >= psbdmpRateLimitRetries {
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

// allowedHosts returns psbdmp.ws plus the BaseURL host, de-duplicated. An
// explicit base port is preserved (host:port entry) so hostPortAllowed can
// scope custom-port instances without opening every port.
func (c *PsbdmpClient) allowedHosts() []string {
	hosts := []string{defaultPsbdmpHost}
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = defaultPsbdmpBaseURL
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
func (c *PsbdmpClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// sleep waits using the injected seam, or really sleeps.
func (c *PsbdmpClient) sleep(d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(d)
		return
	}
	time.Sleep(d)
}

// decodePsbdmpSearch permissively turns a search response body into pastes,
// capped at maxResults when positive. Unrecognised shapes and unusable entries
// are skipped rather than reported as errors.
func decodePsbdmpSearch(data []byte, maxResults int) []PsbdmpPaste {
	entries := psbdmpExtractEntries(data, psbdmpEnvelopeDepth)
	out := make([]PsbdmpPaste, 0, len(entries))
	for _, raw := range entries {
		paste, ok := psbdmpDecodeEntry(raw)
		if !ok {
			continue
		}
		out = append(out, paste)
		if maxResults > 0 && len(out) >= maxResults {
			break
		}
	}
	return out
}

// psbdmpExtractEntries unwraps a JSON value into candidate entry objects. A
// top-level array is used directly; an object is searched for a data/results/
// hits key (unwrapping up to depth levels), and failing that is treated as a
// single entry. Scalars and malformed JSON yield nothing.
func psbdmpExtractEntries(raw json.RawMessage, depth int) []json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	switch trimmed[0] {
	case '[':
		var arr []json.RawMessage
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return nil
		}
		return arr
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &obj); err != nil {
			return nil
		}
		if depth > 0 {
			for _, key := range []string{"data", "results", "hits"} {
				if value, ok := obj[key]; ok {
					if entries := psbdmpExtractEntries(value, depth-1); len(entries) > 0 {
						return entries
					}
				}
			}
		}
		return []json.RawMessage{trimmed}
	default:
		return nil
	}
}

// psbdmpDecodeEntry decodes one candidate entry, reporting ok=false when it
// carries neither an id nor usable text.
func psbdmpDecodeEntry(raw json.RawMessage) (PsbdmpPaste, bool) {
	var wire psbdmpWirePaste
	if err := json.Unmarshal(raw, &wire); err != nil {
		return PsbdmpPaste{}, false
	}

	id := psbdmpFirstNonEmpty(psbdmpRawString(wire.ID), psbdmpRawString(wire.Key))
	text := psbdmpFirstNonEmpty(wire.Text, wire.Content, wire.Body)
	if text == "" {
		if s, ok := psbdmpRawJSONString(wire.Data); ok {
			text = strings.TrimSpace(s)
		}
	}
	if id == "" || text == "" {
		return PsbdmpPaste{}, false
	}

	link := strings.TrimSpace(wire.URL)
	if link == "" {
		link = psbdmpPasteWebURL + id
	}

	tags := psbdmpParseTags(wire.Tags)
	if len(tags) == 0 {
		tags = psbdmpParseTags(wire.Tag)
	}

	return PsbdmpPaste{ID: id, Text: text, URL: link, Tags: tags}, true
}

// psbdmpWirePaste mirrors the plausible entry keys the decoder accepts. The id
// and tag values are kept raw because they may arrive as a number, string or
// array depending on the (unverified) server shape.
type psbdmpWirePaste struct {
	ID      json.RawMessage `json:"id"`
	Key     json.RawMessage `json:"key"`
	Text    string          `json:"text"`
	Content string          `json:"content"`
	Body    string          `json:"body"`
	Data    json.RawMessage `json:"data"`
	URL     string          `json:"url"`
	Tags    json.RawMessage `json:"tags"`
	Tag     json.RawMessage `json:"tag"`
}

// psbdmpRawString renders a raw JSON scalar as a trimmed string. A JSON string
// is unquoted; a number, boolean or bare token is used as written. null, empty
// and non-scalar values yield "".
func psbdmpRawString(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	if unquoted, err := strconv.Unquote(s); err == nil {
		return strings.TrimSpace(unquoted)
	}
	return s
}

// psbdmpRawJSONString returns the value of raw only when it is a JSON string.
func psbdmpRawJSONString(raw json.RawMessage) (string, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return "", false
	}
	unquoted, err := strconv.Unquote(s)
	if err != nil {
		return "", false
	}
	return unquoted, true
}

// psbdmpFirstNonEmpty returns the first value whose trimmed form is non-empty,
// itself trimmed.
func psbdmpFirstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// psbdmpParseTags normalises a tags field that may be a JSON array of strings
// or a single string, splitting on commas and whitespace and dropping empties
// and duplicates.
func psbdmpParseTags(raw json.RawMessage) []string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return nil
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return psbdmpSplitTags(arr)
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return psbdmpSplitTags([]string{single})
	}
	return nil
}

// psbdmpSplitTags splits each value on commas and whitespace, de-duplicating
// while preserving order.
func psbdmpSplitTags(values []string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, value := range values {
		for _, tag := range strings.Fields(strings.ReplaceAll(value, ",", " ")) {
			if seen[tag] {
				continue
			}
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out
}
