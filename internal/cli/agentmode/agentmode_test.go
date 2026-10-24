package agentmode

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every profile must deny the state directory and the platform's key store;
// these are the acceptance points of the sandbox profile.
func TestEveryProfileDeniesStateAndKeyStore(t *testing.T) {
	want := map[Runtime][]string{
		Bwrap:      {".monoagent", ".local/share/keyrings", "/bus", "DBUS_SESSION_BUS_ADDRESS"},
		Seatbelt:   {".monoagent", "Library/Keychains", "com.apple.SecurityServer"},
		ClaudeCode: {"~/.monoagent", "~/Library/Keychains", "~/.local/share/keyrings"},
	}
	for rt, needles := range want {
		out, err := Profile(rt)
		if err != nil {
			t.Fatalf("%s: %v", rt, err)
		}
		for _, n := range needles {
			if !strings.Contains(out, n) {
				t.Errorf("%s profile does not mention %q:\n%s", rt, n, out)
			}
		}
	}
}

func TestUnknownRuntimeIsRefused(t *testing.T) {
	if _, err := Profile("docker"); err == nil {
		t.Fatal("expected an error for an unknown runtime")
	}
}

func TestCommandPrintsProfile(t *testing.T) {
	cmd := NewCommand(Deps{})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"sandbox", "--runtime", "claude-code"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "~/.monoagent") {
		t.Fatalf("output: %s", buf.String())
	}
}

// The bwrap profile really hides the state directory, and still lets the
// agent reach a loopback port (network is not unshared).
func TestBwrapProfileHidesStateDir(t *testing.T) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("bwrap not installed")
	}
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".monoagent"), 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(home, ".monoagent", "monoagent.db")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	script, err := Profile(Bwrap)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sandbox.sh")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	probe := exec.Command("sh", "-c", "cat '"+secret+"' 2>&1; echo rc=$?")
	probe.Env = append(os.Environ(), "HOME="+home)
	if out, err := exec.Command(bwrap, "--bind", "/", "/", "true").CombinedOutput(); err != nil {
		t.Skipf("bwrap cannot create namespaces here: %s", out)
	}
	if out, _ := probe.CombinedOutput(); !strings.Contains(string(out), "secret") {
		t.Fatalf("control run without the sandbox cannot read the file: %s", out)
	}
	run := exec.Command("sh", path, "sh", "-c", "cat '"+secret+"' 2>&1; echo rc=$?")
	run.Env = append(os.Environ(), "HOME="+home)
	out, _ := run.CombinedOutput()
	if strings.Contains(string(out), "secret") || strings.Contains(string(out), "rc=0") {
		t.Fatalf("sandbox exposed ~/.monoagent: %s", out)
	}
}

func TestWindowsRefusalPointsAtTheRecipe(t *testing.T) {
	_, err := Profile(Windows)
	if err == nil || !strings.Contains(err.Error(), WindowsDoc) || !strings.Contains(err.Error(), "WSL2") {
		t.Fatalf("%v", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "http") {
		t.Errorf("the refusal must name the command, not a URL: %v", err)
	}
}

func TestWindowsCommandPrintsTheEmbeddedGuide(t *testing.T) {
	var out bytes.Buffer
	c := newWindowsCmd()
	c.SetOut(&out)
	if err := c.RunE(c, nil); err != nil {
		t.Fatal(err)
	}
	if first, _, _ := strings.Cut(out.String(), "\n"); first != "# Agent mode on Windows" {
		t.Errorf("first line %q", first)
	}
	if !strings.Contains(out.String(), "icacls") || out.Len() < 1000 {
		t.Errorf("embedded guide is missing or short: %d bytes", out.Len())
	}
}
