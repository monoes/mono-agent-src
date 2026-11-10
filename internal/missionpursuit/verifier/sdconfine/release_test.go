package sdconfine

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
)

const (
	ownPrefix = "monoagent-check-4242-100-"
	unitA     = ownPrefix + "0000000000000001"
)

// ctlCalls are the calls made to systemctl, as "<verb> <rest>" without the --user.
func ctlCalls(f *fakeSys) []string {
	var out []string
	for _, c := range f.called() {
		if rest, ok := strings.CutPrefix(c, "systemctl --user "); ok {
			out = append(out, rest)
		}
	}
	return out
}

func TestReleaseKillsStopsAndResetsOnlyItsOwnUnitInThatOrderAndConfirms(t *testing.T) {
	f := newFakeSys()
	c := newConfiner(t, f, Config{})
	done := exec.Command("/bin/sh", "-c", "exit 0")
	done.Run()
	rel := c.releaseFor(unitA, done)
	rel()
	calls := ctlCalls(f)
	if len(calls) < 5 {
		t.Fatalf("calls: %v", calls)
	}
	// what the unit did is read first (it is gone after the stop), then it is ended
	if !strings.HasPrefix(calls[0], "show ") || !strings.Contains(calls[0], "Result") || !strings.Contains(calls[0], unitA) {
		t.Errorf("the unit's result is not read before it is ended: %v", calls)
	}
	want := []string{"kill --signal=SIGKILL --kill-whom=all " + unitA, "stop " + unitA, "reset-failed " + unitA}
	for i, w := range want {
		if calls[i+1] != w {
			t.Errorf("call %d = %q, want %q", i+1, calls[i+1], w)
		}
	}
	if !strings.HasPrefix(calls[4], "show ") || !strings.Contains(calls[4], unitA) {
		t.Errorf("the unit's state is not confirmed after the stop: %v", calls)
	}
	for _, call := range calls {
		if strings.Contains(call, "monoagent-check-*") || strings.Contains(call, " --all") {
			t.Errorf("release touched more than its unit: %q", call)
		}
	}
}

func TestReleaseWaitsUntilTheUnitsCgroupIsEmptyAndGivesUpAfterTenSecondsLoudly(t *testing.T) {
	f := newFakeSys()
	c := newConfiner(t, f, Config{})
	var logged []string
	c.logf = func(format string, a ...any) {
		logged = append(logged, strings.TrimSpace(strings.Join(strings.Fields(format), " "))+" "+unitFrom(a))
	}
	// the cgroup keeps a process for the first three looks, then is empty
	looks := 0
	f.handler = func(bin string, args []string) runResult {
		if bin == "systemctl" && has(args, "show") && has(args, "ControlGroup") && has(args, unitA) {
			looks++
			if looks <= 3 {
				f.files["/sys/fs/cgroup/user.slice/x.service/cgroup.procs"] = "4242\n"
				return runResult{Stdout: "LoadState=loaded\nActiveState=deactivating\nControlGroup=/user.slice/x.service\n"}
			}
			f.files["/sys/fs/cgroup/user.slice/x.service/cgroup.procs"] = ""
			return runResult{Stdout: "LoadState=loaded\nActiveState=inactive\nControlGroup=/user.slice/x.service\n"}
		}
		return runResult{Code: -1}
	}
	start := f.now
	c.release(unitA)()
	if looks != 4 || len(logged) != 0 || f.now.Sub(start) >= 10*time.Second {
		t.Fatalf("looks %d logged %v waited %v", looks, logged, f.now.Sub(start))
	}
	// a unit that never empties: bounded wait, a log line that names the unit, and no more calls than a poll can make
	looks = 0
	f.handler = func(bin string, args []string) runResult {
		if bin == "systemctl" && has(args, "show") && has(args, "ControlGroup") {
			looks++
			f.files["/sys/fs/cgroup/user.slice/x.service/cgroup.procs"] = "4242\n"
			return runResult{Stdout: "LoadState=loaded\nActiveState=deactivating\nControlGroup=/user.slice/x.service\n"}
		}
		return runResult{Code: -1}
	}
	start = f.now
	c.release(unitA)()
	if waited := f.now.Sub(start); waited < 10*time.Second || waited > 12*time.Second {
		t.Fatalf("waited %v, want about 10s", waited)
	}
	if looks > 60 {
		t.Fatalf("%d looks: the poll is not bounded", looks)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], unitA) {
		t.Fatalf("logged %v", logged)
	}
}

func unitFrom(a []any) string {
	var s []string
	for _, x := range a {
		if v, ok := x.(string); ok {
			s = append(s, v)
		}
	}
	return strings.Join(s, " ")
}

func TestReleaseOfAUnitThatNeverStartedIsQuiet(t *testing.T) {
	f := newFakeSys()
	c := newConfiner(t, f, Config{})
	f.handler = func(bin string, args []string) runResult {
		if bin == "systemctl" && (has(args, "kill") || has(args, "stop") || has(args, "reset-failed")) {
			return runResult{Code: 5, Stderr: "Unit " + unitA + " not loaded."}
		}
		return runResult{Code: -1}
	}
	var logged int
	c.logf = func(string, ...any) { logged++ }
	c.release(unitA)()
	if logged != 0 {
		t.Fatalf("a unit that was never there was reported as left behind")
	}
}

func TestReleaseRefusesAnyNameButAOwnUnit(t *testing.T) {
	f := newFakeSys()
	c := newConfiner(t, f, Config{})
	for _, bad := range []string{"", "sshd.service", "monoagent-check-*", "monoagent-check-xyz", unitA + " --now", "-" + unitA, unitA + ".service;x"} {
		c.release(bad)()
		if n := len(ctlCalls(f)); n != 0 {
			t.Errorf("%q: %d calls", bad, n)
		}
	}
}

const listOut = `monoagent-check-4242-100-0000000000000001.service loaded active   running  own-running
monoagent-check-4242-100-0000000000000002.service loaded failed   failed   own-failed
monoagent-check-4242-100-0000000000000003.service loaded inactive dead     own-inactive
monoagent-check-4243-7-0000000000000004.service loaded active running other-live-host
monoagent-check-99-5-0000000000000005.service loaded active running dead-host
monoagent-check-98-1-0000000000000006.service loaded failed failed dead-host-failed
sshd.service loaded active running x
monoagent-check-zz.service loaded active running not ours
monoagent-check-4242-100-0000000000000007.service;rm loaded active running no
`

func TestLeftoversAreTheOwnUnitsThatAreNotInactive(t *testing.T) {
	f := newFakeSys()
	c := newConfiner(t, f, Config{})
	f.handler = func(bin string, args []string) runResult {
		if bin == "systemctl" && has(args, "list-units") {
			return runResult{Stdout: listOut}
		}
		return runResult{Code: -1}
	}
	got, err := c.Leftovers(context.Background())
	if err != nil || strings.Join(got, ",") != ownPrefix+"0000000000000001.service,"+ownPrefix+"0000000000000002.service" {
		t.Fatalf("%v %v", got, err)
	}
	f.handler = func(bin string, args []string) runResult {
		return runResult{Code: 1, Stderr: "Failed to connect to bus"}
	}
	if _, err := c.Leftovers(context.Background()); err == nil {
		t.Fatal("a manager that does not answer is not 'nothing left'")
	}
}

// A weak kill mode leaves the unit "inactive" with a child still in its cgroup: that is left behind, the state alone does not say.
func TestAnInactiveUnitWhoseCgroupStillHoldsAProcessIsLeftBehind(t *testing.T) {
	f := newFakeSys()
	c := newConfiner(t, f, Config{})
	f.handler = func(bin string, args []string) runResult {
		switch {
		case bin == "systemctl" && has(args, "list-units"):
			return runResult{Stdout: ownPrefix + "0000000000000003.service loaded inactive dead x\n"}
		case bin == "systemctl" && has(args, "show") && has(args, "ControlGroup"):
			return runResult{Stdout: "/user.slice/orphan.service\n"}
		}
		return runResult{Code: -1}
	}
	f.files["/sys/fs/cgroup/user.slice/orphan.service/cgroup.procs"] = "777\n"
	got, err := c.Leftovers(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	f.files["/sys/fs/cgroup/user.slice/orphan.service/cgroup.procs"] = ""
	if got, err := c.Leftovers(context.Background()); err != nil || len(got) != 0 {
		t.Fatalf("an empty cgroup was reported: %v %v", got, err)
	}
}

// A second host process must not end the first one's in-flight criterion: only units whose owner is gone are stale.
func TestSweepStopsOnlyUnitsWhoseOwnerIsDeadByExactName(t *testing.T) {
	f := newFakeSys()
	c := newConfiner(t, f, Config{})
	f.handler = func(bin string, args []string) runResult {
		if bin == "systemctl" && has(args, "list-units") {
			return runResult{Stdout: listOut}
		}
		return runResult{Code: -1}
	}
	got, err := c.Sweep(context.Background())
	want := "monoagent-check-98-1-0000000000000006.service,monoagent-check-99-5-0000000000000005.service"
	sort.Strings(got)
	if err != nil || strings.Join(got, ",") != want {
		t.Fatalf("%v %v, want %s", got, err, want)
	}
	var acts []string
	for _, call := range ctlCalls(f) {
		if strings.HasPrefix(call, "stop ") || strings.HasPrefix(call, "kill ") || strings.HasPrefix(call, "reset-failed ") {
			acts = append(acts, call)
		}
	}
	joined := strings.Join(acts, "\n")
	for _, u := range got {
		if !strings.Contains(joined, "stop "+u) || !strings.Contains(joined, "reset-failed "+u) {
			t.Errorf("%s was not stopped and reset:\n%s", u, joined)
		}
	}
	for _, bad := range []string{"sshd", "zz", "rm", "4242", "4243", "*"} {
		if strings.Contains(joined, bad) {
			t.Errorf("sweep touched %q:\n%s", bad, joined)
		}
	}
	// a pid that is alive but with another start time is a reused pid: its units are stale too
	f.alive[99] = "6"
	f.reset()
	got, _ = c.Sweep(context.Background())
	if len(got) != 2 {
		t.Fatalf("a reused pid protected a dead host's unit: %v", got)
	}
	f.alive[99] = "5"
	got, _ = c.Sweep(context.Background())
	if len(got) != 1 {
		t.Fatalf("a live host's unit was swept: %v", got)
	}
}

// Cleanup is the owner's own end-of-life: it stops this process's units (and no one else's).
func TestCleanupStopsOnlyThisProcessesUnits(t *testing.T) {
	f := newFakeSys()
	c := newConfiner(t, f, Config{})
	f.handler = func(bin string, args []string) runResult {
		if bin == "systemctl" && has(args, "list-units") {
			return runResult{Stdout: listOut}
		}
		return runResult{Code: -1}
	}
	got, err := c.Cleanup(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	for _, call := range ctlCalls(f) {
		if strings.HasPrefix(call, "stop ") && !strings.Contains(call, "4242-100") {
			t.Errorf("cleanup stopped %s", call)
		}
	}
}

// The unit's own record tells a program that ran from a unit that did not: the exit code alone cannot (systemd's setup failures
// use codes a program can also exit with, and systemd-run itself exits 1).
func TestTheUnitsResultSeparatesAProgramFromAFailedUnit(t *testing.T) {
	exited := func(code int) *exec.Cmd {
		cmd := exec.Command("/bin/sh", "-c", fmt.Sprintf("exit %d", code))
		cmd.Run()
		return cmd
	}
	for name, tc := range map[string]struct {
		show  string
		code  int
		infra string // substring of the reported failure, "" for none
	}{
		"success":                                  {"LoadState=loaded\nActiveState=inactive\nResult=success\nExecMainCode=1\nExecMainStatus=0\n", 0, ""},
		"program exit 1":                           {"LoadState=loaded\nActiveState=failed\nResult=exit-code\nExecMainCode=1\nExecMainStatus=1\n", 1, ""},
		"program exit 255":                         {"LoadState=loaded\nActiveState=failed\nResult=exit-code\nExecMainCode=1\nExecMainStatus=255\n", 255, ""},
		"namespace setup failed (226)":             {"LoadState=loaded\nActiveState=failed\nResult=exit-code\nExecMainCode=1\nExecMainStatus=226\n", 226, "226"},
		"systemd reserved range":                   {"LoadState=loaded\nActiveState=failed\nResult=exit-code\nExecMainCode=1\nExecMainStatus=200\n", 200, "200"},
		"killed from outside":                      {"LoadState=loaded\nActiveState=failed\nResult=signal\nExecMainCode=2\nExecMainStatus=9\n", 255, "killed"},
		"out of memory is the program":             {"LoadState=loaded\nActiveState=failed\nResult=oom-kill\nExecMainCode=2\nExecMainStatus=9\n", 255, ""},
		"core dump is the program":                 {"LoadState=loaded\nActiveState=failed\nResult=core-dump\nExecMainCode=3\nExecMainStatus=11\n", 255, ""},
		"resources":                                {"LoadState=loaded\nActiveState=failed\nResult=resources\nExecMainCode=0\nExecMainStatus=0\n", 1, "resources"},
		"timeout of the manager":                   {"LoadState=loaded\nActiveState=failed\nResult=timeout\nExecMainCode=0\nExecMainStatus=0\n", 1, "timeout"},
		"still running (timed out by the sandbox)": {"LoadState=loaded\nActiveState=active\nResult=success\nExecMainCode=0\nExecMainStatus=0\n", 255, ""},
		"no unit and the client failed":            {"LoadState=not-found\nActiveState=inactive\n", 1, "does not exist"},
		"no unit and the client succeeded":         {"LoadState=not-found\nActiveState=inactive\n", 0, ""},
	} {
		f := newFakeSys()
		c := newConfiner(t, f, Config{})
		classified := false
		f.handler = func(bin string, args []string) runResult {
			if bin == "systemctl" && has(args, "show") && has(args, "Result") && !classified {
				classified = true
				return runResult{Stdout: tc.show}
			}
			return runResult{Code: -1}
		}
		c.releaseFor(unitA, exited(tc.code))()
		got := c.TakeInfra()
		if tc.infra == "" && len(got) != 0 || tc.infra != "" && (len(got) != 1 || !strings.Contains(got[0], tc.infra)) {
			t.Errorf("%s: infra %v, want %q", name, got, tc.infra)
		}
		if again := c.TakeInfra(); len(again) != 0 {
			t.Errorf("%s: the failure was not cleared by TakeInfra: %v", name, again)
		}
	}
}

// Two SelfTests of one confiner (and of its per-check copies) do not overlap: the second would see the first's units.
func TestSelfTestsAreSerialized(t *testing.T) {
	f := newFakeSys()
	c := newConfiner(t, f, Config{})
	var in, max int32
	c.canaries = func(context.Context, verifier.Confiner, string, verifier.CanaryOptions) []verifier.CanaryResult {
		n := atomic.AddInt32(&in, 1)
		for {
			m := atomic.LoadInt32(&max)
			if n <= m || atomic.CompareAndSwapInt32(&max, m, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&in, -1)
		return []verifier.CanaryResult{{ID: "C0", Verdict: verifier.CanaryHeld}}
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			x := verifier.Confiner(c)
			if i%2 == 1 {
				x = c.Hide("/srv/p")
			}
			x.SelfTest(context.Background())
		}(i)
	}
	wg.Wait()
	if max != 1 {
		t.Fatalf("%d self tests ran at once", max)
	}
}

func TestPlantManagerEnvSetsAndUnsetsByNameAndRefusesOtherNames(t *testing.T) {
	f := newFakeSys()
	c := newConfiner(t, f, Config{})
	undo, err := c.PlantManagerEnv(context.Background(), "MONOAGENT_CANARY_MGR_ab12", "v1")
	if err != nil {
		t.Fatal(err)
	}
	undo()
	calls := ctlCalls(f)
	if len(calls) != 2 || calls[0] != "set-environment MONOAGENT_CANARY_MGR_ab12=v1" || calls[1] != "unset-environment MONOAGENT_CANARY_MGR_ab12" {
		t.Fatalf("%v", calls)
	}
	for _, bad := range []string{"PATH", "HOME", "MONOAGENT_CANARY_MGR_x y", "MONOAGENT_TOKEN", "", "MONOAGENT_CANARY_MGR_a=b"} {
		if _, err := c.PlantManagerEnv(context.Background(), bad, "v"); err == nil {
			t.Errorf("%q was planted in the manager", bad)
		}
	}
	if _, err := c.PlantManagerEnv(context.Background(), "MONOAGENT_CANARY_MGR_ok", "a\nb"); err == nil {
		t.Error("a value with a newline was planted")
	}
}
