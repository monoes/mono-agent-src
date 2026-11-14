//go:build !windows

package stop

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// ----- a command that is not a turn (a merge's git command) -----

// The session unit takes a merge's git command: the placement makes it a session leader (it has no attributes of its own), and
// once it started AdoptPlaced records it, so Stop sees it. Without the placement there is nothing to adopt.
func TestSessAStandaloneCommandIsMadeALeaderAndAdoptedAfterItStarted(t *testing.T) {
	r := newSessRig(t)
	var sp StandalonePlacement = r.placer()
	cmd := exec.Command("/bin/true")
	release, err := sp.PlaceStandalone(cmd)
	if err != nil || release == nil {
		t.Fatalf("a git command with no attributes was not placed: %v", err)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid || cmd.SysProcAttr.Setpgid {
		t.Fatalf("the command is not a session leader of its own: %+v", cmd.SysProcAttr)
	}
	if err := AdoptPlaced(exec.Command("/bin/true")); err != nil {
		t.Fatalf("a command that was never placed has nothing to adopt: %v", err)
	}
	l := r.w.leader(500) // what the started command is
	cmd.Process = &os.Process{Pid: l.PID}
	if err := AdoptPlaced(cmd); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.led.Leaders(7); len(got) != 1 || got[0].PID != 500 || got[0].Stamp != l.Stamp {
		t.Fatalf("the started command is not in the ledger: %+v", got)
	}
	release()
	if err := AdoptPlaced(cmd); err != nil {
		t.Fatalf("a released placement has nothing to adopt: %v", err)
	}
}

func TestSessAStandaloneCommandIsRefusedWhenItCannotBeOneOrAdopted(t *testing.T) {
	r := newSessRig(t)
	p := r.placer()
	grp := exec.Command("/bin/true")
	grp.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if _, err := p.PlaceStandalone(grp); !errors.Is(err, ErrNotPlaced) {
		t.Fatalf("a command that asks for a group was placed: %v", err)
	}
	if _, err := p.PlaceStandalone(nil); !errors.Is(err, ErrNotPlaced) {
		t.Fatalf("nil: %v", err)
	}
	cmd := exec.Command("/bin/true")
	if _, err := p.PlaceStandalone(cmd); err != nil {
		t.Fatal(err)
	}
	// a started command the unit cannot tie (not a session leader): the adoption fails and the caller ends it
	r.w.put(600, r.w.self, 600, 1, "git")
	cmd.Process = &os.Process{Pid: 600}
	if err := AdoptPlaced(cmd); !errors.Is(err, ErrNotPlaced) {
		t.Fatalf("a non-leader was adopted: %v", err)
	}
}

// A short command can end before it is looked at: it is nothing to adopt, not a refusal (a real git command ends in milliseconds).
func TestSessAStandaloneCommandThatAlreadyEndedIsNotARefusal(t *testing.T) {
	r := newSessRig(t)
	cmd := exec.Command("/bin/true")
	if _, err := r.placer().PlaceStandalone(cmd); err != nil {
		t.Fatal(err)
	}
	cmd.Process = &os.Process{Pid: 777} // no such process in the table
	if err := AdoptPlaced(cmd); err != nil {
		t.Fatalf("a command that had ended was refused: %v", err)
	}
	if got, _ := r.led.Leaders(7); len(got) != 0 {
		t.Fatalf("an ended command was recorded: %+v", got)
	}
}
