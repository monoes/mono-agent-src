package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/agentpurposes"
	"github.com/monoes/mono-agent/internal/agentroster"
	"github.com/monoes/mono-agent/internal/storage"
)

var errNotOperator = errors.New("only the operator can do that")

// purposesDB is a database whose roster holds: claude/opus ready, claude/haiku ready, codex/gpt-5 stale (validated
// long ago), gemini/pro failed, and nothing else.
func purposesDB(t *testing.T) string {
	t.Helper()
	dbPath := newTestDBPath(t)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Now()
	for _, r := range []agentroster.Result{
		{Runtime: "claude", Model: "opus", Label: "Claude Opus", Status: agentroster.StatusOK, ValidatedAt: now, EffortLevels: []string{"low", "high"}},
		{Runtime: "claude", Model: "haiku", Status: agentroster.StatusOK, ValidatedAt: now},
		{Runtime: "codex", Model: "gpt-5", Status: agentroster.StatusOK, ValidatedAt: now.Add(-30 * 24 * time.Hour)},
		{Runtime: "gemini", Model: "pro", Status: agentroster.StatusError, ValidatedAt: now},
	} {
		if err := agentroster.Save(ctx, db.DB, r); err != nil {
			t.Fatal(err)
		}
	}
	return dbPath
}

// runPurposes runs `agent purposes <args>` with --json; operator says whether the caller passes the operator check.
func runPurposes(t *testing.T, dbPath string, operator bool, args ...string) (string, error) {
	t.Helper()
	d := testDeps(dbPath, true)
	if operator {
		d.RequireOperator = func(string) error { return nil }
	} else {
		d.RequireOperator = func(what string) error { return errNotOperator }
	}
	var err error
	out := captureStdout(t, func() {
		cmd := d.newAgentPurposesCmd()
		cmd.SetArgs(append(args, "--no-scan"))
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		err = cmd.Execute()
	})
	return out, err
}

type purposeJSON struct {
	ID                  string     `json:"id"`
	Title               string     `json:"title"`
	Description         string     `json:"description"`
	Categories          []string   `json:"categories"`
	Configured          bool       `json:"configured"`
	Pairs               []pairJSON `json:"pairs"`
	Rule                string     `json:"rule"`
	Effort              string     `json:"effort"`
	DifferentFromAuthor bool       `json:"different_from_author"`
	BudgetSharePct      int        `json:"budget_share_pct"`
	Fallback            bool       `json:"fallback"`
}

type pairJSON struct {
	Runtime    string `json:"runtime"`
	Model      string `json:"model"`
	Label      string `json:"label"`
	State      string `json:"state"`
	Selectable bool   `json:"selectable"`
}

type listJSON struct {
	Purposes  []purposeJSON `json:"purposes"`
	Available []pairJSON    `json:"available"`
}

func listPurposes(t *testing.T, dbPath string) listJSON {
	t.Helper()
	out, err := runPurposes(t, dbPath, false, "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	var got listJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("list is not JSON: %v\n%s", err, out)
	}
	return got
}

func findPurpose(t *testing.T, l listJSON, id string) purposeJSON {
	t.Helper()
	for _, p := range l.Purposes {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("purpose %s is not listed", id)
	return purposeJSON{}
}

// Anyone can read the purposes; with nothing set every purpose is unconfigured and the roster is offered with each
// pair's state.
func TestPurposesListShowsEveryPurposeAndTheRosterStates(t *testing.T) {
	dbPath := purposesDB(t)
	l := listPurposes(t, dbPath)
	if len(l.Purposes) != len(agentpurposes.Catalog) {
		t.Fatalf("%d purposes listed, the catalog has %d", len(l.Purposes), len(agentpurposes.Catalog))
	}
	coding := findPurpose(t, l, "coding")
	if coding.Configured || len(coding.Pairs) != 0 || coding.Rule != "any" || coding.Title == "" || coding.Description == "" || len(coding.Categories) == 0 {
		t.Fatalf("coding = %+v", coding)
	}
	states := map[string]pairJSON{}
	for _, p := range l.Available {
		states[p.Runtime+"/"+p.Model] = p
	}
	for key, want := range map[string]struct {
		state      string
		selectable bool
	}{"claude/opus": {"ready", true}, "claude/haiku": {"ready", true}, "codex/gpt-5": {"stale", true}, "gemini/pro": {"failed", false}} {
		if got, ok := states[key]; !ok || got.State != want.state || got.Selectable != want.selectable {
			t.Errorf("available %s = %+v, want state %s selectable %v", key, got, want.state, want.selectable)
		}
	}
	if states["claude/opus"].Label != "Claude Opus" {
		t.Errorf("label = %q", states["claude/opus"].Label)
	}
}

func TestPurposesSetStoresAndListShowsIt(t *testing.T) {
	dbPath := purposesDB(t)
	out, err := runPurposes(t, dbPath, true, "set", "reviewing", "--pair", "claude/opus", "--pair", "codex/gpt-5", "--rule", "order",
		"--effort", "high", "--different-model", "--budget-share", "25", "--fallback")
	if err != nil {
		t.Fatalf("set: %v\n%s", err, out)
	}
	var one struct {
		Purpose purposeJSON `json:"purpose"`
	}
	if err := json.Unmarshal([]byte(out), &one); err != nil {
		t.Fatal(err)
	}
	p := one.Purpose
	if p.ID != "reviewing" || !p.Configured || p.Rule != "order" || p.Effort != "high" || !p.DifferentFromAuthor || p.BudgetSharePct != 25 || !p.Fallback || len(p.Pairs) != 2 {
		t.Fatalf("set result = %+v", p)
	}
	if p.Pairs[0].Runtime != "claude" || p.Pairs[0].Model != "opus" || p.Pairs[0].State != "ready" || !p.Pairs[0].Selectable || p.Pairs[1].State != "stale" {
		t.Fatalf("pairs = %+v", p.Pairs)
	}
	listed := findPurpose(t, listPurposes(t, dbPath), "reviewing")
	if len(listed.Pairs) != 2 || listed.Rule != "order" || !listed.Configured {
		t.Fatalf("listed = %+v", listed)
	}
	// Other purposes are untouched, and setting again REPLACES the purpose's settings.
	if findPurpose(t, listPurposes(t, dbPath), "coding").Configured {
		t.Error("coding became configured")
	}
	if _, err := runPurposes(t, dbPath, true, "set", "reviewing", "--pair", "claude/haiku"); err != nil {
		t.Fatal(err)
	}
	again := findPurpose(t, listPurposes(t, dbPath), "reviewing")
	if len(again.Pairs) != 1 || again.Pairs[0].Model != "haiku" || again.Rule != "any" || again.Effort != "" || again.DifferentFromAuthor || again.Fallback || again.BudgetSharePct != 0 {
		t.Fatalf("a second set must replace everything: %+v", again)
	}
}

// Only the operator writes: a caller that fails the operator check changes nothing, whatever it passes.
func TestPurposesSetAndResetAreOperatorOnly(t *testing.T) {
	dbPath := purposesDB(t)
	if _, err := runPurposes(t, dbPath, true, "set", "coding", "--pair", "claude/opus"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"set", "coding", "--pair", "claude/haiku"},
		{"set", "reviewing", "--pair", "claude/haiku"},
		{"reset", "coding"},
		{"reset", "--all"},
		{"set", "no-such-purpose"}, // the operator check comes before anything else is read
		{"set"},
	} {
		out, err := runPurposes(t, dbPath, false, args...)
		if !errors.Is(err, errNotOperator) {
			t.Errorf("%v by a non-operator: err = %v\n%s", args, err, out)
		}
	}
	l := listPurposes(t, dbPath)
	coding := findPurpose(t, l, "coding")
	if len(coding.Pairs) != 1 || coding.Pairs[0].Model != "opus" || findPurpose(t, l, "reviewing").Configured {
		t.Fatalf("a refused caller changed the purposes: coding %+v", coding)
	}
	// Deps with no operator check wired refuse too: an unwired check never opens a command.
	d := testDeps(dbPath, true)
	var err error
	captureStdout(t, func() {
		cmd := d.newAgentPurposesCmd()
		cmd.SetArgs([]string{"set", "coding", "--pair", "claude/haiku"})
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		err = cmd.Execute()
	})
	if err == nil {
		t.Fatal("set ran with no operator check wired")
	}
}

func TestPurposesSetRefusesWhatIsNotAValidChoice(t *testing.T) {
	dbPath := purposesDB(t)
	for name, c := range map[string]struct {
		args []string
		code int
	}{
		"unknown purpose":           {[]string{"set", "nonsense", "--pair", "claude/opus"}, 2},
		"a failed pair":             {[]string{"set", "coding", "--pair", "gemini/pro"}, 3},
		"a pair not in the roster":  {[]string{"set", "coding", "--pair", "nobody/model"}, 3},
		"a malformed pair":          {[]string{"set", "coding", "--pair", "/opus"}, 3},
		"a good pair and a bad one": {[]string{"set", "coding", "--pair", "claude/opus", "--pair", "gemini/pro"}, 3},
		"an unknown rule":           {[]string{"set", "coding", "--pair", "claude/opus", "--rule", "random"}, 3},
		"a share over 100":          {[]string{"set", "coding", "--budget-share", "101"}, 3},
		"an effort the models lack": {[]string{"set", "coding", "--pair", "claude/haiku", "--effort", "high"}, 3},
		"a pair twice":              {[]string{"set", "coding", "--pair", "claude/opus", "--pair", "claude/opus"}, 3},
		"no purpose named":          {[]string{"set"}, 3},
		"two purposes named":        {[]string{"set", "coding", "reviewing"}, 3},
	} {
		out, err := runPurposes(t, dbPath, true, c.args...)
		if err == nil || exitCodeFor(err) != c.code {
			t.Errorf("%s: err = %v (exit %d), want exit %d\n%s", name, err, exitCodeFor(err), c.code, out)
		}
	}
	if findPurpose(t, listPurposes(t, dbPath), "coding").Configured {
		t.Fatal("a refused set stored something")
	}
	// A stale pair is a validated pair: it can be chosen. The effort must be offered by a chosen model.
	if _, err := runPurposes(t, dbPath, true, "set", "coding", "--pair", "codex/gpt-5"); err != nil {
		t.Fatalf("a stale pair was refused: %v", err)
	}
	if _, err := runPurposes(t, dbPath, true, "set", "coding", "--pair", "claude/opus", "--effort", "high"); err != nil {
		t.Fatalf("an effort the chosen model offers was refused: %v", err)
	}
}

// A chosen pair that later stops being usable stays in the purpose, shown with its state, so the pane can say so.
func TestPurposesKeepAChosenPairThatStoppedBeingUsable(t *testing.T) {
	dbPath := purposesDB(t)
	if _, err := runPurposes(t, dbPath, true, "set", "coding", "--pair", "claude/opus", "--pair", "claude/haiku"); err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := agentroster.Save(context.Background(), db.DB, agentroster.Result{Runtime: "claude", Model: "haiku", Status: agentroster.StatusError, ValidatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := agentroster.Remove(context.Background(), db.DB, "claude", "opus"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	coding := findPurpose(t, listPurposes(t, dbPath), "coding")
	if len(coding.Pairs) != 2 {
		t.Fatalf("pairs = %+v", coding.Pairs)
	}
	for _, p := range coding.Pairs {
		if p.Selectable {
			t.Errorf("%s/%s is selectable", p.Runtime, p.Model)
		}
		want := map[string]string{"opus": "unknown", "haiku": "failed"}[p.Model]
		if p.State != want {
			t.Errorf("%s state = %q, want %q", p.Model, p.State, want)
		}
	}
}

func TestPurposesReset(t *testing.T) {
	dbPath := purposesDB(t)
	for _, id := range []string{"coding", "reviewing", "testing"} {
		if _, err := runPurposes(t, dbPath, true, "set", id, "--pair", "claude/opus"); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runPurposes(t, dbPath, true, "reset", "coding")
	if err != nil || strings.TrimSpace(strings.Join(strings.Fields(out), "")) != `{"reset":["coding"]}` {
		t.Fatalf("reset coding: %v %s", err, out)
	}
	if again, err := runPurposes(t, dbPath, true, "reset", "coding"); err != nil || !strings.Contains(strings.Join(strings.Fields(again), ""), `"reset":[]`) {
		t.Fatalf("resetting what was not set: %v %s", err, again)
	}
	l := listPurposes(t, dbPath)
	if findPurpose(t, l, "coding").Configured || !findPurpose(t, l, "reviewing").Configured {
		t.Fatal("reset coding touched another purpose or did not reset")
	}
	out, err = runPurposes(t, dbPath, true, "reset", "--all")
	var all struct {
		Reset []string `json:"reset"`
	}
	if err != nil || json.Unmarshal([]byte(out), &all) != nil || len(all.Reset) != 2 {
		t.Fatalf("reset --all: %v %s", err, out)
	}
	for _, p := range listPurposes(t, dbPath).Purposes {
		if p.Configured {
			t.Errorf("%s is still configured", p.ID)
		}
	}
	for name, args := range map[string][]string{"unknown purpose": {"reset", "nonsense"}, "nothing named": {"reset"}, "both": {"reset", "coding", "--all"}} {
		if _, err := runPurposes(t, dbPath, true, args...); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := runPurposes(t, dbPath, true, "reset", "nonsense"); exitCodeFor(err) != 2 {
		t.Errorf("reset of an unknown purpose: exit %d, want 2", exitCodeFor(err))
	}
}
