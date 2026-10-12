package core

import (
	"fmt"
	"testing"
	"time"
)

// FileScanned must turn the per-repository counter into a percentage once the
// scan's file total is known. Nothing ever set ProgressPercent, so the
// dashboard's scan bar and the "scanning N%" status line were pinned at zero.
func TestScanProgressReportsPercentage(t *testing.T) {
	sp := &ScanProgress{}
	sp.BeginScan("https://example.com/repo")
	sp.SetTotalFiles(4)

	for i := 0; i < 2; i++ {
		sp.FileScanned(fmt.Sprintf("file-%d", i))
	}
	if got := sp.GetSnapshot()["progress"]; got != 50 {
		t.Fatalf("progress at 2/4 = %v, want 50", got)
	}

	for i := 2; i < 4; i++ {
		sp.FileScanned(fmt.Sprintf("file-%d", i))
	}
	snap := sp.GetSnapshot()
	if got := snap["progress"]; got != 100 {
		t.Fatalf("progress at 4/4 = %v, want 100", got)
	}
	if got := snap["files_processed"]; got != int64(4) {
		t.Fatalf("files_processed = %v, want 4", got)
	}
}

// A new repository resets the per-repo percentage but not the cumulative file
// counter the dashboard shows as a running total.
func TestScanProgressResetsPerRepository(t *testing.T) {
	sp := &ScanProgress{}
	sp.BeginScan("a")
	sp.SetTotalFiles(2)
	sp.FileScanned("x")
	sp.FileScanned("y")

	sp.BeginScan("b")
	snap := sp.GetSnapshot()
	if got := snap["progress"]; got != 0 {
		t.Fatalf("progress after BeginScan = %v, want 0", got)
	}
	if got := snap["files_processed"]; got != int64(2) {
		t.Fatalf("files_processed = %v, want 2 (cumulative across repos)", got)
	}
}

// With no known total the percentage must stay at zero rather than divide by
// zero or report nonsense.
func TestScanProgressWithoutTotalStaysZero(t *testing.T) {
	sp := &ScanProgress{}
	sp.BeginScan("repo")
	sp.FileScanned("file")
	if got := sp.GetSnapshot()["progress"]; got != 0 {
		t.Fatalf("progress with no total = %v, want 0", got)
	}
}

// GetSnapshot must carry the "errors" and "estimated_time_remaining" fields the
// Activity panel reads. It emitted neither, so the Errors tile was pinned at
// zero and the ETA never rendered.
func TestScanProgressSnapshotCarriesErrorsAndETA(t *testing.T) {
	sp := &ScanProgress{}
	sp.BeginScan("repo")
	sp.SetTotalFiles(4)
	sp.FileScanned("a")

	snap := sp.GetSnapshot()
	if errs, ok := snap["errors"]; !ok || errs != int64(0) {
		t.Fatalf("snapshot errors = %v (present=%v), want 0", errs, ok)
	}
	if _, ok := snap["estimated_time_remaining"]; !ok {
		t.Fatal("snapshot is missing estimated_time_remaining")
	}
}

// RecordError must feed the snapshot's Errors tile.
func TestRecordErrorIncrementsSnapshot(t *testing.T) {
	sp := &ScanProgress{}
	sp.RecordError()
	sp.RecordError()
	if got := sp.GetSnapshot()["errors"]; got != int64(2) {
		t.Fatalf("errors after two RecordError calls = %v, want 2", got)
	}
}

// A finished scan must not report a stale ETA: the estimate only makes sense
// while files are still being processed.
func TestEstimateRemainingSeconds(t *testing.T) {
	cases := []struct {
		name    string
		done    int64
		total   int64
		elapsed time.Duration
		want    int
	}{
		{"half done at 1 file/s", 10, 20, 10 * time.Second, 10},
		{"unknown total", 5, 0, 5 * time.Second, 0},
		{"no progress yet", 0, 20, 5 * time.Second, 0},
		{"already complete", 20, 20, 5 * time.Second, 0},
		{"no elapsed time", 5, 10, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := estimateRemainingSeconds(c.done, c.total, c.elapsed); got != c.want {
				t.Fatalf("estimateRemainingSeconds(%d, %d, %s) = %d, want %d",
					c.done, c.total, c.elapsed, got, c.want)
			}
		})
	}
}

// While idle the snapshot must not surface an ETA even if the last repository
// stopped before finishing (a timeout or operator skip leaves total > done).
func TestScanProgressIdleReportsNoETA(t *testing.T) {
	sp := &ScanProgress{}
	sp.BeginScan("repo")
	sp.SetTotalFiles(10)
	sp.FileScanned("a")
	sp.EndScan()

	if got := sp.GetSnapshot()["estimated_time_remaining"]; got != 0 {
		t.Fatalf("idle estimated_time_remaining = %v, want 0", got)
	}
}
