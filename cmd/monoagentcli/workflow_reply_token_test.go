package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/logging"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The review's reproduction: a node puts the run's resume URL in its data
// (trigger → core.set {resume: $execution.resumeUrl} → core.wait webhook →
// comm.email_send) and the chat runs it. Neither the chat's `workflow run
// --json` nor its `workflow execution --json` (or text) has the reply token;
// the user's own terminal still shows it.
func TestReplyTokenInNodeDataIsHiddenFromAMarkedProcess(t *testing.T) {
	_, cfg := persistTestEnv(t)
	t.Setenv("MONOAGENT_PUBLIC_URL", "http://127.0.0.1:19722")
	asOperatorProcess(t)
	var created workflow.Workflow
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "create", "resume-in-data")), &created); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, n := range [][3]string{
		{"trigger.manual", "Start", `{}`},
		{"core.set", "Keep", `{"resume":"{{ $execution.resumeUrl }}"}`},
		{"core.wait", "Wait", `{"resume":"webhook"}`},
		{"comm.email_send", "Mail", `{"to":"ada@example.test","subject":"Hi","body":"{{ $json.resume }}"}`},
	} {
		var node workflow.WorkflowNode
		if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "node", "add", created.ID, "--type", n[0], "--name", n[1], "--config", n[2])), &node); err != nil {
			t.Fatal(err)
		}
		if len(ids) > 0 {
			runWorkflowSubcmd(t, cfg, "connect", created.ID, "--from", ids[len(ids)-1], "--to", node.ID)
		}
		ids = append(ids, node.ID)
	}
	runWorkflowSubcmd(t, cfg, "activate", created.ID)

	t.Setenv("MONOAGENT_AI_CALLER", "chat")
	ran := runWorkflowSubcmd(t, cfg, "run", created.ID, "--timeout", "30s")
	var rec struct {
		ExecutionID string `json:"execution_id"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal([]byte(ran), &rec); err != nil || rec.Status != "WAITING" {
		t.Fatalf("run: %v\n%s", err, ran)
	}
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var token, spec string
	if err := db.DB.QueryRow(`SELECT token, spec FROM wait_replies WHERE execution_id = ?`, rec.ExecutionID).Scan(&token, &spec); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(spec, `"outbound":true`) {
		t.Fatalf("the wait in front of the email is not marked: %s", spec)
	}
	hidden := "http://127.0.0.1:19722/reply/" + workflow.ReplyTokenHidden
	if strings.Contains(ran, token) || !strings.Contains(ran, hidden) {
		t.Errorf("the chat's `workflow run --json`:\n%s", ran)
	}
	shown := runWorkflowSubcmd(t, cfg, "execution", rec.ExecutionID)
	if strings.Contains(shown, token) || !strings.Contains(shown, `"resume": "`+hidden+`"`) {
		t.Errorf("the chat's `workflow execution --json`:\n%s", shown)
	}
	cfg.JSONOutput = false
	if text := runWorkflowSubcmd(t, cfg, "execution", rec.ExecutionID); strings.Contains(text, token) {
		t.Errorf("the chat's `workflow execution`:\n%s", text)
	}

	asOperatorProcess(t)
	cfg.JSONOutput = true
	if mine := runWorkflowSubcmd(t, cfg, "execution", rec.ExecutionID); !strings.Contains(mine, `"resume": "http://127.0.0.1:19722/reply/`+token+`"`) {
		t.Errorf("the user's terminal lost the resume URL:\n%s", mine)
	}
}

// A node's error can hold a reply link, and the engine logs it: the log
// lines of a marked process (at --log-level=warn, which shows them) hide
// the token; the user's terminal keeps it.
func TestReplyTokenInALogLineIsHiddenFromAMarkedProcess(t *testing.T) {
	_, cfg := persistTestEnv(t)
	asOperatorProcess(t)
	token := strings.Repeat("k", 43)
	var created workflow.Workflow
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "create", "failing-reply")), &created); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, n := range [][3]string{{"trigger.manual", "Start", `{}`},
		{"http.request", "Call", `{"url":"http://127.0.0.1:1/reply/` + token + `","method":"GET"}`}} {
		var node workflow.WorkflowNode
		if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "node", "add", created.ID, "--type", n[0], "--name", n[1], "--config", n[2])), &node); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, node.ID)
	}
	runWorkflowSubcmd(t, cfg, "connect", created.ID, "--from", ids[0], "--to", ids[1])
	runWorkflowSubcmd(t, cfg, "activate", created.ID)
	t.Cleanup(func() { logging.SetLineFilter(nil) })

	logging.SetLineFilter(agentLogFilter())
	mine, _, _ := runLogsFor(t, cfg, created.ID, "--log-level=warn")
	if !strings.Contains(mine, "/reply/"+token) {
		t.Fatalf("the user's terminal: the failure's log lines have no reply link:\n%s", mine)
	}
	t.Setenv("MONOAGENT_AI_CALLER", "chat")
	logging.SetLineFilter(agentLogFilter())
	theirs, _, _ := runLogsFor(t, cfg, created.ID, "--log-level=warn")
	if strings.Contains(theirs, token) || !strings.Contains(theirs, "/reply/"+workflow.ReplyTokenHidden) {
		t.Errorf("the chat's log lines:\n%s", theirs)
	}
}

// The reviews' template transforms: each half of a slice, printf "%x", a
// split, a join with "-", "ZZ" or "abcdef" put between every character, and
// the token cut into seven 6-character pieces in separate fields, put in a
// node's data by a run the chat started. The chat's views have none of
// them; the user's terminal has all of them.
func TestTransformedReplyTokenIsHiddenFromAMarkedProcess(t *testing.T) {
	_, cfg := persistTestEnv(t)
	t.Setenv("MONOAGENT_PUBLIC_URL", "http://127.0.0.1:19727")
	asOperatorProcess(t)
	var created workflow.Workflow
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "create", "resume-transformed")), &created); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, n := range [][3]string{
		{"trigger.manual", "Start", `{}`},
		{"core.set", "Keep", `{"head":"{{ slice $execution.resumeUrl 0 50 }}","tail":"{{ slice $execution.resumeUrl 50 }}",` +
			`"hex":"{{ printf \"%x\" $execution.resumeUrl }}","chars":"{{ split \"\" $execution.resumeUrl }}",` +
			`"dashes":"{{ join \"-\" (split \"\" $execution.resumeUrl) }}","zz":"{{ replace \"\" \"ZZ\" $execution.resumeUrl }}",` +
			`"six":"{{ replace \"\" \"abcdef\" $execution.resumeUrl }}",` +
			pieceFields(29, 6, 7) + `}`},
		{"core.wait", "Wait", `{"resume":"webhook"}`},
		{"comm.email_send", "Mail", `{"to":"ada@example.test","subject":"Hi","body":"{{ $json.head }}{{ $json.tail }}"}`},
	} {
		var node workflow.WorkflowNode
		if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "node", "add", created.ID, "--type", n[0], "--name", n[1], "--config", n[2])), &node); err != nil {
			t.Fatal(err)
		}
		if len(ids) > 0 {
			runWorkflowSubcmd(t, cfg, "connect", created.ID, "--from", ids[len(ids)-1], "--to", node.ID)
		}
		ids = append(ids, node.ID)
	}
	runWorkflowSubcmd(t, cfg, "activate", created.ID)

	t.Setenv("MONOAGENT_AI_CALLER", "chat")
	ran := runWorkflowSubcmd(t, cfg, "run", created.ID, "--timeout", "30s")
	var rec struct {
		ExecutionID string `json:"execution_id"`
	}
	if err := json.Unmarshal([]byte(ran), &rec); err != nil {
		t.Fatal(err)
	}
	resume, err := workflow.ExecutionResumeURL(rec.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	token := resume[strings.LastIndex(resume, "/")+1:]
	forms := []string{resume[:50], resume[50:], fmt.Sprintf("%x", token), strings.Join(strings.Split(token, ""), " "),
		strings.Join(strings.Split(token[:10], ""), "-"), strings.Join(strings.Split(token[:10], ""), "ZZ"),
		strings.Join(strings.Split(token[:10], ""), "abcdef")}
	for i := 0; i < 7; i++ {
		forms = append(forms, `"`+token[6*i:6*i+6]+`"`)
	}
	shown := runWorkflowSubcmd(t, cfg, "execution", rec.ExecutionID)
	for _, out := range []string{ran, shown} {
		for _, form := range forms {
			if strings.Contains(out, form) {
				t.Errorf("the chat got %q:\n%s", form, out)
			}
		}
	}
	asOperatorProcess(t)
	mine := runWorkflowSubcmd(t, cfg, "execution", rec.ExecutionID)
	for _, form := range forms {
		if !strings.Contains(mine, form) {
			t.Errorf("the user's terminal lost %q:\n%s", form, mine)
		}
	}
}

// pieceFields is n core.set fields p0… holding the resume URL cut in pieces
// of size from offset from.
func pieceFields(from, size, n int) string {
	var f []string
	for i := 0; i < n; i++ {
		f = append(f, fmt.Sprintf(`"p%d":"{{ slice $execution.resumeUrl %d %d }}"`, i, from+size*i, from+size*(i+1)))
	}
	return strings.Join(f, ",")
}

// The log filter follows the turn-aware verdict: a process inside a coder session's turn that carries no
// marker has its reply links hidden from the log lines (issue 1333); the user's own terminal keeps them.
func TestAgentLogFilterHidesReplyTokensFromAnUnmarkedProcessInACoderTurn(t *testing.T) {
	asOperatorProcess(t)
	token := strings.Repeat("k", 43)
	line := "node failed: GET http://127.0.0.1:19731/reply/" + token + "\n"
	opgate.SetForTest(t, func(bool) string { return "" })
	if f := agentLogFilter(); f != nil && f(line) != line {
		t.Errorf("the user's terminal outside a turn: the line changed:\n%s", f(line))
	}
	opgate.SetForTest(t, func(bool) string { return "this process runs inside a coder turn" })
	f := agentLogFilter()
	if f == nil {
		t.Fatal("a cleared-marker turn gets no log filter")
	}
	if got := f(line); strings.Contains(got, token) {
		t.Errorf("a cleared-marker turn's log line holds the reply token:\n%s", got)
	}
}

// The flag that lets the log filter keep its verdict is set after the gate is registered, never before, and a
// verdict asked before registration is not kept.
func TestLogFilterKeepsNoVerdictFromBeforeTheGateIsRegistered(t *testing.T) {
	asOperatorProcess(t)
	opgate.SetForTest(t, func(bool) string { return "" })
	gateRegistered.Store(false)
	t.Cleanup(func() { gateRegistered.Store(false); afterGateRegister = func() {} })
	line := "GET http://127.0.0.1:19731/reply/" + strings.Repeat("k", 43) + "\n"
	f := agentLogFilter()
	if f(line) != line {
		t.Fatal("the user's terminal: the line changed")
	}
	opgate.SetForTest(t, func(bool) string { return "this process runs inside a coder turn" })
	if strings.Contains(f(line), strings.Repeat("k", 43)) {
		t.Fatal("a verdict asked before the gate was registered was kept")
	}
	var between bool
	afterGateRegister = func() { between = gateRegistered.Load() }
	registerOperatorGate(&globalConfig{})
	if between {
		t.Error("the flag was set before the gate was fully registered")
	}
	if !gateRegistered.Load() {
		t.Error("the flag is not set after registration")
	}
}
