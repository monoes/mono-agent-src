package chat

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/storage"
)

// Deleting a conversation stops the processes its turns left running: the
// events that name them are deleted with it, so nothing could find them later.
func TestDeleteStopsTheBackgroundProcessesOfItsTurns(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	pid := background(t)
	r.leaveBehind(t, c.ID, pid)

	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if !gone(pid) {
		t.Error("the process the conversation left running survived its deletion")
	}
	if _, err := r.store.GetConversation(c.ID, "default"); err == nil {
		t.Error("the conversation is still there")
	}
}

// A process that cannot be shown to be the turn's own is not stopped, and the
// record of it stays: the delete is refused with exit 5 (survivors_remain),
// and goes through once nothing is left running.
func TestDeleteKeepsTheRecordWhileAProcessSurvives(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	c2 := exec.Command("sleep", "60") // no exec-tree token: no identity
	if err := c2.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c2.Process.Kill(); c2.Wait() })
	r.leaveBehind(t, c.ID, c2.Process.Pid)

	out, err := r.history(t, nil, "delete", c.ID)
	if exitCodeFor(err) != exitSurvivors {
		t.Fatalf("exit %d, want %d: %v\n%s", exitCodeFor(err), exitSurvivors, err, out)
	}
	var ce *coderError
	if !errors.As(err, &ce) || ce.code != "survivors_remain" {
		t.Errorf("error %v is not survivors_remain", err)
	}
	if !strings.Contains(out, `"deleted": false`) || !strings.Contains(out, "sleep 60") {
		t.Errorf("the result does not name the survivor: %s", out)
	}
	if _, err := r.store.GetConversation(c.ID, "default"); err != nil {
		t.Fatalf("the conversation was deleted with a process still running: %v", err)
	}
	if !processAlive(c2.Process.Pid) {
		t.Error("an unidentified process was killed")
	}

	c2.Process.Kill()
	c2.Wait()
	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete once the process is gone: %v\n%s", err, out)
	}
}

// A delete that is refused (a turn is still active) stops nothing.
func TestRefusedDeleteStopsNoProcess(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	pid := background(t)
	r.leaveBehind(t, c.ID, pid)
	if _, _, err := r.store.CreateTurn(c.ID, "default", "live-1", "i", "go"); err != nil {
		t.Fatal(err)
	}

	if _, err := r.history(t, nil, "delete", c.ID); exitCodeFor(err) != 3 {
		t.Fatalf("delete with an active turn: exit %d, want 3 (%v)", exitCodeFor(err), err)
	}
	if !processAlive(pid) {
		t.Error("a refused delete stopped a process")
	}
	if _, err := r.store.GetConversation(c.ID, "default"); err != nil {
		t.Errorf("a refused delete removed the conversation: %v", err)
	}
}

// A record whose pid now belongs to another process (the turn's process is gone and the pid was
// reused: its start time is not the recorded one) is stale: it is dropped with the conversation,
// the unrelated process is never signalled, and it does not make the conversation undeletable.
func TestDeleteDropsAStaleRecordAndNeverSignalsTheNewOwnerOfThePid(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	other := startStranger(t)
	r.recordRef(t, c.ID, chatevents.ProcessRef{Pid: other.Process.Pid, Identity: "tree:the-turn-that-is-gone", Start: "aabbccddeeff:1"})

	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("a stale record blocked the delete: %v\n%s", err, out)
	}
	if !processAlive(other.Process.Pid) {
		t.Error("the process that now owns the pid was signalled")
	}
}

// An identity that no longer matches is not proof of a reused pid: a left-running process can
// change its own environment (exec under a clean one). With the recorded start time unchanged it is
// still the same process, so it stays a survivor and the conversation is kept.
func TestDeleteKeepsAProcessWhoseEnvironmentChanged(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	same := startStranger(t)
	start := processStart(same.Process.Pid)
	if start == "" {
		t.Skip("no start time on this platform")
	}
	r.recordRef(t, c.ID, chatevents.ProcessRef{Pid: same.Process.Pid, Identity: "tree:what-it-had-before", Start: start})

	out, err := r.history(t, nil, "delete", c.ID)
	if exitCodeFor(err) != exitSurvivors {
		t.Fatalf("exit %d, want %d: %v\n%s", exitCodeFor(err), exitSurvivors, err, out)
	}
	if _, err := r.store.GetConversation(c.ID, "default"); err != nil {
		t.Errorf("the conversation was deleted with its process still running: %v", err)
	}
	if !processAlive(same.Process.Pid) {
		t.Error("a process that could not be shown to be someone else's was signalled")
	}
}

// startStranger starts a process that carries no exec-tree token and is reaped when it dies, so a
// signal sent to it shows as the end of the process.
func startStranger(t *testing.T) *exec.Cmd {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("a process's start time comes from /proc")
	}
	c := exec.Command("sleep", "60")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	go func() { c.Wait(); close(reaped) }()
	t.Cleanup(func() { c.Process.Kill(); <-reaped })
	return c
}

// recordRef records a turn that ended with ref still running.
func (r *accessRig) recordRef(t *testing.T, convID string, ref chatevents.ProcessRef) {
	t.Helper()
	if _, _, err := r.store.CreateTurn(convID, "default", "left-1", "i", "go"); err != nil {
		t.Fatal(err)
	}
	n := chatevents.NoticePayload{Code: noticeCoderBackground, Severity: chatevents.SeverityWarning, Processes: []chatevents.ProcessRef{ref}}
	if _, err := r.store.AppendEvent("default", convID, "left-1", chatevents.EventNotice, n); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.store.FinalizeTurn("default", convID, "left-1", chatevents.StatusCompleted, "", nil, false); err != nil {
		t.Fatal(err)
	}
}

// A process a turn recorded while the delete was stopping the others is stopped too:
// the record is collected again after each stop.
func TestDeleteStopsAProcessRecordedWhileItWasStopping(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	first := background(t)
	r.leaveBehind(t, c.ID, first)
	late := make(chan int, 1)
	rounds := 0
	afterBackgroundRound = func() {
		rounds++
		if rounds != 1 {
			return
		}
		pid := background(t)
		late <- pid
		if _, _, err := r.store.CreateTurn(c.ID, "default", "late-1", "i", "go"); err != nil {
			t.Error(err)
		}
		if _, err := r.store.AppendEvent("default", c.ID, "late-1", chatevents.EventNotice, backgroundNotice([]int{pid})); err != nil {
			t.Error(err)
		}
		if _, _, err := r.store.FinalizeTurn("default", c.ID, "late-1", chatevents.StatusCompleted, "", nil, false); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterBackgroundRound = func() {} })

	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	var lateOne int
	select {
	case lateOne = <-late:
	default:
		t.Fatal("the delete never stopped a round of processes")
	}
	if !gone(first) || !gone(lateOne) {
		t.Error("a process survived the deletion")
	}
}

// The success output says what was stopped.
func TestDeleteListsWhatItStopped(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	pid := background(t)
	r.leaveBehind(t, c.ID, pid)

	out, err := r.history(t, nil, "delete", c.ID)
	if err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	var res struct {
		Deleted bool  `json:"deleted"`
		Stopped []int `json:"stopped"`
	}
	decodeChatJSON(t, out, &res)
	if !res.Deleted || len(res.Stopped) != 1 || res.Stopped[0] != pid {
		t.Errorf("result %s does not list the stopped process %d", out, pid)
	}
}

// A process that carries ANOTHER turn's marker is never signalled and is reported: the
// record is the turn's only if the marker is the one the host read from the turn's own runtime.
func TestDeleteNeverSignalsAProcessWithAnotherTurnsMarker(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	pid := background(t) // carries "tree:tok-<test>", which is not the turn's
	r.recordRef(t, c.ID, chatevents.ProcessRef{Pid: pid, Identity: processIdentity(pid), Start: processStart(pid), Root: "tree:the-turns-own-marker"})

	out, err := r.history(t, nil, "delete", c.ID)
	if exitCodeFor(err) != exitSurvivors {
		t.Fatalf("exit %d, want %d: %v\n%s", exitCodeFor(err), exitSurvivors, err, out)
	}
	if !processAlive(pid) {
		t.Error("a process with another turn's marker was signalled")
	}
	if _, err := r.store.GetConversation(c.ID, "default"); err != nil {
		t.Errorf("the conversation was deleted: %v", err)
	}
}

// A turn whose own marker the host could not read has no process it can show to be its own.
func TestDeleteSignalsNothingWhenTheTurnsMarkerWasNotRead(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	pid := background(t)
	r.recordRef(t, c.ID, chatevents.ProcessRef{Pid: pid, Identity: processIdentity(pid), Start: processStart(pid)}) // no Root

	if out, err := r.history(t, nil, "delete", c.ID); exitCodeFor(err) != exitSurvivors {
		t.Fatalf("exit %d, want %d: %v\n%s", exitCodeFor(err), exitSurvivors, err, out)
	}
	if !processAlive(pid) {
		t.Error("a process was signalled without the turn's marker")
	}
}

// The right marker with another start time is a process that got the pid later: not signalled,
// and it does not block the delete.
func TestDeleteNeverSignalsARightMarkerWithAnotherStartTime(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	pid := background(t)
	id := processIdentity(pid)
	r.recordRef(t, c.ID, chatevents.ProcessRef{Pid: pid, Identity: id, Start: "aabbccddeeff:1", Root: id})

	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if !processAlive(pid) {
		t.Error("a process that is not the recorded one was signalled")
	}
}

// A process that cannot be pinned (no pidfd) is not stopped: it is reported.
func TestDeleteSignalsNothingThatCannotBePinned(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	pid := background(t)
	r.leaveBehind(t, c.ID, pid)
	old := pinProcess
	pinProcess = func(int) (pinnedProcess, error) { return nil, errCannotPin }
	t.Cleanup(func() { pinProcess = old })

	if out, err := r.history(t, nil, "delete", c.ID); exitCodeFor(err) != exitSurvivors {
		t.Fatalf("exit %d, want %d: %v\n%s", exitCodeFor(err), exitSurvivors, err, out)
	}
	if !processAlive(pid) {
		t.Error("a process that could not be pinned was signalled")
	}
}

// A turn that records a process after the delete has stopped the others but before it commits is not
// lost: the delete re-reads the record inside its write transaction, gives way, and stops it too.
func TestDeleteStopsAProcessRecordedBeforeTheDeleteCommits(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	first := background(t)
	r.leaveBehind(t, c.ID, first)
	late := make(chan int, 1)
	calls := 0
	beforeGuardedDelete = func() {
		calls++
		if calls != 1 {
			return
		}
		pid := background(t)
		late <- pid
		if _, _, err := r.store.CreateTurn(c.ID, "default", "late-1", "i", "go"); err != nil {
			t.Error(err)
		}
		if _, err := r.store.AppendEvent("default", c.ID, "late-1", chatevents.EventNotice, backgroundNotice([]int{pid})); err != nil {
			t.Error(err)
		}
		if _, _, err := r.store.FinalizeTurn("default", c.ID, "late-1", chatevents.StatusCompleted, "", nil, false); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { beforeGuardedDelete = func() {} })

	out, err := r.history(t, nil, "delete", c.ID)
	if err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	var lateOne int
	select {
	case lateOne = <-late:
	default:
		t.Fatal("the seam never ran")
	}
	if !gone(first) || !gone(lateOne) {
		t.Error("a process survived the deletion")
	}
	if _, err := r.store.GetConversation(c.ID, "default"); err == nil {
		t.Error("the conversation is still there")
	}
}

// A turn process that has ended but is not reaped yet (a zombie) runs nothing: it is gone, not a
// survivor that blocks the delete.
func TestDeleteTreatsAZombieAsGone(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	// The shell starts a short child and becomes a process that never reaps it.
	parent := exec.Command("bash", "-c", "sleep 0.1 & exec sleep 60")
	if err := parent.Start(); err != nil {
		t.Skip("no bash: " + err.Error())
	}
	reaped := make(chan struct{})
	go func() { parent.Wait(); close(reaped) }()
	t.Cleanup(func() { parent.Process.Kill(); <-reaped })
	var zombie int
	for i := 0; i < 100 && zombie == 0; i++ {
		time.Sleep(50 * time.Millisecond)
		for _, k := range childrenOf(parent.Process.Pid) {
			if procStateLetter(k) == 'Z' {
				zombie = k
			}
		}
	}
	if zombie == 0 {
		t.Skip("no zombie to test with")
	}
	r.recordRef(t, c.ID, chatevents.ProcessRef{Pid: zombie, Identity: "tree:x", Root: "tree:x", Start: processStart(zombie)})

	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("a zombie blocked the delete: %v\n%s", err, out)
	}
}

// A Start recorded in another form (an unhashed boot id) is not evidence that the pid was reused: the
// record is not dropped as stale, and the process that still matches is stopped as usual.
func TestDeleteDoesNotDropARecordWithAnIncomparableStart(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	pid := background(t)
	id := processIdentity(pid)
	r.recordRef(t, c.ID, chatevents.ProcessRef{Pid: pid, Identity: id, Root: id, Start: "3f2c1e0a-9b8d-4c7e-a1b2-c3d4e5f60718:123"})

	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if !gone(pid) {
		t.Error("a process that still matches was dropped from the record while running")
	}
}

// The mission tokens a conversation's runs minted end with it: they are revoked by owner in the same
// transaction as the delete, whether or not a later conductor would have swept them. Another
// conversation's, and an unrelated owner's, are untouched.
func TestDeleteRevokesTheMissionTokensOfItsRuns(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	other := r.conv(t, ai.AccessFull)
	db, err := storage.NewDatabase(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	at := agenttokens.NewStore(db.DB)
	ctx := context.Background()
	mint := func(owner string) string {
		t.Helper()
		tok, _, err := at.CreateMission(ctx, "default", agenttokens.MissionBinding{Session: 1, Task: 1, Role: agenttokens.RoleWorker, Actor: "w", Owner: owner}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return tok.ID
	}
	own := mint(dynorg.TurnOwner("default", c.ID, "t1"))
	ownRun := mint(dynorg.ConversationOwner("default", c.ID))
	others := mint(dynorg.TurnOwner("default", other.ID, "t1"))
	// A token of ANOTHER profile that happens to carry the same owner string is not this profile's.
	foreign, _, err := at.CreateMission(ctx, "second", agenttokens.MissionBinding{Session: 1, Task: 1, Role: agenttokens.RoleWorker, Actor: "w", Owner: dynorg.TurnOwner("default", c.ID, "t9")}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	stranger := mint("run-unrelated")

	// An already revoked token keeps the time it was revoked.
	if _, err := at.Revoke(ctx, "default", own); err != nil {
		t.Fatal(err)
	}
	before, _ := at.Get(ctx, "default", own)
	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if _, err := r.store.GetConversation(c.ID, "default"); err == nil {
		t.Error("the conversation is still there")
	}
	if after, _ := at.Get(ctx, "default", own); before.RevokedAt == nil || after.RevokedAt == nil || !after.RevokedAt.Equal(*before.RevokedAt) {
		t.Errorf("a token revoked earlier changed its revocation time: %v -> %v", before.RevokedAt, after.RevokedAt)
	}
	revoked := func(id string) bool {
		t.Helper()
		tok, err := at.Get(ctx, "default", id)
		if err != nil {
			t.Fatal(err)
		}
		return tok.RevokedAt != nil
	}
	if !revoked(own) || !revoked(ownRun) {
		t.Error("a mission token of the deleted conversation's runs is still valid")
	}
	if f, err := at.Get(ctx, "second", foreign.ID); err != nil || f.RevokedAt != nil {
		t.Errorf("another profile's mission token with the same owner was revoked (%v)", err)
	}
	if revoked(others) || revoked(stranger) {
		t.Error("a mission token of another conversation or owner was revoked")
	}
}
