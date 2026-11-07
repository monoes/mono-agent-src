//go:build windows

package procchain

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

const helperEnv = "PROCCHAIN_TEST_HELPER"

// The helper child prints its own chain; it is this test binary run again.
func TestHelperPrintChain(t *testing.T) {
	if os.Getenv(helperEnv) == "" {
		t.Skip("helper")
	}
	chain, ok := Chain()
	parts := []string{strconv.FormatBool(ok)}
	for _, p := range chain {
		parts = append(parts, strconv.Itoa(p))
	}
	os.Stdout.WriteString("CHAIN " + strings.Join(parts, " ") + "\n")
}

func runHelper(t *testing.T) []string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperPrintChain$", "-test.v")
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helper: %v", err)
	}
	for _, l := range strings.Split(string(out), "\n") {
		if f, ok := strings.CutPrefix(strings.TrimSpace(l), "CHAIN "); ok {
			return strings.Fields(f)
		}
	}
	t.Fatalf("no chain in %q", out)
	return nil
}

// A child's real chain on Windows runs from the child through this process.
func TestRealChainOfAChildNamesThisProcess(t *testing.T) {
	f := runHelper(t)
	if len(f) < 3 {
		t.Fatalf("chain %v", f)
	}
	if f[2] != strconv.Itoa(os.Getpid()) {
		t.Fatalf("the child's parent is %s, want %d (chain %v)", f[2], os.Getpid(), f)
	}
}

func TestRealStampIsEmptyForAnExitedProcessWhoseObjectIsStillHeld(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if Stamp(pid) == "" {
		// it may already be over; the loop below covers that case too
		t.Log("no stamp at once")
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if Stamp(pid) == "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// cmd.Wait has not run, so the object (and so the number) is still held: it has exited, so it has no stamp
	if s := Stamp(pid); s != "" {
		t.Fatalf("an exited process has the stamp %q", s)
	}
	_ = cmd.Wait()
}

func TestRealParentOfAProcessThatDoesNotExistIsNotBelieved(t *testing.T) {
	if _, ok := parentPID(os.Getpid()); !ok {
		t.Skip("the test runner's own parent cannot be read here")
	}
	if p, ok := parentPID(0x7ffffff0); ok {
		t.Fatalf("a process that does not exist has the parent %d", p)
	}
}

func TestRealDescendsFromThisProcessInAChild(t *testing.T) {
	f := runHelper(t)
	if len(f) < 3 || f[0] != "true" && f[0] != "false" {
		t.Fatalf("chain %v", f)
	}
	found := false
	for _, s := range f[1:] {
		if s == strconv.Itoa(os.Getpid()) {
			found = true
		}
	}
	if !found {
		t.Fatalf("this process is not in the child's chain %v", f)
	}
	if !InChain([]int{4242, os.Getpid()}, os.Getpid(), Stamp(os.Getpid())) {
		t.Error("InChain refused this process with its own stamp")
	}
	if InChain([]int{os.Getpid()}, os.Getpid(), "start:1") {
		t.Error("a pid whose process has another creation time counted")
	}
}

// A stamp that carries the boot is not the stamp of a process of another boot, and the boot is stable within one.
func TestBootStampCarriesTheBootAndIsStable(t *testing.T) {
	a, b := BootStamp(os.Getpid()), BootStamp(os.Getpid())
	if a == "" || a != b {
		t.Fatalf("boot stamps %q %q", a, b)
	}
	if !strings.Contains(a, "@boot-") {
		t.Fatalf("no boot in %q", a)
	}
	if SameStamp(strings.Split(a, "@")[0]+"@boot-1", a) {
		t.Error("a stamp of another boot matched")
	}
	if !SameStamp(a, Stamp(os.Getpid())) {
		t.Error("a boot stamp does not match the plain stamp of the same process")
	}
}
