package proxy

import "testing"

// TestPoolReAdmitsBenchedMemberAfterCooldown verifies a benched proxy is
// re-admitted once the cooldown elapses, instead of being permanently disabled
// (which silently fell back to a direct connection forever).
func TestPoolReAdmitsBenchedMemberAfterCooldown(t *testing.T) {
	p, err := New([]string{"http://proxy-a:1"})
	if err != nil {
		t.Fatal(err)
	}
	p.SetBanAfter(1)

	u, ok := p.Next()
	if !ok {
		t.Fatal("first Next returned no proxy")
	}
	p.ReportFailure(u)

	if _, ok := p.Next(); ok {
		t.Fatal("a just-benched member was handed out within the cooldown")
	}

	// Simulate the cooldown elapsing.
	p.mu.Lock()
	p.banCooldown = 0
	p.mu.Unlock()

	if _, ok := p.Next(); !ok {
		t.Fatal("benched member was not re-admitted after the cooldown")
	}
}
