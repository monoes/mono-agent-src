package chat

import (
	"sync"
	"testing"
)

type killLog struct {
	mu   sync.Mutex
	pids []int
}

func (k *killLog) kill(pid int) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.pids = append(k.pids, pid)
	return nil
}

func (k *killLog) n() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.pids)
}

// The group of a stopped turn is killed by number only while its leader is still unreaped; once the leader has been
// settled, no later kill by number may happen (the number may name an unrelated group by then).
func TestAStoppedTurnsGroupIsKilledOnlyWhileTheLeaderIsHeld(t *testing.T) {
	var k killLog
	held := &execTurn{pid: 4242, done: make(chan struct{})}
	held.stopping = true
	held.killGroupIfHeld(k.kill)
	if k.n() != 1 {
		t.Fatalf("a held leader's group was not killed at the end of the grace: %v", k.pids)
	}

	k = killLog{}
	gone := &execTurn{pid: 4242, done: make(chan struct{})}
	gone.settle(k.kill) // the leader exited, Stop was never asked for: nothing to kill
	gone.killGroupIfHeld(k.kill)
	if k.n() != 0 {
		t.Fatalf("a group was killed after the leader's exit without a stop: %v", k.pids)
	}

	k = killLog{}
	stopped := &execTurn{pid: 4242, done: make(chan struct{})}
	stopped.stopping = true
	stopped.settle(k.kill) // exit seen while still unreaped: the stragglers go with it, once
	stopped.killGroupIfHeld(k.kill)
	if k.n() != 1 {
		t.Fatalf("a stopped turn's group was killed %d times, want exactly 1 (before the reap): %v", k.n(), k.pids)
	}
}
