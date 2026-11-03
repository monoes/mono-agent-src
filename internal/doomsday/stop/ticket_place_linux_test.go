//go:build linux

package stop

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Against the user's systemd manager (MONOAGENT_DOOMSDAY_REAL_CGROUP=1): Place and PlaceLeased make the command
// start inside a ticket cgroup of the unit, and a refusal (unit stopped) starts nothing. Only its own transient
// unit is created and stopped.
func TestRealPlaceBornInTicketCgroupAndRefusedOnceTheUnitIsGone(t *testing.T) {
	realEnv(t)
	k := LinuxKernel{}
	host := SystemHost()
	dir := RunDir{Dir: t.TempDir()}
	const gen = 987654323
	rec, err := UnitCreator{K: k, Dir: dir, Host: host}.Create(bg, gen)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.StopUnit(bg, rec.Name) })
	p, err := NewPlacer(k, dir, gen)
	if err != nil {
		t.Fatal(err)
	}
	sleepBin, _ := trustedBinary("/usr/bin/sleep", "/bin/sleep")

	cmd := exec.Command(sleepBin, "30")
	if err := p.PlaceLeased(cmd); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waited) }()
	b, err := os.ReadFile("/proc/" + strconv.Itoa(cmd.Process.Pid) + "/cgroup")
	if err != nil || !strings.Contains(string(b), rec.Name+"/t-") {
		t.Fatalf("the child is not in a ticket cgroup of the unit: %q %v", b, err)
	}
	proof, err := ProveUnit(k, rec)
	if err != nil {
		t.Fatal(err)
	}
	defer proof.Cg.Close()
	if empty, _ := UnitEmpty(proof.Cg, rec); empty {
		t.Fatal("the unit is empty while the placed child lives")
	}
	_ = cmd.Process.Kill()
	<-waited
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if empty, _ := UnitEmpty(proof.Cg, rec); empty {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if empty, err := UnitEmpty(proof.Cg, rec); err != nil || !empty {
		t.Fatalf("not empty after the child ended: %v %v", empty, err)
	}

	if err := host.StopUnit(bg, rec.Name); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	cmd2 := exec.Command(sleepBin, "1")
	for time.Now().Before(deadline) {
		rel, perr := p.Place(cmd2)
		if err = perr; err != nil {
			break
		}
		rel()
		cmd2 = exec.Command(sleepBin, "1")
		time.Sleep(50 * time.Millisecond)
	}
	if err == nil {
		t.Fatal("a command was placed in a stopped unit")
	}
	if cmd2.SysProcAttr != nil && cmd2.SysProcAttr.UseCgroupFD {
		t.Error("a refused placement left the cgroup fd on the command")
	}
}
