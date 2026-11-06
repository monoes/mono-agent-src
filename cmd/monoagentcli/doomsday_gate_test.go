package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const doomsdayTestCharter = `{"goal":"g","limits":{"budget_micro":1},
"pins":{"boss":{"provider":"p","model":"m","version":"1"},"judging_policy":"j"}}`

func writeDoomsdayCharter(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(p, []byte(doomsdayTestCharter), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// doomsdayVerbs is every verb of the group, and whether it writes (start and
// stop do, and are the user's own). A new verb fails
// TestDoomsdayVerbsAreExactlyTheClassifiedOnes until it is classified here.
var doomsdayVerbs = map[string][]string{
	"start":  {"--charter", "PATH", "--yes-i-lift", "--hash", "x"},
	"stop":   {},
	"status": nil,
	"render": nil,
	// The finish verbs (internal/cli/doomsday/finishverbs.go) are classified, gated and tested in
	// doomsday_finish_gate_test.go; the read-only strategy verbs in doomsday_strategy_gate_test.go.
}

func TestDoomsdayVerbsAreExactlyTheClassifiedOnes(t *testing.T) {
	grp := newDoomsdayCmd(sendEnv(t))
	var got []string
	for _, c := range grp.Commands() {
		got = append(got, c.Name())
	}
	sort.Strings(got)
	want := "boss,feed,finish,overview,render,rubric,start,status,stop,strategy"
	if governorVerbExpected { // the governor's read-only status group (doomsday_governor_gate_test.go)
		want = "boss,feed,finish,governor,overview,render,rubric,start,status,stop,strategy"
	}
	if strings.Join(got, ",") != want {
		t.Fatalf("doomsday verbs changed: %v: classify the new verb and its gate", got)
	}
}

// Nobody starts a run without the typed acknowledgement: a correct charter, a hash that is not the charter's and the
// script flag still start nothing, and the group is not listed anywhere. No pinned connection is made for it.
func TestStartWithAWrongHashStartsNothingAndTheGroupIsHidden(t *testing.T) {
	noPins(t)
	cfg := sendEnv(t)
	cfg.JSONOutput = false
	root := newRootCmd()
	grp, _, err := root.Find([]string{"doomsday"})
	if err != nil || grp == nil || grp.Name() != "doomsday" {
		t.Fatalf("doomsday group not found: %v", err)
	}
	if !grp.Hidden {
		t.Fatal("the doomsday group is listed in help")
	}
	help := captureStdout(t, func() { root.SetArgs([]string{"--help"}); _ = root.Execute() })
	if strings.Contains(help, "doomsday") {
		t.Fatal("help names the doomsday group")
	}
	file := writeDoomsdayCharter(t)
	for name, args := range map[string][]string{
		"script flags": {"--charter", file, "--yes-i-lift", "--hash", strings.Repeat("a", 64)},
		"bare":         {"--charter", file},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := runSendCLI(t, cfg, newDoomsdayCmd(cfg), append([]string{"start"}, args...)...)
			if err == nil {
				t.Fatalf("start did not refuse: %s", out)
			}
			if strings.Contains(out, "started") {
				t.Fatalf("a run started: %s", out)
			}
		})
	}
}

func TestDoomsdayWritingVerbsAreOperatorOnly(t *testing.T) {
	file := writeDoomsdayCharter(t)
	for ctxName, setup := range gateOSMenuContexts() {
		for _, verb := range []string{"start", "stop"} {
			t.Run(ctxName+"/"+verb, func(t *testing.T) {
				cfg := sendEnv(t)
				setup(t)
				args := []string{verb}
				for _, a := range doomsdayVerbs[verb] {
					if a == "PATH" {
						a = file
					}
					args = append(args, a)
				}
				_, err := runSendCLI(t, cfg, newDoomsdayCmd(cfg), args...)
				requireOperatorOnly(t, err)
			})
		}
	}
}

// No surface an agent reaches names the mode's store or tables: the MCP
// server, the workflow nodes, the HTTP API, the other CLI packages and verbs,
// the desktop app. Only the doomsday packages, their CLI verb and the
// coderhost/tasks seams (listed) may.
func TestNoAgentReachableSurfaceTouchesTheDoomsdayTables(t *testing.T) {
	root := filepath.Join("..", "..")
	scanned := []string{"internal/mcp", "internal/nodes", "internal/workflow", "internal/httpapi", "internal/cli", "internal/tools", "cmd/monoagentcli", "wails-app"}
	allowed := func(rel string) bool {
		return strings.HasPrefix(rel, "internal/cli/doomsday/") || strings.HasPrefix(rel, "cmd/monoagentcli/doomsday")
	}
	var bad []string
	for _, dir := range scanned {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if allowed(rel) {
				return nil
			}
			b, _ := os.ReadFile(p)
			for _, needle := range []string{"internal/doomsday", "doomsday_", "cli/doomsday"} {
				if strings.Contains(string(b), needle) {
					bad = append(bad, rel+" mentions "+needle)
				}
			}
			return nil
		})
	}
	if len(bad) > 0 {
		t.Fatalf("agent-reachable code touches the doomsday store or tables:\n%s", strings.Join(bad, "\n"))
	}
}

var _ = cobra.Command{}
