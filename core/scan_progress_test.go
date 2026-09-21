package core

import (
	"fmt"
	"testing"
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
