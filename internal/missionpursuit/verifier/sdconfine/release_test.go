package sdconfine

import (
	"context"
	"strings"
	"testing"
	"time"
)

const unitA = "monoagent-check-0000000000000001"

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
	rel := c.release(unitA)
	rel()
	calls := ctlCalls(f)
	if len(calls) < 4 {
		t.Fatalf("calls: %v", calls)
	}
	want := []string{"kill --signal=SIGKILL --kill-whom=all " + unitA, "stop " + unitA, "reset-failed " + unitA}
	for i, w := range want {
		if calls[i] != w {
			t.Errorf("call %d = %q, want %q", i, calls[i], w)
		}
	}
	if !strings.HasPrefix(calls[3], "show ") || !strings.Contains(calls[3], unitA) {
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
		if bin == "systemctl" && has(args, "show") && has(args, unitA) {
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
		if bin == "systemctl" && has(args, "show") {
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

const listOut = `monoagent-check-0000000000000001.service loaded active   running  run-u1
monoagent-check-0000000000000002.service loaded failed   failed   run-u2
monoagent-check-0000000000000003.service loaded inactive dead     run-u3
sshd.service loaded active running x
monoagent-check-zz.service loaded active running not ours
monoagent-check-0000000000000004.service;rm loaded active running no
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
	if err != nil || strings.Join(got, ",") != "monoagent-check-0000000000000001.service,monoagent-check-0000000000000002.service" {
		t.Fatalf("%v %v", got, err)
	}
	f.handler = func(bin string, args []string) runResult {
		return runResult{Code: 1, Stderr: "Failed to connect to bus"}
	}
	if _, err := c.Leftovers(context.Background()); err == nil {
		t.Fatal("a manager that does not answer is not 'nothing left'")
	}
}

func TestSweepStopsExactlyTheStaleUnitsByName(t *testing.T) {
	f := newFakeSys()
	c := newConfiner(t, f, Config{})
	f.handler = func(bin string, args []string) runResult {
		if bin == "systemctl" && has(args, "list-units") {
			return runResult{Stdout: listOut}
		}
		return runResult{Code: -1}
	}
	got, err := c.Sweep(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	var stops []string
	for _, call := range ctlCalls(f) {
		if strings.HasPrefix(call, "stop ") || strings.HasPrefix(call, "kill ") || strings.HasPrefix(call, "reset-failed ") {
			stops = append(stops, call)
		}
	}
	joined := strings.Join(stops, "\n")
	for _, u := range got {
		if !strings.Contains(joined, "stop "+u) || !strings.Contains(joined, "reset-failed "+u) {
			t.Errorf("%s was not stopped and reset:\n%s", u, joined)
		}
	}
	for _, bad := range []string{"sshd", "zz", "rm", "0000000000000003", "0000000000000004", "*"} {
		if strings.Contains(joined, bad) {
			t.Errorf("sweep touched %q:\n%s", bad, joined)
		}
	}
}
