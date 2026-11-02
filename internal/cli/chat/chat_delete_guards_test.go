package chat

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// chainOf starts a chain of `depth` shell scripts, each the parent of the next, ending in a sleep, and returns
// the pid of the top script and of the process at the bottom.
func chainOf(t *testing.T, depth int) (top, bottom int) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the process tree comes from /proc")
	}
	dir := t.TempDir()
	for i := 0; i < depth; i++ {
		body := "#!/bin/sh\nsleep 60 &\nwait\n"
		if i > 0 {
			body = fmt.Sprintf("#!/bin/sh\n'%s' &\nwait\n", filepath.Join(dir, fmt.Sprintf("s%d.sh", i-1)))
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("s%d.sh", i)), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c := exec.Command(filepath.Join(dir, fmt.Sprintf("s%d.sh", depth-1)))
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	go func() { c.Wait(); close(reaped) }()
	t.Cleanup(func() { c.Process.Kill(); <-reaped })
	bottom = c.Process.Pid
	// depth scripts and one sleep: wait for the chain to exist, then walk down it.
	for level := 0; level < depth; level++ {
		var kids []int
		for i := 0; i < 200 && len(kids) == 0; i++ {
			kids = childrenOf(bottom)
			if len(kids) == 0 {
				time.Sleep(20 * time.Millisecond)
			}
		}
		if len(kids) == 0 {
			t.Fatalf("the chain is only %d deep", level)
		}
		bottom = kids[0]
	}
	return c.Process.Pid, bottom
}

// The runner is looked for at most eight levels below the launcher: a deeper one is not found (the turn's
// processes are then reported, not stopped), and one at the limit still is.
func TestTheLauncherBoundIsEightLevels(t *testing.T) {
	top, bottom := chainOf(t, 8) // the sleep is 8 levels below the top script
	if !descendantOf(bottom, top) {
		t.Error("a process 8 levels below the launcher was not found")
	}
	top9, bottom9 := chainOf(t, 9)
	if descendantOf(bottom9, top9) {
		t.Error("a process 9 levels below the launcher was found: the bound moved")
	}
}

// A conversation that has two records, one made before the turn's marker was recorded (no root: it cannot be
// shown to be the turn's) and one made now, loses what can be proven and keeps the conversation for the rest:
// the new one is stopped, the old one is listed as a survivor, nothing is deleted, and the answer says how
// many were stopped before the refusal. Stopping the old one by hand makes the next delete go through.
func TestMixedLegacyAndNewRecords(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	legacy := background(t)
	fresh := background(t)
	id := processIdentity(fresh)
	r.recordRefs(t, c.ID,
		chatevents.ProcessRef{Pid: legacy, Identity: processIdentity(legacy)}, // before the root was recorded
		chatevents.ProcessRef{Pid: fresh, Identity: id, Start: processStart(fresh), Root: id},
	)

	out, err := r.history(t, nil, "delete", c.ID)
	if exitCodeFor(err) != exitSurvivors {
		t.Fatalf("exit %d, want %d: %v\n%s", exitCodeFor(err), exitSurvivors, err, out)
	}
	var res struct {
		Deleted   bool       `json:"deleted"`
		Stopped   []int      `json:"stopped"`
		Survivors []survivor `json:"survivors"`
	}
	decodeChatJSON(t, out, &res)
	if res.Deleted || len(res.Stopped) != 1 || res.Stopped[0] != fresh || len(res.Survivors) != 1 || res.Survivors[0].Pid != legacy {
		t.Errorf("answer %s", out)
	}
	if !gone(fresh) {
		t.Error("the process that could be proven the turn's was not stopped")
	}
	if !processAlive(legacy) {
		t.Error("the process that could not be proven the turn's was signalled")
	}
	if _, err := r.store.GetConversation(c.ID, "default"); err != nil {
		t.Errorf("the conversation was deleted: %v", err)
	}
	// Once the old one is gone by hand, the same delete succeeds.
	if p, err := os.FindProcess(legacy); err == nil {
		p.Kill()
	}
	if !gone(legacy) {
		t.Fatal("the legacy process did not end")
	}
	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete after stopping it by hand: %v\n%s", err, out)
	}
}

// recordRefs records one finished turn of the conversation that left the given processes running.
func (r *accessRig) recordRefs(t *testing.T, convID string, refs ...chatevents.ProcessRef) {
	t.Helper()
	if _, _, err := r.store.CreateTurn(convID, "default", "mixed-1", "i", "go"); err != nil {
		t.Fatal(err)
	}
	pids := make([]int, len(refs))
	for i, ref := range refs {
		pids[i] = ref.Pid
	}
	n := chatevents.NoticePayload{Code: noticeCoderBackground, Severity: chatevents.SeverityWarning, Pids: pids, Processes: refs}
	if _, err := r.store.AppendEvent("default", convID, "mixed-1", chatevents.EventNotice, n); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.store.FinalizeTurn("default", convID, "mixed-1", chatevents.StatusCompleted, "", nil, false); err != nil {
		t.Fatal(err)
	}
}

// Deleting while a turn keeps recording processes: whatever the answer, it is consistent. A delete that
// reports success leaves no recorded process of its conversation running and no conversation (and a
// second delete finds nothing); one that reports an error never leaves the conversation deleted. Meant to be
// run with -race as well.
func TestDeleteRacingARecordingTurn(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process identity comes from /proc")
	}
	const rounds = 8
	for round := 0; round < rounds; round++ {
		r := newAccessRig(t, true)
		c := r.conv(t, ai.AccessFull)
		first := background(t)
		r.leaveBehind(t, c.ID, first)

		var mu sync.Mutex
		var recorded []int // processes whose record the store acknowledged
		recorded = append(recorded, first)
		stopRecording := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { // a turn that keeps leaving a process behind
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stopRecording:
					return
				default:
				}
				pid := background(t)
				turn := fmt.Sprintf("race-%d", i)
				if _, _, err := r.store.CreateTurn(c.ID, "default", turn, "i", "go"); err != nil {
					return
				}
				if _, err := r.store.AppendEvent("default", c.ID, turn, chatevents.EventNotice, backgroundNotice([]int{pid})); err != nil {
					return // the conversation was deleted: not recorded
				}
				mu.Lock()
				recorded = append(recorded, pid)
				mu.Unlock()
				if _, _, err := r.store.FinalizeTurn("default", c.ID, turn, chatevents.StatusCompleted, "", nil, false); err != nil {
					return
				}
				time.Sleep(2 * time.Millisecond)
			}
		}()

		// The delete runs while the turn above keeps recording (the command prints on the process's stdout,
		// so two deletes cannot run side by side in one process: the second one follows).
		out, err := r.history(t, nil, "delete", c.ID)
		close(stopRecording)
		wg.Wait()

		succeeded := 0
		if err == nil {
			succeeded = 1
		} else if code := exitCodeFor(err); code != 3 && code != 5 {
			t.Errorf("round %d: unexpected exit %d: %v\n%s", round, code, err, out)
		}
		// A second delete of a conversation that is gone is not found; of one that was kept, it can go through.
		if succeeded == 1 {
			if _, err := r.history(t, nil, "delete", c.ID); exitCodeFor(err) != 2 {
				t.Errorf("round %d: a second delete of a deleted conversation: exit %d (%v), want 2 (not found)", round, exitCodeFor(err), err)
			}
		}
		_, getErr := r.store.GetConversation(c.ID, "default")
		gone := getErr != nil
		if succeeded == 1 && !gone {
			t.Errorf("round %d: a delete reported success and the conversation is still there", round)
		}
		if succeeded == 0 && gone {
			t.Errorf("round %d: the delete reported an error and the conversation is gone", round)
		}
		if succeeded == 1 {
			mu.Lock()
			pids := append([]int(nil), recorded...)
			mu.Unlock()
			for _, pid := range pids {
				if !waitGone(pid) {
					t.Errorf("round %d: a delete reported success while the recorded process %d still runs", round, pid)
				}
			}
		}
	}
}

func waitGone(pid int) bool {
	for i := 0; i < 50; i++ {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
