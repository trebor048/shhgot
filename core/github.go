package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/github"
)

type GitHubClientWrapper struct {
	*github.Client
	Token            string
	RateLimitedUntil time.Time
}

const (
	defaultPerPage    = 100             // GitHub API max page size
	defaultSleep      = 5 * time.Second // Default event-poll interval
	defaultMaxPages   = 3               // Default pages of events per cycle
	defaultWorkerPool = 8               // Default concurrent event processors
)

// perfTuning resolves the "performance" section of config.yaml into the
// values used by the event-poll loops, falling back to the compiled-in
// defaults. api_sleep_seconds is floored at 1s because time.Tick panics on a
// non-positive interval.
func perfTuning(s *Session) (perPage int, sleep time.Duration, maxPages, workerPool int) {
	perPage, sleep, maxPages, workerPool = defaultPerPage, defaultSleep, defaultMaxPages, defaultWorkerPool
	if s == nil || s.Config == nil {
		return
	}
	p := s.Config.Performance
	perPage = clampInt(p.Int(p.APIPerPage, perPage), 1, 100)
	maxPages = clampInt(p.Int(p.APIPagesPerCycle, maxPages), 1, 100)
	workerPool = clampInt(p.Int(p.WorkerPoolSize, workerPool), 1, 1000)
	if secs := p.Int(p.APISleepSeconds, int(defaultSleep/time.Second)); secs >= 1 {
		sleep = time.Duration(secs) * time.Second
	}
	return
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// markNewEvents returns the events whose IDs are not already in seen, marking
// them so a page overlap or a fast re-poll cannot dispatch the same event twice.
func markNewEvents(events []*github.Event, seen map[string]bool, mu *sync.Mutex) []*github.Event {
	out := make([]*github.Event, 0, len(events))
	mu.Lock()
	for _, e := range events {
		if seen[e.GetID()] {
			continue
		}
		seen[e.GetID()] = true
		out = append(out, e)
	}
	mu.Unlock()
	return out
}

// conditionalGET issues a GET, sending the resource's previous ETag as
// If-None-Match. notModified reports a 304 Not Modified, which GitHub does not
// count against the primary rate limit. go-github v17 offers no way to pass a
// header through its list helpers, so the request is built by hand.
func conditionalGET(ctx context.Context, client *GitHubClientWrapper, urlStr, etag string, v interface{}) (*github.Response, bool, error) {
	req, err := client.NewRequest("GET", urlStr, nil)
	if err != nil {
		return nil, false, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := client.Do(ctx, req, v)
	// CheckResponse turns a 304 into a *github.ErrorResponse. That is the
	// conditional request succeeding, not a failure.
	if resp != nil && resp.StatusCode == http.StatusNotModified {
		return resp, true, nil
	}
	return resp, false, err
}

// listEventsPage fetches one page of the public events feed. Pages are numbered
// explicitly and 1-based because go-github's ListOptions omits a zero page,
// which made the previous loop fetch page 1 twice per cycle.
func listEventsPage(ctx context.Context, client *GitHubClientWrapper, perPage, page int, etag string) ([]*github.Event, *github.Response, bool, error) {
	var events []*github.Event
	resp, notModified, err := conditionalGET(ctx, client,
		fmt.Sprintf("events?per_page=%d&page=%d", perPage, page), etag, &events)
	return events, resp, notModified, err
}

// Repository metadata is fetched once per PushEvent, but the same repository
// pushes many times: an uncached GetByID is the largest consumer of the API
// budget. The cache is short lived because stars and size do change.
const (
	repoCacheTTL     = 15 * time.Minute
	repoCacheMaxSize = 5000
)

type repoCacheEntry struct {
	repo *github.Repository
	at   time.Time
}

var (
	repoCacheMu sync.Mutex
	repoCache   = map[int64]repoCacheEntry{}
)

func cachedRepository(id int64) (*github.Repository, bool) {
	repoCacheMu.Lock()
	defer repoCacheMu.Unlock()
	e, ok := repoCache[id]
	if !ok || time.Since(e.at) > repoCacheTTL {
		return nil, false
	}
	return e.repo, true
}

func cacheRepository(id int64, repo *github.Repository) {
	repoCacheMu.Lock()
	defer repoCacheMu.Unlock()
	if len(repoCache) >= repoCacheMaxSize {
		for k, e := range repoCache {
			if time.Since(e.at) > repoCacheTTL {
				delete(repoCache, k)
			}
		}
		// Still full of live entries: drop it rather than grow without bound.
		if len(repoCache) >= repoCacheMaxSize {
			repoCache = map[int64]repoCacheEntry{}
		}
	}
	repoCache[id] = repoCacheEntry{repo: repo, at: time.Now()}
}

// isUnauthorized reports whether a GitHub API call was rejected with HTTP 401
// (bad credentials — revoked or expired token). The raw *github.Response is
// checked first, then the wrapped *github.ErrorResponse, because some callers
// observe a nil response on transport errors.
func isUnauthorized(err error, resp *github.Response) bool {
	if resp != nil && resp.StatusCode == http.StatusUnauthorized {
		return true
	}
	if er, ok := err.(*github.ErrorResponse); ok && er.Response != nil {
		return er.Response.StatusCode == http.StatusUnauthorized
	}
	return false
}

// RateLimitManager and its rate-limit bookkeeping were removed: nothing ever
// read the tracked limits (the field and its manager var were both dead), and
// per-token pacing is handled by the RateLimitedUntil field on
// GitHubClientWrapper and the rotation in GetClient.

func GetRepositories(session *Session) {
	localCtx, cancel := context.WithCancel(session.Context)
	defer cancel()

	perPage, sleep, maxPages, workerPool := perfTuning(session)

	observedKeys := map[string]bool{}
	var observedKeysMutex sync.Mutex
	// seenRepos dedupes clones. The public events feed is dominated by repeated
	// pushes to the same repository, and cloning + scanning it once per
	// PushEvent is the most expensive way to re-learn nothing.
	// scanning.rescan_repositories restores the old per-push behaviour.
	seenRepos := map[int64]bool{}
	var seenReposMutex sync.Mutex
	rescanRepositories := session.Config != nil &&
		session.Config.Scanning.Bool(session.Config.Scanning.RescanRepositories, false)
	noDataCount := 0
	// One ETag per page. Sending it back as If-None-Match makes an unchanged
	// page answer 304 Not Modified, which GitHub does not count against the
	// primary rate limit.
	etags := make([]string, maxPages+1)

	ticker := time.NewTicker(sleep)
	defer ticker.Stop()
	for {
		// page advances only after a page is handled, so the error paths below
		// retry the same page (as the original loop did) rather than skipping it.
		for page := 1; page <= maxPages; {
			// Rotate through tokens to distribute rate limit usage
			client := session.GetClient()

			// Add timeout to API call
			ctx, cancelCall := context.WithTimeout(localCtx, 10*time.Second)
			events, resp, notModified, err := listEventsPage(ctx, client, perPage, page, etags[page])
			cancelCall()

			if notModified {
				// Nothing newer than the last poll; older pages are older still.
				session.FreeClient(client)
				noDataCount = 0
				break
			}

			if err != nil {
				// A revoked/expired token is dropped from config.yaml and the
				// pool immediately so it is never retried.
				if isUnauthorized(err, resp) {
					session.RemoveUnauthorizedToken(client.Token)
					session.FreeClient(client) // no-op: removed clients are dropped
					continue
				}

				if _, ok := err.(*github.RateLimitError); ok {
					if resp != nil {
						client.RateLimitedUntil = resp.Rate.Reset.Time
						RecordAPIRate(0, resp.Rate.Reset.Time)
					}
					session.FreeClient(client)
					session.Progress.IncrementRateLimited()
					continue
				}

				if _, ok := err.(*github.AbuseRateLimitError); ok {
					session.FreeClient(client)
					session.Progress.IncrementRateLimited()
					time.Sleep(5 * time.Second)
					continue
				}

				// Check for 422 Unprocessable Entity (pagination limit)
				if resp != nil && resp.StatusCode == 422 {
					session.Log.Debug("Pagination limit reached for events API")
					session.FreeClient(client)
					break
				}

				session.Log.Debug("Error getting GitHub events: %s", err)
				session.FreeClient(client)
				noDataCount++
				if noDataCount > 3 {
					session.Log.Debug("Resetting after errors...")
					noDataCount = 0
					break
				}
				time.Sleep(1 * time.Second)
				continue
			}

			session.FreeClient(client)
			noDataCount = 0

			if resp != nil {
				RecordAPIRate(resp.Rate.Remaining, resp.Rate.Reset.Time)
				// Remember this page's ETag so the next cycle can ask "has it
				// changed?" for free.
				if tag := resp.Header.Get("ETag"); tag != "" {
					etags[page] = tag
				}

				if page == 1 {
					tokenMessage := fmt.Sprintf("[?] Token %s[..] has %d/%d calls remaining.", MaskToken(client.Token), resp.Rate.Remaining, resp.Rate.Limit)

					if resp.Rate.Remaining < 50 {
						session.Log.Warn("%s", tokenMessage)
					} else {
						session.Log.Debug("%s", tokenMessage)
					}
				}
			}

			if len(events) == 0 {
				break
			}

			newEvents := markNewEvents(events, observedKeys, &observedKeysMutex)

			if len(newEvents) == 0 {
				// This page held only events already dispatched; older pages are
				// older still, so stop paging instead of spending quota.
				break
			}

			// Process events concurrently
			processBatch := func(events []*github.Event) {
				for _, e := range events {
					if e.GetType() == "PushEvent" {
						dst := &github.PushEvent{}
						json.Unmarshal(e.GetRawPayload(), dst)
						id := e.GetRepo().GetID()
						// An id of 0 means the payload carried no repository;
						// do not let every such event collapse into one.
						if !rescanRepositories && id != 0 {
							seenReposMutex.Lock()
							dup := seenRepos[id]
							seenRepos[id] = true
							seenReposMutex.Unlock()
							if dup {
								continue
							}
						}
						session.Repositories <- GitResource{
							Id:   id,
							Type: GITHUB_SOURCE,
							Url:  e.GetRepo().GetURL(),
							Ref:  dst.GetRef(),
						}
					} else if e.GetType() == "IssueCommentEvent" {
						dst := &github.IssueCommentEvent{}
						json.Unmarshal(e.GetRawPayload(), dst)

						// Build a link back to the source issue/PR so matches
						// can be traced to the exact comment.
						url := dst.Comment.GetHTMLURL()
						if url == "" {
							url = dst.Issue.GetHTMLURL()
						}
						if url == "" {
							// Fall back to the repository itself.
							repoUrl := e.GetRepo().GetURL()
							if strings.Contains(repoUrl, "api.github.com/repos/") {
								url = strings.Replace(repoUrl, "api.github.com/repos/", "github.com/", 1)
							} else {
								url = repoUrl
							}
						}

						session.Comments <- Comment{
							Body: dst.Comment.GetBody(),
							Url:  url,
						}
					}
				}
			}

			// Batch process events for better performance
			batchSize := len(newEvents) / workerPool
			if batchSize < 1 {
				batchSize = 1
			}

			for i := 0; i < len(newEvents); i += batchSize {
				end := i + batchSize
				if end > len(newEvents) {
					end = len(newEvents)
				}
				go processBatch(newEvents[i:end])
			}

			page++
		}

		select {
		case <-ticker.C:
			continue
		case <-localCtx.Done():
			cancel()
			return
		}
	}
}

func GetGists(session *Session) {
	localCtx, cancel := context.WithCancel(session.Context)
	defer cancel()

	_, sleep, _, _ := perfTuning(session)

	observedKeys := map[string]bool{}
	opt := &github.GistListOptions{}

	var client *GitHubClientWrapper
	ticker := time.NewTicker(sleep)
	defer ticker.Stop()
	for {
		if client != nil {
			session.FreeClient(client)
		}

		client = session.GetClient()
		callCtx, cancelCall := context.WithTimeout(localCtx, 10*time.Second)
		gists, resp, err := client.Gists.ListAll(callCtx, opt)
		cancelCall()

		if err != nil {
			if isUnauthorized(err, resp) {
				session.RemoveUnauthorizedToken(client.Token)
				// Client is dropped at the top of the next iteration; skip the
				// rest of this pass and keep polling with any remaining tokens.
				continue
			}

			// Both rate-limit cases fall through to the top of the loop, which
			// returns this client to the pool and picks another token - the same
			// rotation the events path uses. They used to break out of the loop
			// entirely, so one rate-limited token stopped gist polling for the
			// rest of the run. The client is deliberately NOT freed here: the top
			// of the loop does that, and freeing twice would double-fill the pool.
			if _, ok := err.(*github.RateLimitError); ok {
				if resp != nil {
					client.RateLimitedUntil = resp.Rate.Reset.Time
				}
				session.Progress.IncrementRateLimited()
				continue
			}

			if _, ok := err.(*github.AbuseRateLimitError); ok {
				// Park it briefly: FreeClient routes a future deadline to the
				// exhausted set, so it is not handed straight back out.
				client.RateLimitedUntil = time.Now().Add(5 * time.Second)
				session.Progress.IncrementRateLimited()
				continue
			}

			session.Log.Warn("Error getting GitHub Gists: %s ... trying again", err)
		}

		if resp != nil {
			RecordAPIRate(resp.Rate.Remaining, resp.Rate.Reset.Time)
		}

		newGists := make([]*github.Gist, 0, len(gists))
		for _, e := range gists {
			if observedKeys[e.GetID()] {
				continue
			}

			newGists = append(newGists, e)
		}

		for _, e := range newGists {
			observedKeys[e.GetID()] = true
			if u := e.GetGitPullURL(); u != "" {
				session.Gists <- u
			}
		}

		opt.Since = time.Now()

		select {
		case <-ticker.C:
			continue
		case <-localCtx.Done():
			cancel()
			return
		}
	}
}

func GetRepository(session *Session, id int64) (*github.Repository, error) {
	// The same repository generates many events; skip the API call for one seen
	// recently. This is the single biggest saving on the API budget, which is
	// otherwise one GetByID per PushEvent.
	if repo, ok := cachedRepository(id); ok {
		return repo, nil
	}

	client := session.GetClient()
	defer session.FreeClient(client)

	repo, resp, err := client.Repositories.GetByID(session.Context, id)

	if err != nil {
		if isUnauthorized(err, resp) {
			session.RemoveUnauthorizedToken(client.Token)
		}
		return nil, err
	}

	if resp != nil {
		if resp.Rate.Remaining <= 1 {
			client.RateLimitedUntil = resp.Rate.Reset.Time
			session.Progress.IncrementRateLimited()
		}
		RecordAPIRate(resp.Rate.Remaining, resp.Rate.Reset.Time)
	}

	cacheRepository(id, repo)

	return repo, nil
}
