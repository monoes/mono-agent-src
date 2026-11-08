package doomsday

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// Install hands the launcher a Place hook built from PlaceRelease. With no run open the hook leaves the command alone and
// returns a release to call; with a run open whose unit cannot be placed in, it refuses (the launcher starts nothing)
// and leaves no cgroup fd on the command. Mutant tried: Install without Place: the hook is nil and the test is red.
func TestInstallGivesTheLauncherAPlaceHookThatFollowsTheOpenRun(t *testing.T) {
	w, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	var got Hooks
	h := &coderhost.Host{Store: w.host, L: &fakeLauncher{}}
	g := &HostGate{Store: w.w.Store, Profile: "p1", RunDir: w.w.RunDir, Kernel: w.w.Kernel,
		InstallHooks: func(_ *coderhost.Store, _ *coderhost.Host, hk Hooks) error { got = hk; return nil }}
	if err := g.Install(w.host, h); err != nil {
		t.Fatal(err)
	}
	if got.Place == nil {
		t.Fatal("Install did not give the launcher a Place hook")
	}
	cmd := exec.Command("true")
	rel, err := got.Place(cmd, "p1", "c1")
	if err != nil || rel == nil || cmd.SysProcAttr != nil {
		t.Fatalf("no run open: err=%v release=%v attr=%+v", err, rel != nil, cmd.SysProcAttr)
	}
	rel()

	w.start(t)
	cmd = exec.Command("true")
	if rel, err = got.Place(cmd, "p1", "c1"); err == nil || rel != nil || cmd.SysProcAttr != nil {
		t.Fatalf("run open, unit cannot be placed in: err=%v release=%v attr=%+v", err, rel != nil, cmd.SysProcAttr)
	}
	if _, err = got.Place(exec.Command("true"), "other", "c1"); err == nil {
		t.Fatal("a foreign profile was placed")
	}
	g.Kernel = nil
	if _, err = got.Place(exec.Command("true"), "p1", "c1"); err == nil {
		t.Fatal("placed with no kernel while a run is open")
	}
}

// Inventory of every place the chat package starts a process. A process that belongs to a coder turn must go through
// the launcher's place hook when a run is open; everything else is listed with the reason it is not a turn child. A new
// start site fails this test until it is classified. Mutant tried: add an unlisted exec.Command to coder_host_exec.go: red.
func TestEveryCoderChildStartGoesThroughPlace(t *testing.T) {
	allowed := map[string]string{
		"coder_host_exec.go:exec.Command(l.bin":     "the turn's process: placed by l.place in startProcess (bound launch); a legacy start refuses when place is set",
		"coder_host_exec.go:exec.CommandContext":    "short-lived `chat history finish` helper, not a turn child",
		"coder_host_exec.go:clicore.DetachStart":    "legacy start: refused when place is set (see startProcess)",
		"coder_host.go:exec.Command(exe":            "starts the host itself, not a turn child",
		"coder_host.go:clicore.DetachStart":         "starts the host itself, not a turn child",
		"coder_host_stamp_other.go:exec.Command":    "ps lookup, not a turn child",
		"coder_host_signal_windows.go:exec.Command": "taskkill, not a turn child",
		"coder_background.go:exec.Command":          "ps lookup, not a turn child",
		"coder_queue_add.go:exec.CommandContext":    "queues a command for the host, short-lived, not a turn child",
	}
	dir := filepath.Join("..", "chat")
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no chat sources: %v", err)
	}
	site := regexp.MustCompile(`exec\.Command(Context)?\(|clicore\.DetachStart\(|os\.StartProcess\(|\.Start\(\)`)
	seen := map[string]bool{}
	for _, f := range files {
		base := filepath.Base(f)
		if strings.HasSuffix(base, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") || !site.MatchString(line) {
				continue
			}
			key := ""
			for k := range allowed {
				if strings.HasPrefix(k, base+":") && strings.Contains(line, strings.TrimPrefix(k, base+":")) {
					key = k
				}
			}
			// the bound launch (one file per OS that can run it): the turn's own process, started by boundDelivery.start right after the
			// placement hook (pre) ran on the command and before the process exists; the checks below hold each file to that order
			if key == "" && boundLaunchFiles[base] && strings.Contains(line, "cmd.Start()") {
				key = "bound_start:" + base
			}
			if key == "" {
				t.Errorf("%s starts a process that is not classified: %s", base, strings.TrimSpace(line))
			}
			seen[key] = true
		}
	}
	for k := range allowed {
		if !seen[k] {
			t.Errorf("inventory entry %q matches no start site any more: update the inventory", k)
		}
	}
	execSrc, _ := os.ReadFile(filepath.Join(dir, "coder_host_exec.go"))
	if !strings.Contains(string(execSrc), "l.place(c, profileID, conversationID)") || !strings.Contains(string(execSrc), "bd.start(cmd, pre)") {
		t.Error("the turn's process is not started through the place hook")
	}
	if !strings.Contains(string(execSrc), "a legacy start would run outside the placement") {
		t.Error("a legacy start with a place hook is no longer refused")
	}
	for name := range boundLaunchFiles {
		if !seen["bound_start:"+name] {
			t.Errorf("the bound launch's Start site was not found in %s", name)
		}
		bound, _ := os.ReadFile(filepath.Join(dir, name))
		i := strings.Index(string(bound), "pre(cmd)")
		j := strings.Index(string(bound), "cmd.Start()")
		if i < 0 || j < 0 || i > j {
			t.Errorf("%s: the placement no longer runs before the process exists", name)
		}
	}
}

// boundLaunchFiles are the files that start a coder turn through the bound launch, one per OS that can run it.
var boundLaunchFiles = map[string]bool{"coder_host_bound_linux.go": true, "coder_host_bound_darwin.go": true}
