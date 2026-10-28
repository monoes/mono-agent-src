//go:build darwin

package agentmode

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Runs /usr/bin/sandbox-exec with the generated profile for a HOME reached through a
// symlink: a benign command still runs and a read of a denied file fails.
func TestSeatbeltRealRun(t *testing.T) {
	if _, err := exec.LookPath("/usr/bin/sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(base, "volume", "me")
	if err := os.MkdirAll(filepath.Join(real, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, ".ssh", "id_x"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "home")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	prof, err := seatbeltProfile(link)
	if err != nil {
		t.Fatal(err)
	}
	pf := filepath.Join(t.TempDir(), "agent.sb")
	if err := os.WriteFile(pf, []byte(prof), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/usr/bin/sandbox-exec", "-f", pf, "/bin/echo", "ok").CombinedOutput(); err != nil || string(out) != "ok\n" {
		t.Fatalf("benign command under the profile: %v %q", err, out)
	}
	for _, p := range []string{filepath.Join(link, ".ssh", "id_x"), filepath.Join(real, ".ssh", "id_x")} {
		if out, err := exec.Command("/usr/bin/sandbox-exec", "-f", pf, "/bin/cat", p).CombinedOutput(); err == nil {
			t.Errorf("read of %s was not denied: %q", p, out)
		}
	}
}
