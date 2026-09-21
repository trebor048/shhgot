package main

import "testing"

func snapshotURLs(items []ActivityItem) map[string]bool {
	have := make(map[string]bool, len(items))
	for _, i := range items {
		have[i.URL] = true
	}
	return have
}

// Skip has to do two things at once: take the repository out of the dashboard's
// activity lists immediately, and record it so the worker holding it stops at its
// next checkpoint. Doing only the first would leave the scan running on a row the
// operator can no longer see.
func TestSkipRemovesFromListsAndRecords(t *testing.T) {
	a := newRepoActivity()
	const fetching = "https://github.com/acme/fetching"
	const scanning = "https://github.com/acme/scanning"

	a.StartFetch(fetching)
	a.StartScan(scanning)

	if got := snapshotURLs(a.Snapshot().Fetching); !got[fetching] {
		t.Fatalf("fetching list should contain %q before the skip", fetching)
	}
	if got := snapshotURLs(a.Snapshot().Scanning); !got[scanning] {
		t.Fatalf("scanning list should contain %q before the skip", scanning)
	}

	a.Skip(fetching)
	a.Skip(scanning)

	snap := a.Snapshot()
	if got := snapshotURLs(snap.Fetching); got[fetching] {
		t.Errorf("%q should be gone from the fetching list after Skip", fetching)
	}
	if got := snapshotURLs(snap.Scanning); got[scanning] {
		t.Errorf("%q should be gone from the scanning list after Skip", scanning)
	}
	if !a.IsSkipped(fetching) || !a.IsSkipped(scanning) {
		t.Error("both skipped URLs should be recorded as skipped")
	}
	if a.IsSkipped("https://github.com/acme/never-skipped") {
		t.Error("a URL that was never skipped must not report as skipped")
	}
}

// A skip can arrive while a worker sits between checkpoints, so skipping a URL
// that is not currently in either list still has to be remembered - otherwise the
// worker would sail past the checkpoint and scan it anyway.
func TestSkipRecordsURLThatIsNotInFlight(t *testing.T) {
	a := newRepoActivity()
	const queued = "https://github.com/acme/queued"

	a.Skip(queued)

	if !a.IsSkipped(queued) {
		t.Fatal("skipping a URL that is not in flight must still record it")
	}
	if snap := a.Snapshot(); len(snap.Fetching) != 0 || len(snap.Scanning) != 0 {
		t.Errorf("skipping an unknown URL must not add it to any list: %+v", snap)
	}
}

// Skip is not a failure: a repository the operator abandoned deliberately must
// not be counted as one that failed to clone or scan.
func TestSkipDoesNotCountAsFailure(t *testing.T) {
	a := newRepoActivity()
	a.StartFetch("https://github.com/acme/one")
	a.Skip("https://github.com/acme/one")

	if snap := a.Snapshot(); snap.TotalFailed != 0 {
		t.Errorf("TotalFailed = %d, want 0 - a skip is deliberate, not a failure", snap.TotalFailed)
	}
}
