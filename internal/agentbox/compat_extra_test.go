//go:build linux

package agentbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Compat mode keeps code-at-next-login files read-only, not just the shell rc files (R08-04).
func TestCompatKeepsLoginHooksReadOnly(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	for _, f := range []string{".zshenv", ".zprofile", ".zlogin", ".bash_login", ".pam_environment"} {
		if err := os.WriteFile(filepath.Join(r.home, f), []byte("orig"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{".config/environment.d", ".local/bin"} {
		if err := os.MkdirAll(filepath.Join(r.home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	p, err := r.box(Compat).Wrap("true", nil, r.env())
	if err != nil {
		t.Fatal(err)
	}
	j := strings.Join(p.Argv, " ")
	for _, f := range []string{".zshenv", ".zprofile", ".zlogin", ".bash_login", ".pam_environment", ".config/environment.d", ".local/bin"} {
		full := mountPoint(filepath.Join(r.home, f))
		if !strings.Contains(j, "--ro-bind "+full+" "+full) {
			t.Errorf("compat leaves ~/%s writable:\n%s", f, j)
		}
	}
}

// Compat masks the agent sockets under XDG_RUNTIME_DIR, not only the bus and keyring (R08-05).
func TestCompatMasksAgentSockets(t *testing.T) {
	needBwrap(t)
	r := newRig(t)
	run := t.TempDir()
	for _, d := range []string{"gnupg", "gcr", "p11-kit"} {
		if err := os.MkdirAll(filepath.Join(run, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"openssh_agent", "ssh-agent.socket"} {
		if err := os.WriteFile(filepath.Join(run, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := r.box(Compat).Wrap("true", nil, append(r.env(), "XDG_RUNTIME_DIR="+run))
	if err != nil {
		t.Fatal(err)
	}
	j := strings.Join(p.Argv, " ")
	for _, d := range []string{"gnupg", "gcr", "p11-kit"} {
		if !strings.Contains(j, "--tmpfs "+filepath.Join(run, d)) {
			t.Errorf("%s not masked:\n%s", d, j)
		}
	}
	for _, f := range []string{"openssh_agent", "ssh-agent.socket"} {
		if !strings.Contains(j, "--ro-bind /dev/null "+filepath.Join(run, f)) {
			t.Errorf("%s not masked:\n%s", f, j)
		}
	}
}
