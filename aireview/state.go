package aireview

import "fmt"

// State is the lifecycle state of an AI review job.
type State string

const (
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StatePaused    State = "paused"
	StateCancelled State = "cancelled"
	StateDone      State = "done"
	StateDud       State = "dud"
	StateFailed    State = "failed"
)

var transitions = map[State]map[State]bool{
	StateQueued:  {StateRunning: true, StateCancelled: true},
	StateRunning: {StatePaused: true, StateCancelled: true, StateDone: true, StateFailed: true, StateDud: true},
	StatePaused:  {StateRunning: true, StateCancelled: true},
}

// Transition reports whether moving from cur to next is allowed. Same-state
// transitions are always allowed (idempotent operations).
func Transition(cur, next State) error {
	if cur == next {
		return nil
	}
	if allowed, ok := transitions[cur]; ok && allowed[next] {
		return nil
	}
	return fmt.Errorf("invalid state transition %s -> %s", cur, next)
}
