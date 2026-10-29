//go:build darwin

package agentmode

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// sandboxRun runs argv under sandbox-exec with the profile text.
func sandboxRun(t *testing.T, prof string, argv ...string) ([]byte, error) {
	t.Helper()
	pf := filepath.Join(t.TempDir(), "agent.sb")
	if err := os.WriteFile(pf, []byte(prof), 0o600); err != nil {
		t.Fatal(err)
	}
	return exec.Command("/usr/bin/sandbox-exec", append([]string{"-f", pf}, argv...)...).CombinedOutput()
}

// APFS is case-insensitive by default: the same file is reached as /x/Me and /x/me. A rule
// written for one spelling must still deny the other, in both directions.
func TestSeatbeltCaseAlias(t *testing.T) {
	if _, err := exec.LookPath("/usr/bin/sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	onDisk := filepath.Join(base, "MixedCase")
	other := filepath.Join(base, "mixedcase")
	if err := os.MkdirAll(filepath.Join(onDisk, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(onDisk, ".ssh", "id_x"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(other, ".ssh", "id_x")); err != nil {
		t.Skip("this volume is case-sensitive; the alias does not exist")
	}
	// control: a profile for an unrelated home does not stop the read, so a denial below is the rule
	unrelated, err := seatbeltProfile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(onDisk, ".ssh", "id_x"), filepath.Join(other, ".ssh", "id_x")} {
		if out, err := sandboxRun(t, unrelated, "/bin/cat", p); err != nil || string(out) != "key" {
			t.Fatalf("control read of %s: %v %q", p, err, out)
		}
	}
	for _, c := range []struct{ name, home string }{{"profile for the on-disk case", onDisk}, {"profile for the other case", other}} {
		t.Run(c.name, func(t *testing.T) {
			prof, err := seatbeltProfile(c.home)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{filepath.Join(onDisk, ".ssh", "id_x"), filepath.Join(other, ".ssh", "id_x")} {
				out, err := sandboxRun(t, prof, "/bin/cat", p)
				if err == nil || !strings.Contains(string(out), "Operation not permitted") {
					t.Errorf("read of %s was not denied by the sandbox: %v %q", p, err, out)
				}
			}
		})
	}
}

// The Mach service denials: /usr/bin/security reads a keychain through securityd, so it works
// without the profile and fails with it. The keychain is a throwaway file outside every path the
// profile denies, so only the mach-lookup rules can be the cause. `security create-keychain`
// adds the keychain to the user's search list, so the test is opt-in
// (MONOAGENT_TEST_KEYCHAIN=1) and restores the list when it ends.
func TestSeatbeltMachDenial(t *testing.T) {
	if os.Getenv("MONOAGENT_TEST_KEYCHAIN") != "1" {
		t.Skip("opt-in: set MONOAGENT_TEST_KEYCHAIN=1 (it creates a keychain and touches the keychain search list)")
	}
	if _, err := exec.LookPath("/usr/bin/sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not available")
	}
	const sec = "/usr/bin/security"
	before, err := exec.Command(sec, "list-keychains", "-d", "user").Output()
	if err != nil {
		t.Skipf("security list-keychains: %v", err)
	}
	kc := filepath.Join(t.TempDir(), "throwaway.keychain-db")
	t.Cleanup(func() {
		_ = exec.Command(sec, "delete-keychain", kc).Run()
		args := []string{"list-keychains", "-d", "user", "-s"}
		for _, l := range strings.Split(strings.TrimSpace(string(before)), "\n") {
			args = append(args, strings.Trim(strings.TrimSpace(l), `"`))
		}
		_ = exec.Command(sec, args...).Run()
	})
	if out, err := exec.Command(sec, "create-keychain", "-p", "pw", kc).CombinedOutput(); err != nil {
		t.Skipf("cannot create a throwaway keychain here: %v %s", err, out)
	}
	if out, err := exec.Command(sec, "unlock-keychain", "-p", "pw", kc).CombinedOutput(); err != nil {
		t.Skipf("cannot unlock it: %v %s", err, out)
	}
	if out, err := exec.Command(sec, "add-generic-password", "-a", "acct", "-s", "svc", "-w", "the-secret", kc).CombinedOutput(); err != nil {
		t.Skipf("cannot add an item: %v %s", err, out)
	}
	find := []string{sec, "find-generic-password", "-a", "acct", "-s", "svc", "-w", kc}
	out, err := exec.Command(find[0], find[1:]...).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "the-secret" {
		t.Fatalf("control run without the profile: %v %q", err, out)
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prof, err := seatbeltProfile(home)
	if err != nil {
		t.Fatal(err)
	}
	out, err = sandboxRun(t, prof, find...)
	if err == nil || strings.Contains(string(out), "the-secret") {
		t.Fatalf("keychain read under the profile was not denied: %v %q", err, out)
	}
	// the denial must come from the Mach rules: the same run with them removed works again
	weak := strings.Replace(prof, "(deny mach-lookup", "(allow mach-lookup", 1)
	if out, err := sandboxRun(t, weak, find...); err != nil || strings.TrimSpace(string(out)) != "the-secret" {
		t.Fatalf("with the mach-lookup rule relaxed the read should work: %v %q", err, out)
	}
}
