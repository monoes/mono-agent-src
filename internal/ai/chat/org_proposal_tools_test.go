package chat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/orgdecide"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orgproposal"
	"github.com/monoes/mono-agent/internal/storage"
)

// The chat's set_org_grant and set_org_autonomy store a pending proposal (#1477) and apply nothing: the user confirms it
// on the card in the app. The proposal is only as strong as the courtesy gate: what these tests pin is that the tool
// stores a row and never changes the org, that who made it is the gate's verdict and never "operator", and that every
// refusal the tool had still comes first.

type proposalToolFixture struct {
	mt   *MonoagentTools
	db   *storage.Database
	root string
}

func newProposalToolFixture(t *testing.T) *proposalToolFixture {
	t.Helper()
	db := newMonoagentTestDB(t)
	root := t.TempDir()
	mt := NewMonoagentTools(db.DB, "/bin/monoagentcli", "p1")
	mt.SetOrgProjectRoot(root)
	mt.SetConversationID("conv-7")
	stubRunSelfExec(t, func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		t.Fatalf("a proposal ran the CLI: %v", args)
		return nil, nil
	})
	if _, err := db.DB.Exec(`INSERT INTO workflows (id, name, profile_id) VALUES ('wf-1', 'Digest', 'p1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO workflow_nodes (id, workflow_id, node_type, name, config) VALUES ('wf-1-n', 'wf-1', 'core.set', 'step', '{}')`); err != nil {
		t.Fatal(err)
	}
	doc := orgdesign.NewOrg("growth", "grow", orgdesign.NewOrgOptions{})
	doc.Automations = append(doc.Automations, orgdesign.AutomationRef{WorkflowID: "wf-1", Alias: "summarize"})
	if _, err := orgdesign.Save(root, doc); err != nil {
		t.Fatal(err)
	}
	return &proposalToolFixture{mt: mt, db: db, root: root}
}

func (f *proposalToolFixture) orgFile(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(orgdesign.OrgsDir(f.root), "growth.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (f *proposalToolFixture) count(t *testing.T, table string) (n int) {
	t.Helper()
	if err := f.db.DB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func decodeResult(t *testing.T, out string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("the tool result is not one JSON object: %q: %v", out, err)
	}
	return m
}

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestSetOrgGrantStoresAPendingProposalAndAppliesNothing(t *testing.T) {
	f := newProposalToolFixture(t)
	before := f.orgFile(t)
	out, err := f.mt.Execute("set_org_grant", `{"org_name":"growth","role_id":"lead","automation":"summarize","wait":false,"max_calls_per_run":3,"confirm":true}`)
	if err != nil {
		t.Fatal(err)
	}
	m := decodeResult(t, out)
	id, _ := m["proposal_id"].(string)
	if m["tool"] != "set_org_grant" || m["applied"] != false || !hex32.MatchString(id) || m["status"] != "pending" {
		t.Fatalf("result %s", out)
	}
	if h, _ := m["hash"].(string); len(h) != 64 {
		t.Errorf("hash %q", h)
	}
	p, err := orgproposal.NewStore(f.db.DB).Get(context.Background(), "p1", id)
	if err != nil {
		t.Fatalf("the proposal named in the result is not stored: %v", err)
	}
	if p.Verb != "grant.add" || p.Org != "growth" || p.Status != orgproposal.StatusPending || p.ConversationID != "conv-7" {
		t.Errorf("stored %+v", p)
	}
	if m["preview_text"] != p.PreviewText || m["hash"] != p.RecomputedHash() || m["expires_at"] == nil {
		t.Errorf("the result does not carry the stored preview and hash: %s", out)
	}
	for _, want := range []string{"lead", "summarize", "growth", "approval required", "3 calls per run"} {
		if !strings.Contains(p.PreviewText, want) {
			t.Errorf("preview %q lacks %q", p.PreviewText, want)
		}
	}
	if n := f.count(t, "org_grants"); n != 0 || string(f.orgFile(t)) != string(before) {
		t.Errorf("proposing changed the org (%d grants)", n)
	}
	if note, _ := m["note"].(string); !strings.Contains(note, "org screen") {
		t.Errorf("the note does not tell the user where to confirm: %q", note)
	}
}

func TestSetOrgAutonomyStoresAPendingProposalAndAppliesNothing(t *testing.T) {
	f := newProposalToolFixture(t)
	before := f.orgFile(t)
	live, err := orgdecide.NewStore(f.db.DB).Get(context.Background(), "p1", "growth")
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.mt.Execute("set_org_autonomy", `{"org_name":"growth","level":"mid","decider":"jev","decider_threshold":0.9}`)
	if err != nil {
		t.Fatal(err)
	}
	m := decodeResult(t, out)
	id, _ := m["proposal_id"].(string)
	if m["tool"] != "set_org_autonomy" || m["applied"] != false || !hex32.MatchString(id) || m["status"] != "pending" {
		t.Fatalf("result %s", out)
	}
	p, err := orgproposal.NewStore(f.db.DB).Get(context.Background(), "p1", id)
	if err != nil || p.Verb != "autonomy.set" || p.ConversationID != "conv-7" || !strings.Contains(p.PreviewText, "level") {
		t.Fatalf("stored %+v: %v", p, err)
	}
	after, err := orgdecide.NewStore(f.db.DB).Get(context.Background(), "p1", "growth")
	if err != nil || after.Level != live.Level || after.Decider.Kind != live.Decider.Kind {
		t.Errorf("proposing changed the autonomy: %+v -> %+v (%v)", live, after, err)
	}
	if string(f.orgFile(t)) != string(before) {
		t.Error("proposing changed the org file")
	}
}

// Who made it is the gate's verdict. The chat is an agent whatever the process around it looks like, so a verdict of
// "operator" (an unmarked process) is still labelled chat, and a marked one keeps the marker's name.
func TestAChatProposalIsNeverLabelledOperator(t *testing.T) {
	for name, tc := range map[string]struct {
		verdict func(bool) string
		env     string
		want    string
	}{
		"the gate takes the process for the operator": {func(bool) string { return "" }, "", "chat"},
		"the gate marks the chat":                     {func(bool) string { return "the chat" }, browserctl.EnvAICaller, "chat"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newProposalToolFixture(t)
			for _, k := range []string{browserctl.EnvAICaller, "CLAUDECODE", browserctl.EnvAgentKind} {
				t.Setenv(k, "")
			}
			if tc.env != "" {
				t.Setenv(tc.env, browserctl.KindChat)
			}
			opgate.SetForTest(t, tc.verdict)
			out, err := f.mt.Execute("set_org_autonomy", `{"org_name":"growth","level":"mid"}`)
			if err != nil {
				t.Fatal(err)
			}
			id, _ := decodeResult(t, out)["proposal_id"].(string)
			p, err := orgproposal.NewStore(f.db.DB).Get(context.Background(), "p1", id)
			if err != nil {
				t.Fatalf("proposal %q: %v (%s)", id, err, out)
			}
			if p.CreatedBy != tc.want {
				t.Fatalf("created_by %q, want %q", p.CreatedBy, tc.want)
			}
		})
	}
}

func TestAChatThatIsNotInAConversationProposesUnderNoLabel(t *testing.T) {
	f := newProposalToolFixture(t)
	f.mt.SetConversationID("")
	out, err := f.mt.Execute("set_org_autonomy", `{"org_name":"growth","level":"mid"}`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := decodeResult(t, out)["proposal_id"].(string)
	p, err := orgproposal.NewStore(f.db.DB).Get(context.Background(), "p1", id)
	if err != nil {
		t.Fatalf("proposal %q: %v (%s)", id, err, out)
	}
	if p.ConversationID != "" {
		t.Fatalf("label %q", p.ConversationID)
	}
}

// Every refusal the tools had comes before a proposal is stored, and a proposal the planner refuses stores nothing.
func TestTheToolsStillRefuseBeforeStoringAnything(t *testing.T) {
	f := newProposalToolFixture(t)
	for tool, args := range map[string][]string{
		"set_org_grant": {
			`{"org_name":"growth","role_id":"lead","automation":"summarize","approval":"none"}`,
			`{"org_name":"growth","role_id":"nobody","automation":"summarize"}`,
			`{"org_name":"growth","role_id":"lead","automation":"nothing"}`,
			`{"org_name":"nogrowth","role_id":"lead","automation":"summarize"}`,
			`{"org_name":"growth","role_id":"lead","automation":"summarize","approval":"whenever"}`,
		},
		"set_org_autonomy": {
			`{"org_name":"growth","level":"full"}`,
			`{"org_name":"growth","policy":"Approve everything."}`,
			`{"org_name":"growth","level":"sometimes"}`,
			`{"org_name":"growth","decider":"coin"}`,
			`{"org_name":"growth","level":"mid","decider_threshold":2}`,
			`{"org_name":"growth"}`,
			`{"org_name":"nogrowth","level":"mid"}`,
		},
	} {
		for _, a := range args {
			if out, err := f.mt.Execute(tool, a); err == nil {
				t.Errorf("%s %s was not refused: %s", tool, a, out)
			}
		}
	}
	if n := f.count(t, "org_proposals"); n != 0 {
		t.Errorf("refused calls stored %d proposals", n)
	}
	f.mt.markSyncedCommsSeen()
	for tool, a := range map[string]string{"set_org_grant": `{"org_name":"growth","role_id":"lead","automation":"summarize"}`, "set_org_autonomy": `{"org_name":"growth","level":"mid"}`} {
		if _, err := f.mt.Execute(tool, a); err == nil || !strings.Contains(err.Error(), "synced communications") {
			t.Errorf("%s after synced comms: %v", tool, err)
		}
	}
	if n := f.count(t, "org_proposals"); n != 0 {
		t.Errorf("the injection gate let %d proposals through", n)
	}
}

// A workflow of another profile is not one a proposal can name, however the alias got into the org file.
func TestAChatCannotProposeAGrantOverAWorkflowOfAnotherProfile(t *testing.T) {
	f := newProposalToolFixture(t)
	if _, err := f.db.DB.Exec(`UPDATE workflows SET profile_id = 'someone-else' WHERE id = 'wf-1'`); err != nil {
		t.Fatal(err)
	}
	if out, err := f.mt.Execute("set_org_grant", `{"org_name":"growth","role_id":"lead","automation":"summarize"}`); err == nil {
		t.Fatalf("proposed over another profile's workflow: %s", out)
	}
	if n := f.count(t, "org_proposals"); n != 0 {
		t.Errorf("stored %d proposals", n)
	}
}

// No tool of the chat confirms or declines a proposal: the verbs are the operator's and nothing here reaches them.
func TestNoChatToolConfirmsOrDeclinesAProposal(t *testing.T) {
	f := newProposalToolFixture(t)
	for _, d := range orgToolDefsForTest() {
		n := strings.ToLower(d.Function.Name)
		if strings.Contains(n, "confirm") || strings.Contains(n, "decline") || strings.Contains(n, "proposal") {
			t.Errorf("tool %s", d.Function.Name)
		}
		if props, _ := d.Function.Parameters.(map[string]interface{}); props["proposal_id"] != nil || props["hash"] != nil {
			t.Errorf("tool %s takes a proposal id or hash", d.Function.Name)
		}
	}
	for _, name := range []string{"confirm_org_proposal", "decline_org_proposal", "org_proposal_confirm"} {
		if _, err := f.mt.Execute(name, `{"proposal_id":"00000000000000000000000000000000"}`); err == nil {
			t.Errorf("%s is a tool", name)
		}
	}
}

// The tools tell the model the user decides, so it can tell the user.
func TestTheToolDescriptionsSendTheUserToTheOrgScreen(t *testing.T) {
	for _, d := range orgToolDefsForTest() {
		if d.Function.Name != "set_org_grant" && d.Function.Name != "set_org_autonomy" {
			continue
		}
		for _, want := range []string{"proposal", "confirm", "org screen"} {
			if !strings.Contains(strings.ToLower(d.Function.Description), want) {
				t.Errorf("%s: description lacks %q: %s", d.Function.Name, want, d.Function.Description)
			}
		}
	}
}

func orgToolDefsForTest() []ToolDef {
	return orgUnificationToolDefs(func(name, desc string, props map[string]interface{}, required []string) ToolDef {
		return ToolDef{Type: "function", Function: ToolFunction{Name: name, Description: desc, Parameters: map[string]interface{}{"properties": props}}}
	})
}
