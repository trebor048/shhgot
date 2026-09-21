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
	defaultWorkerPool = 20              // Default concurrent event processors
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

// RateLimitManager tracks and manages rate limits across tokens
type RateLimitManager struct {
	sync.Mutex
	limits map[string]*RateLimit
}

type RateLimit struct {
	Remaining int
	Reset     time.Time
	LastCheck time.Time
}

var rateLimitMgr = &RateLimitManager{
	limits: make(map[string]*RateLimit),
}

func GetRepositories(session *Session) {
	localCtx, cancel := context.WithCancel(session.Context)
	defer cancel()

	perPage, sleep, maxPages, workerPool := perfTuning(session)

	observedKeys := map[string]bool{}
	var observedKeysMutex sync.Mutex
	pageCount := 0
	noDataCount := 0
	tokenIndex := 0

	for c := time.Tick(sleep); ; {
		opt := &github.ListOptions{PerPage: perPage}

		for {
			if pageCount >= maxPages {
				break
			}

			// Rotate through tokens to distribute rate limit usage
			client := session.GetClient()

			// Add timeout to API call
			ctx, cancel := context.WithTimeout(localCtx, 10*time.Second)
			events, resp, err := client.Activity.ListEvents(ctx, opt)
			cancel()

			if err != nil {
				// A revoked/expired token is dropped from config.yaml and the
				// pool immediately so it is never retried.
				if isUnauthorized(err, resp) {
					session.RemoveUnauthorizedToken(client.Token)
					session.FreeClient(client) // no-op: removed clients are dropped
					continue
				}

				if _, ok := err.(*github.RateLimitError); ok {
					client.RateLimitedUntil = resp.Rate.Reset.Time
					session.FreeClient(client)
					session.Progress.IncrementRateLimited()
					tokenIndex++
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

			if opt.Page == 0 {
				tokenMessage := fmt.Sprintf("[?] Token %s[..] has %d/%d calls remaining.", MaskToken(client.Token), resp.Rate.Remaining, resp.Rate.Limit)

				if resp.Rate.Remaining < 50 {
					session.Log.Warn("%s", tokenMessage)
				} else {
					session.Log.Debug("%s", tokenMessage)
				}
			}

			if len(events) == 0 {
				break
			}

			newEvents := make([]*github.Event, 0, len(events))

			// remove duplicates
			observedKeysMutex.Lock()
			for _, e := range events {
				if observedKeys[*e.ID] {
					continue
				}

				newEvents = append(newEvents, e)
			}
			observedKeysMutex.Unlock()

			// Process events concurrently
			processBatch := func(events []*github.Event) {
				for _, e := range events {
					if *e.Type == "PushEvent" {
						observedKeysMutex.Lock()
						observedKeys[*e.ID] = true
						observedKeysMutex.Unlock()

						dst := &github.PushEvent{}
						json.Unmarshal(e.GetRawPayload(), dst)
						session.Repositories <- GitResource{
							Id:   e.GetRepo().GetID(),
							Type: GITHUB_SOURCE,
							Url:  e.GetRepo().GetURL(),
							Ref:  dst.GetRef(),
						}
					} else if *e.Type == "IssueCommentEvent" {
						observedKeysMutex.Lock()
						observedKeys[*e.ID] = true
						observedKeysMutex.Unlock()

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

			pageCount++
			opt.Page++
		}

		select {
		case <-c:
			pageCount = 0 // Reset page count for next cycle
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
	for c := time.Tick(sleep); ; {
		if client != nil {
			session.FreeClient(client)
		}

		client = session.GetClient()
		gists, resp, err := client.Gists.ListAll(localCtx, opt)

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
				client.RateLimitedUntil = resp.Rate.Reset.Time
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

		newGists := make([]*github.Gist, 0, len(gists))
		for _, e := range gists {
			if observedKeys[*e.ID] {
				continue
			}

			newGists = append(newGists, e)
		}

		for _, e := range newGists {
			observedKeys[*e.ID] = true
			session.Gists <- *e.GitPullURL
		}

		opt.Since = time.Now()

		select {
		case <-c:
			continue
		case <-localCtx.Done():
			cancel()
			return
		}
	}
}

func GetRepository(session *Session, id int64) (*github.Repository, error) {
	client := session.GetClient()
	defer session.FreeClient(client)

	repo, resp, err := client.Repositories.GetByID(session.Context, id)

	if err != nil {
		if isUnauthorized(err, resp) {
			session.RemoveUnauthorizedToken(client.Token)
		}
		return nil, err
	}

	if resp.Rate.Remaining <= 1 {
		client.RateLimitedUntil = resp.Rate.Reset.Time
		session.Progress.IncrementRateLimited()
	}

	return repo, nil
}
