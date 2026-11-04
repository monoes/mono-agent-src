package publicationnodes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/publication"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// asTheUsersRun clears what makes a process not the operator's own terminal
// (a `go test` started from an agent's shell inherits its markers).
func asTheUsersRun(t *testing.T) {
	t.Helper()
	for _, k := range append(browserctl.AgentContextEnv(), browserctl.EnvAICaller, workflow.EnvWorkflowRun) {
		t.Setenv(k, "")
	}
}

// sentSend queues a send as an agent and has the user approve it; the send
// runs and is sent. It returns its id.
func sentSend(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	t.Setenv(hilsend.EnvAgentContext, "coder")
	out, handled, err := hilsend.Guard(ctx, "comm.email_send", workflow.NodeInput{}, map[string]interface{}{
		"smtp_host": "smtp.example.test", "from": "me@example.test", "to": "ada@example.test", "subject": "Hello", "body": "Hi"})
	t.Setenv(hilsend.EnvAgentContext, "")
	if err != nil || !handled {
		t.Fatalf("queue a send: handled %v, %v", handled, err)
	}
	id := out[0].Items[0].JSON["hil_id"].(string)
	sent := func(context.Context, hilsend.Run) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	}
	if _, err := hilsend.Resolve(ctx, hilsend.Store{DB: db, Profile: "default"}, id, true, hilsend.Edit{}, false, sent); err != nil {
		t.Fatal(err)
	}
	return id
}

// A run an AI started (the chat's or an agent's `workflow run`, the org side,
// a process that is not the operator's) registers only what belongs to a
// send the user approved, as `publication register` asks of an agent; the
// user's own runs register as before (#831 F2).
func TestRegisterFromAnAIRunNeedsAnApprovedSend(t *testing.T) {
	asTheUsersRun(t)
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	in := workflow.NodeInput{ExecutionID: "execution", NodeID: "register", Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"text": "hello"})}}
	config := func(approval string) map[string]interface{} {
		return map[string]interface{}{"platform": "email", "kind": "post", "body": "{{ $json.text }}", "approval": approval}
	}
	count := func() int {
		entries, err := publication.NewStore(db.DB, "default").List(ctx, publication.Filter{})
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	runs := map[string]func() context.Context{
		"a chat's run": func() context.Context {
			return workflow.WithTrigger(ctx, "trigger.manual", map[string]interface{}{workflow.AICallerKey: "chat"})
		},
		"an org's call":    func() context.Context { return workflow.WithTrigger(ctx, workflow.TriggerTypeOrgTool, nil) },
		"a workflow's run": func() context.Context { t.Setenv(workflow.EnvWorkflowRun, "exec-7"); return ctx },
	}
	for name, run := range runs {
		for _, approval := range []string{"", "missing"} {
			if _, err := (&RegisterNode{}).Execute(run(), in, config(approval)); err == nil || !strings.Contains(err.Error(), "needs an approved send") {
				t.Errorf("%s, approval %q: %v", name, approval, err)
			}
		}
		t.Setenv(workflow.EnvWorkflowRun, "")
	}
	if n := count(); n != 0 {
		t.Fatalf("%d publications registered without approval", n)
	}

	id := sentSend(t, ctx, db.DB)
	other := config(id)
	other["platform"] = "custom"
	if _, err := (&RegisterNode{}).Execute(runs["a chat's run"](), in, other); err == nil || !strings.Contains(err.Error(), "not the publication's") {
		t.Errorf("a publication on another platform than the send's: %v", err)
	}
	if _, err := (&RegisterNode{}).Execute(runs["a chat's run"](), in, config("{{ \""+id+"\" }}")); err != nil {
		t.Errorf("a chat's run with a sent send: %v", err)
	}
	again := workflow.NodeInput{ExecutionID: "execution-2", NodeID: "register", Items: in.Items}
	if _, err := (&RegisterNode{}).Execute(runs["a chat's run"](), again, config(id)); err == nil || !strings.Contains(err.Error(), "already registered a publication") {
		t.Errorf("the approval again: %v", err)
	}
	if _, err := (&RegisterNode{}).Execute(ctx, workflow.NodeInput{ExecutionID: "mine", NodeID: "register", Items: in.Items}, config("")); err != nil {
		t.Errorf("the user's own run: %v", err)
	}
	if n := count(); n != 2 {
		t.Errorf("%d publications, want 2", n)
	}
}

// Each item names its own send: approval is held back from the engine's
// one-pass resolution and evaluated per item.
func TestRegisterApprovalIsPerItem(t *testing.T) {
	asTheUsersRun(t)
	if !slices.Contains((&RegisterNode{}).PerItemConfigFields(), "approval") {
		t.Fatal("approval is resolved once for every item")
	}
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	a, b := sentSend(t, ctx, db.DB), sentSend(t, ctx, db.DB)
	in := workflow.NodeInput{ExecutionID: "execution", NodeID: "register", Items: []workflow.Item{
		workflow.NewItem(map[string]interface{}{"text": "one", "hil_id": a}),
		workflow.NewItem(map[string]interface{}{"text": "two", "hil_id": b}),
	}}
	config := map[string]interface{}{"platform": "email", "kind": "post", "body": "{{ $json.text }}", "approval": "{{ $json.hil_id }}"}
	run := workflow.WithTrigger(ctx, "trigger.manual", map[string]interface{}{workflow.AICallerKey: "chat"})
	if out, err := (&RegisterNode{}).Execute(run, in, config); err != nil || len(out[0].Items) != 2 {
		t.Fatalf("two items, two sends: %v %v", out, err)
	}
}

// A run that counts as an AI's only because its workflow changed since the
// user approved it says so, and how to approve it (#831 round 4, R1).
func TestRegisterSaysHowToApproveAChangedWorkflow(t *testing.T) {
	asTheUsersRun(t)
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	in := workflow.NodeInput{WorkflowID: "wf-7", ExecutionID: "execution", NodeID: "register", Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"text": "hello"})}}
	config := map[string]interface{}{"platform": "email", "kind": "post", "body": "{{ $json.text }}"}
	_, err = (&RegisterNode{}).Execute(workflow.WithAIAuthored(ctx), in, config)
	if err == nil || !strings.Contains(err.Error(), "this workflow changed since you last saved or activated it") ||
		!strings.Contains(err.Error(), "`monoagentcli --profile default workflow activate wf-7`") {
		t.Errorf("an unapproved definition: %v", err)
	}
	chat := workflow.WithTrigger(ctx, "trigger.manual", map[string]interface{}{workflow.AICallerKey: "chat"})
	if _, err := (&RegisterNode{}).Execute(chat, in, config); err == nil || strings.Contains(err.Error(), "workflow activate") ||
		strings.Contains(err.Error(), "called this one") {
		t.Errorf("a chat's run is no definition the user can approve: %v", err)
	}
	// Called by a workflow the user never approved: that one is to approve
	// (#831 round 6), named when the call recorded it.
	called := workflow.WithTrigger(ctx, workflow.TriggerTypeExecuteWorkflow, map[string]interface{}{
		workflow.AICallerKey: workflow.UnapprovedWorkflowCaller, workflow.UnapprovedCallerKey: "wf-parent"})
	if _, err = (&RegisterNode{}).Execute(called, in, config); err == nil || !strings.Contains(err.Error(), "the workflow that called this one, wf-parent,") ||
		!strings.Contains(err.Error(), "`monoagentcli --profile default workflow activate wf-parent`") {
		t.Errorf("called by an unapproved workflow: %v", err)
	}
	// The chat ran the unapproved caller: approving it would not make this
	// run the user's, so the text stays the chat's.
	byChat := workflow.WithTrigger(ctx, workflow.TriggerTypeExecuteWorkflow, map[string]interface{}{
		workflow.AICallerKey: "chat", workflow.UnapprovedCallerKey: "wf-parent"})
	if _, err = (&RegisterNode{}).Execute(byChat, in, config); err == nil || strings.Contains(err.Error(), "workflow activate") ||
		strings.Contains(err.Error(), "called this one") {
		t.Errorf("called by an unapproved workflow the chat ran: %v", err)
	}
	unnamed := workflow.WithTrigger(ctx, workflow.TriggerTypeExecuteWorkflow, map[string]interface{}{workflow.AICallerKey: workflow.UnapprovedWorkflowCaller})
	if _, err = (&RegisterNode{}).Execute(unnamed, in, config); err == nil || !strings.Contains(err.Error(), "the workflow that called this one changed since") ||
		strings.Contains(err.Error(), "workflow activate") {
		t.Errorf("called by an unapproved workflow it can't name: %v", err)
	}
}

// brokenApprovals is a workflow store whose check of the user's approval
// keeps failing.
type brokenApprovals struct{ *workflow.SQLiteWorkflowStore }

func (brokenApprovals) DefinitionApproved(context.Context, string, string) (bool, error) {
	return false, errors.New("database is locked")
}

// A register refused because whether the user approved the workflow could
// not be checked names the store error, not a change the user didn't make
// (#831 round 5, C4).
func TestRegisterNamesAFailedApprovalCheck(t *testing.T) {
	asTheUsersRun(t)
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	store := workflow.NewSQLiteWorkflowStore(db.DB)
	wf := &workflow.Workflow{ID: "wf-reg", Name: "Registers", ProfileID: "default",
		Nodes: []workflow.WorkflowNode{
			{ID: "wf-reg-t", WorkflowID: "wf-reg", Type: "trigger.schedule", Name: "Every hour"},
			{ID: "wf-reg-r", WorkflowID: "wf-reg", Type: "publication.register", Name: "Register",
				Config: map[string]interface{}{"platform": "email", "kind": "post", "body": "hello"}},
		},
		Connections: []workflow.WorkflowConnection{{ID: "wf-reg-c", WorkflowID: "wf-reg", SourceNodeID: "wf-reg-t", SourceHandle: "main", TargetNodeID: "wf-reg-r", TargetHandle: "main"}},
	}
	if err := store.CreateWorkflow(ctx, wf); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWorkflowNodes(ctx, wf.ID, wf.Nodes); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveWorkflowConnections(ctx, wf.ID, wf.Connections); err != nil {
		t.Fatal(err)
	}
	reg := workflow.NewNodeTypeRegistry()
	reg.Register("publication.register", func() workflow.NodeExecutor { return &RegisterNode{} })
	dag, err := workflow.BuildDAG(wf.Nodes, wf.Connections)
	if err != nil {
		t.Fatal(err)
	}
	exec := &workflow.WorkflowExecution{ID: "exec-reg", WorkflowID: wf.ID, ProfileID: "default", TriggerType: "trigger.schedule", TriggerNodeID: "wf-reg-t"}
	err = workflow.RunExecution(ctx, exec, wf, dag, reg, brokenApprovals{store}, nil, workflow.NewExpressionEngine(), zerolog.Nop())
	if err == nil || !strings.Contains(err.Error(), "could not be checked (database is locked)") || strings.Contains(err.Error(), "changed since") {
		t.Errorf("the register's refusal: %v", err)
	}
}

// A command that runs the node for a caller it judged an agent's although the process carries no marker
// (a process inside a coder turn that cleared its markers) says so in ctx (workflow.WithAgentCaller): the
// run takes the agent path, as a marked agent's does. Without the verdict nothing changes.
func TestRegisterTakesTheAgentPathFromTheCommandsVerdict(t *testing.T) {
	asTheUsersRun(t)
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	in := workflow.NodeInput{ExecutionID: "execution", NodeID: "register", Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"text": "hello"})}}
	config := func(approval string) map[string]interface{} {
		return map[string]interface{}{"platform": "email", "kind": "post", "body": "{{ $json.text }}", "approval": approval}
	}
	count := func() int {
		entries, err := publication.NewStore(db.DB, "default").List(ctx, publication.Filter{})
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	const why = "this command runs inside a coder session's turn, so a model is running it"
	inTurn := workflow.WithAgentCaller(ctx, why)
	for _, approval := range []string{"", "missing"} {
		_, err := (&RegisterNode{}).Execute(inTurn, in, config(approval))
		if err == nil || !strings.Contains(err.Error(), "needs an approved send") || !strings.HasSuffix(err.Error(), "This run is not the user's: "+why) {
			t.Errorf("in a turn, approval %q: %v", approval, err)
		}
	}
	if n := count(); n != 0 {
		t.Fatalf("%d publications registered in a turn without approval", n)
	}
	// a marker wins: the text of the marked path is the one operator.Refusal gives, whatever ctx carries
	t.Setenv("CLAUDECODE", "1")
	_, err = (&RegisterNode{}).Execute(inTurn, in, config(""))
	if err == nil || strings.Contains(err.Error(), why) || !strings.Contains(err.Error(), "This run is not the user's: "+operator.Refusal()) {
		t.Errorf("marked and in a turn: %v", err)
	}
	t.Setenv("CLAUDECODE", "")

	id := sentSend(t, ctx, db.DB)
	if _, err := (&RegisterNode{}).Execute(inTurn, in, config(id)); err != nil {
		t.Errorf("in a turn with a sent send: %v", err)
	}
	if n := count(); n != 1 {
		t.Errorf("%d publications, want the send's one", n)
	}
	again := workflow.NodeInput{ExecutionID: "execution-2", NodeID: "register", Items: in.Items}
	if _, err := (&RegisterNode{}).Execute(inTurn, again, config(id)); err == nil || !strings.Contains(err.Error(), "already registered a publication") {
		t.Errorf("the approval again: %v", err)
	}
	// no verdict, no marker: the user's own run, byte for byte as before
	if _, err := (&RegisterNode{}).Execute(ctx, workflow.NodeInput{ExecutionID: "mine", NodeID: "register", Items: in.Items}, config("")); err != nil {
		t.Errorf("the user's own run: %v", err)
	}
	if n := count(); n != 2 {
		t.Errorf("%d publications, want 2", n)
	}
}

// A registration that fails after the approval was claimed gives the claim back (the failure here is an
// invalid URL, which the claim does not look at): the same send registers a valid publication afterwards.
func TestRegisterGivesTheClaimBackWhenTheRegistrationFails(t *testing.T) {
	asTheUsersRun(t)
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	run := workflow.WithTrigger(ctx, "trigger.manual", map[string]interface{}{workflow.AICallerKey: "chat"})
	in := workflow.NodeInput{ExecutionID: "execution", NodeID: "register", Items: []workflow.Item{workflow.NewItem(nil)}}
	id := sentSend(t, ctx, db.DB)
	config := func(remote, url string) map[string]interface{} {
		return map[string]interface{}{"platform": "email", "kind": "post", "body": "hello", "approval": id, "remote_id": remote, "url": url}
	}
	if _, err := (&RegisterNode{}).Execute(run, in, config("9", "ftp://example.test/x")); err == nil || !strings.Contains(err.Error(), "HTTP(S) URLs") {
		t.Fatalf("an invalid publication: %v", err)
	}
	// another execution: the same one would match the failed attempt's idempotency key and pass as a retry
	again := workflow.NodeInput{ExecutionID: "execution-2", NodeID: "register", Items: in.Items}
	if _, err := (&RegisterNode{}).Execute(run, again, config("1", "")); err != nil {
		t.Fatalf("the approval was not given back after the failed registration: %v", err)
	}
}
