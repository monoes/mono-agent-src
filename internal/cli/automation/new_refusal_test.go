package automation

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `automation new --install` installs the package with local trust, so a coder turn may scaffold
// (no --install) but never install: the refusal comes before anything is written.
func TestNewInstallIsRefusedInACoderTurnButScaffoldingIsNot(t *testing.T) {
	d := testDeps(&testConfig{DBPath: filepath.Join(t.TempDir(), "a.db")})
	d.Refusal = func() string { return "inside a coder turn" }
	base := t.TempDir()
	run := func(args ...string) error {
		cmd := d.newAutomationNewCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		return cmd.Execute()
	}
	denied := filepath.Join(base, "denied")
	if err := run("denied-pkg", "--dir", denied, "--install"); err == nil || !strings.Contains(err.Error(), "inside a coder turn") {
		t.Fatalf("new --install in a turn: %v (want the refusal)", err)
	}
	if _, err := os.Stat(denied); err == nil {
		t.Fatal("a refused new --install still wrote the package")
	}
	if err := run("staged-pkg", "--dir", filepath.Join(base, "staged")); err != nil {
		t.Fatalf("scaffolding in a turn: %v", err)
	}
}
