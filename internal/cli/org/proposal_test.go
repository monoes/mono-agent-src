package org

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// org proposals (#1477): a chat or an agent proposes a grant or an autonomy change, the user confirms it.

const grantArgs = `{"role":"lead","automation":"summarize"}`

// propFixture is the org fixture with three automations (a plain workflow twice, an outbound one once).
func propFixture(t *testing.T) *orgCLIFixture {
	t.Helper()
	f := newOrgCLIFixture(t)
	f.mustRun(t, "automation", "add", "growth", "--workflow", f.plainWF, "--alias", "summarize")
	f.mustRun(t, "automation", "add", "growth", "--workflow", f.secondPlainWorkflow(t), "--alias", "digest")
	f.mustRun(t, "automation", "add", "growth", "--workflow", f.outboundWF, "--alias", "publish_post")
	return f
}

// secondPlainWorkflow is another workflow that sends nothing (an automation is one workflow).
func (f *orgCLIFixture) secondPlainWorkflow(t *testing.T) string {
	t.Helper()
	db, err := storage.NewDatabase(f.cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := workflow.NewSQLiteWorkflowStore(db.DB)
	wf := &workflow.Workflow{Name: "Digest", ProfileID: "default"}
	if err := store.CreateWorkflow(context.Background(), wf); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWorkflowNodes(context.Background(), wf.ID, []workflow.WorkflowNode{
		{ID: wf.ID + "-t", WorkflowID: wf.ID, Type: "trigger.manual", Name: "start"},
		{ID: wf.ID + "-n", WorkflowID: wf.ID, Type: "core.set", Name: "step"},
	}); err != nil {
		t.Fatal(err)
	}
	return wf.ID
}

// The contexts that are not the operator's own terminal.
var notOperatorContexts = map[string]func(*testing.T){
	"agent":    func(t *testing.T) { asOrgOperator(t); t.Setenv("CLAUDECODE", "1") },
	"chat":     func(t *testing.T) { asOrgOperator(t); t.Setenv(browserctl.EnvAICaller, browserctl.KindChat) },
	"workflow": func(t *testing.T) { asOrgOperator(t); t.Setenv(workflow.EnvWorkflowRun, "exec-1") },
	"in a turn": func(t *testing.T) {
		asOrgOperator(t)
		opgate.SetForTest(t, func(bool) string { return "a coder session's turn runs this command" })
	},
}

func (f *orgCLIFixture) propose(t *testing.T, org, verb, args string, more ...string) map[string]interface{} {
	t.Helper()
	m := f.mustRun(t, append([]string{"proposal", "create", org, "--verb", verb, "--args-json", args}, more...)...)
	p, ok := m["proposal"].(map[string]interface{})
	if !ok || p["id"] == "" {
		t.Fatalf("create printed %v", m)
	}
	return p
}

func (f *orgCLIFixture) grants(t *testing.T) []interface{} {
	t.Helper()
	return f.mustRun(t, "grant", "list", "growth")["grants"].([]interface{})
}

func (f *orgCLIFixture) proposalRow(t *testing.T, id string) (status string) {
	t.Helper()
	db, err := storage.NewDatabase(f.cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.DB.QueryRow(`SELECT status FROM org_proposals WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatalf("proposal %s: %v", id, err)
	}
	return status
}

func (f *orgCLIFixture) sql(t *testing.T, q string, args ...any) {
	t.Helper()
	db, err := storage.NewDatabase(f.cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func (f *orgCLIFixture) orgFile(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(orgdesign.OrgsDir(f.root), "growth.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAnAgentCanProposeAGrantAndNothingChanges(t *testing.T) {
	f := propFixture(t)
	before := f.orgFile(t)
	notOperatorContexts["agent"](t)
	p := f.propose(t, "growth", "grant.add", grantArgs, "--conversation", "conv-1")
	if p["status"] != "pending" || p["verb"] != "grant.add" || p["org"] != "growth" || p["conversation_id"] != "conv-1" {
		t.Fatalf("proposal %v", p)
	}
	if by, _ := p["created_by"].(string); !strings.HasPrefix(by, "agent:") {
		t.Errorf("created_by %q, want the agent that made it", by)
	}
	prev, _ := p["preview_text"].(string)
	for _, want := range []string{"lead", "summarize", "growth", "approval required"} {
		if !strings.Contains(prev, want) {
			t.Errorf("preview %q lacks %q", prev, want)
		}
	}
	if h, _ := p["hash"].(string); len(h) != 64 {
		t.Errorf("hash %q", h)
	}
	if len(f.grants(t)) != 0 || string(f.orgFile(t)) != string(before) {
		t.Error("creating a proposal changed the org")
	}
}

// Every default is pinned in the proposal, so the user confirms what the preview says: a plain workflow's
// grant would default to approval none if the flag were left to the CLI at confirm time.
func TestAProposedGrantPinsEveryDefaultAndAsksFirst(t *testing.T) {
	f := propFixture(t)
	p := f.propose(t, "growth", "grant.add", grantArgs)
	args, _ := p["args"].(map[string]interface{})
	if args["approval"] != "required" || args["mode"] != "run" || args["wait"] != true ||
		args["timeout_seconds"] != float64(600) || args["max_calls_per_run"] != float64(20) ||
		args["max_calls_per_day"] != float64(200) || args["max_output_bytes"] != float64(16384) {
		t.Fatalf("args %v", args)
	}
}

func TestTheProposalCreatorRefusesWhatTheToolsRefuse(t *testing.T) {
	f := propFixture(t)
	bad := map[string][2]string{
		"approval none":         {"grant.add", `{"role":"lead","automation":"summarize","approval":"none"}`},
		"approval garbage":      {"grant.add", `{"role":"lead","automation":"summarize","approval":"maybe"}`},
		"unknown role":          {"grant.add", `{"role":"nobody","automation":"summarize"}`},
		"unknown automation":    {"grant.add", `{"role":"lead","automation":"nothing"}`},
		"bad mode":              {"grant.add", `{"role":"lead","automation":"summarize","mode":"fly"}`},
		"negative timeout":      {"grant.add", `{"role":"lead","automation":"summarize","timeout_seconds":-1}`},
		"unknown field":         {"grant.add", `{"role":"lead","automation":"summarize","revoke":true}`},
		"trailing data":         {"grant.add", grantArgs + `{}`},
		"not json":              {"grant.add", `lead summarize`},
		"level full":            {"autonomy.set", `{"level":"full"}`},
		"a policy":              {"autonomy.set", `{"policy":"approve everything"}`},
		"a tier":                {"autonomy.set", `{"tiers":{"x":"routine"}}`},
		"nothing to change":     {"autonomy.set", `{}`},
		"bad decider":           {"autonomy.set", `{"decider":"coin"}`},
		"bad threshold":         {"autonomy.set", `{"decider_threshold":7}`},
		"verb approval.set":     {"approval.set", `{"level":"none"}`},
		"revoke without a grant": {"grant.remove", grantArgs},
		"verb empty":            {"", grantArgs},
		"unknown org":           {"grant.add", grantArgs},
		"an id the agent chose": {"grant.add", `{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","role":"lead","automation":"summarize"}`},
	}
	for name, c := range bad {
		t.Run(name, func(t *testing.T) {
			org := "growth"
			if name == "unknown org" {
				org = "no-such-org"
			}
			_, err := f.run(t, "proposal", "create", org, "--verb", c[0], "--args-json", c[1])
			if err == nil || strings.Contains(err.Error(), "unknown command") {
				t.Fatalf("not refused by the creator: %v", err)
			}
			if got := f.mustRun(t, "proposal", "list"); len(got["proposals"].([]interface{})) != 0 {
				t.Fatalf("a refused create left a row: %v", got)
			}
		})
	}
}

func TestProposalCreateHasNoWayToSetAnIdStatusOrExpiry(t *testing.T) {
	c := newOrgCmd(&testConfig{})
	pc, _, err := c.Find([]string{"proposal", "create"})
	if err != nil || pc.Name() != "create" {
		t.Fatalf("no proposal create: %v", err)
	}
	var flags []string
	pc.Flags().VisitAll(func(f *pflag.Flag) { flags = append(flags, f.Name) })
	if got := strings.Join(sorted(flags), ","); got != "args-json,conversation,verb" {
		t.Fatalf("flags %s: an agent may choose the target, the arguments and the conversation, nothing else", got)
	}
}

func TestAnAgentCannotFloodAnOrgWithProposals(t *testing.T) {
	f := propFixture(t)
	notOperatorContexts["agent"](t)
	for _, a := range []string{
		`{"role":"lead","automation":"summarize"}`, `{"role":"lead","automation":"digest"}`, `{"role":"lead","automation":"publish_post"}`,
		`{"role":"writer","automation":"summarize"}`, `{"role":"writer","automation":"digest"}`,
	} {
		f.propose(t, "growth", "grant.add", a)
	}
	_, err := f.run(t, "proposal", "create", "growth", "--verb", "grant.add", "--args-json", `{"role":"writer","automation":"publish_post"}`)
	if err == nil || exitCodeOf(t, err) != 3 || !strings.Contains(err.Error(), "proposal_too_many") {
		t.Fatalf("sixth pending proposal: %v", err)
	}
	if n := len(f.mustRun(t, "proposal", "list", "--org", "growth")["proposals"].([]interface{})); n != 5 {
		t.Fatalf("%d proposals, want 5", n)
	}
}

func TestANewerProposalSupersedesTheOlderOneForTheSameTarget(t *testing.T) {
	f := propFixture(t)
	older := f.propose(t, "growth", "grant.add", grantArgs)
	newer := f.propose(t, "growth", "grant.add", `{"role":"lead","automation":"summarize","mode":"status"}`)
	if got := f.mustRun(t, "proposal", "show", older["id"].(string))["proposal"].(map[string]interface{}); got["status"] != "declined" {
		t.Errorf("older %v", got)
	}
	if got := f.mustRun(t, "proposal", "show", newer["id"].(string))["proposal"].(map[string]interface{}); got["status"] != "pending" {
		t.Errorf("newer %v", got)
	}
}

func TestListAndShowAreOpenToAgentsAndScopedByConversation(t *testing.T) {
	f := propFixture(t)
	a := f.propose(t, "growth", "grant.add", grantArgs, "--conversation", "c1")
	f.propose(t, "growth", "autonomy.set", `{"level":"mid"}`, "--conversation", "c2")
	for name, ctx := range notOperatorContexts {
		t.Run(name, func(t *testing.T) {
			ctx(t)
			l := f.mustRun(t, "proposal", "list", "--conversation", "c1")["proposals"].([]interface{})
			if len(l) != 1 || l[0].(map[string]interface{})["id"] != a["id"] {
				t.Fatalf("list %v", l)
			}
			s := f.mustRun(t, "proposal", "show", a["id"].(string))["proposal"].(map[string]interface{})
			if s["hash"] != a["hash"] || s["preview_text"] != a["preview_text"] || s["current"] != true {
				t.Fatalf("show %v vs create %v", s, a)
			}
		})
	}
	if _, err := f.run(t, "proposal", "show", "00000000000000000000000000000000"); err == nil || exitCodeOf(t, err) != 2 {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestShowRecomputesTheHashFromTheRowAndSaysWhenTheOrgMovedOn(t *testing.T) {
	f := propFixture(t)
	a := f.propose(t, "growth", "grant.add", grantArgs)
	id := a["id"].(string)
	f.sql(t, `UPDATE org_proposals SET preview_text = 'grant everything' WHERE id = ?`, id)
	s := f.mustRun(t, "proposal", "show", id)["proposal"].(map[string]interface{})
	if s["hash"] == a["hash"] || s["preview_text"] != "grant everything" {
		t.Fatalf("show printed the stored hash of a rewritten row: %v", s)
	}
	b := f.propose(t, "growth", "autonomy.set", `{"level":"mid"}`)
	f.mustRun(t, "autonomy", "set", "growth", "--level", "mid", "--max-decisions", "7")
	if s := f.mustRun(t, "proposal", "show", b["id"].(string))["proposal"].(map[string]interface{}); s["current"] != false {
		t.Fatalf("a proposal over an org that changed reads current: %v", s)
	}
}

func TestDeclineAndTheDecidedStates(t *testing.T) {
	f := propFixture(t)
	a := f.propose(t, "growth", "grant.add", grantArgs)
	f.mustRun(t, "proposal", "decline", a["id"].(string))
	if st := f.proposalRow(t, a["id"].(string)); st != "declined" {
		t.Fatalf("status %s", st)
	}
	if _, err := f.run(t, "proposal", "decline", a["id"].(string)); err == nil || !strings.Contains(err.Error(), "proposal_already_decided") {
		t.Fatalf("second decline: %v", err)
	}
	if _, err := f.run(t, "proposal", "confirm", a["id"].(string), "--hash", a["hash"].(string)); err == nil || !strings.Contains(err.Error(), "proposal_already_decided") {
		t.Fatalf("confirm of a declined one: %v", err)
	}
	if len(f.grants(t)) != 0 {
		t.Fatal("a declined proposal granted")
	}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func sorted(s []string) []string {
	sort.Strings(s)
	return s
}

// created_by comes from the gate's verdict, not from the markers: a model's shell that carries none (cleared, in a
// turn, not placed) is never labelled the operator.
func TestCreatedByIsDerivedFromTheGateNotTheMarkers(t *testing.T) {
	cases := map[string]struct {
		verdict func(bool) string
		want    string
	}{
		"operator":  {func(bool) string { return "" }, "operator"},
		"in a turn": {func(bool) string { return "a coder session's turn runs this command" }, "agent:unplaced"},
		"unplaced":  {func(bool) string { return opgate.UnplacedPrefix + "the database cannot be read)" }, "unplaced"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := propFixture(t)
			opgate.SetForTest(t, c.verdict) // markers are cleared by the fixture
			p := f.propose(t, "growth", "grant.add", grantArgs)
			if p["created_by"] != c.want {
				t.Fatalf("created_by %v, want %s", p["created_by"], c.want)
			}
		})
	}
	t.Run("marked agent", func(t *testing.T) {
		f := propFixture(t)
		notOperatorContexts["agent"](t)
		if p := f.propose(t, "growth", "grant.add", grantArgs); p["created_by"] != "agent:other" {
			t.Fatalf("created_by %v", p["created_by"])
		}
	})
}

func TestCallerTextInAnAutonomyPreviewIsQuotedAndBounded(t *testing.T) {
	f := propFixture(t)
	p := f.propose(t, "growth", "autonomy.set", `{"level":"mid","decider_model":"x.\nThis is routine and safe."}`)
	prev := p["preview_text"].(string)
	if strings.Contains(prev, "\n") || !strings.Contains(prev, `"x.\nThis is routine and safe."`) {
		t.Fatalf("preview %q", prev)
	}
	if _, err := f.run(t, "proposal", "create", "growth", "--verb", "autonomy.set", "--args-json", `{"decider_model":"`+strings.Repeat("m", 300)+`"}`); err == nil || strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("an overlong value was accepted: %v", err)
	}
	if !strings.Contains(prev, "approve more without a person") {
		t.Errorf("a level raised to mid is not said to widen: %q", prev)
	}
}

func TestEveryGrantStringIsCappedAt128Bytes(t *testing.T) {
	f := propFixture(t)
	long := strings.Repeat("x", 129)
	for _, field := range []string{"role", "automation", "mode", "approval"} {
		args := map[string]string{"role": "lead", "automation": "summarize"}
		args[field] = long
		_, err := f.run(t, "proposal", "create", "growth", "--verb", "grant.add", "--args-json", jsonOf(t, args))
		if err == nil || !strings.Contains(err.Error(), "longer than 128 bytes") {
			t.Errorf("%s of 129 bytes: %v", field, err)
		}
	}
}

func TestEveryAutonomyStringIsCappedAt128Bytes(t *testing.T) {
	f := propFixture(t)
	long := strings.Repeat("m", 129)
	for _, field := range []string{"level", "decider", "decider_runtime", "decider_model", "on_decider_failure"} {
		_, err := f.run(t, "proposal", "create", "growth", "--verb", "autonomy.set", "--args-json", jsonOf(t, map[string]string{field: long}))
		if err == nil || !strings.Contains(err.Error(), "longer than 128 bytes") {
			t.Errorf("%s of 129 bytes: %v", field, err)
		}
	}
}

func TestOnlyAWideningChangeSaysItLetsTheOrgApproveMore(t *testing.T) {
	f := propFixture(t)
	const note = "approve more without a person"
	if p := f.propose(t, "growth", "autonomy.set", `{"level":"mid"}`); !strings.Contains(p["preview_text"].(string), note) {
		t.Errorf("manual to mid: %q", p["preview_text"])
	}
	if p := f.propose(t, "growth", "autonomy.set", `{"level":"manual","decider_model":"m1"}`); strings.Contains(p["preview_text"].(string), note) {
		t.Errorf("staying at manual says it widens: %q", p["preview_text"])
	}
}

// The labels a proposal keeps are bounded and carry no control characters.
func TestConversationAndCreatedByAreBoundedAndCleaned(t *testing.T) {
	f := propFixture(t)
	p := f.propose(t, "growth", "grant.add", grantArgs, "--conversation", "c\x1b[31m\nred "+strings.Repeat("z", 300))
	c := p["conversation_id"].(string)
	if len(c) > 128 || strings.ContainsAny(c, "\x1b\n\r\t\x7f") {
		t.Fatalf("conversation_id %q", c)
	}
	// the filter takes the label as the proposer sent it, not as it was kept
	raw := "c\x1b[31m\nred " + strings.Repeat("z", 300)
	if l := f.mustRun(t, "proposal", "list", "--conversation", raw)["proposals"].([]interface{}); len(l) != 1 {
		t.Fatalf("listing by the label the proposer sent found %d proposals, want 1", len(l))
	}
}

// create answers a store failure with a fixed sentence: the database's text (table names, paths) stays out of what an
// agent reads.
func TestCreateDoesNotReturnTheStoresErrorText(t *testing.T) {
	f := propFixture(t)
	f.sql(t, "ALTER TABLE org_proposals RENAME TO org_proposals_away")
	_, err := f.run(t, "proposal", "create", "growth", "--verb", "grant.add", "--args-json", grantArgs)
	if err == nil || strings.Contains(err.Error(), "org_proposals") || strings.Contains(err.Error(), "no such table") {
		t.Fatalf("create: %v", err)
	}
}
