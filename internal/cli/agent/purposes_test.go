package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/agentpurposes"
	"github.com/monoes/mono-agent/internal/agentroster"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/storage"
)

// seedPurposesRoster stores a roster with one pair of each status.
func seedPurposesRoster(t *testing.T, dbPath string) {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Now()
	for _, r := range []agentroster.Result{
		{Runtime: "claude", Model: "opus", Status: agentroster.StatusOK, ValidatedAt: now},
		{Runtime: "claude", Model: "haiku", Status: agentroster.StatusOK, ValidatedAt: now},
		{Runtime: "claude", Model: "old", Status: agentroster.StatusOK, ValidatedAt: now.Add(-30 * 24 * time.Hour)},
		{Runtime: "codex", Model: "bad", Status: agentroster.StatusAuth, ValidatedAt: now},
	} {
		if err := agentroster.Save(ctx, db.DB, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := agentroster.AddManual(ctx, db.DB, "pi", "fresh"); err != nil {
		t.Fatal(err)
	}
}

// runPurposes runs `agent purposes ARGS` and returns stdout, stderr and the error.
func runPurposes(t *testing.T, d Deps, args ...string) (string, string, error) {
	t.Helper()
	oldErr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	var runErr error
	out := captureStdout(t, func() {
		cmd := d.newAgentPurposesCmd()
		cmd.SetArgs(args)
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		runErr = cmd.Execute()
	})
	os.Stderr = oldErr
	w.Close()
	var stderr bytes.Buffer
	_, _ = io.Copy(&stderr, r)
	return out, stderr.String(), runErr
}

// The list JSON is exactly the contract the Settings pane reads: this file.
func TestPurposesListMatchesTheGolden(t *testing.T) {
	dbPath := newTestDBPath(t)
	seedPurposesRoster(t, dbPath)
	d := testDeps(dbPath, true)
	if _, _, err := runPurposes(t, d, "set", "coding", "--pair", "claude:opus", "--pair", "claude:old", "--rule", "order", "--effort", "high"); err == nil {
		t.Fatal("a stale pair was chosen")
	}
	if _, _, err := runPurposes(t, d, "set", "review", "--pair", "claude:opus", "--pair", "claude:haiku", "--rule", "order", "--different-from-author", "on", "--fallback", "any", "--budget-share", "25", "--effort", "high"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runPurposes(t, d, "list")
	if err != nil {
		t.Fatal(err)
	}
	// Normalize the clock-dependent part: none of the output holds a time.
	golden := filepath.Join("testdata", "purposes_list.golden.json")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if out != string(want) {
		t.Errorf("list output differs from %s (UPDATE_GOLDEN=1 rewrites it):\n%s", golden, out)
	}
}

// Whatever the values, the keys are the contract's and nothing else.
func TestPurposesListHasExactlyTheContractKeys(t *testing.T) {
	dbPath := newTestDBPath(t)
	seedPurposesRoster(t, dbPath)
	out, _, err := runPurposes(t, testDeps(dbPath, true), "list")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	decodeJSON(t, out, &doc)
	if got := sortedKeys(doc); strings.Join(got, ",") != "purposes,roster" {
		t.Errorf("top-level keys = %v", got)
	}
	purposes := doc["purposes"].([]any)
	if len(purposes) != 18 {
		t.Errorf("%d purposes", len(purposes))
	}
	const want = "budgetSharePct,category,description,differentFromAuthor,effort,fallback,id,isDefault,pairs,rule,title"
	for _, p := range purposes {
		if got := strings.Join(sortedKeys(p.(map[string]any)), ","); got != want {
			t.Errorf("purpose keys = %s", got)
		}
	}
	for _, r := range doc["roster"].([]any) {
		if got := strings.Join(sortedKeys(r.(map[string]any)), ","); got != "model,runtime,status" {
			t.Errorf("roster keys = %s", got)
		}
	}
	statuses := map[string]string{}
	for _, r := range doc["roster"].([]any) {
		m := r.(map[string]any)
		statuses[m["runtime"].(string)+":"+m["model"].(string)] = m["status"].(string)
	}
	wantStatus := map[string]string{"claude:opus": "ok", "claude:haiku": "ok", "claude:old": "stale", "codex:bad": "failed", "pi:fresh": "unvalidated"}
	for k, v := range wantStatus {
		if statuses[k] != v {
			t.Errorf("%s = %q, want %q", k, statuses[k], v)
		}
	}
}

func sortedKeys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestPurposesSetRefusesAPairThatIsNotValidated(t *testing.T) {
	dbPath := newTestDBPath(t)
	seedPurposesRoster(t, dbPath)
	d := testDeps(dbPath, true)
	for pair, status := range map[string]string{"claude:old": "stale", "codex:bad": "failed", "pi:fresh": "unvalidated", "claude:nope": "unvalidated"} {
		_, _, err := runPurposes(t, d, "set", "coding", "--pair", "claude:opus", "--pair", pair)
		if exitCodeFor(err) != 3 || err == nil || !strings.Contains(err.Error(), "("+status+")") || !strings.Contains(err.Error(), pair[:strings.Index(pair, ":")]+"/") {
			t.Errorf("%s: exit %d, %v", pair, exitCodeFor(err), err)
		}
	}
	out, _, _ := runPurposes(t, d, "list")
	var doc purposesView
	decodeJSON(t, out, &doc)
	for _, p := range doc.Purposes {
		if !p.IsDefault {
			t.Errorf("%s was changed by a refused set", p.ID)
		}
	}
}

func TestPurposesSetResetAndTheArgumentChecks(t *testing.T) {
	dbPath := newTestDBPath(t)
	seedPurposesRoster(t, dbPath)
	d := testDeps(dbPath, true)
	out, _, err := runPurposes(t, d, "set", "review", "--pair", "claude:opus", "--pair", "claude:haiku", "--rule", "cheapest", "--different-from-author", "on", "--fallback", "any", "--effort", "high", "--budget-share", "40")
	if err != nil {
		t.Fatal(err)
	}
	var v purposeView
	decodeJSON(t, out, &v)
	if v.ID != "review" || v.IsDefault || len(v.Pairs) != 2 || v.Pairs[0].Status != "ok" || v.Rule != "cheapest" || !v.DifferentFromAuthor || v.Fallback != "any" || v.Effort != "high" || v.BudgetSharePct != 40 {
		t.Errorf("set = %+v", v)
	}
	// A second set replaces the whole setting; flags left out take their defaults.
	out, _, err = runPurposes(t, d, "set", "review", "--pair", "claude:haiku")
	decodeJSON(t, out, &v)
	if err != nil || len(v.Pairs) != 1 || v.Rule != "any" || v.DifferentFromAuthor || v.Fallback != "none" || v.BudgetSharePct != 0 {
		t.Errorf("replace = %+v %v", v, err)
	}
	out, _, err = runPurposes(t, d, "reset", "review")
	decodeJSON(t, out, &v)
	if err != nil || !v.IsDefault || len(v.Pairs) != 0 {
		t.Errorf("reset = %+v %v", v, err)
	}
	for name, args := range map[string][]string{
		"unknown purpose": {"set", "wizardry"},
		"no purpose":      {"set"},
		"pair syntax":     {"set", "coding", "--pair", "claude"},
		"bad rule":        {"set", "coding", "--rule", "random"},
		"bad fallback":    {"set", "coding", "--fallback", "maybe"},
		"bad diff":        {"set", "coding", "--different-from-author", "maybe"},
		"budget over":     {"set", "coding", "--budget-share", "101"},
		"bad effort":      {"set", "coding", "--effort", "Very High"},
		"reset unknown":   {"reset", "wizardry"},
	} {
		if _, _, err := runPurposes(t, d, args...); exitCodeFor(err) != 3 {
			t.Errorf("%s: exit %d, %v", name, exitCodeFor(err), err)
		}
	}
}

func TestPurposesListReportsACorruptRowAndReadsItAsTheDefault(t *testing.T) {
	dbPath := newTestDBPath(t)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveConfig(agentpurposes.Prefix+"p1.coding", `{"v":1,"pairs":[{"runtime":"claude"`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	out, stderr, err := runPurposes(t, testDeps(dbPath, true), "list")
	if err != nil {
		t.Fatal(err)
	}
	var doc purposesView
	decodeJSON(t, out, &doc)
	if !doc.Purposes[0].IsDefault || doc.Purposes[0].ID != "coding" {
		t.Errorf("coding = %+v", doc.Purposes[0])
	}
	if !strings.Contains(stderr, "coding") || !strings.Contains(stderr, "default") {
		t.Errorf("stderr = %q", stderr)
	}
}

// set and reset are the user's own: an agent, the chat and a workflow are
// refused before the arguments are read and before the database is opened, and
// so is a command whose check is not wired. list stays open.
func TestPurposesSetAndResetRefuseWhoeverIsNotTheOperator(t *testing.T) {
	dbPath := newTestDBPath(t)
	seedPurposesRoster(t, dbPath)
	broken := testDeps(dbPath, true)
	broken.OpenDB = func() (*storage.Database, error) { return nil, errors.New("must not be opened") }
	for name, refusal := range map[string]func() string{
		"an agent":   func() string { return "CLAUDECODE is set, so an agent is running this command" },
		"not wired":  nil,
		"the chat":   func() string { return "MONOAGENT_AI_CALLER is set, so the chat is running this command" },
		"a workflow": func() string { return "MONOAGENT_WORKFLOW_RUN is set, so a workflow run is running this command" },
	} {
		d := broken
		d.Refusal = refusal
		for _, args := range [][]string{
			{"set", "coding", "--pair", "claude:opus"},
			{"set"}, // wrong arguments: still the refusal first
			{"set", "wizardry", "--rule", "random"},
			{"reset", "coding"},
			{"reset"},
		} {
			_, _, err := runPurposes(t, d, args...)
			if clicore.ExitCodeFor(err) != 3 || !strings.Contains(err.Error(), "only the user") {
				t.Errorf("%s: %v: exit %d, %v", name, args, clicore.ExitCodeFor(err), err)
			}
			if err != nil && strings.Contains(err.Error(), "must not be opened") {
				t.Errorf("%s: %v opened the database before refusing", name, args)
			}
		}
	}
	// Nothing was changed, and list still answers an agent.
	d := testDeps(dbPath, true)
	d.Refusal = func() string { return "an agent" }
	out, _, err := runPurposes(t, d, "list")
	var doc purposesView
	decodeJSON(t, out, &doc)
	if err != nil || len(doc.Purposes) != 18 {
		t.Fatalf("list for an agent: %v", err)
	}
	for _, p := range doc.Purposes {
		if !p.IsDefault {
			t.Errorf("%s changed", p.ID)
		}
	}
}
