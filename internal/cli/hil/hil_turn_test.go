package hil

import (
	"database/sql"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/workflow"
)

// `hil list` hides a reply link's token from a process that is not the user, and a process inside a
// coder session's turn is not the user although it carries no marker (issue 1425).
func TestHILListHidesReplyTokensFromAProcessInACoderTurnWithoutMarkers(t *testing.T) {
	t.Setenv("MONOAGENT_PUBLIC_URL", "http://127.0.0.1:19729")
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
	cfg := &testConfig{DBPath: newHILTestDB(t), JSONOutput: true, ProfileID: "default"}
	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	resume, err := workflow.ExecutionResumeURL("ex-t")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, readonly_data, editable_data, node_config, profile_id)
		VALUES ('h1', 'ex-t', 'wf-t', 'n', 'Review', 'pending', ?, '{}', '{"outbound":false}', 'default')`, `{"link":"`+resume+`"}`); err != nil {
		t.Fatal(err)
	}
	mine, err := runHIL(t, cfg, "list")
	if err != nil || len(mine) != 1 || mine[0].ReadonlyData["link"] != resume {
		t.Fatalf("the user's terminal: %+v %v", mine, err)
	}
	opgate.SetForTest(t, func(bool) string { return "this process runs inside a coder turn" })
	theirs, err := runHIL(t, cfg, "list")
	hidden := "http://127.0.0.1:19729/reply/" + workflow.ReplyTokenHidden
	if err != nil || len(theirs) != 1 || theirs[0].ReadonlyData["link"] != hidden {
		t.Errorf("a coder turn without markers: %+v %v", theirs, err)
	}
	// A process the gate cannot place is hidden from like an agent: no error, and the link stays hidden.
	opgate.SetForTest(t, func(bool) string { return opgate.UnplacedPrefix + "database is locked)" })
	unplaced, err := runHIL(t, cfg, "list")
	if err != nil || len(unplaced) != 1 || unplaced[0].ReadonlyData["link"] != hidden {
		t.Errorf("an unplaced process: %+v %v", unplaced, err)
	}
}
