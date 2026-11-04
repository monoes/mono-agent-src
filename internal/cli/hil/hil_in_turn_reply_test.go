package hil

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A process inside a coder turn that cleared its markers is handled like a marked one: `hil list` hides the
// reply tokens in the items' data from it (the gate's turn check, operator.SetTurnVerdict, is what the command
// line registers). A marked caller's output is a fixed literal, the same bytes as before the turn check
// counted; the user's own terminal sees the link.
func TestHILListHidesReplyTokensFromAProcessInsideACoderTurn(t *testing.T) {
	t.Setenv("MONOAGENT_PUBLIC_URL", "http://127.0.0.1:19724")
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
	cfg := &testConfig{DBPath: newHILTestDB(t), JSONOutput: true, ProfileID: "default"}
	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	resume, err := workflow.ExecutionResumeURL("ex-r")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, readonly_data, editable_data, node_config, profile_id)
		VALUES ('h1', 'ex-r', 'wf-r', 'n', 'Review', 'pending', ?, ?, '{"outbound":false}', 'default')`,
		`{"link":"`+resume+`"}`, `{"body":"Answer at `+resume+`"}`); err != nil {
		t.Fatal(err)
	}
	// what a marked caller has always been given, as a literal
	const golden = `[{"link":"http://127.0.0.1:19724/reply/[reply token hidden]"},{"body":"Answer at http://127.0.0.1:19724/reply/[reply token hidden]"}]`
	view := func() string {
		t.Helper()
		items, err := runHIL(t, cfg, "list")
		if err != nil || len(items) != 1 {
			t.Fatalf("%+v %v", items, err)
		}
		b, _ := json.Marshal([]map[string]interface{}{items[0].ReadonlyData, items[0].EditableData})
		return string(b)
	}

	if got := view(); !containsToken(got, resume) {
		t.Fatalf("the user's terminal lost the link: %s", got)
	}
	t.Setenv("MONOAGENT_AI_CALLER", "chat")
	if got := view(); got != golden {
		t.Fatalf("a marked caller's output changed:\n got: %s\nwant: %s", got, golden)
	}
	t.Setenv("MONOAGENT_AI_CALLER", "")
	for name, why := range map[string]string{
		"in a turn":        "this command runs inside a coder session's turn, so a model is running it",
		"cannot be placed": operator.UnplacedPrefix + " (database unreadable)",
	} {
		operator.SetTurnVerdict(func() string { return why })
		if got := view(); got != golden {
			t.Errorf("%s, markers cleared:\n got: %s\nwant: %s", name, got, golden)
		}
	}
	operator.SetTurnVerdict(func() string { return "" })
	if got := view(); !containsToken(got, resume) {
		t.Errorf("a turn check that places the process as the user's: %s", got)
	}
	operator.SetTurnVerdict(nil)
}

func containsToken(view, resume string) bool {
	return strings.Contains(view, resume[len(resume)-43:])
}
