package main

import (
	"sort"
	"sync"
	"time"
)

// RepoActivity tracks currently fetched/scanned repositories plus aggregate
// counters, for display in the web dashboard. It is safe for concurrent use
// from the scanner's worker goroutines.
type RepoActivity struct {
	mu sync.Mutex

	fetching map[string]time.Time // url -> when fetch (API/clone) started
	scanning map[string]time.Time // url -> when scan started

	// skipped holds repositories the operator abandoned from the dashboard. A
	// worker checks it before cloning, again before scanning and periodically
	// during a scan, so a skip takes effect at the next checkpoint rather than
	// instantly - a clone already in flight cannot be interrupted.
	skipped map[string]bool

	TotalFetched int64
	TotalCloned  int64
	TotalScanned int64
	TotalFailed  int64
	RateLimited  int64
}

// maxSkippedRepos bounds the skip set so a scripted client cannot grow it
// without limit. Manual skips never come close.
const maxSkippedRepos = 10000

var activity = newRepoActivity()

// Activity broadcast is debounced so rapid fetch/scan state changes are
// coalesced into at most a few pushes per second instead of flooding the
// dashboard with an event per goroutine mutation.
var (
	activityDirty bool
	activityMu    sync.Mutex
)

// afterChange marks the activity state dirty and schedules a coalesced
// broadcast of the latest snapshot.
func (a *RepoActivity) afterChange() {
	activityMu.Lock()
	if !activityDirty {
		activityDirty = true
		go func() {
			time.Sleep(200 * time.Millisecond)
			activityMu.Lock()
			activityDirty = false
			activityMu.Unlock()
			snap := a.Snapshot()
			broadcastFeed(FeedEvent{Type: "activity", Activity: &snap})
		}()
	}
	activityMu.Unlock()
}

func newRepoActivity() *RepoActivity {
	return &RepoActivity{
		fetching: make(map[string]time.Time),
		scanning: make(map[string]time.Time),
		skipped:  make(map[string]bool),
	}
}

// Skip abandons an in-flight repository. It leaves the activity lists at once so
// the dashboard drops the row, and is recorded so whichever worker holds it stops
// at its next checkpoint. Skipping a URL that is not in flight still records it,
// which covers the race where a worker is between checkpoints.
func (a *RepoActivity) Skip(url string) {
	a.mu.Lock()
	delete(a.fetching, url)
	delete(a.scanning, url)
	if len(a.skipped) < maxSkippedRepos {
		a.skipped[url] = true
	}
	a.mu.Unlock()
	a.afterChange()
}

// IsSkipped reports whether the operator abandoned this URL.
func (a *RepoActivity) IsSkipped(url string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.skipped[url]
}

// IncRateLimited bumps the rate-limited counter that the dashboard's Activity
// tab and the TUI status line both read. core's ProgressManager counts rate
// limits too, but nothing reads its counter; this is the one on screen.
func (a *RepoActivity) IncRateLimited() {
	a.mu.Lock()
	a.RateLimited++
	a.mu.Unlock()
	a.afterChange()
}

// StartFetch records that a repository retrieval (API metadata + clone) began.
func (a *RepoActivity) StartFetch(url string) {
	a.mu.Lock()
	a.fetching[url] = time.Now()
	a.TotalFetched++
	a.mu.Unlock()
	a.afterChange()
}

// StartScan moves a repository from the fetching set into the scanning set.
func (a *RepoActivity) StartScan(url string) {
	a.mu.Lock()
	delete(a.fetching, url)
	a.scanning[url] = time.Now()
	a.TotalCloned++
	a.mu.Unlock()
	a.afterChange()
}

// FinishScan removes a repository from the scanning set.
func (a *RepoActivity) FinishScan(url string) {
	a.mu.Lock()
	delete(a.scanning, url)
	a.TotalScanned++
	a.mu.Unlock()
	a.afterChange()
}

// FailRepo records a failed fetch/scan and removes the url from any set.
func (a *RepoActivity) FailRepo(url string) {
	a.mu.Lock()
	delete(a.fetching, url)
	delete(a.scanning, url)
	a.TotalFailed++
	a.mu.Unlock()
	a.afterChange()
}

// ActivityItem is a single in-flight repository in the snapshot.
type ActivityItem struct {
	URL   string `json:"url"`
	Since int64  `json:"since"` // seconds elapsed since it entered the phase
}

// ActivitySnapshot is a point-in-time view served by /api/activity.
type ActivitySnapshot struct {
	Fetching     []ActivityItem `json:"fetching"`
	Scanning     []ActivityItem `json:"scanning"`
	TotalFetched int64          `json:"total_fetched"`
	TotalCloned  int64          `json:"total_cloned"`
	TotalScanned int64          `json:"total_scanned"`
	TotalFailed  int64          `json:"total_failed"`
	RateLimited  int64          `json:"rate_limited"`
}

// Snapshot returns a consistent copy of the current activity state.
func (a *RepoActivity) Snapshot() ActivitySnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()

	s := ActivitySnapshot{
		TotalFetched: a.TotalFetched,
		TotalCloned:  a.TotalCloned,
		TotalScanned: a.TotalScanned,
		TotalFailed:  a.TotalFailed,
		RateLimited:  a.RateLimited,
		Fetching:     make([]ActivityItem, 0),
		Scanning:     make([]ActivityItem, 0),
	}
	now := time.Now()
	for url, t := range a.fetching {
		s.Fetching = append(s.Fetching, ActivityItem{URL: url, Since: int64(now.Sub(t).Seconds())})
	}
	for url, t := range a.scanning {
		s.Scanning = append(s.Scanning, ActivityItem{URL: url, Since: int64(now.Sub(t).Seconds())})
	}
	sort.Slice(s.Fetching, func(i, j int) bool { return s.Fetching[i].Since > s.Fetching[j].Since })
	sort.Slice(s.Scanning, func(i, j int) bool { return s.Scanning[i].Since > s.Scanning[j].Since })
	return s
}
