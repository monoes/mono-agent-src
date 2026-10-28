package extensioncli

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/extension"
)

// allowDeps resolves a profile name to p-<name> without a database.
func allowDeps(t *testing.T) *Deps {
	d := testDepsDB(false, filepath.Join(t.TempDir(), "allow.db"))
	d.ResolveProfileID = func(_ *sql.DB, idOrName string) (string, error) {
		if idOrName == "ghost" {
			return "", clicore.InvalidInput("profile %q not found", idOrName)
		}
		return "p-" + strings.ToLower(idOrName), nil
	}
	return d
}

func TestBrowsersAllowRestrictsListsAndLifts(t *testing.T) {
	pairTestHome(t)
	home, _ := os.UserHomeDir()
	seedPaired(t, home, extension.PairedBrowser{ID: "aaaa1111bbbb2222", Label: "Edge Work", PairedAt: time.Now()})
	d := allowDeps(t)

	out, err := runExt(t, d, "browsers", "list")
	if err != nil || !strings.Contains(out, "any profile") {
		t.Fatalf("a record from before the limit must list as unrestricted: %q, %v", out, err)
	}
	out, err = runExt(t, d, "browsers", "allow", "aaaa", "Work", "Home")
	if err != nil || !strings.Contains(out, "p-work, p-home") {
		t.Fatalf("allow = %q, %v", out, err)
	}
	list, _ := extension.ListPairedBrowsers()
	if !list[0].Restricted || strings.Join(list[0].AllowedProfiles, ",") != "p-work,p-home" {
		t.Fatalf("stored = %+v", list[0])
	}
	if out, _ = runExt(t, d, "browsers", "list"); !strings.Contains(out, "p-work, p-home") {
		t.Fatalf("list = %q", out)
	}
	if out, err = runExt(t, d, "browsers", "allow", "aaaa"); err != nil || !strings.Contains(out, "none (default browser only)") {
		t.Fatalf("empty set = %q, %v", out, err)
	}
	if out, err = runExt(t, d, "browsers", "allow", "aaaa", "--any"); err != nil || !strings.Contains(out, "any profile") {
		t.Fatalf("--any = %q, %v", out, err)
	}
	if _, err := runExt(t, d, "browsers", "allow", "aaaa", "ghost"); err == nil {
		t.Fatal("an unknown profile was accepted")
	}
	if _, err := runExt(t, d, "browsers", "allow", "aaaa", "Work", "--any"); err == nil {
		t.Fatal("--any with profiles was accepted")
	}
	if _, err := runExt(t, d, "browsers", "allow", "zzzz", "Work"); err == nil {
		t.Fatal("an unknown browser was accepted")
	}
}

// A marked process (an agent's shell, a workflow run) cannot widen what a browser may run.
func TestBrowsersAllowIsOperatorOnly(t *testing.T) {
	pairTestHome(t)
	home, _ := os.UserHomeDir()
	seedPaired(t, home, extension.PairedBrowser{ID: "aaaa1111bbbb2222", Label: "Edge Work", Restricted: true, AllowedProfiles: []string{"p-a"}, PairedAt: time.Now()})
	t.Setenv("CLAUDECODE", "1")
	_, err := runExt(t, allowDeps(t), "browsers", "allow", "aaaa", "--any")
	if !operatorOnlyErr(err) {
		t.Fatalf("err = %v", err)
	}
	list, _ := extension.ListPairedBrowsers()
	if !list[0].Restricted {
		t.Fatalf("the limit was lifted: %+v", list[0])
	}
}
