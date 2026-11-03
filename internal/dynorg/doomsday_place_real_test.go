//go:build linux

package dynorg

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
)

// The real thing, against the user's systemd manager: a git child of the conductor is BORN in the run unit's
// ticket cgroup (clone3 with CLONE_INTO_CGROUP, no hold stub), the unit is not empty while it runs and is empty
// again after. It creates and stops only its own transient unit (monoagent-run-<gen>-<nonce>, the one name shape
// the product creates; the generation is a number no real run reaches). Set MONOAGENT_DOOMSDAY_REAL_CGROUP=1.
func TestRealGitChildIsBornInTheTicketCgroupAndTheUnitIsNotEmptyWhileItRuns(t *testing.T) {
	if os.Getenv("MONOAGENT_DOOMSDAY_REAL_CGROUP") != "1" {
		t.Skip("set MONOAGENT_DOOMSDAY_REAL_CGROUP=1 to run against the real user manager")
	}
	repo := newRepo(t)
	k := stop.LinuxKernel{}
	dir := stop.RunDir{Dir: t.TempDir()}
	host := stop.SystemHost()
	const gen = 987654322
	rec, err := stop.UnitCreator{K: k, Dir: dir, Host: host}.Create(context.Background(), gen)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.StopUnit(context.Background(), rec.Name) })
	proof, err := stop.ProveUnit(k, rec)
	if err != nil || proof.Gone {
		t.Fatalf("proof %+v, %v", proof, err)
	}
	cg := proof.Cg
	defer cg.Close()
	if empty, err := stop.UnitEmpty(cg, rec); err != nil || !empty {
		t.Fatalf("a fresh unit is not empty: %v, %v", empty, err)
	}

	pl, err := stop.NewPlacer(k, dir, gen)
	if err != nil {
		t.Fatal(err)
	}
	ctx := withGitPlacer(context.Background(), pl.Place)
	done := make(chan error, 1)
	go func() {
		// a git alias that sleeps: git is the child, `sleep` its grandchild; both must be in the cgroup
		_, err := gitRun(ctx, repo, "-c", "alias.nap=!sleep 4", "nap")
		done <- err
	}()

	var sub string
	var pids []int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(pids) == 0 {
		subs, err := cg.Subgroups()
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range subs {
			if s == "" {
				continue
			}
			if ps, _ := cg.Procs(s); len(ps) > 0 {
				sub, pids = s, ps
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(pids) == 0 {
		t.Fatal("the git child is in no ticket cgroup of the unit")
	}
	if !strings.HasPrefix(sub, "t-") {
		t.Errorf("sub-cgroup %q is not a ticket cgroup", sub)
	}
	for _, pid := range pids {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
		if err != nil || !strings.Contains(string(b), rec.Name+"/"+sub) {
			t.Errorf("pid %d: /proc cgroup = %q (%v), want inside %s/%s", pid, b, err, rec.Name, sub)
		}
	}
	if empty, err := stop.UnitEmpty(cg, rec); err != nil || empty {
		t.Fatalf("the unit is empty while git runs in it: %v, %v", empty, err)
	}

	if err := <-done; err != nil {
		t.Fatalf("git: %v", err)
	}
	if empty, err := stop.UnitEmpty(cg, rec); err != nil || !empty {
		t.Fatalf("the unit is not empty after git ended: %v, %v", empty, err)
	}
	if subs, _ := cg.Subgroups(); len(subs) > 1 {
		t.Errorf("the ticket cgroup was not removed: %v", subs)
	}
}
