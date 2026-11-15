package chat

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/storage"
)

// coder.appAccess: on by default, `coder set --app-access on|off` changes it
// (the operator's refusal of an agent is tested in cmd/monoagentcli), and
// --help says the command is the user's own.
func TestCoderSetAppAccess(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	withCoderCaps(t)
	var st coderStatus
	out, _ := runCoderCLI(t, dbPath, "status")
	decodeChatJSON(t, out, &st)
	if st.AppAccess == nil || !*st.AppAccess || !strings.Contains(out, `"appAccess":true`) {
		t.Fatalf("default appAccess: %s", out)
	}
	out, code := runCoderCLI(t, dbPath, "set", "--app-access", "off")
	st = coderStatus{}
	decodeChatJSON(t, out, &st)
	if code != 0 || st.AppAccess == nil || *st.AppAccess || *st.SendApproval != true {
		t.Fatalf("set off: exit %d %s", code, out)
	}
	out, _ = runCoderCLI(t, dbPath, "status")
	if !strings.Contains(out, `"appAccess":false`) {
		t.Fatalf("off is not kept: %s", out)
	}
	if _, code := runCoderCLI(t, dbPath, "set", "--app-access", "maybe"); code != 3 {
		t.Errorf("--app-access maybe: exit %d", code)
	}
	if out, code := runCoderCLI(t, dbPath, "set", "--app-access", "on"); code != 0 || !strings.Contains(out, `"appAccess":true`) {
		t.Errorf("set on: exit %d %s", code, out)
	}

	cmd := newCoderSetCmd(testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"}))
	var help bytes.Buffer
	cmd.SetOut(&help)
	cmd.SetArgs([]string{"--help"})
	_ = cmd.Execute()
	if h := help.String(); !strings.Contains(h, "--app-access") || !strings.Contains(h, clicore.OperatorOnlyHelp) {
		t.Errorf("coder set --help:\n%s", h)
	}
}

// runOrgTurn runs one dynamic-org turn through the fake monomind and returns
// the lead's and the worker's system prompts and the lead's first and second
// tool results; leadTools is the lead's tools file.
func runOrgTurn(t *testing.T, s coderSettings, call, call2 string) (lead, worker, reply, reply2 string) {
	lead, worker, reply, reply2, _ = runOrgTurnTools(t, s, call, call2)
	return lead, worker, reply, reply2
}

func runOrgTurnTools(t *testing.T, s coderSettings, call, call2 string) (lead, worker, reply, reply2, leadTools string) {
	t.Helper()
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("ORG_LEAD_CALL", call)
	t.Setenv("ORG_LEAD_CALL2", call2)
	dbPath := newChatCLITestDB(t)
	bin, argsLog := writeOrgMonomind(t)
	withCoderCaps(t, append(monomind.CoderCapabilities, monomind.CapAgentExecFullAccessTools, monomind.CapAgentExecAccessRead)...)
	withCoderScan(t, orgScan(true))
	s.Enabled = true
	setCoderSettings(t, dbPath, s)
	store := openTestChatStore(t, dbPath)
	conv, err := store.CreateConversationMode("default", "agent", "general", "claude", "", "opus", ai.ModeCoder, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, code := runChatHistory(t, dbPath, "default", "set-org", conv.ID, "dynamic"); code != 0 {
		t.Fatalf("set-org: exit %d", code)
	}
	if out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "turn-1", "--", "tidy my tasks"); err != nil {
		t.Fatalf("turn: %v\n%s", err, out)
	}
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(filepath.Dir(argsLog), name))
		return string(b)
	}
	return read("lead-system.txt"), read("worker-system.txt"), read("reply.json"), read("reply2.json"), read("lead-tools.json")
}

const (
	spawnWorkspace   = `{"v":1,"type":"tool_call","id":"c1","name":"org_spawn","args":{"brief":"tidy my tasks","access":"workspace","wait":true}}`
	spawnTaskManager = `{"v":1,"type":"tool_call","id":"c2","name":"org_spawn","args":{"brief":"tidy my tasks","role":"task-manager","wait":true}}`
	// From dynorg's workspaceRules and sendRule.
	workspaceRuleText = "every part of the user's mono-agent through"
	sendRuleText      = "NEVER finalize a send"
	// From profilectx's block: the profile summary.
	profileSummaryText = "You work for the mono-agent profile"
)

// The kill switch, end to end: off, the lead's prompt has no workspace or
// send rules, an explicit workspace spawn is refused, and a role that would
// default to workspace runs as coding without them.
func TestDynamicOrgWithAppAccessOff(t *testing.T) {
	off := false
	lead, worker, reply, reply2, tools := runOrgTurnTools(t, coderSettings{AppAccess: &off}, spawnWorkspace, spawnTaskManager)
	if lead == "" || worker == "" || tools == "" {
		t.Fatalf("nothing captured: lead %d bytes, worker %d bytes, tools %d bytes", len(lead), len(worker), len(tools))
	}
	if !strings.Contains(tools, "org_spawn") || strings.Contains(tools, "workspace (coding plus") {
		t.Errorf("app access off: org_spawn still offers workspace: %s", tools)
	}
	if strings.Contains(lead, workspaceRuleText) || strings.Contains(worker, workspaceRuleText) {
		t.Errorf("app access off: a prompt keeps the workspace rules")
	}
	// The send rule stays in both; the profile summary is in neither, the
	// profile id to pass as --profile is in both.
	for name, p := range map[string]string{"lead": lead, "worker": worker} {
		if !strings.Contains(p, sendRuleText) {
			t.Errorf("app access off: the %s's prompt lost the send rule", name)
		}
		if strings.Contains(p, profileSummaryText) {
			t.Errorf("app access off: the %s's prompt carries the profile summary", name)
		}
		if !strings.Contains(p, "`--profile default`") {
			t.Errorf("app access off: the %s's prompt lacks its profile id", name)
		}
	}
	if !strings.Contains(reply, "app access off") {
		t.Errorf("an explicit workspace spawn was not refused: %s", reply)
	}
	if !strings.Contains(reply2, `\"access\":\"coding\"`) {
		t.Errorf("task-manager was not lowered to coding: %s", reply2)
	}
}

func TestDynamicOrgWithAppAccessOn(t *testing.T) {
	lead, worker, _, reply2, tools := runOrgTurnTools(t, coderSettings{}, spawnWorkspace, spawnTaskManager)
	if !strings.Contains(tools, "workspace (coding plus") {
		t.Errorf("app access on: org_spawn does not offer workspace: %s", tools)
	}
	for _, rule := range []string{workspaceRuleText, sendRuleText, profileSummaryText} {
		if !strings.Contains(lead, rule) || !strings.Contains(worker, rule) {
			t.Errorf("app access on: a prompt lacks %q", rule)
		}
	}
	if !strings.Contains(reply2, `\"access\":\"workspace\"`) {
		t.Errorf("task-manager did not get workspace: %s", reply2)
	}
}

// The dynamic-org lead gets the profile context once: in its own coder
// prompt, not again in the org part (#471).
func TestDynamicOrgLeadGetsTheProfileOnce(t *testing.T) {
	lead, worker, _, _ := runOrgTurn(t, coderSettings{}, "", "")
	const header = "You work for the mono-agent profile"
	if n := strings.Count(lead, header); n != 1 {
		t.Errorf("the lead's prompt has the profile block %d times, want 1:\n%s", n, lead)
	}
	if n := strings.Count(worker, header); n != 1 {
		t.Errorf("the worker's prompt has the profile block %d times, want 1", n)
	}
}

// The solo coder lead too: off, no profile summary (the vault item names
// included); on, the summary is there.
func TestCoderLeadPromptHasTheProfileOnlyWithAppAccess(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	custom, _ := filepath.EvalSymlinks(t.TempDir())
	addTestProfile(t, dbPath, "work", "Work Stuff", custom)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(`INSERT INTO vault_secrets (id, seq, profile_id, kind, name, ciphertext, nonce, created_at, updated_at)
		VALUES ('s1', 1, 'work', 'login', 'bank-login', x'53454352455421', x'00', '2026-01-01', '2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "work", JSONOutput: true})
	on := coderLeadSystemPrompt(context.Background(), db.DB, cfg, custom, true, true)
	off := coderLeadSystemPrompt(context.Background(), db.DB, cfg, custom, false, true)
	if !strings.Contains(on, profileSummaryText) || !strings.Contains(on, "bank-login") {
		t.Errorf("app access on: the lead lacks the profile summary:\n%s", on)
	}
	if strings.Contains(off, profileSummaryText) || strings.Contains(off, "bank-login") || strings.Contains(off, "Work Stuff") {
		t.Errorf("app access off: the lead carries the profile summary:\n%s", off)
	}
	if !strings.Contains(off, "`--profile work`") {
		t.Errorf("app access off: the lead lacks its profile id:\n%s", off)
	}
}

// coder status in text says what the setting is.
func TestCoderStatusTextNamesAppAccess(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	withCoderCaps(t)
	off := false
	setCoderSettings(t, dbPath, coderSettings{AppAccess: &off})
	cmd := newCoderCmd(testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"}))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"status"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "app access (profile summary in prompts, org workers' MonoAgent surfaces): off") {
		t.Errorf("coder status:\n%s", out.String())
	}
}
