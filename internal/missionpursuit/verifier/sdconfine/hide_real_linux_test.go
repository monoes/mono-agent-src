//go:build linux

package sdconfine

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
)

// runIn runs a shell script in a unit of c (through Place, as the sandbox would) and returns its output and exit status.
func runIn(t *testing.T, r *realRig, c verifier.Confiner, script string) (string, int) {
	t.Helper()
	work, err := os.MkdirTemp(r.base, "crit-")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"subject", "scratch/home", "scratch/tmp"} {
		os.MkdirAll(filepath.Join(work, d), 0o700)
	}
	cmd := exec.Command("/the/exe")
	lim := finish.Limits{CPUSeconds: 20, AddressSpace: 8 << 30, OpenFiles: 256, FileSize: 1 << 20, Procs: 3001}
	cmd.Args = finish.LimitsHelperArgv("/the/exe", lim, "/usr/bin/sh", []string{"-c", script})
	cmd.Dir = filepath.Join(work, "subject")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + work + "/scratch/home", "TMPDIR=" + work + "/scratch/tmp"}
	cmd.SysProcAttr = sandboxAttr()
	var out bytes.Buffer
	cmd.Stdout = &out
	release, err := c.Place(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	code := 0
	var ee *exec.ExitError
	if err := cmd.Run(); errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), code
}

// A live project folder outside the places the filesystem rules already hide (here a system folder stands in for it) is visible
// read-only without Hide and inaccessible with it.
func TestRealHideMakesALiveRootOutsideTheHiddenPlacesInaccessible(t *testing.T) {
	r := newRealRig(t)
	root := ""
	for _, c := range []string{"/usr/share/zoneinfo", "/usr/share/man", "/usr/share/doc", "/usr/share/locale"} {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			root = c
			break
		}
	}
	if root == "" {
		t.Skip("no stand-in folder")
	}
	if out, code := runIn(t, r, r.c, "ls '"+root+"' >/dev/null && echo visible"); code != 0 || out != "visible\n" {
		t.Fatalf("without Hide the stand-in is not visible: %q %d", out, code)
	}
	if out, code := runIn(t, r, r.c.Hide(root), "ls '"+root+"' >/dev/null 2>&1 && echo visible"); out == "visible\n" || code == 0 {
		t.Fatalf("with Hide the live root is still visible: %q %d", out, code)
	}
	// and a root that is not there (the usual case: under the hidden /home) is no error
	if _, code := runIn(t, r, r.c.Hide("/nonexistent/live/project"), "true"); code != 0 {
		t.Fatalf("a root that does not exist in the unit broke the unit: %d", code)
	}
	noUnitsLeft(t, r.c)
}
