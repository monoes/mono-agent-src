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
	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
)

// These tests start real transient services on the user's systemd manager, one at a time, with tiny caps and short timeouts,
// inside a scratch area. They skip where there is no user manager (a hosted runner, a container, nokc's sandbox) and FAIL there
// only where MONOAGENT_REQUIRE_SANDBOX_TESTS=1. The fork-bomb and memory-hog canaries are not part of them (see
// TestRealHeavyCanaries, off unless MONOAGENT_CONFINER_HEAVY_TESTS=1).

var noMountFences = []string{"PrivateUsers", "PrivatePIDs", "ProtectProc", "ProcSubset", "ProtectSystem", "ProtectHome", "TemporaryFileSystem",
	"InaccessiblePaths", "PrivateTmp", "PrivateDevices", "ProtectKernelTunables", "ProtectKernelModules", "ProtectKernelLogs",
	"ProtectControlGroups", "ProtectClock", "BindPaths", "PrivateIPC", "PrivateNetwork", "CapabilityBoundingSet"}

type realRig struct {
	c                      *Confiner
	base, home, rt, tmpdir string
}

func tinyCaps() Caps { return Caps{MemoryBytes: 256 << 20, Tasks: 64, CPUPercent: 100} }

func newRealRig(t *testing.T) *realRig {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("never run as root")
	}
	root, err := os.MkdirTemp("", "r")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { finish.RemoveTree(root) })
	r := &realRig{base: filepath.Join(root, "base"), home: filepath.Join(root, "home"), rt: filepath.Join(root, "rt"), tmpdir: filepath.Join(root, "tmp")}
	for _, d := range []string{r.base, r.home, r.rt, r.tmpdir, filepath.Join(r.home, ".ssh"), filepath.Join(r.home, ".monoagent"), filepath.Join(r.home, ".config")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{".ssh/id_planted", ".monoagent/db_planted"} {
		if err := os.WriteFile(filepath.Join(r.home, f), []byte("planted marker, not a secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	c, err := New(context.Background(), Config{ScratchBase: r.base, Caps: tinyCaps()})
	if err != nil {
		if IsUnavailable(err) && os.Getenv("MONOAGENT_REQUIRE_SANDBOX_TESTS") != "1" {
			t.Skipf("no confinement here: %v", err)
		}
		t.Fatalf("a confinement is required here: %v", err)
	}
	r.c = c
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		c.Sweep(ctx)
	})
	return r
}

func (r *realRig) opts(only ...string) verifier.CanaryOptions {
	return verifier.CanaryOptions{SkipHeavy: len(only) == 0, Only: only, HomeDir: r.home, RuntimeDir: r.rt, TmpDir: r.tmpdir}
}

func (r *realRig) canaries(t *testing.T, c *Confiner, only ...string) []verifier.CanaryResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return verifier.RunCanariesWith(ctx, c, r.base, r.opts(only...))
}

func report(rs []verifier.CanaryResult) string {
	var b strings.Builder
	for _, r := range rs {
		b.WriteString(r.ID + "=" + string(r.Verdict) + " (" + r.Detail + ")\n")
	}
	return b.String()
}

func noUnitsLeft(t *testing.T, c *Confiner) {
	t.Helper()
	left, err := c.Leftovers(context.Background())
	if err != nil || len(left) != 0 {
		t.Fatalf("units left behind: %v %v", left, err)
	}
}

func TestRealTheLightCanariesHoldAndNothingIsLeftBehind(t *testing.T) {
	r := newRealRig(t)
	rs := r.canaries(t, r.c)
	if err := verifier.CanariesHold(rs); err != nil {
		t.Fatalf("%v\n%s", err, report(rs))
	}
	if len(rs) != 8 {
		t.Fatalf("%d light canaries ran:\n%s", len(rs), report(rs))
	}
	noUnitsLeft(t, r.c)
}

func TestRealTheOutliveAndTimeoutCanariesHoldAndTheUnitIsGone(t *testing.T) {
	r := newRealRig(t)
	rs := r.canaries(t, r.c, "C10", "C10b", "C11")
	if err := verifier.CanariesHold(rs); err != nil {
		t.Fatalf("%v\n%s", err, report(rs))
	}
	noUnitsLeft(t, r.c)
}

// mutated is a confiner whose builder, after the self-check, emits a list altered by mut: the property is on the check's list and
// gone from the unit, which is exactly what a bug in the builder would be.
func mutated(c *Confiner, mut func([]string) []string) *Confiner {
	m := *c
	m.mutate = mut
	return &m
}

func without(names ...string) func([]string) []string {
	return func(props []string) []string {
		var out []string
		for _, p := range props {
			drop := false
			for _, n := range names {
				if p == n || strings.HasPrefix(p, n+"=") {
					drop = true
				}
			}
			if !drop {
				out = append(out, p)
			}
		}
		return out
	}
}

// Revert proof on a real manager: take a fence out of the unit and the canary that watches it goes red. Several fences overlap
// on purpose (ProtectHome and the empty /run; ProtectProc and the private pid namespace), so the overlapping ones go together.
func TestRealTakingAFenceOutBreaksItsCanary(t *testing.T) {
	r := newRealRig(t)
	for _, tc := range []struct {
		canary string
		drop   []string
	}{
		{"C4", []string{"ProtectHome", "TemporaryFileSystem"}},
		{"C1", []string{"ProtectHome", "TemporaryFileSystem"}},
		{"C7", []string{"PrivateNetwork"}},
		// A user manager implies a user namespace for every setting that needs a namespace (the mount, ipc and network ones), and a
		// process in it cannot read the host's /proc/<pid>/environ: the host's environment stays unreadable until ALL of them are
		// gone, so the pid, proc, user, filesystem and network fences are taken out together.
		{"C5", noMountFences},
		{"C6", []string{"PrivateIPC"}},
		{"C6", []string{"PrivatePIDs"}},
		{"C3", []string{"ProtectHome", "TemporaryFileSystem", "PrivateTmp"}},
	} {
		rs := r.canaries(t, mutated(r.c, without(tc.drop...)), tc.canary)
		if len(rs) != 1 || rs[0].Verdict != verifier.CanaryBroken {
			t.Errorf("without %v: %s", tc.drop, report(rs))
		}
	}
	// the control is untouched by any of them: a fence that "holds" because everything is broken would fail here
	if rs := r.canaries(t, mutated(r.c, without("PrivateNetwork")), "C0"); len(rs) != 1 || rs[0].Verdict != verifier.CanaryHeld {
		t.Errorf("control: %s", report(rs))
	}
	noUnitsLeft(t, r.c)
}

// With the unit's own kill mode weakened and the release skipped, a program that leaves a child behind outlives its check: the
// outlive canary sees it. (The sweep in the cleanup ends it; its RuntimeMaxSec is the backstop.)
func TestRealWithoutTheKillModeAndTheReleaseAnOrphanOutlivesTheCheck(t *testing.T) {
	r := newRealRig(t)
	// (a unit with its own pid namespace ends all its processes when its first one exits, whatever the kill mode says, so the pid
	// namespace goes too)
	m := mutated(r.c, func(p []string) []string {
		var out []string
		for _, q := range without("PrivatePIDs")(p) {
			if q == "KillMode=control-group" {
				q = "KillMode=process"
			}
			out = append(out, q)
		}
		return out
	})
	m.noRelease = true
	rs := r.canaries(t, m, "C10")
	left, lerr := r.c.Leftovers(context.Background())
	t.Logf("leftovers: %v %v", left, lerr)
	if len(rs) != 1 || rs[0].Verdict != verifier.CanaryBroken {
		t.Fatalf("%s", report(rs))
	}
	if _, err := r.c.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	noUnitsLeft(t, r.c)
}

// propRow is one property: what to set, which property to read back from the running unit, and what the read-back must show when
// the property is applied. pinned: the manager's default equals the value, so absence cannot be seen by reading it back (the
// property is explicit so a changed default or a drop-in cannot weaken the unit; its revert proof is the self-check refusal).
type propRow struct {
	set, show string
	ok        func(out string) bool
	pinned    bool
}

func reads(want string) func(string) bool {
	return func(out string) bool { return strings.Contains(out, want) }
}

func rows() []propRow {
	eq := func(set string) propRow {
		return propRow{set: set, show: strings.SplitN(set, "=", 2)[0], ok: reads(set)}
	}
	return []propRow{
		eq("PrivateNetwork=yes"), eq("PrivateIPC=yes"), eq("RestrictAddressFamilies=AF_UNIX"), eq("ProtectSystem=strict"), eq("ProtectHome=tmpfs"),
		eq("TemporaryFileSystem=/run"),
		{set: "InaccessiblePaths=-/var/run -/mnt -/media", show: "InaccessiblePaths", ok: reads("-/mnt")},
		eq("PrivateTmp=yes"), eq("PrivateDevices=yes"), eq("ProtectKernelTunables=yes"), eq("ProtectKernelModules=yes"), eq("ProtectKernelLogs=yes"),
		eq("ProtectControlGroups=yes"), eq("ProtectClock=yes"),
		{set: "BindPaths=<work>", show: "BindPaths", ok: reads("/crit-")},
		eq("PrivateUsers=yes"), eq("PrivatePIDs=yes"), eq("ProtectProc=invisible"), eq("ProcSubset=pid"), eq("NoNewPrivileges=yes"),
		{set: "CapabilityBoundingSet=", show: "CapabilityBoundingSet", ok: func(o string) bool { return strings.TrimSpace(o) == "CapabilityBoundingSet=" }},
		eq("RestrictNamespaces=yes"), eq("LockPersonality=yes"),
		{set: "MemoryDenyWriteExecute=no", show: "MemoryDenyWriteExecute", ok: reads("=no"), pinned: true},
		// the read-back shows the allowed list: the filter is on when it is a list of calls, and keyctl's are out of it
		{set: "SystemCallFilter=@system-service", show: "SystemCallFilter", ok: func(o string) bool { return strings.Contains(o, " execve ") && strings.Contains(o, " write") }},
		{set: "SystemCallFilter=~keyctl add_key request_key", show: "SystemCallFilter", ok: func(o string) bool { return !strings.Contains(o, "request_key") && strings.Contains(o, " execve ") }},
		eq("UMask=0077"), eq("KeyringMode=private"), eq("MemoryMax=268435456"), eq("MemorySwapMax=0"), eq("TasksMax=64"),
		{set: "CPUQuota=100%", show: "CPUQuotaPerSecUSec", ok: reads("CPUQuotaPerSecUSec=1s")},
		eq("LimitCPU=300"), eq("LimitAS=8589934592"), eq("LimitNOFILE=777"), eq("LimitFSIZE=4096"), eq("LimitCORE=0"),
		{set: "RuntimeMaxSec=315", show: "RuntimeMaxUSec", ok: reads("RuntimeMaxUSec=5min 15s")},
		{set: "KillMode=control-group", show: "KillMode", ok: reads("=control-group"), pinned: true},
		{set: "SendSIGKILL=yes", show: "SendSIGKILL", ok: reads("=yes"), pinned: true},
		{set: "TimeoutStopSec=5", show: "TimeoutStopUSec", ok: reads("TimeoutStopUSec=5s")},
	}
}

// startSleeper starts a unit through Place that sleeps, and returns its name and a stop function. mut alters the property list
// after the self-check.
func startSleeper(t *testing.T, r *realRig, c *Confiner) (string, func()) {
	t.Helper()
	work, err := os.MkdirTemp(r.base, "crit-")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"subject", "scratch/home", "scratch/tmp"} {
		if err := os.MkdirAll(filepath.Join(work, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("/the/exe")
	lim := finish.Limits{CPUSeconds: 300, AddressSpace: 8 << 30, OpenFiles: 777, FileSize: 4096, Procs: 3001}
	cmd.Args = finish.LimitsHelperArgv("/the/exe", lim, "/usr/bin/sleep", []string{"60"})
	cmd.Dir = filepath.Join(work, "subject")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + work + "/scratch/home", "TMPDIR=" + work + "/scratch/tmp"}
	cmd.SysProcAttr = sandboxAttr()
	release, err := c.Place(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		release()
		t.Fatal(err)
	}
	unit := strings.TrimPrefix(cmd.Args[6], "--unit=")
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	return unit + "|" + work, func() {
		release()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("the client did not end after the release")
		}
	}
}

func showUnit(t *testing.T, unit, prop string) string {
	t.Helper()
	ctl, err := trustedBinary("/usr/bin/systemctl", "/bin/systemctl")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := exec.Command(ctl, "--user", "show", "-p", prop, unit)
	cmd.Env = clientEnv(t)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("show %s %s: %v", unit, prop, err)
	}
	return out.String()
}

func clientEnv(t *testing.T) []string {
	t.Helper()
	d := os.Getenv("XDG_RUNTIME_DIR")
	return []string{"XDG_RUNTIME_DIR=" + d, "DBUS_SESSION_BUS_ADDRESS=unix:path=" + d + "/bus"}
}

// Every property of the list is applied by the manager (read back from the running unit), and the unit without it differs. This
// is the per-property revert proof for the properties no light canary can see by itself (they overlap: the fence has two layers).
func TestRealEveryPropertyIsAppliedByTheManagerAndItsAbsenceIsSeen(t *testing.T) {
	r := newRealRig(t)
	if r.c.version < 256 {
		t.Skip("PrivatePIDs needs systemd 256")
	}
	run := func(c *Confiner) (map[string]string, func()) {
		unitWork, stop := startSleeper(t, r, c)
		unit, _, _ := strings.Cut(unitWork, "|")
		got := map[string]string{}
		for _, row := range rows() {
			if _, ok := got[row.show]; !ok {
				got[row.show] = showUnit(t, unit, row.show)
			}
		}
		return got, stop
	}
	full, stop := run(r.c)
	for _, row := range rows() {
		if !row.ok(full[row.show]) {
			t.Errorf("%s is not applied: %q", row.set, full[row.show])
		}
	}
	stop()
	for _, row := range rows() {
		if row.pinned {
			continue
		}
		drop := func(p []string) []string {
			var out []string
			for _, q := range p {
				if q == row.set || (strings.HasPrefix(q, "BindPaths=") && strings.HasPrefix(row.set, "BindPaths=")) {
					continue
				}
				out = append(out, q)
			}
			return out
		}
		got, stop := run(mutated(r.c, drop))
		stop()
		if row.ok(got[row.show]) {
			t.Errorf("%s: the unit without it still reads %.120q", row.set, got[row.show])
		}
	}
	noUnitsLeft(t, r.c)
}

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
		time.Sleep(700 * time.Millisecond)
		ctl, _ := trustedBinary("/usr/bin/systemctl", "/bin/systemctl")
		k := exec.Command(ctl, "--user", "kill", "--signal=SIGKILL", "--kill-whom=all", strings.TrimPrefix(c.Args[6], "--unit="))
		k.Env = clientEnv(t)
		k.Run()
	}
	onPlaced = killer
	if _, code := run(`sleep 30`); code == 0 {
		t.Error("a program killed by a signal looked like a pass")
	}
	onPlaced = nil
	if _, code := run(`exit 0`); code != 0 {
		t.Errorf("exit 0 became %d", code)
	}
	noUnitsLeft(t, r.c)
}

// A stale unit (a crashed host's) is found and ended by name.
func TestRealSweepEndsAStaleUnit(t *testing.T) {
	r := newRealRig(t)
	unitWork, stop := startSleeper(t, r, r.c)
	unit, _, _ := strings.Cut(unitWork, "|")
	left, err := r.c.Leftovers(context.Background())
	if err != nil || len(left) != 1 || left[0] != unit+".service" {
		t.Fatalf("%v %v (want %s)", left, err, unit)
	}
	swept, err := r.c.Sweep(context.Background())
	if err != nil || len(swept) != 1 {
		t.Fatalf("%v %v", swept, err)
	}
	stop()
	noUnitsLeft(t, r.c)
}

// SelfTest end to end on the real manager with the heavy canaries held back.
func TestRealSelfTestPassesWithTheLightCanaries(t *testing.T) {
	r := newRealRig(t)
	r.c.canaries = func(ctx context.Context, conf verifier.Confiner, base string, o verifier.CanaryOptions) []verifier.CanaryResult {
		o.SkipHeavy = true
		o.HomeDir, o.RuntimeDir, o.TmpDir = r.home, r.rt, r.tmpdir
		return verifier.RunCanariesWith(ctx, conf, base, o)
	}
	if err := r.c.SelfTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	noUnitsLeft(t, r.c)
}

// The fork bomb and the memory hog run only on a machine the owner names.
func TestRealHeavyCanaries(t *testing.T) {
	if os.Getenv("MONOAGENT_CONFINER_HEAVY_TESTS") != "1" {
		t.Skip("set MONOAGENT_CONFINER_HEAVY_TESTS=1 on a machine meant for it")
	}
	r := newRealRig(t)
	rs := r.canaries(t, r.c, "C8", "C9")
	if err := verifier.CanariesHold(rs); err != nil {
		t.Fatalf("%v\n%s", err, report(rs))
	}
	noUnitsLeft(t, r.c)
}
