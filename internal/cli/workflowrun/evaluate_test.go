package workflowrun

import (
	"context"
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/datatable"
	"github.com/monoes/mono-agent/internal/evaluation"
	"github.com/monoes/mono-agent/internal/workflow"
)

// evalDB is a migrated database with the data table "QA" (three rows) and
// workflow "ev": trigger.evaluation over QA → checkIfEvaluating → a set
// that "answers" with the row's guess → setInputs → setOutputs → string
// similarity, categorization and a custom metric. "plain" has only an
// evaluation trigger and a set step; "nometrics" lacks a setMetrics node.
func evalDB(t *testing.T, jsonOut bool) *testConfig {
	t.Helper()
	testhome.SetHome(t, t.TempDir())
	cfg := &testConfig{DBPath: filepath.Join(t.TempDir(), "e.db"), JSONOutput: jsonOut, ProfileID: "default"}
	db, err := openTestDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	dt := datatable.New(db.DB, "default")
	tbl, err := dt.CreateTable(ctx, "QA", []datatable.Column{{Name: "question", Type: datatable.TypeString},
		{Name: "expected", Type: datatable.TypeString}, {Name: "guess", Type: datatable.TypeString}, {Name: "points", Type: datatable.TypeNumber}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dt.Insert(ctx, &tbl, []map[string]interface{}{
		{"question": "2+2", "expected": "4", "guess": "4", "points": 1.0},
		{"question": "capital of France", "expected": "Paris", "guess": "paris", "points": 2.0},
		{"question": "color of the sky", "expected": "blue", "guess": "blue sky", "points": 3.0},
	}); err != nil {
		t.Fatal(err)
	}

	store := workflow.NewSQLiteWorkflowStore(db.DB)
	save := func(id string, nodes []workflow.WorkflowNode, edges [][3]string) {
		wf := &workflow.Workflow{ID: id, Name: id, ProfileID: "default"}
		for _, n := range nodes {
			n.ID, n.WorkflowID = id+"-"+n.Name, id
			if err := n.MarshalConfig(); err != nil {
				t.Fatal(err)
			}
			wf.Nodes = append(wf.Nodes, n)
		}
		for i, e := range edges {
			wf.Connections = append(wf.Connections, workflow.WorkflowConnection{ID: id + "-c" + string(rune('a'+i)), WorkflowID: id,
				SourceNodeID: id + "-" + e[0], SourceHandle: e[1], TargetNodeID: id + "-" + e[2], TargetHandle: "main"})
		}
		if err := store.CreateWorkflow(ctx, wf); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveWorkflowNodes(ctx, id, wf.Nodes); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveWorkflowConnections(ctx, id, wf.Connections); err != nil {
			t.Fatal(err)
		}
	}
	trigger := workflow.WorkflowNode{Type: workflow.TriggerTypeEvaluation, Name: "Rows", Config: map[string]interface{}{"table": "QA"}}
	answer := workflow.WorkflowNode{Type: "core.set", Name: "Answer", Config: map[string]interface{}{"answer": "{{ $json.guess }}"}}
	metric := func(name, metric string, extra map[string]interface{}) workflow.WorkflowNode {
		c := map[string]interface{}{"operation": "setMetrics", "metric": metric}
		for k, v := range extra {
			c[k] = v
		}
		return workflow.WorkflowNode{Type: evaluation.NodeType, Name: name, Config: c}
	}
	save("ev", []workflow.WorkflowNode{
		trigger,
		{Type: evaluation.NodeType, Name: "Check", Config: map[string]interface{}{"operation": "checkIfEvaluating"}},
		answer,
		{Type: evaluation.NodeType, Name: "Inputs", Config: map[string]interface{}{"operation": "setInputs",
			"inputs": []interface{}{map[string]interface{}{"name": "question", "value": "{{ $json.question }}"}}}},
		{Type: evaluation.NodeType, Name: "Outputs", Config: map[string]interface{}{"operation": "setOutputs",
			"outputs": []interface{}{map[string]interface{}{"name": "answer", "value": "{{ $json.answer }}"}}}},
		metric("Similarity", "stringSimilarity", map[string]interface{}{"expected_answer": "{{ $json.expected }}", "actual_answer": "{{ $json.answer }}"}),
		// A metrics node outputs only its metric: the next one reads the
		// answer from the node that has it, as in n8n.
		metric("Exact", "categorization", map[string]interface{}{"expected_answer": `{{ $node["Answer"].json.expected }}`,
			"actual_answer": `{{ $node["Answer"].json.answer }}`}),
		metric("Points", "customMetrics", map[string]interface{}{"metrics": []interface{}{
			map[string]interface{}{"name": "points", "value": `{{ $node["Answer"].json.points }}`}}}),
	}, [][3]string{{"Rows", "main", "Check"}, {"Check", "evaluation", "Answer"}, {"Answer", "main", "Inputs"},
		{"Inputs", "main", "Outputs"}, {"Outputs", "main", "Similarity"}, {"Similarity", "main", "Exact"}, {"Exact", "main", "Points"}})
	save("plain", []workflow.WorkflowNode{trigger, answer}, [][3]string{{"Rows", "main", "Answer"}})
	save("nometrics", []workflow.WorkflowNode{trigger, answer}, [][3]string{{"Rows", "main", "Answer"}})
	return cfg
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// `workflow evaluate --json` runs every row of the 3-row data table as its
// own execution, records each case's inputs, outputs and metrics, writes
// the outputs into the rows, and keeps each metric's mean.
func TestEvaluateRunsEveryDatasetRow(t *testing.T) {
	cfg := evalDB(t, true)
	out, err := runEvalCmd(t, cfg, "evaluate", "ev")
	if err != nil {
		t.Fatalf("evaluate: %v\n%s", err, out)
	}
	var run evaluation.Run
	if err := json.Unmarshal([]byte(out), &run); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if run.Status != evaluation.RunCompleted || run.CaseCount != 3 || run.Failed != 0 || len(run.Cases) != 3 {
		t.Fatalf("run = %+v", run)
	}
	wantMean := map[string]float64{"String similarity": (1 + 0.8 + 0.5) / 3, "Categorization": 1.0 / 3, "points": 2}
	for name, want := range wantMean {
		if !near(run.Metrics[name], want) {
			t.Errorf("mean %s = %v, want %v", name, run.Metrics[name], want)
		}
	}
	if _, ok := run.Metrics["executionTime"]; !ok {
		t.Errorf("no executionTime in %v", run.Metrics)
	}
	execs := map[string]bool{}
	for i, c := range run.Cases {
		if c.Status != evaluation.CaseSuccess || c.ExecutionID == "" || c.RowNumber != i {
			t.Errorf("case %d = %+v", i, c)
		}
		execs[c.ExecutionID] = true
	}
	if len(execs) != 3 {
		t.Errorf("cases share executions: %v", execs)
	}
	second := run.Cases[1]
	if second.Inputs["question"] != "capital of France" || second.Outputs["answer"] != "paris" ||
		!near(second.Metrics["String similarity"].(float64), 0.8) || second.Metrics["Categorization"] != 0.0 {
		t.Errorf("case 2 = %+v", second)
	}

	// Each case ran as an evaluation execution.
	detail, err := runWorkflowCmdIn(t, cfg, "", "execution", second.ExecutionID)
	if err != nil || !strings.Contains(detail, `"trigger_type": "trigger.evaluation"`) {
		t.Errorf("execution: %v\n%s", err, detail)
	}

	// The outputs were written into the dataset rows.
	db, err := openTestDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dt := datatable.New(db.DB, "default")
	tbl, _ := dt.FindTable(context.Background(), "QA")
	rows, _ := dt.Select(context.Background(), tbl, datatable.Query{})
	for i, want := range []string{"4", "paris", "blue sky"} {
		if rows[i]["answer"] != want {
			t.Errorf("row %d answer = %v, want %q", i, rows[i]["answer"], want)
		}
	}

	// `workflow evaluations` lists the run; --run shows its cases.
	list, err := runEvalCmd(t, cfg, "evaluations", "ev")
	if err != nil {
		t.Fatal(err)
	}
	var runs []evaluation.Run
	if err := json.Unmarshal([]byte(list), &runs); err != nil || len(runs) != 1 || runs[0].ID != run.ID || runs[0].CaseCount != 3 {
		t.Fatalf("evaluations: %v\n%s", err, list)
	}
	one, err := runEvalCmd(t, cfg, "evaluations", "ev", "--run", run.ID)
	if err != nil || !strings.Contains(one, `"cases"`) || !strings.Contains(one, second.ExecutionID) {
		t.Errorf("evaluations --run: %v\n%s", err, one)
	}
	if _, err := runEvalCmd(t, cfg, "evaluations", "plain", "--run", run.ID); clicore.ExitCodeFor(err) != 2 {
		t.Errorf("a run of another workflow: %v", err)
	}
}

// --dataset reads another table; a filter and a row limit pick the rows.
func TestEvaluateDatasetOverrideAndLimit(t *testing.T) {
	cfg := evalDB(t, true)
	db, err := openTestDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dt := datatable.New(db.DB, "default")
	other, _ := dt.CreateTable(context.Background(), "Other", nil)
	if _, err := dt.Insert(context.Background(), &other, []map[string]interface{}{
		{"question": "q", "expected": "x", "guess": "x", "points": 5.0}}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	out, err := runEvalCmd(t, cfg, "evaluate", "ev", "--dataset", "Other")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var run evaluation.Run
	_ = json.Unmarshal([]byte(out), &run)
	if run.CaseCount != 1 || run.Metrics["points"] != 5.0 || run.Dataset["table"] != "Other" {
		t.Errorf("run = %+v", run)
	}

	ds, err := evaluation.ParseDataset(map[string]interface{}{"table": "QA", "limit_rows": true, "max_rows": 1.0,
		"filters": `[{"column": "points", "condition": "gte", "value": 2}]`})
	if err != nil {
		t.Fatal(err)
	}
	db, _ = openTestDB(cfg)
	defer db.Close()
	rows, err := ds.LoadRows(context.Background(), db.DB, "default", nil)
	if err != nil || len(rows) != 1 || rows[0].JSON["question"] != "capital of France" || rows[0].JSON["row_number"] != 0 {
		t.Errorf("rows %v %v", rows, err)
	}
}

// A run that cannot start is stored with n8n's error code and exits 1.
func TestEvaluateRefusesWithoutMetrics(t *testing.T) {
	cfg := evalDB(t, true)
	out, err := runEvalCmd(t, cfg, "evaluate", "nometrics")
	if clicore.ExitCodeFor(err) != 1 {
		t.Fatalf("exit %d: %v\n%s", clicore.ExitCodeFor(err), err, out)
	}
	var run evaluation.Run
	if err := json.Unmarshal([]byte(out), &run); err != nil || run.Status != evaluation.RunError || run.ErrorCode != evaluation.ErrMetricsNodeNotFound {
		t.Fatalf("run %+v (%v)\n%s", run, err, out)
	}
	list, _ := runEvalCmd(t, cfg, "evaluations", "nometrics")
	if !strings.Contains(list, evaluation.ErrMetricsNodeNotFound) {
		t.Errorf("stored run: %s", list)
	}
	if _, err := runEvalCmd(t, cfg, "evaluate", "missing"); clicore.ExitCodeFor(err) != 2 {
		t.Errorf("unknown workflow: %v", err)
	}
}

// A manual run of a workflow whose only trigger is trigger.evaluation does
// not fire it: the dataset branch stays idle.
func TestManualRunLeavesEvaluationTriggerIdle(t *testing.T) {
	cfg := evalDB(t, true)
	db, err := openTestDB(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := workflow.NewSQLiteWorkflowStore(db.DB).SetWorkflowActive(context.Background(), "plain", true); err != nil {
		t.Fatal(err)
	}
	db.Close()
	out, err := runWorkflowCmdIn(t, cfg, "", "run", "plain", "--input", `{"guess": "x"}`)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	var rec executionSummaryJSON
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, n := range rec.Nodes {
		if n.Status == "SUCCESS" && n.Type == "core.set" {
			t.Errorf("the set step ran in a manual run: %+v", n)
		}
	}
}

// The human output ends with the run's means.
func TestEvaluateHumanOutput(t *testing.T) {
	cfg := evalDB(t, false)
	out, err := runEvalCmd(t, cfg, "evaluate", "ev")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"case 1 (row 1): success", "case 3 (row 3): success", "Cases: 3 (0 failed)", "Categorization=0.333"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func runEvalCmd(t *testing.T, cfg *testConfig, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		d := testDeps(cfg)
		root := &cobra.Command{Use: "workflow", SilenceUsage: true, SilenceErrors: true}
		root.AddCommand(d.NewEvaluateCmd(), d.NewEvaluationsCmd())
		root.SetArgs(args)
		err = root.Execute()
	})
	return out, err
}
