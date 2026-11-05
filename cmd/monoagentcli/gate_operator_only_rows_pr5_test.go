package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/apikeys"
	"github.com/monoes/mono-agent/internal/monomind"
)

// operatorOnlyRowsPR5 are the org verbs converted in the fifth change of issue 1333, the two that only
// refuse and that nothing in a turn runs: `org role set-access ... full` (a role that runs like a coder
// chat, no approval gate) and `org teardown-profile` (revokes the profile's API keys, org grants and
// endpoints). Neither is a recovery verb, see TestNeitherOrgVerbIsARecoveryVerb. The args reach each gate
// before anything else: no --yes-i-understand for the grant, and --project (which the verb refuses for
// everyone, after the gate) for the teardown, so the operator's pass changes nothing and calls no monomind.
func operatorOnlyRowsPR5() []opRow {
	return []opRow{
		{path: "org role set-access", build: newOrgCmd, args: []string{"role", "set-access", "growth", "builder", "full"}},
		{path: "org teardown-profile", build: newOrgCmd, args: []string{"teardown-profile", "--project", "no-such-project"}},
	}
}

func TestNeitherOrgVerbIsARecoveryVerb(t *testing.T) {
	for _, r := range operatorOnlyRowsPR5() {
		if r.recovery {
			t.Errorf("%s is marked as a recovery verb", r.path)
		}
	}
}

// org families whose leaves are each a row, or open/held with a reason. A new leaf nobody decided about, and
// a converted verb whose row is deleted, both fail.
var orgGateFamilies = []string{"org role", "org teardown-profile", "org grant", "org autonomy", "org approve", "org chat approve", "org sign"}

const (
	heldOrgB   = "held: class B, a chat org tool or the org runner drives it with limits (issue 1333, waits for the owner's answers)"
	heldOrgSig = "class C: org sign has its own signature guard; not decided (issue 1333)"
)

var openOrgLeaves = map[string]string{
	"org grant list":         readOnlyVerb,
	"org autonomy show":      readOnlyVerb,
	"org grant add":          "operator-only since #1473 (org_turn_gate_test.go, class_b_test.go); the chat tool set_org_grant only proposes",
	"org grant remove":       "narrows what a role can run; not part of this change",
	"org autonomy decisions": readOnlyVerb,
	"org autonomy needs-you": readOnlyVerb,
	"org autonomy pause":     heldOrgB + ": the same widening guard as autonomy set (autonomy.go refuseWidening)",
	"org autonomy resume":    heldOrgB + ": the same widening guard as autonomy set (autonomy.go refuseWidening)",
	"org autonomy set":       "operator-only since #1473; the chat tool set_org_autonomy only proposes",
	"org approve":            heldOrgB + ": the chat's bash allow-list admits `org approve`; a refused caller may approve only what sends nothing",
	"org chat approve":       heldOrgB + ": the same approval path through the org chat",
	"org sign":               heldOrgSig,
}

func TestEveryLeafOfTheOrgGateFamiliesIsARowOrAnAcknowledgedOpenVerb(t *testing.T) {
	all, _ := treeKeys(t)
	rows := map[string]bool{}
	for _, r := range operatorOnlyRows() {
		rows[r.path] = true
	}
	var bad []string
	seen := map[string]bool{}
	for _, c := range allCommands(newRootCmd()) {
		k := commandKey(c)
		in := false
		for _, f := range orgGateFamilies {
			in = in || k == f || strings.HasPrefix(k, f+" ")
		}
		if !in || c.HasSubCommands() || c.Hidden {
			continue
		}
		seen[k] = true
		_, open := openOrgLeaves[k]
		switch {
		case rows[k] && open:
			bad = append(bad, k+": both a row and an open verb")
		case !rows[k] && !open:
			bad = append(bad, k+": a leaf of an org gate family with no row and no entry in openOrgLeaves")
		}
	}
	for k, why := range openOrgLeaves {
		if !all[k] || !seen[k] {
			bad = append(bad, k+": listed as open, but not a leaf command of the tree")
		}
		if why == "" {
			bad = append(bad, k+": an open verb needs a reason")
		}
	}
	for _, r := range operatorOnlyRowsPR5() {
		if !seen[r.path] {
			bad = append(bad, r.path+": a PR 5 row whose command is not a leaf of an org gate family")
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("the org families, the rows and the open verbs disagree:\n%s", strings.Join(bad, "\n"))
	}
}

func orgKeys(t *testing.T, cfg *globalConfig) *apikeys.Store {
	t.Helper()
	d, err := initDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return apikeys.NewStore(d.DB)
}

func activeKeys(t *testing.T, cfg *globalConfig) int {
	t.Helper()
	n, err := orgKeys(t, cfg).CountActive(context.Background(), "default")
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A refused teardown revokes nothing: the profile's key still authenticates afterwards. The operator's
// teardown (nothing running, no folder) does revoke it, and --dry-run is open to the process the gate refuses.
func TestOrgTeardownProfileRefusedInATurnChangesNothingAndDryRunStaysOpen(t *testing.T) {
	needAncestry(t)
	t.Setenv(monomind.EnvOverride, "/nonexistent/monomind")
	cfg := opEnv(t)
	if _, _, err := orgKeys(t, cfg).Create(context.Background(), "default", "ci", false); err != nil {
		t.Fatal(err)
	}
	markThisProcessAsACoderTurn(t, cfg)

	err := opRow{build: newOrgCmd, args: []string{"teardown-profile"}}.run(t, cfg)
	requireOperatorOnly(t, err)
	if !strings.Contains(err.Error(), "coder session's turn") {
		t.Fatalf("refused, but not by the turn ancestry: %v", err)
	}
	if n := activeKeys(t, cfg); n != 1 {
		t.Fatalf("the refused teardown left %d active keys, want 1", n)
	}
	if err := (opRow{build: newOrgCmd, args: []string{"teardown-profile", "--dry-run"}}).run(t, cfg); err != nil {
		t.Fatalf("--dry-run in a turn: %v (it is anyone's)", err)
	}
	if n := activeKeys(t, cfg); n != 1 {
		t.Fatalf("--dry-run changed the keys: %d active", n)
	}

	cfg = opEnv(t) // the operator, on a copy of the same fixture
	if _, _, err := orgKeys(t, cfg).Create(context.Background(), "default", "ci", false); err != nil {
		t.Fatal(err)
	}
	if err := (opRow{build: newOrgCmd, args: []string{"teardown-profile"}}).run(t, cfg); err != nil {
		t.Fatalf("the operator's teardown: %v", err)
	}
	if n := activeKeys(t, cfg); n != 0 {
		t.Fatalf("the operator's teardown left %d active keys", n)
	}
}

// The gate is first: before the org root lookup (which opens the database), before --project is looked at.
// A token holder never creates the database, and a process the gate refuses is told so, not that the
// database cannot be opened or that --project does not fit.
func TestOrgVerbsAreGatedBeforeTheRootLookup(t *testing.T) {
	for _, args := range [][]string{
		{"teardown-profile"},
		{"teardown-profile", "--project", "no-such-project"},
		{"role", "set-access", "growth", "builder", "full"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cfg := opEnv(t)
			cfg.DBPath = filepath.Join(t.TempDir(), "never.db")
			gateOSMenuContexts()["session-token"](t)
			err := opRow{build: newOrgCmd, args: args}.run(t, cfg)
			requireOperatorOnly(t, err)
			if _, statErr := os.Stat(cfg.DBPath); statErr == nil {
				t.Fatal("the database was created for a refused token holder")
			}
		})
		t.Run("unopenable database/"+strings.Join(args, " "), func(t *testing.T) {
			cfg := opEnv(t)
			cfg.DBPath = t.TempDir() // a directory: the database cannot be opened
			asAgentProcess(t)
			requireOperatorOnly(t, opRow{build: newOrgCmd, args: args}.run(t, cfg))
		})
	}
}

// A refused full-access grant asks monomind nothing (the role is unchanged); revoking (scoped) is not gated.
func TestOrgRoleSetAccessRefusedInATurnReachesNoMonomindAndScopedStaysOpen(t *testing.T) {
	needAncestry(t)
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "args.log")
	bin := filepath.Join(dir, "monomind")
	script := "#!/bin/sh\n" +
		`if [ "$1" = "--version" ]; then echo '{"v":1,"version":"9.0.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1","org-role-full-access"]}'; exit 0; fi` + "\n" +
		`echo "$*" >> '` + argsLog + "'\necho done\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monomind.EnvOverride, bin)
	cfg := opEnv(t)
	markThisProcessAsACoderTurn(t, cfg)
	project := t.TempDir()

	err := opRow{build: newOrgCmd, args: []string{"--project", project, "role", "set-access", "growth", "builder", "full", "--yes-i-understand"}}.run(t, cfg)
	requireOperatorOnly(t, err)
	if b, _ := os.ReadFile(argsLog); len(b) != 0 {
		t.Fatalf("monomind was asked to grant: %s", b)
	}
	// an invalid access is the same usage error for everyone: only "full" is the gated spelling
	err = opRow{build: newOrgCmd, args: []string{"--project", project, "role", "set-access", "growth", "builder", "bogus"}}.run(t, cfg)
	if err == nil || isOperatorOnly(err) {
		t.Fatalf("bogus access in a turn: %v (want the usage error)", err)
	}
	if err := (opRow{build: newOrgCmd, args: []string{"--project", project, "role", "set-access", "growth", "builder", "scoped"}}).run(t, cfg); err != nil {
		t.Fatalf("revoking in a turn: %v (scoped is not gated)", err)
	}
	if b, _ := os.ReadFile(argsLog); !strings.Contains(string(b), "scoped") {
		t.Fatalf("the revoke did not reach monomind: %q", b)
	}
}
