//go:build linux

package sdconfine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
)

// systemd expands $ in a service's command line; a criterion's own text must arrive unchanged, with its environment, and the
// client's exit status must be the program's.
func TestRealTheProgramsArgumentsEnvironmentAndExitStatusArriveAsTheSandboxMeantThem(t *testing.T) {
	r := newRealRig(t)
	work, err := os.MkdirTemp(r.base, "crit-")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"subject", "scratch/home", "scratch/tmp"} {
		os.MkdirAll(filepath.Join(work, d), 0o700)
	}
	var onPlaced func(*exec.Cmd)
	run := func(script string) (string, int) {
		cmd := exec.Command("/the/exe")
		lim := finish.Limits{CPUSeconds: 20, AddressSpace: 8 << 30, OpenFiles: 256, FileSize: 1 << 20, Procs: 3001}
		cmd.Args = finish.LimitsHelperArgv("/the/exe", lim, "/usr/bin/sh", []string{"-c", script})
		cmd.Dir = filepath.Join(work, "subject")
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + work + "/scratch/home", "TMPDIR=" + work + "/scratch/tmp", "WEIRD=a$b%h"}
		cmd.SysProcAttr = sandboxAttr()
		var out bytes.Buffer
		cmd.Stdout = &out
		release, err := r.c.Place(cmd)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if onPlaced != nil {
			go onPlaced(cmd)
		}
		err = cmd.Run()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return out.String(), code
	}
	if out, code := run(`echo '$HOME ${X} %h $$' "$WEIRD" "$HOME"; exit 7`); code != 7 || out != "$HOME ${X} %h $$ a$b%h "+work+"/scratch/home\n" {
		t.Errorf("code %d out %q", code, out)
	}
	// a unit killed from outside (the kernel's OOM kill is the same signal) ends the client with a non-zero status: it never looks
	// like a pass. (A program cannot signal pid 1 of its own pid namespace, so the kill comes from the manager.)
	killer := func(c *exec.Cmd) {
		unit := unitOf(c.Args)
		for i := 0; i < 100 && !strings.Contains(showUnitQuiet(unit, "ActiveState"), "active"); i++ { // wait until it runs, not a fixed time
			time.Sleep(50 * time.Millisecond)
		}
		ctl, _ := trustedBinary("/usr/bin/systemctl", "/bin/systemctl")
		k := exec.Command(ctl, "--user", "kill", "--signal=SIGKILL", "--kill-whom=all", unitOf(c.Args))
		k.Env = clientEnv(t)
		k.Run()
	}
	onPlaced = killer
	r.c.TakeInfra()
	if _, code := run(`sleep 30`); code == 0 {
		t.Error("a program killed by a signal looked like a pass")
	}
	if infra := r.c.TakeInfra(); len(infra) != 1 || !strings.Contains(infra[0], "killed") {
		t.Errorf("a unit killed from outside is not reported as infrastructure: %v", infra)
	}
	onPlaced = nil
	if _, code := run(`exit 7`); code != 7 {
		t.Errorf("exit 7 became %d", code)
	}
	if infra := r.c.TakeInfra(); len(infra) != 0 {
		t.Errorf("a program's own exit status was reported as infrastructure: %v", infra)
	}
	if _, code := run(`exit 0`); code != 0 {
		t.Errorf("exit 0 became %d", code)
	}
	noUnitsLeft(t, r.c)
}

// A stale unit (a crashed host's: its owner pid is gone) is found and ended by name; a live host's unit is not touched.
func TestRealSweepEndsADeadHostsUnitAndLeavesALiveOnes(t *testing.T) {
	r := newRealRig(t)
	dead := *r.c
	dead.owner = "2147480000-1" // no such process
	deadUnit, stopDead := startSleeper(t, r, &dead)
	liveUnit, stopLive := startSleeper(t, r, r.c)
	dUnit, _, _ := strings.Cut(deadUnit, "|")
	lUnit, _, _ := strings.Cut(liveUnit, "|")
	own, err := r.c.Leftovers(context.Background())
	t.Logf("dead %s live %s state %q %q", dUnit, lUnit, showUnitQuiet(lUnit, "ActiveState"), showUnitQuiet(dUnit, "ActiveState"))
	if err != nil || len(own) != 1 || own[0] != lUnit+".service" {
		t.Fatalf("own leftovers %v %v (want only %s)", own, err, lUnit)
	}
	swept, err := r.c.Sweep(context.Background())
	if err != nil || len(swept) != 1 || swept[0] != dUnit+".service" {
		t.Fatalf("swept %v %v (want only %s)", swept, err, dUnit)
	}
	if out := showUnitQuiet(lUnit, "ActiveState"); !strings.Contains(out, "=active") {
		t.Fatalf("the live host's unit was touched: %q", out)
	}
	stopDead()
	stopLive()
	noUnitsLeft(t, r.c)
}

// A unit that never started (its setup failed) is infrastructure, not the program's exit status: "exit 1" would otherwise pass.
func TestRealAUnitThatNeverStartedIsReportedNotJudged(t *testing.T) {
	r := newRealRig(t)
	m := mutated(r.c, func(p []string) []string {
		var out []string
		for _, q := range p {
			if strings.HasPrefix(q, "BindPaths=") {
				q = "BindPaths=/nonexistent-zz-source"
			}
			out = append(out, q)
		}
		return out
	})
	r.c.TakeInfra()
	_, code := runIn(t, r, m, "exit 1")
	infra := m.TakeInfra()
	if code == 0 || len(infra) != 1 || !strings.Contains(infra[0], "226") {
		t.Fatalf("code %d infra %v", code, infra)
	}
	// and with systemd-run itself unable to create the unit (a taken name), the client's exit 1 is not the program's
	noUnitsLeft(t, r.c)
}

// Paths with a space, a quote, a backslash, a colon or a dollar work on a real manager (quoted, not refused).
func TestRealOddProjectAndScratchPathsWork(t *testing.T) {
	r := newRealRig(t)
	for _, name := range []string{"My Project", "q'uote", `back\slash`, "co:lon", "dol$x"} {
		base := filepath.Join(filepath.Dir(r.base), name)
		if err := os.MkdirAll(base, 0o700); err != nil {
			t.Fatal(err)
		}
		c, err := New(context.Background(), Config{ScratchBase: base, Caps: tinyCaps()})
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		rr := *r
		rr.base = base
		out, code := runIn(t, &rr, c.Hide(filepath.Join(filepath.Dir(r.base), "live "+name)), "pwd; echo ok")
		if code != 0 || !strings.HasSuffix(out, "/subject\nok\n") {
			t.Errorf("%q: %d %q", name, code, out)
		}
	}
	noUnitsLeft(t, r.c)
}
