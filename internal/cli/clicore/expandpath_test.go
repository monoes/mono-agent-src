package clicore

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandPathEResolvesHome(t *testing.T) {
	old := userHomeDir
	userHomeDir = func() (string, error) { return filepath.FromSlash("/h/me"), nil }
	defer func() { userHomeDir = old }()
	for in, want := range map[string]string{
		"~/x/y.db":  filepath.FromSlash("/h/me/x/y.db"),
		"~":         filepath.FromSlash("/h/me"),
		"/abs/y.db": "/abs/y.db",
		"rel/y.db":  "rel/y.db",
	} {
		if got, err := ExpandPathE(in); err != nil || got != want {
			t.Errorf("ExpandPathE(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if got, err := expandPathFor("windows", `~\x\y.db`); err != nil || got != filepath.Join(filepath.FromSlash("/h/me"), `x\y.db`) {
		t.Errorf("windows ~\\ = %q, %v", got, err)
	}
	// a backslash after ~ is an ordinary name character off Windows
	if got, err := expandPathFor("linux", `~\x`); err != nil || got != `~\x` {
		t.Errorf("linux ~\\ = %q, %v", got, err)
	}
}

// A home directory that cannot be found must be an error, not a literal "~/..." path
// that the caller then creates under the working directory.
func TestExpandPathEFailsWithoutAHome(t *testing.T) {
	old := userHomeDir
	userHomeDir = func() (string, error) { return "", errors.New("$HOME is not defined") }
	defer func() { userHomeDir = old }()
	for _, in := range []string{"~/.monoagent/x.db", "~"} {
		got, err := ExpandPathE(in)
		if err == nil || got != "" || !strings.Contains(err.Error(), "cannot resolve your home directory: set HOME") {
			t.Errorf("ExpandPathE(%q) = %q, %v", in, got, err)
		}
	}
	if got, err := ExpandPathE("/abs/y.db"); err != nil || got != "/abs/y.db" {
		t.Errorf("an absolute path needs no home: %q, %v", got, err)
	}
}
