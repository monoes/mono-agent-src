//go:build linux

package doomsday

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
)

// Against the user's systemd manager (MONOAGENT_DOOMSDAY_REAL_CGROUP=1; run it directly): a run started on a real transient
// unit of its own, then a child placed through the gate's Place hook is BORN in a ticket cgroup of that unit and the unit
// is not empty while it lives. Only the run's own unit is created and stopped.
func TestRealGatePlacementBornInTicketCgroup(t *testing.T) {
	if os.Getenv("MONOAGENT_DOOMSDAY_REAL_CGROUP") != "1" {
		t.Skip("set MONOAGENT_DOOMSDAY_REAL_CGROUP=1 to run against the real user manager")
	}
	w, _ := newWire(t, func() error { return nil }, stop.LinuxKernel{}, nil)
	host := stop.SystemHost()
	w.w.Host = host
	e := w.w.Engine()
	w.deps.Engine = func() Engine { return e }
	w.start(t)
	rec, ok, err := w.w.RunDir.ReadUnitFile(1)
	if err != nil || !ok {
		t.Fatalf("no unit record: %v %v", ok, err)
	}
	t.Cleanup(func() { _ = host.StopUnit(context.Background(), rec.Name) })

	g := &HostGate{Store: w.w.Store, Profile: "p1", RunDir: w.w.RunDir, Kernel: w.w.Kernel}
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sleepBin, "30")
	release, err := g.PlaceRelease(cmd, "p1", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		release()
		t.Fatal(err)
	}
	release() // the launcher closes the descriptor once the process exists
	b, err := os.ReadFile("/proc/" + strconv.Itoa(cmd.Process.Pid) + "/cgroup")
	if err != nil || !strings.Contains(string(b), rec.Name+"/t-") {
		t.Fatalf("the child is not in a ticket cgroup of the unit: %q %v", b, err)
	}
	proof, err := stop.ProveUnit(stop.LinuxKernel{}, rec)
	if err != nil {
		t.Fatal(err)
	}
	defer proof.Cg.Close()
	if empty, _ := stop.UnitEmpty(proof.Cg, rec); empty {
		t.Fatal("the unit is empty while the placed child lives")
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
