package chat

import (
	"sync"
	"testing"
	"time"
)

type sigLog struct {
	mu    sync.Mutex
	terms int
	kills int
}

func (s *sigLog) term(int) error { s.mu.Lock(); s.terms++; s.mu.Unlock(); return nil }
func (s *sigLog) kill(int) error { s.mu.Lock(); s.kills++; s.mu.Unlock(); return nil }
func (s *sigLog) n() (int, int)  { s.mu.Lock(); defer s.mu.Unlock(); return s.terms, s.kills }

func newSigTurn(s *sigLog) *execTurn {
	return &execTurn{pid: 4242, done: make(chan struct{}), term: s.term, kill: s.kill}
}

// Where the leader cannot be held (no pidfd/waitid support: macOS, Windows) a stop behaves exactly as it always did,
// whenever it comes: the group is terminated, and once the leader is reaped the stragglers are killed.
func TestAStopWhereTheLeaderIsNotHeldSignalsTheGroupAsItAlwaysDid(t *testing.T) {
	var s sigLog
	turn := newSigTurn(&s) // held stays false: the waiter never settled
	turn.Stop()
	if terms, _ := s.n(); terms != 1 {
		t.Fatalf("the group was terminated %d times, want 1", terms)
	}
	close(turn.done) // the leader was reaped
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, kills := s.n(); kills == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stragglers of the group were not killed once the leader was reaped")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Where the leader is held, the stragglers die with its exit before the reap, and nothing is signalled by number after.
func TestAHeldLeadersGroupIsSignalledOnlyBeforeTheReap(t *testing.T) {
	var s sigLog
	stopped := newSigTurn(&s)
	stopped.Stop() // while the leader still runs
	stopped.settle()
	stopped.killGroupIfHeld() // the grace period ends after the exit: nothing more
	if terms, kills := s.n(); terms != 1 || kills != 1 {
		t.Fatalf("stopped before the exit: %d terminations, %d kills, want 1 and 1", terms, kills)
	}

	s = sigLog{}
	late := newSigTurn(&s)
	late.settle() // the leader exited (held, not yet reaped), nobody stopped it
	late.Stop()   // a stop in that window still ends the stragglers, as it always did
	if terms, kills := s.n(); terms != 1 || kills != 1 {
		t.Fatalf("stopped between the exit and the reap: %d terminations, %d kills, want 1 and 1", terms, kills)
	}

	s = sigLog{}
	reaped := newSigTurn(&s)
	reaped.settle()
	reaped.markReaped() // the pid is free now
	reaped.Stop()
	reaped.killGroupIfHeld()
	if terms, kills := s.n(); terms != 0 || kills != 0 {
		t.Fatalf("stopped after the reap: %d terminations, %d kills, want none", terms, kills)
	}

	s = sigLog{}
	slow := newSigTurn(&s)
	slow.held = true
	slow.Stop()
	slow.killGroupIfHeld() // the grace period ended with the leader still unreaped
	if _, kills := s.n(); kills != 1 {
		t.Fatalf("grace expiry with the leader unreaped: %d kills, want 1", kills)
	}
}

// A failed bound delivery kills the leader itself; the exit that follows must take the group with it even though Stop
// has not been called yet.
func TestAKilledLeadersGroupGoesWithItWhenTheLauncherMarkedTheStopFirst(t *testing.T) {
	var s sigLog
	turn := newSigTurn(&s)
	turn.markStopping()
	turn.settle()
	if _, kills := s.n(); kills != 1 {
		t.Fatalf("the group was killed %d times when the marked leader exited, want 1", kills)
	}
}
