package aiproviders

import (
	"errors"
	"testing"
)

// TestStreamVerdictCompletionWithoutTextIsEmpty verifies a completion marker
// with no text is treated as an empty response, matching Chat.
func TestStreamVerdictCompletionWithoutTextIsEmpty(t *testing.T) {
	if err := streamVerdict("p", false, true); !errors.Is(err, ErrEmptyResponse) {
		t.Fatalf("streamVerdict(false, true) = %v, want ErrEmptyResponse", err)
	}
	if err := streamVerdict("p", true, true); err != nil {
		t.Fatalf("streamVerdict(true, true) = %v, want nil", err)
	}
}
