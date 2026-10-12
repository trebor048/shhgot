package aifilter

import "testing"

// TestQuickFilterDoesNotSuppressTestSubstring verifies a high-entropy candidate
// containing the substring "test" (but not the whole word) is not suppressed as
// a placeholder.
func TestQuickFilterDoesNotSuppressTestSubstring(t *testing.T) {
	got := QuickFilter("sk-contest-K7mQ2vNp9Rt4Wx6Lz3Bd8Gh5Jc1Fa", "", "main.go")
	if got.Suppress {
		t.Fatalf("Suppress = true (reason %q), want false for a 'test' substring", got.Reason)
	}
}
