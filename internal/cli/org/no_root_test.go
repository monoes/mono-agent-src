package org

import (
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// With a database that cannot be opened an org command has no profile folder
// to act on. It must fail, and must not fall back to the current directory
// (filepath.Join("", ...) is relative to it).
func TestOrgCommandsWithUnopenableDBTouchNothingUnderCwd(t *testing.T) {
	groups := map[string][][]string{
		"proxy": {
			{"list"}, {"status", "acme"}, {"logs", "acme"}, {"report", "acme"}, {"costs", "acme"},
			{"flow", "acme"}, {"questions", "acme"}, {"approvals", "acme"}, {"gates", "acme"},
			{"decisions", "acme"}, {"memory", "acme"}, {"events", "acme"}, {"reload", "acme"},
			{"validate", "acme"}, {"answer", "acme", "q1", "yes"}, {"approve", "acme", "ceo", "ship"},
		},
		"root": {
			{"documents", "acme"}, {"schedule-audit", "acme"}, {"queued", "acme"},
			{"sign", "acme"}, {"serve"}, {"serve", "--stop"}, {"stop", "acme"}, {"pause", "acme"},
			{"resume", "acme"}, {"summary"}, {"legacy", "list"},
			{"chat", "history", "acme"}, {"chat", "approve", "acme", "gate-1"}, {"chat", "dismiss", "acme", "q1"},
		},
		"profile": {
			{"send", "acme", "--to", "ceo", "--body", "hi"}, {"rename", "acme", "beta"},
			{"delete", "acme", "--force"}, {"autonomy", "show", "acme"},
			{"proposal", "list"}, {"proposal", "show", "abc"}, {"proposal", "create", "acme", "--verb", "grant.add", "--args-json", "{}"},
		},
	}
	for group, cmds := range groups {
		for _, args := range cmds {
			name := group + "/" + strings.Join(args, " ")
			t.Run(name, func(t *testing.T) {
				testhome.SetHome(t, t.TempDir())
				t.Setenv("MONOMIND_BIN", "/nonexistent/monomind")
				cwd := t.TempDir()
				t.Chdir(cwd)
				// the database path is a directory: it cannot be opened
				cfg := &testConfig{DBPath: t.TempDir(), ProfileID: "default"}
				cmd := newOrgCmd(cfg)
				cmd.SetArgs(args)
				cmd.SilenceUsage, cmd.SilenceErrors = true, true
				var err error
				captureStdout(t, func() { err = cmd.Execute() })
				if err == nil {
					t.Fatalf("org %v succeeded with no database", args)
				}
				if strings.Contains(err.Error(), "accepts") || strings.Contains(err.Error(), "unknown") {
					t.Fatalf("org %v failed on its arguments, not the database: %v", args, err)
				}
				entries, _ := os.ReadDir(cwd)
				if len(entries) != 0 {
					var names []string
					for _, e := range entries {
						names = append(names, filepath.Join(cwd, e.Name()))
					}
					t.Fatalf("org %v wrote under the current directory: %v", args, names)
				}
			})
		}
	}
}

// `org summary` keeps reporting the local facts (the org files under an
// explicit --project) when the database cannot be opened, and still writes
// nothing under the current directory.
func TestOrgSummaryWithUnopenableDBKeepsLocalFacts(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	project := t.TempDir()
	writeOrgFile(t, project, "acme.json", `{"name":"acme"}`)
	cwd := t.TempDir()
	t.Chdir(cwd)
	cmd := newOrgCmd(&testConfig{DBPath: t.TempDir(), ProfileID: "default"})
	cmd.SetArgs([]string{"summary", "--fast", "--project", project})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	var err error
	out := captureStdout(t, func() { err = cmd.Execute() })
	if err != nil || !strings.Contains(out, `"acme"`) {
		t.Fatalf("summary: err %v, out %s", err, out)
	}
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Fatalf("summary wrote under the current directory: %v", entries)
	}
}

// noRootGuard is exactly these verbs. Each is outside requireRoot's up-front root lookup (which opens
// the database) for a reason written next to its entry; adding one is a decision, and so is removing one
// (the operator-gated verbs, set-access, teardown-profile and proposal confirm and decline, must stay in it, or a refused caller reaches the database first).
func TestNoRootGuardIsExactlyTheseVerbs(t *testing.T) {
	want := []string{"org proposal confirm", "org proposal decline", "org role set-access", "org sections-runtimes", "org summary", "org teardown-profile"}
	var got []string
	for k := range noRootGuard {
		got = append(got, k)
	}
	sort.Strings(got)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("noRootGuard = %v, want exactly %v", got, want)
	}
}
