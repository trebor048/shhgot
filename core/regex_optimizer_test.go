package core

import "testing"

// CompilePattern used to cache by the regex string alone, so two signatures
// that share a regex silently collapsed into the first one's entry: the second
// signature's name/priority/part were lost and the priority buckets in Stats
// under-counted. Each signature must keep its own metadata.
func TestCompilePatternKeepsPerSignatureMetadata(t *testing.T) {
	ro := NewRegexOptimizer(4, 1000)

	const expr = `AKIA[0-9A-Z]{16}`
	high, err := ro.CompilePattern(expr, "AWS Access Key ID", 3, "contents")
	if err != nil {
		t.Fatalf("compile high priority: %v", err)
	}
	low, err := ro.CompilePattern(expr, "AWS Key (low)", 1, "contents")
	if err != nil {
		t.Fatalf("compile low priority: %v", err)
	}

	if high == low {
		t.Fatal("two signatures sharing a regex collapsed into one pattern entry")
	}
	if high.Name != "AWS Access Key ID" || high.Priority != 3 || high.Part != "contents" {
		t.Errorf("first pattern metadata = %+v", high)
	}
	if low.Name != "AWS Key (low)" || low.Priority != 1 || low.Part != "contents" {
		t.Errorf("second pattern metadata = %+v", low)
	}
	// The regex string is still compiled once: both signatures share it.
	if high.Pattern == nil || low.Pattern == nil || high.Pattern != low.Pattern {
		t.Error("the same regex string was not shared between the two signatures")
	}

	// Recompiling an identical signature is idempotent: no duplicate entry.
	again, err := ro.CompilePattern(expr, "AWS Access Key ID", 3, "contents")
	if err != nil {
		t.Fatalf("recompile identical signature: %v", err)
	}
	if again != high {
		t.Error("an identical signature produced a second pattern entry")
	}

	stats := ro.Stats()
	if got := stats["patterns_total"]; got != 2 {
		t.Errorf("patterns_total = %v, want 2", got)
	}
	if got := stats["patterns_compiled"]; got != 2 {
		t.Errorf("patterns_compiled = %v, want 2", got)
	}
	if got := stats["priority_1"]; got != 1 {
		t.Errorf("priority_1 = %v, want 1", got)
	}
	if got := stats["priority_3"]; got != 1 {
		t.Errorf("priority_3 = %v, want 1", got)
	}
}
