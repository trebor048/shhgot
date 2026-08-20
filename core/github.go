package core

import (
	"context"
	"encoding/json"
	"fmt"
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
	perPage    = 100             // Increased from 300 for better performance
	sleep      = 5 * time.Second // Faster polling for more results
	maxPages   = 3               // Get more events per cycle
	workerPool = 20              // More concurrent workers
)

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
				tokenMessage := fmt.Sprintf("[?] Token %s[..] has %d/%d calls remaining.", client.Token[:10], resp.Rate.Remaining, resp.Rate.Limit)

				if resp.Rate.Remaining < 50 {
					session.Log.Warn(tokenMessage)
				} else {
					session.Log.Debug(tokenMessage)
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
			if _, ok := err.(*github.RateLimitError); ok {
				client.RateLimitedUntil = resp.Rate.Reset.Time
				session.FreeClient(client)
				session.Progress.IncrementRateLimited()
				break
			}

			if _, ok := err.(*github.AbuseRateLimitError); ok {
				session.FreeClient(client)
				session.Progress.IncrementRateLimited()
				break
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
		return nil, err
	}

	if resp.Rate.Remaining <= 1 {
		client.RateLimitedUntil = resp.Rate.Reset.Time
		session.Progress.IncrementRateLimited()
	}

	return repo, nil
}
