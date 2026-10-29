package chat

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// --abandon-processes deletes the conversation and leaves the processes its turns left running: nothing
// is signalled, and what is left is listed.
func TestAbandonProcessesDeletesAndSignalsNothing(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	pid := background(t)
	r.leaveBehind(t, c.ID, pid)

	out, err := r.history(t, nil, "delete", c.ID, "--abandon-processes", "--confirm", abandonConfirmPhrase)
	if err != nil {
		t.Fatalf("delete --abandon-processes: %v\n%s", err, out)
	}
	var res struct {
		Deleted   bool       `json:"deleted"`
		Stopped   []int      `json:"stopped"`
		Abandoned []survivor `json:"abandoned"`
	}
	decodeChatJSON(t, out, &res)
	if !res.Deleted || len(res.Stopped) != 0 || len(res.Abandoned) != 1 || res.Abandoned[0].Pid != pid {
		t.Errorf("result %s", out)
	}
	if !processAlive(pid) {
		t.Error("a process was signalled by a delete that was told to leave it running")
	}
	if _, err := r.store.GetConversation(c.ID, "default"); err == nil {
		t.Error("the conversation is still there")
	}
}

// The delete that normally keeps the conversation (a process that cannot be shown to be the turn's
// own, exit 5) goes through with --abandon-processes.
func TestAbandonProcessesDeletesWhereTheNormalDeleteIsRefused(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	stranger := exec.Command("sleep", "60") // no exec-tree token: cannot be stopped
	if err := stranger.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stranger.Process.Kill(); stranger.Wait() })
	r.leaveBehind(t, c.ID, stranger.Process.Pid)

	if _, err := r.history(t, nil, "delete", c.ID); exitCodeFor(err) != exitSurvivors {
		t.Fatalf("the normal delete: exit %d, want %d", exitCodeFor(err), exitSurvivors)
	}
	if out, err := r.history(t, nil, "delete", c.ID, "--abandon-processes", "--confirm", abandonConfirmPhrase); err != nil {
		t.Fatalf("with --abandon-processes: %v\n%s", err, out)
	}
	if !processAlive(stranger.Process.Pid) {
		t.Error("the process was signalled")
	}
}

// Leaving processes running is the operator's: an agent, the chat or a workflow is refused before anything
// is deleted, and nothing is signalled.
func TestAbandonProcessesIsTheOperatorsAlone(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	pid := background(t)
	r.leaveBehind(t, c.ID, pid)

	out, err := r.history(t, errNotOperator, "delete", c.ID, "--abandon-processes", "--confirm", abandonConfirmPhrase)
	if exitCodeFor(err) != 3 {
		t.Fatalf("an agent's --abandon-processes: exit %d (%v)\n%s", exitCodeFor(err), err, out)
	}
	if _, err := r.store.GetConversation(c.ID, "default"); err != nil {
		t.Errorf("the conversation was deleted: %v", err)
	}
	if !processAlive(pid) {
		t.Error("a process was signalled")
	}
}

// Without the words nothing is deleted: the processes that would be left running are listed, and the
// message says what to add.
func TestAbandonProcessesNeedsTheWordsAndPreviewsWithout(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	pid := background(t)
	r.leaveBehind(t, c.ID, pid)

	for _, args := range [][]string{
		{"--abandon-processes"},
		{"--abandon-processes", "--confirm", "yes"},
		{"--abandon-processes", "--confirm", strings.ToUpper(abandonConfirmPhrase)},
	} {
		out, err := r.history(t, nil, append([]string{"delete", c.ID}, args...)...)
		if exitCodeFor(err) != 3 || !strings.Contains(err.Error(), abandonConfirmPhrase) {
			t.Fatalf("%v: exit %d (%v)", args, exitCodeFor(err), err)
		}
		var res struct {
			Deleted      bool       `json:"deleted"`
			WouldAbandon []survivor `json:"would_abandon"`
		}
		decodeChatJSON(t, out, &res)
		if res.Deleted || len(res.WouldAbandon) != 1 || res.WouldAbandon[0].Pid != pid {
			t.Errorf("%v: preview %s", args, out)
		}
		if _, err := r.store.GetConversation(c.ID, "default"); err != nil {
			t.Fatalf("%v: the conversation was deleted: %v", args, err)
		}
	}
	if !processAlive(pid) {
		t.Error("a process was signalled")
	}
}

// --confirm alone does nothing and says so; the plain delete is as it was.
func TestConfirmWithoutAbandonIsRefusedAndThePlainDeleteIsUnchanged(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	if _, err := r.history(t, nil, "delete", c.ID, "--confirm", abandonConfirmPhrase); exitCodeFor(err) != 3 {
		t.Fatalf("--confirm alone: exit %d (%v)", exitCodeFor(err), err)
	}
	pid := background(t)
	r.leaveBehind(t, c.ID, pid)
	out, err := r.history(t, nil, "delete", c.ID)
	if err != nil {
		t.Fatalf("plain delete: %v\n%s", err, out)
	}
	if !gone(pid) {
		t.Error("the plain delete did not stop the process")
	}
}

// With nothing left running --abandon-processes is an ordinary delete that reports nothing abandoned, and
// a conversation that leads an open mission still has it stopped (that is not a process).
func TestAbandonProcessesWithNothingRunningStillStopsAMission(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	ts, id := r.leadSession(t, c.ID)
	out, err := r.history(t, nil, "delete", c.ID, "--abandon-processes", "--confirm", abandonConfirmPhrase)
	if err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	var res struct {
		Abandoned []survivor `json:"abandoned"`
	}
	decodeChatJSON(t, out, &res)
	if res.Abandoned == nil || len(res.Abandoned) != 0 {
		t.Errorf("abandoned = %v, want an empty list", res.Abandoned)
	}
	if got := sessionState(t, ts, id); got != "stopped" {
		t.Errorf("the lead's mission session is %q", got)
	}
}

// A conversation with an active turn is refused with the usual answer, with or without the words: the
// preview does not tell the operator to add --confirm for a delete that would be refused anyway.
func TestAbandonProcessesDoesNotLetAnActiveTurnThrough(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	pid := background(t)
	r.leaveBehind(t, c.ID, pid)
	if _, _, err := r.store.CreateTurn(c.ID, "default", "live-1", "i", "go"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--abandon-processes"}, {"--abandon-processes", "--confirm", abandonConfirmPhrase}} {
		_, err := r.history(t, nil, append([]string{"delete", c.ID}, args...)...)
		if exitCodeFor(err) != 3 || err == nil || !strings.Contains(err.Error(), "active turn") || strings.Contains(err.Error(), "--confirm") {
			t.Fatalf("%v: exit %d (%v), want the active-turn refusal", args, exitCodeFor(err), err)
		}
	}
	if _, err := r.store.GetConversation(c.ID, "default"); err != nil {
		t.Errorf("the conversation was deleted: %v", err)
	}
	if !processAlive(pid) {
		t.Error("a process was signalled")
	}
}

// A process a turn records while the delete is running is not missed by the report: the transaction's last
// check sees the new record, the delete goes round again and lists it too, still signalling nothing.
func TestAbandonProcessesListsAProcessRecordedWhileDeleting(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	first := background(t)
	r.leaveBehind(t, c.ID, first)
	var late int
	calls := 0
	beforeGuardedDelete = func() {
		calls++
		if calls != 1 {
			return
		}
		late = background(t)
		if _, _, err := r.store.CreateTurn(c.ID, "default", "late-1", "i", "go"); err != nil {
			t.Error(err)
		}
		if _, err := r.store.AppendEvent("default", c.ID, "late-1", chatevents.EventNotice, backgroundNotice([]int{late})); err != nil {
			t.Error(err)
		}
		if _, _, err := r.store.FinalizeTurn("default", c.ID, "late-1", chatevents.StatusCompleted, "", nil, false); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { beforeGuardedDelete = func() {} })

	out, err := r.history(t, nil, "delete", c.ID, "--abandon-processes", "--confirm", abandonConfirmPhrase)
	if err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	var res struct {
		Abandoned []survivor `json:"abandoned"`
	}
	decodeChatJSON(t, out, &res)
	got := map[int]bool{}
	for _, s := range res.Abandoned {
		got[s.Pid] = true
	}
	if !got[first] || !got[late] || len(res.Abandoned) != 2 {
		t.Errorf("abandoned = %+v, want both %d and %d", res.Abandoned, first, late)
	}
	if !processAlive(first) || !processAlive(late) {
		t.Error("a process was signalled")
	}
}
