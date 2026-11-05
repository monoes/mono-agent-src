package workflowrun

import (
	"encoding/json"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/browserctl/browserctltest"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/nodes/browserjev"
	"github.com/monoes/mono-agent/internal/workflow"
)

// `workflow templates run`, `workflow evaluate` and `workflow retry` start runs like `workflow run`, so they
// mark them the same way: on the turn-aware verdict (opgate.Refusal), where the environment markers alone
// called a coder-turn process whose markers were cleared the user (issue 1455). The mark is what the run's
// browser level, user-only packages, AIRun and send queue follow from.

const (
	inCoderTurn = "this process runs inside a coder turn"
)

func clearMarkers(t *testing.T) {
	t.Helper()
	for _, k := range append(browserctl.AgentContextEnv(), browserjev.EnvAICaller, browserctl.EnvAgentKind) {
		t.Setenv(k, "")
	}
}

// marksOf is the AI-caller mark (nil: none) of every execution of the trigger type, oldest first.
func marksOf(t *testing.T, cfg *testConfig, where string, args ...interface{}) []interface{} {
	t.Helper()
	db, err := openTestDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.DB.Query(`SELECT trigger_data FROM workflow_executions WHERE `+where+` ORDER BY created_at, id`, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var marks []interface{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &data); err != nil {
			t.Fatal(err)
		}
		marks = append(marks, data[workflow.AICallerKey])
	}
	if len(marks) == 0 {
		t.Fatalf("no execution matches %s", where)
	}
	return marks
}

func verdicts(t *testing.T, run func(label string, want interface{})) {
	t.Helper()
	clearMarkers(t)
	opgate.SetForTest(t, func(bool) string { return "" })
	run("the user's terminal outside a turn", nil)
	opgate.SetForTest(t, func(bool) string { return inCoderTurn })
	run("a coder turn without markers", browserctl.KindOther)
	// A process the gate cannot place is an agent's too.
	opgate.SetForTest(t, func(bool) string { return opgate.UnplacedPrefix + "database is locked)" })
	run("a process the turn check could not place", browserctl.KindOther)
	opgate.SetForTest(t, func(bool) string { return inCoderTurn })
	t.Setenv("CLAUDECODE", "1")
	browserctltest.AsAgentKind(t, browserctl.KindCoder)
	run("a marked coder keeps its own kind", browserctl.KindCoder)
}

func TestTemplatesRunMarksARunOfAProcessInACoderTurn(t *testing.T) {
	cfg := runDB(t, true)
	verdicts(t, func(label string, want interface{}) {
		if out, err := runWorkflowCmdIn(t, cfg, "", "templates", "run", "hello", "--keep"); err != nil {
			t.Fatalf("%s: %v\n%s", label, err, out)
		}
		marks := marksOf(t, cfg, `workflow_id != 'w' AND workflow_id != 'off'`)
		if got := marks[len(marks)-1]; got != want {
			t.Errorf("%s: the run is marked %v, want %v", label, got, want)
		}
	})
}

func TestEvaluateMarksEveryCaseOfAProcessInACoderTurn(t *testing.T) {
	cfg := evalDB(t, true)
	verdicts(t, func(label string, want interface{}) {
		if out, err := runEvalCmd(t, cfg, "evaluate", "ev"); err != nil && exitCode(err) != 1 {
			t.Fatalf("%s: %v\n%s", label, err, out)
		}
		marks := marksOf(t, cfg, `workflow_id = 'ev'`)
		for i, got := range marks[len(marks)-3:] {
			if got != want {
				t.Errorf("%s: case %d is marked %v, want %v", label, i, got, want)
			}
		}
	})
}

func TestRetryMarksARunOfAProcessInACoderTurn(t *testing.T) {
	cfg := runDB(t, true)
	clearMarkers(t)
	opgate.SetForTest(t, func(bool) string { return "" })
	out, err := runWorkflowCmdIn(t, cfg, "", "run", "w", "--input", `{"who":"zed"}`)
	if err != nil {
		t.Fatal(err)
	}
	var first executionSummaryJSON
	if err := json.Unmarshal([]byte(out), &first); err != nil {
		t.Fatal(err)
	}
	last := func(label string, want interface{}) {
		t.Helper()
		if out, err := runWorkflowCmdIn(t, cfg, "", "retry", first.ExecutionID); err != nil {
			t.Fatalf("%s: %v\n%s", label, err, out)
		}
		marks := marksOf(t, cfg, `workflow_id = 'w'`)
		if got := marks[len(marks)-1]; got != want {
			t.Errorf("%s: the retry is marked %v, want %v", label, got, want)
		}
	}
	last("the user's retry of the user's run", nil)
	opgate.SetForTest(t, func(bool) string { return inCoderTurn })
	last("a coder turn without markers retries the user's run", browserctl.KindOther)
	opgate.SetForTest(t, func(bool) string { return opgate.UnplacedPrefix + "database is locked)" })
	last("a process the turn check could not place", browserctl.KindOther)
	t.Setenv("CLAUDECODE", "1")
	browserctltest.AsAgentKind(t, browserctl.KindCoder)
	last("a marked coder keeps its own kind", browserctl.KindCoder)
}

// The user retrying an agent's run does not turn it into the user's: the new run keeps the old mark.
func TestRetryByTheUserKeepsAnAgentsMark(t *testing.T) {
	cfg := runDB(t, true)
	clearMarkers(t)
	opgate.SetForTest(t, func(bool) string { return inCoderTurn })
	out, err := runWorkflowCmdIn(t, cfg, "", "run", "w", "--input", `{"who":"zed"}`)
	if err != nil {
		t.Fatal(err)
	}
	var first executionSummaryJSON
	if err := json.Unmarshal([]byte(out), &first); err != nil {
		t.Fatal(err)
	}
	opgate.SetForTest(t, func(bool) string { return "" })
	if out, err := runWorkflowCmdIn(t, cfg, "", "retry", first.ExecutionID); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	marks := marksOf(t, cfg, `workflow_id = 'w'`)
	if got := marks[len(marks)-1]; got != browserctl.KindOther {
		t.Errorf("the user's retry of an agent's run is marked %v, want it to keep %v", got, browserctl.KindOther)
	}
}
