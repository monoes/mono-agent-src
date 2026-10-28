package chat

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
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
