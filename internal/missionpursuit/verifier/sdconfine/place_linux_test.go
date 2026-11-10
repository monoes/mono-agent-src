//go:build linux

package sdconfine

import (
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
)

const testWork = "/w/crit-1"

// sandboxCmd is the command finish.Sandbox hands to Place: the limits helper around the program, the working copy of the
// subject as its directory, an environment built from nothing, its own process group and an empty user and network namespace.
func sandboxCmd(prog string, args ...string) *exec.Cmd {
	lim := finish.Limits{CPUSeconds: 7, AddressSpace: 5 << 30, OpenFiles: 33, FileSize: 4096, Procs: 3001}
	cmd := exec.Command("/the/exe")
	cmd.Args = finish.LimitsHelperArgv("/the/exe", lim, prog, args)
	cmd.Dir = testWork + "/subject"
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + testWork + "/scratch/home", "TMPDIR=" + testWork + "/scratch/tmp", "LANG=C"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET}
	return cmd
}

func placeFor(t *testing.T, mut func(*Confiner)) (*Confiner, *fakeSys) {
	t.Helper()
	f := newFakeSys()
	c := newConfiner(t, f, Config{})
	if mut != nil {
		mut(c)
	}
	return c, f
}

func snapshot(cmd *exec.Cmd) string {
	return strings.Join(append([]string{cmd.Path, cmd.Dir}, append(cmd.Args, cmd.Env...)...), "\x00")
}

func TestPlaceTurnsTheSandboxCommandIntoOneTransientServiceWithTheDesignsArgv(t *testing.T) {
	c, _ := placeFor(t, nil)
	cmd := sandboxCmd("/usr/bin/sh", "-c", `echo "$HOME" $$ 100%`)
	release, err := c.Place(cmd)
	if err != nil || release == nil {
		t.Fatalf("%v", err)
	}
	want := []string{"/usr/bin/systemd-run", "--user", "--quiet", "--wait", "--pipe", "--unit=monoagent-check-4242-100-0000000000000001"}
	pre := Properties(Spec{Work: testWork, Lim: finish.Limits{CPUSeconds: 7, AddressSpace: 5 << 30, OpenFiles: 33, FileSize: 4096, Procs: 3001}, Caps: c.caps, PrivatePIDs: true})
	for _, p := range pre {
		want = append(want, "-p", p)
	}
	// the program starts under env -i: the manager's environment (a login session's imported secrets) never reaches it
	want = append(want, "--working-directory="+testWork+"/subject", "--", "/usr/bin/env", "-i", "PATH=/usr/local/bin:/usr/bin:/bin", "HOME="+testWork+"/scratch/home",
		"TMPDIR="+testWork+"/scratch/tmp", "LANG=C", "/usr/bin/sh", "-c", `echo "$$HOME" $$$$ 100%`)
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("argv:\n%s\nwant\n%s", strings.Join(cmd.Args, "\n"), strings.Join(want, "\n"))
	}
	if cmd.Path != "/usr/bin/systemd-run" {
		t.Fatalf("path %q", cmd.Path)
	}
	// the client sees only what it needs to reach the user manager: nothing of the host's environment, nothing of the criterion's
	if !reflect.DeepEqual(cmd.Env, []string{"XDG_RUNTIME_DIR=/run/user/1000", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus"}) {
		t.Fatalf("client env %v", cmd.Env)
	}
	// still in its own process group (the verifier refuses a confiner that dropped it)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid || cmd.SysProcAttr.Pgid != 0 {
		t.Fatalf("the process group was dropped: %+v", cmd.SysProcAttr)
	}
}

// systemd expands $VAR and $$ in a service's command line: a criterion's own dollar signs (in its arguments and in its environment
// values, which are arguments of env now) must reach the program unchanged and must not be filled in from the manager.
func TestDollarSignsInTheProgramArgumentsAndEnvironmentAreEscapedForTheManager(t *testing.T) {
	c, _ := placeFor(t, nil)
	cmd := sandboxCmd("/usr/bin/echo", "$HOME", "${X}", "$$", "a$b", "100%", "%h", ";", "plain")
	cmd.Env = append(cmd.Env, "K=a$b%h")
	if _, err := c.Place(cmd); err != nil {
		t.Fatal(err)
	}
	i := indexOf(cmd.Args, "--")
	got := cmd.Args[len(cmd.Args)-8:]
	if want := []string{"$$HOME", "$${X}", "$$$$", "a$$b", "100%", "%h", ";", "plain"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args %q, want %q", got, want)
	}
	if !contains(cmd.Args[i:], "K=a$$b%h") {
		t.Fatalf("the environment value is not escaped: %v", cmd.Args[i:])
	}
	if contains(cmd.Args, "--setenv=K=a$b%h") || strings.Contains(strings.Join(cmd.Args, "\n"), "--setenv") {
		t.Fatal("the environment goes through --setenv, which the manager merges with its own")
	}
}

func indexOf(xs []string, s string) int {
	for i, x := range xs {
		if x == s {
			return i
		}
	}
	return -1
}

// Revert proof for the scrub: the self-check refuses a command line whose program is not under env -i.
func TestTheArgvSelfCheckRefusesAProgramNotUnderEnvDashI(t *testing.T) {
	c, _ := placeFor(t, nil)
	good := []string{"--user", "--unit=" + unitA, "--", c.envBin, "-i", "PATH=/usr/bin", "/usr/bin/true"}
	if err := checkArgv(good, c.envBin); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string][]string{
		"no env":         {"--user", "--", "/usr/bin/true"},
		"env without -i": {"--user", "--", c.envBin, "PATH=/usr/bin", "/usr/bin/true"},
		"setenv used":    {"--user", "--setenv=A=b", "--", c.envBin, "-i", "/usr/bin/true"},
		"no separator":   {"--user", c.envBin, "-i"},
		"another env":    {"--user", "--", "/tmp/env", "-i", "/usr/bin/true"},
	} {
		if checkArgv(bad, c.envBin) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// A command that is not the shape the sandbox makes is refused, and refused means untouched (nothing half-rewritten can run).
func TestPlaceRefusesAnyShapeItDoesNotKnow(t *testing.T) {
	for name, mut := range map[string]func(*exec.Cmd){
		"not the limits helper": func(c *exec.Cmd) { c.Args = []string{"/the/exe", "/usr/bin/sh", "-c", "true"} },
		"relative program": func(c *exec.Cmd) {
			c.Args = finish.LimitsHelperArgv("/the/exe", finish.Limits{CPUSeconds: 1, AddressSpace: 1, OpenFiles: 1, FileSize: 1, Procs: 1}, "sh", nil)
		},
		"no directory":             func(c *exec.Cmd) { c.Dir = "" },
		"directory is not subject": func(c *exec.Cmd) { c.Dir = testWork + "/other" },
		"directory is relative":    func(c *exec.Cmd) { c.Dir = "w/crit-1/subject" },
		"home outside the work":    func(c *exec.Cmd) { c.Env[1] = "HOME=/home/x" },
		"tmp outside the work":     func(c *exec.Cmd) { c.Env[2] = "TMPDIR=/tmp" },
		"no home":                  func(c *exec.Cmd) { c.Env = c.Env[:1] },
		"no process group":         func(c *exec.Cmd) { c.SysProcAttr.Setpgid = false },
		"no sysprocattr":           func(c *exec.Cmd) { c.SysProcAttr = nil },
		"work path with a percent": func(c *exec.Cmd) {
			c.Dir, c.Env[1], c.Env[2] = "/w/a%b/subject", "HOME=/w/a%b/scratch/home", "TMPDIR=/w/a%b/scratch/tmp"
		},
		"work path with a newline": func(c *exec.Cmd) {
			c.Dir, c.Env[1], c.Env[2] = "/w/a\nb/subject", "HOME=/w/a\nb/scratch/home", "TMPDIR=/w/a\nb/scratch/tmp"
		},
		"program with an equals": func(c *exec.Cmd) {
			c.Args = finish.LimitsHelperArgv("/the/exe", finish.Limits{CPUSeconds: 1, AddressSpace: 1, OpenFiles: 1, FileSize: 1, Procs: 1}, "/usr/bin/a=b", nil)
		},
		"newline in an env value":  func(c *exec.Cmd) { c.Env = append(c.Env, "K=a\nb") },
		"env entry without equals": func(c *exec.Cmd) { c.Env = append(c.Env, "KEY") },
		"env name with a dash":     func(c *exec.Cmd) { c.Env = append(c.Env, "A-B=1") },
		"nul in an argument":       func(c *exec.Cmd) { c.Args[len(c.Args)-1] = "a\x00b" },
		"program under a dollar": func(c *exec.Cmd) {
			c.Args = finish.LimitsHelperArgv("/the/exe", finish.Limits{CPUSeconds: 1, AddressSpace: 1, OpenFiles: 1, FileSize: 1, Procs: 1}, "/usr/bin/$x", nil)
		},
	} {
		c, f := placeFor(t, nil)
		cmd := sandboxCmd("/usr/bin/sh", "-c", "true")
		mut(cmd)
		before := snapshot(cmd)
		release, err := c.Place(cmd)
		if err == nil || release != nil {
			t.Errorf("%s: placed (%v)", name, err)
			continue
		}
		if snapshot(cmd) != before {
			t.Errorf("%s: the command was touched although it was refused", name)
		}
		if len(f.called()) != 0 {
			t.Errorf("%s: the manager was asked something: %v", name, f.called())
		}
	}
}

// Revert proof, one subtest per property: if the builder ever emits a list without one of the design's properties, Place refuses
// and names it; nothing is run with a weaker fence.
func TestPlaceRefusesWhenTheBuilderDropsAnyOneProperty(t *testing.T) {
	for _, p := range goldenProps {
		p := p
		c, f := placeFor(t, func(c *Confiner) {
			c.propsFn = func(s Spec) []string {
				var out []string
				dropped := false
				for _, q := range Properties(s) {
					if q == p && !dropped {
						dropped = true
						continue
					}
					out = append(out, q)
				}
				return out
			}
		})
		cmd := sandboxCmd("/usr/bin/true")
		before := snapshot(cmd)
		release, err := c.Place(cmd)
		var u *Unavailable
		if err == nil || release != nil || !errors.As(err, &u) || u.Missing != MissingProperty || !strings.Contains(err.Error(), p) {
			t.Errorf("without %q: release %v err %v", p, release != nil, err)
		}
		if snapshot(cmd) != before || len(f.called()) != 0 {
			t.Errorf("without %q: something was done anyway", p)
		}
	}
}

func TestHideGivesAPerCheckCopyThatAlsoHidesTheLiveRoot(t *testing.T) {
	c, _ := placeFor(t, nil)
	h := c.Hide("/srv/live-project")
	cmd := sandboxCmd("/usr/bin/true")
	if _, err := h.Place(cmd); err != nil {
		t.Fatal(err)
	}
	if !contains(cmd.Args, "InaccessiblePaths=-/var/run -/mnt -/media -/srv/live-project") {
		t.Fatalf("the live root is not hidden: %v", cmd.Args)
	}
	// the original is unchanged and a second copy does not inherit the first's root
	cmd = sandboxCmd("/usr/bin/true")
	if _, err := c.Place(cmd); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(cmd.Args, " "), "live-project") {
		t.Fatal("Hide changed the confiner it was called on")
	}
	if h2 := c.Hide("/srv/other"); h2 == h {
		t.Fatal("Hide returned a shared copy")
	}
	// an unsafe root is refused at Place, never silently ignored (a root that is not hidden is a root the program can read)
	bad := c.Hide("/srv/a%b")
	cmd = sandboxCmd("/usr/bin/true")
	if _, err := bad.Place(cmd); err == nil {
		t.Fatal("an unsafe root was ignored")
	}
}

func TestEachPlaceMakesItsOwnUnitNameAndAnOddIDIsRefused(t *testing.T) {
	c, _ := placeFor(t, nil)
	a, b := sandboxCmd("/usr/bin/true"), sandboxCmd("/usr/bin/true")
	if _, err := c.Place(a); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Place(b); err != nil {
		t.Fatal(err)
	}
	if unitOf(a.Args) == unitOf(b.Args) || !strings.HasPrefix(unitOf(a.Args), "monoagent-check-4242-100-") {
		t.Fatalf("%q %q", unitOf(a.Args), unitOf(b.Args))
	}
	c.sys.newID = func() string { return "../../evil" }
	cmd := sandboxCmd("/usr/bin/true")
	if _, err := c.Place(cmd); err == nil {
		t.Fatal("a unit id that is not 16 hex digits was used")
	}
}

// A work path or a live root with a space, quote, backslash, colon or dollar is quoted for the manager, not refused; one it cannot
// carry (a percent, a double quote, a control character) is refused with a typed error that names the reason.
func TestOddPathsAreQuotedForTheManagerOrRefusedWithATypedError(t *testing.T) {
	for _, dir := range []string{"/w/My Project", "/w/q'uote", `/w/back\slash`, "/w/co:lon", "/w/dol$x", "/w/uni\u00e9"} {
		c, _ := placeFor(t, nil)
		cmd := sandboxCmd("/usr/bin/true")
		cmd.Dir = dir + "/subject"
		cmd.Env[1], cmd.Env[2] = "HOME="+dir+"/scratch/home", "TMPDIR="+dir+"/scratch/tmp"
		if _, err := c.Place(cmd); err != nil {
			t.Errorf("%q: %v", dir, err)
			continue
		}
		if !contains(cmd.Args, "BindPaths="+quoteUnit(dir)) || !contains(cmd.Args, "--working-directory="+dir+"/subject") {
			t.Errorf("%q: %v", dir, cmd.Args)
		}
	}
	for _, dir := range []string{"/w/a%b", `/w/a"b`, "/w/a\nb", "/w/a\x01b"} {
		c, _ := placeFor(t, nil)
		cmd := sandboxCmd("/usr/bin/true")
		cmd.Dir = dir + "/subject"
		cmd.Env[1], cmd.Env[2] = "HOME="+dir+"/scratch/home", "TMPDIR="+dir+"/scratch/tmp"
		before := snapshot(cmd)
		_, err := c.Place(cmd)
		var pe *PathError
		if !errors.As(err, &pe) || pe.Reason == "" || snapshot(cmd) != before {
			t.Errorf("%q: %v", dir, err)
		}
	}
	// the live root, quoted in the hidden list after its dash
	c, _ := placeFor(t, nil)
	h := c.Hide("/home/me/My Project")
	cmd := sandboxCmd("/usr/bin/true")
	if _, err := h.Place(cmd); err != nil {
		t.Fatal(err)
	}
	if !contains(cmd.Args, `InaccessiblePaths=-/var/run -/mnt -/media -"/home/me/My Project"`) {
		t.Fatalf("%v", cmd.Args)
	}
}

func unitOf(args []string) string {
	for _, a := range args {
		if u, ok := strings.CutPrefix(a, "--unit="); ok {
			return u
		}
	}
	return ""
}
