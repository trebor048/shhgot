package aireview

import "testing"

func TestTransition(t *testing.T) {
	ok := []struct{ cur, next State }{
		{StateQueued, StateRunning},
		{StateQueued, StateCancelled},
		{StateRunning, StatePaused},
		{StateRunning, StateDone},
		{StateRunning, StateFailed},
		{StateRunning, StateDud},
		{StatePaused, StateRunning},
		{StatePaused, StateCancelled},
		{StateRunning, StateRunning},
	}
	for _, tc := range ok {
		if err := Transition(tc.cur, tc.next); err != nil {
			t.Errorf("Transition(%s -> %s) = %v, want nil", tc.cur, tc.next, err)
		}
	}
	bad := []struct{ cur, next State }{
		{StateQueued, StateDone},
		{StateQueued, StatePaused},
		{StateDone, StateRunning},
		{StateFailed, StateRunning},
		{StateCancelled, StateRunning},
		{StateDud, StateRunning},
	}
	for _, tc := range bad {
		if err := Transition(tc.cur, tc.next); err == nil {
			t.Errorf("Transition(%s -> %s) = nil, want error", tc.cur, tc.next)
		}
	}
}
