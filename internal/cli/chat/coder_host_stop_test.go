package chat

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/coderhost"
)

type sigLog struct {
	mu    sync.Mutex
	terms int
	kills int
}

func (s *sigLog) term(int) error { s.mu.Lock(); s.terms++; s.mu.Unlock(); return nil }
func (s *sigLog) kill(int) error { s.mu.Lock(); s.kills++; s.mu.Unlock(); return nil }
func (s *sigLog) n() (int, int)  { s.mu.Lock(); defer s.mu.Unlock(); return s.terms, s.kills }

func newSigTurn(s *sigLog, strict bool) *execTurn {
	return &execTurn{pid: 4242, done: make(chan struct{}), term: s.term, kill: s.kill, strict: strict}
}

func waitKills(t *testing.T, s *sigLog, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, kills := s.n(); kills >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the group's stragglers were not killed (kills %d, want %d)", func() int { _, k := s.n(); return k }(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The ordinary (unbound) turn stops exactly as it always did, on every OS: whenever Stop comes, the group is terminated,
// and once the leader has been reaped (before or after the Stop) its stragglers are killed.
func TestAnOrdinaryTurnIsStoppedAsItAlwaysWas(t *testing.T) {
	var s sigLog
	running := newSigTurn(&s, false)
	running.Stop()
	if terms, _ := s.n(); terms != 1 {
		t.Fatalf("the group was terminated %d times, want 1", terms)
	}
	close(running.done) // the leader was reaped
	waitKills(t, &s, 1)

	s = sigLog{}
	ended := newSigTurn(&s, false)
	close(ended.done) // the leader ended and was reaped on its own
	ended.markReaped()
	ended.Stop() // a stop after that still signals the group, as the shipped launcher does
	if terms, _ := s.n(); terms != 1 {
		t.Fatalf("a stop after the natural end terminated the group %d times, want 1", terms)
	}
	waitKills(t, &s, 1)
}

// A bound (strict) turn: its held leader's group is signalled only before the reap, and a stop between the exit and
// the reap still ends the stragglers.
func TestABoundTurnsGroupIsNotSignalledByNumberAfterTheReap(t *testing.T) {
	var s sigLog
	stopped := newSigTurn(&s, true)
	stopped.Stop() // while the leader still runs
	stopped.settle()
	stopped.killGroupIfHeld() // the grace period ends after the exit: nothing more
	if terms, kills := s.n(); terms != 1 || kills != 1 {
		t.Fatalf("stopped before the exit: %d terminations, %d kills, want 1 and 1", terms, kills)
	}

	s = sigLog{}
	late := newSigTurn(&s, true)
	late.settle() // the leader exited (held, not yet reaped), nobody stopped it
	late.Stop()   // a stop in that window still ends the stragglers
	if terms, kills := s.n(); terms != 1 || kills != 1 {
		t.Fatalf("stopped between the exit and the reap: %d terminations, %d kills, want 1 and 1", terms, kills)
	}

	s = sigLog{}
	reaped := newSigTurn(&s, true)
	reaped.settle()
	reaped.markReaped() // the pid is free now
	reaped.Stop()
	reaped.killGroupIfHeld()
	close(reaped.done)
	time.Sleep(50 * time.Millisecond)
	if terms, kills := s.n(); terms != 0 || kills != 0 {
		t.Fatalf("stopped after the reap: %d terminations, %d kills, want none", terms, kills)
	}

	s = sigLog{}
	slow := newSigTurn(&s, true)
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
	turn := newSigTurn(&s, true)
	turn.markStopping()
	turn.settle()
	if _, kills := s.n(); kills != 1 {
		t.Fatalf("the group was killed %d times when the marked leader exited, want 1", kills)
	}
}

// The credential-pipe marker never reaches a turn through the host's own environment.
func TestTheTurnEnvironmentDropsTheCredentialMarkers(t *testing.T) {
	in := []string{"PATH=/bin", coderhost.CredFDEnv + "=7", coderhost.MissionFDEnv + "=3", coderhost.SessionTokenEnv + "=mst-x", agenttokens.EnvMissionToken + "=mat-x", "HOME=/h"}
	out := strings.Join(turnEnv(in), "\n")
	for _, name := range []string{coderhost.CredFDEnv, coderhost.MissionFDEnv, coderhost.SessionTokenEnv, agenttokens.EnvMissionToken} {
		if strings.Contains(out, name+"=") {
			t.Errorf("%s reached the turn's environment", name)
		}
	}
	if !strings.Contains(out, "PATH=/bin") || !strings.Contains(out, "HOME=/h") {
		t.Errorf("ordinary variables were dropped: %s", out)
	}
}

// A bound turn on a platform that cannot hold its leader (awaitExitNoReap reports false, so settle never runs) is stopped as an
// ordinary one: the group is terminated and, once the leader is reaped, its stragglers are killed.
func TestABoundTurnWhoseLeaderCannotBeHeldIsStoppedLikeAnOrdinaryOne(t *testing.T) {
	var s sigLog
	turn := newSigTurn(&s, true) // strict, but never settled: held stays false
	turn.Stop()
	if terms, _ := s.n(); terms != 1 {
		t.Fatalf("the group was terminated %d times, want 1", terms)
	}
	turn.markReaped()
	close(turn.done)
	waitKills(t, &s, 1)
}
