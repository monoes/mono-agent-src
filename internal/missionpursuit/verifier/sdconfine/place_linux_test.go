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
	want := []string{"/usr/bin/systemd-run", "--user", "--quiet", "--wait", "--collect", "--pipe", "--unit=monoagent-check-0000000000000001"}
	pre := Properties(Spec{Work: testWork, Lim: finish.Limits{CPUSeconds: 7, AddressSpace: 5 << 30, OpenFiles: 33, FileSize: 4096, Procs: 3001}, Caps: c.caps, PrivatePIDs: true})
	for _, p := range pre {
		want = append(want, "-p", p)
	}
	want = append(want, "--setenv=PATH=/usr/local/bin:/usr/bin:/bin", "--setenv=HOME="+testWork+"/scratch/home", "--setenv=TMPDIR="+testWork+"/scratch/tmp", "--setenv=LANG=C",
		"--working-directory="+testWork+"/subject", "--", "/usr/bin/sh", "-c", `echo "$$HOME" $$$$ 100%`)
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

// systemd expands $VAR and $$ in the arguments of a service's command line: a criterion's own dollar signs must reach the
// program unchanged and must not be filled in from the manager's environment.
func TestDollarSignsInTheProgramArgumentsAreEscapedForTheManager(t *testing.T) {
	c, _ := placeFor(t, nil)
	cmd := sandboxCmd("/usr/bin/echo", "$HOME", "${X}", "$$", "a$b", "100%", "%h", ";", "plain")
	if _, err := c.Place(cmd); err != nil {
		t.Fatal(err)
	}
	got := cmd.Args[len(cmd.Args)-8:]
	if want := []string{"$$HOME", "$${X}", "$$$$", "a$$b", "100%", "%h", ";", "plain"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("args %q, want %q", got, want)
	}
	// environment values are not expanded by the manager and stay as they are
	cmd = sandboxCmd("/usr/bin/true")
	cmd.Env = append(cmd.Env, "K=a$b%h")
	if _, err := c.Place(cmd); err != nil || !contains(cmd.Args, "--setenv=K=a$b%h") {
		t.Fatalf("%v %v", err, cmd.Args)
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
		"work path with a space": func(c *exec.Cmd) {
			c.Dir = "/w/a b/subject"
			c.Env[1] = "HOME=/w/a b/scratch/home"
			c.Env[2] = "TMPDIR=/w/a b/scratch/tmp"
		},
		"work path with a colon": func(c *exec.Cmd) {
			c.Dir = "/w/a:b/subject"
			c.Env[1] = "HOME=/w/a:b/scratch/home"
			c.Env[2] = "TMPDIR=/w/a:b/scratch/tmp"
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
	if !contains(cmd.Args, "InaccessiblePaths=-/var/run -/mnt -/media /srv/live-project") {
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
	bad := c.Hide("/srv/a b")
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
	if a.Args[6] == b.Args[6] || !strings.HasPrefix(a.Args[6], "--unit=monoagent-check-") {
		t.Fatalf("%q %q", a.Args[6], b.Args[6])
	}
	c.sys.newID = func() string { return "../../evil" }
	cmd := sandboxCmd("/usr/bin/true")
	if _, err := c.Place(cmd); err == nil {
		t.Fatal("a unit id that is not 16 hex digits was used")
	}
}
