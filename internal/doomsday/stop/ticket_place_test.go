package stop

import (
	"errors"
	"os/exec"
	"testing"
)

// A placer that cannot prove the unit places nothing and the command is left exactly as it was: the caller must
// not start it.
func TestPlaceRefusesWhatItCannotProveAndLeavesTheCommandUntouched(t *testing.T) {
	k := newFakeKernel()
	dir := RunDir{Dir: t.TempDir()}
	if _, err := NewPlacer(nil, dir, 1); !errors.Is(err, ErrNotPlaced) {
		t.Errorf("no kernel: %v", err)
	}
	if _, err := NewPlacer(k, dir, 1); !errors.Is(err, ErrNotPlaced) {
		t.Errorf("no unit record: %v", err)
	}
	// a record whose boot differs from this boot: the unit is gone, so nothing may be placed in it
	rec := UnitRecord{Kind: "cgroup2", BootID: "another-boot", Generation: 1, Nonce: "abababababababababababababababab"}
	rec.Name = "monoagent-run-1-" + rec.Nonce + ".service"
	p := &Placer{K: k, Rec: rec}
	cmd := exec.Command("true")
	if release, err := p.Place(cmd); !errors.Is(err, ErrNotPlaced) || release != nil {
		t.Fatalf("a gone unit placed a command: %v", err)
	}
	if cmd.SysProcAttr != nil {
		t.Errorf("a refused placement changed the command: %+v", cmd.SysProcAttr)
	}
	if _, err := p.Place(nil); !errors.Is(err, ErrNotPlaced) {
		t.Errorf("nil command: %v", err)
	}
}
