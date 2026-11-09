package evalnode

import (
	"context"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/datatable"
	"github.com/monoes/mono-agent/internal/evaluation"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/testhome"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Tests never touch the real ~/.monoagent.
func TestMain(m *testing.M) { testhome.Main(m) }

func items(js ...map[string]interface{}) []workflow.Item {
	out := make([]workflow.Item, len(js))
	for i, j := range js {
		out[i] = workflow.Item{JSON: j}
	}
	return out
}

func exec(t *testing.T, ctx context.Context, in workflow.NodeInput, cfg map[string]interface{}) map[string][]workflow.Item {
	t.Helper()
	outs, err := (&Node{}).Execute(ctx, in, cfg)
	if err != nil {
		t.Fatalf("execute %v: %v", cfg["operation"], err)
	}
	got := map[string][]workflow.Item{}
	for _, o := range outs {
		got[o.Handle] = o.Items
	}
	return got
}

func metricOf(t *testing.T, cfg map[string]interface{}, js ...map[string]interface{}) []map[string]interface{} {
	t.Helper()
	cfg["operation"] = "setMetrics"
	var out []map[string]interface{}
	for _, it := range exec(t, context.Background(), workflow.NodeInput{Items: items(js...)}, cfg)["main"] {
		out = append(out, it.JSON)
	}
	return out
}

// The deterministic metrics give n8n's exact values: string similarity is
// 1 - edit distance / longer length in UTF-16 units, categorization an
// exact match of the trimmed answers, tools used the share of expected
// tools (by tool name, any case) the steps called.
func TestDeterministicMetrics(t *testing.T) {
	cases := []struct {
		cfg  map[string]interface{}
		want map[string]interface{}
	}{
		{map[string]interface{}{"metric": "stringSimilarity", "expected_answer": "kitten", "actual_answer": "sitting"},
			map[string]interface{}{"String similarity": 1 - 3.0/7}},
		{map[string]interface{}{"metric": "stringSimilarity", "expected_answer": " same ", "actual_answer": "same"},
			map[string]interface{}{"String similarity": 1.0}},
		// "😀" is two UTF-16 units, as JavaScript counts it.
		{map[string]interface{}{"metric": "stringSimilarity", "expected_answer": "a😀", "actual_answer": "a"},
			map[string]interface{}{"String similarity": 1 - 2.0/3}},
		{map[string]interface{}{"metric": "categorization", "expected_answer": "billing", "actual_answer": " billing\n", "metric_name": "cat"},
			map[string]interface{}{"cat": 1.0}},
		{map[string]interface{}{"metric": "categorization", "expected_answer": "billing", "actual_answer": "Billing"},
			map[string]interface{}{"Categorization": 0.0}},
		{map[string]interface{}{"metric": "categorization", "expected_answer": 3.0, "actual_answer": "3"},
			map[string]interface{}{"Categorization": 1.0}},
		{map[string]interface{}{"metric": "toolsUsed", "expected_tools": "Get Events, send_email, Search DB",
			"intermediate_steps": []interface{}{
				map[string]interface{}{"action": map[string]interface{}{"tool": "get_events"}},
				map[string]interface{}{"action": map[string]interface{}{"tool": "SEND_EMAIL"}},
				map[string]interface{}{"observation": "malformed"},
			}},
			map[string]interface{}{"Tools Used": 2.0 / 3}},
		{map[string]interface{}{"metric": "toolsUsed", "expected_tools": "Get Events",
			"intermediate_steps": `[{"action":{"tool":"Get_Events"}}]`},
			map[string]interface{}{"Tools Used": 1.0}},
		{map[string]interface{}{"metric": "customMetrics", "metrics": []interface{}{
			map[string]interface{}{"name": "length", "value": "12"},
			map[string]interface{}{"name": "passed", "value": true},
			map[string]interface{}{"name": "score", "value": 0.25}}},
			map[string]interface{}{"length": 12.0, "passed": 1.0, "score": 0.25}},
	}
	for _, c := range cases {
		got := metricOf(t, c.cfg, map[string]interface{}{})
		if len(got) != 1 || len(got[0]) != len(c.want) {
			t.Errorf("%v: got %v, want %v", c.cfg, got, c.want)
			continue
		}
		for k, v := range c.want {
			if g, ok := got[0][k].(float64); !ok || math.Abs(g-v.(float64)) > 1e-12 {
				t.Errorf("%v: %s = %v, want %v", c.cfg["metric"], k, got[0][k], v)
			}
		}
	}
}

// Each item computes its own metric from its own fields.
func TestMetricsResolvePerItem(t *testing.T) {
	got := metricOf(t, map[string]interface{}{"metric": "categorization",
		"expected_answer": "{{ $json.want }}", "actual_answer": "{{ $json.got }}"},
		map[string]interface{}{"want": "a", "got": "a"}, map[string]interface{}{"want": "a", "got": "b"})
	if len(got) != 2 || got[0]["Categorization"] != 1.0 || got[1]["Categorization"] != 0.0 {
		t.Errorf("per item: %v", got)
	}
}

func TestMetricErrors(t *testing.T) {
	for _, c := range []struct {
		cfg  map[string]interface{}
		want string
	}{
		{map[string]interface{}{"metric": "stringSimilarity", "actual_answer": "x"}, "expected answer is missing"},
		{map[string]interface{}{"metric": "categorization", "expected_answer": "x"}, "actual answer is missing"},
		{map[string]interface{}{"metric": "toolsUsed", "intermediate_steps": "[]"}, "expected tool name missing"},
		{map[string]interface{}{"metric": "toolsUsed", "expected_tools": "a"}, "intermediate steps missing"},
		{map[string]interface{}{"metric": "customMetrics", "metrics": `[{"name": "x", "value": "abc"}]`}, `"x" isn't a number`},
		{map[string]interface{}{"metric": "customMetrics", "metrics": `[{"name": "", "value": 1}]`}, "metric name missing"},
		{map[string]interface{}{"metric": "nope"}, "unknown metric"},
		{map[string]interface{}{"metric": "correctness", "expected_answer": "a", "actual_answer": "b"}, `set "runtime"`},
	} {
		c.cfg["operation"] = "setMetrics"
		_, err := (&Node{}).Execute(context.Background(), workflow.NodeInput{Items: items(map[string]interface{}{})}, c.cfg)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err %v, want %q", c.cfg, err, c.want)
		}
	}
}

// fakeTurns replaces monomind with reply and records each turn.
func fakeTurns(t *testing.T, reply func(system, prompt string) string) *[]monomind.ExecOptions {
	t.Helper()
	testhome.SetHome(t, t.TempDir())
	var mu sync.Mutex
	var seen []monomind.ExecOptions
	oldEnsure, oldExec := ensureMonomind, execTurn
	ensureMonomind = func(context.Context) (string, *monomind.VersionInfo, error) { return "/bin/monomind", nil, nil }
	execTurn = func(_ context.Context, o monomind.ExecOptions, _ func(monomind.Event)) (*monomind.TurnResult, error) {
		mu.Lock()
		seen = append(seen, o)
		mu.Unlock()
		return &monomind.TurnResult{ResultText: reply(o.SystemPrompt, o.Prompt)}, nil
	}
	t.Cleanup(func() { ensureMonomind, execTurn = oldEnsure, oldExec })
	return &seen
}

// Correctness and helpfulness are scored by one model turn per item: the
// prompt carries the case, the reply's integer score is the metric. The
// attached chat model picks the runtime and model.
func TestModelJudgedMetrics(t *testing.T) {
	seen := fakeTurns(t, func(system, prompt string) string {
		if strings.Contains(prompt, "Lyon") {
			return `{"extended_reasoning": "x", "reasoning_summary": "y", "score": 2}`
		}
		return "```json\n{\"extended_reasoning\": \"same city\", \"reasoning_summary\": \"match\", \"score\": 5}\n```"
	})
	in := workflow.NodeInput{
		Items:    items(map[string]interface{}{"out": "Paris"}, map[string]interface{}{"out": "Lyon"}),
		SubNodes: []workflow.SubNode{{ID: "m", Kind: "ai_languageModel", Config: map[string]interface{}{"runtime": "codex", "model": "gpt-5"}}},
	}
	got := exec(t, context.Background(), in, map[string]interface{}{"operation": "setMetrics", "metric": "correctness",
		"runtime": "claude", "expected_answer": "The capital is Paris", "actual_answer": "{{ $json.out }}"})["main"]
	if len(got) != 2 || got[0].JSON["Correctness"] != 5.0 || got[1].JSON["Correctness"] != 2.0 {
		t.Fatalf("scores: %v", got)
	}
	if len(*seen) != 2 {
		t.Fatalf("%d turns", len(*seen))
	}
	first := (*seen)[0]
	if first.Runtime != "codex" || first.Model != "gpt-5" {
		t.Errorf("runtime/model from the attached model: %s/%s", first.Runtime, first.Model)
	}
	if !strings.Contains(first.Prompt, "The capital is Paris") || !strings.Contains(first.Prompt, "\nParis\n") ||
		!strings.Contains(first.SystemPrompt, `"score"`) {
		t.Errorf("turn: system %q prompt %q", first.SystemPrompt, first.Prompt)
	}

	*seen = nil
	got = exec(t, context.Background(), workflow.NodeInput{Items: items(map[string]interface{}{})}, map[string]interface{}{
		"operation": "setMetrics", "metric": "helpfulness", "runtime": "claude", "user_query": "Capital of France?",
		"actual_answer": "Paris", "metric_name": "helpful", "input_prompt": "Q={user_query} A={actual_answer}"})["main"]
	if got[0].JSON["helpful"] != 5.0 || (*seen)[0].Prompt != "Q=Capital of France? A=Paris" || (*seen)[0].Runtime != "claude" {
		t.Errorf("helpfulness: %v, turn %+v", got, (*seen)[0])
	}
}

// A reply without a valid score is asked again once, then fails.
func TestModelJudgeRetriesOnce(t *testing.T) {
	n := 0
	fakeTurns(t, func(string, string) string {
		n++
		if n == 1 {
			return `{"score": 7}`
		}
		return `{"score": 4}`
	})
	got := metricOf(t, map[string]interface{}{"metric": "correctness", "runtime": "claude", "expected_answer": "a", "actual_answer": "b"},
		map[string]interface{}{})
	if got[0]["Correctness"] != 4.0 || n != 2 {
		t.Errorf("retry: %v after %d turns", got, n)
	}
	n = 0
	fakeTurns(t, func(string, string) string { n++; return "no json" })
	_, err := (&Node{}).Execute(context.Background(), workflow.NodeInput{Items: items(map[string]interface{}{})}, map[string]interface{}{
		"operation": "setMetrics", "metric": "correctness", "runtime": "claude", "expected_answer": "a", "actual_answer": "b"})
	if err == nil || !strings.Contains(err.Error(), "failed to evaluate correctness") || n != 2 {
		t.Errorf("err %v after %d turns", err, n)
	}
}

// evalCtx is the context of a run of an evaluation case: the profile
// database with a run, its case and a data table row.
func evalCtx(t *testing.T) (context.Context, *evaluation.Store, string, *evaluation.Case, *datatable.Table) {
	t.Helper()
	db := testdb.Open(t).DB
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "p1")
	dt := datatable.New(db, "p1")
	tbl, err := dt.CreateTable(ctx, "Cases", []datatable.Column{{Name: "question", Type: datatable.TypeString}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := dt.Insert(ctx, &tbl, []map[string]interface{}{{"question": "q1"}, {"question": "q2"}})
	if err != nil {
		t.Fatal(err)
	}
	store := evaluation.NewStore(db, "p1")
	run, err := store.CreateRun(ctx, "wf1", nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &evaluation.Case{RowID: "2", RowNumber: 1}
	if err := store.AddCase(ctx, run.ID, c); err != nil {
		t.Fatal(err)
	}
	if rows[1]["id"] != int64(2) {
		t.Fatalf("row ids: %v", rows)
	}
	ctx = workflow.WithTrigger(ctx, workflow.TriggerTypeEvaluation, map[string]interface{}{
		workflow.EvaluationKey: map[string]interface{}{"run_id": run.ID, "case_id": c.ID, "row_id": "2", "row_number": 1.0, "table": tbl.ID}})
	return ctx, store, run.ID, c, &tbl
}

// checkIfEvaluating sends the items to "evaluation" in an evaluation run
// and to "normal" otherwise.
func TestCheckIfEvaluating(t *testing.T) {
	in := workflow.NodeInput{Items: items(map[string]interface{}{"a": 1.0})}
	cfg := map[string]interface{}{"operation": "checkIfEvaluating"}
	if got := exec(t, context.Background(), in, cfg); len(got[HandleNormal]) != 1 || len(got[HandleEvaluation]) != 0 {
		t.Errorf("normal run: %v", got)
	}
	ctx, _, _, _, _ := evalCtx(t)
	if got := exec(t, ctx, in, cfg); len(got[HandleEvaluation]) != 1 || len(got[HandleNormal]) != 0 {
		t.Errorf("evaluation run: %v", got)
	}
	// Only the engine sets the trigger type: data that merely looks like
	// an evaluation's does not make a manual run one.
	manual := workflow.WithTrigger(context.Background(), "trigger.manual", map[string]interface{}{
		workflow.EvaluationKey: map[string]interface{}{"run_id": "r", "case_id": "c"}})
	if got := exec(t, manual, in, cfg); len(got[HandleNormal]) != 1 {
		t.Errorf("manual run with evaluation-like data: %v", got)
	}
}

// In an evaluation run setInputs records the inputs, setOutputs writes the
// outputs into the dataset row (adding the column) and records them, and
// setMetrics records the first item's metrics; the first value set for a
// name wins. Outside one they pass the items on and write nothing.
func TestRecordsCaseInEvaluationRun(t *testing.T) {
	ctx, store, runID, c, tbl := evalCtx(t)
	in := workflow.NodeInput{Items: items(map[string]interface{}{"x": 1.0})}
	got := exec(t, ctx, in, map[string]interface{}{"operation": "setInputs",
		"inputs": []interface{}{map[string]interface{}{"name": "question", "value": "q2"}}})
	if len(got["main"]) != 1 || got["main"][0].JSON["x"] != 1.0 {
		t.Errorf("setInputs passes items on: %v", got)
	}
	exec(t, ctx, in, map[string]interface{}{"operation": "setOutputs",
		"outputs": `[{"name": "answer", "value": "a2"}, {"name": "tokens", "value": 42}]`})
	exec(t, ctx, in, map[string]interface{}{"operation": "setOutputs", "table": "Cases",
		"outputs": []interface{}{map[string]interface{}{"name": "answer", "value": "ignored"}}})
	exec(t, ctx, workflow.NodeInput{Items: items(map[string]interface{}{"g": "b"}, map[string]interface{}{"g": "c"})},
		map[string]interface{}{"operation": "setMetrics", "metric": "categorization", "expected_answer": "b", "actual_answer": "{{ $json.g }}"})

	if err := store.LoadCase(ctx, c); err != nil {
		t.Fatal(err)
	}
	if c.Inputs["question"] != "q2" || c.Outputs["answer"] != "a2" || c.Outputs["tokens"] != 42.0 || c.Metrics["Categorization"] != 1.0 || len(c.Metrics) != 1 {
		t.Errorf("recorded: inputs %v outputs %v metrics %v", c.Inputs, c.Outputs, c.Metrics)
	}
	dt := datatable.New(vault.DBFromContext(ctx), "p1")
	t2, _ := dt.FindTable(ctx, tbl.ID)
	rows, _ := dt.Select(ctx, t2, datatable.Query{})
	if rows[0]["answer"] != nil || rows[1]["answer"] != "ignored" || rows[1]["tokens"] != 42.0 {
		t.Errorf("dataset rows: %v", rows)
	}
	if typ, _ := t2.ColumnType("tokens"); typ != datatable.TypeNumber {
		t.Errorf("tokens column type %q", typ)
	}
	if r, _ := store.GetRun(ctx, runID); len(r.Cases) != 1 {
		t.Errorf("run: %+v", r)
	}

	// A normal run: nothing is written, the items pass on.
	plain := vault.ContextWithDB(context.Background(), vault.DBFromContext(ctx))
	got = exec(t, plain, in, map[string]interface{}{"operation": "setOutputs", "table": "Cases",
		"outputs": []interface{}{map[string]interface{}{"name": "answer", "value": "nope"}}})
	if len(got["main"]) != 1 {
		t.Errorf("normal setOutputs: %v", got)
	}
	rows, _ = dt.Select(ctx, t2, datatable.Query{})
	if rows[1]["answer"] != "ignored" {
		t.Errorf("a normal run wrote the dataset: %v", rows)
	}
}

func TestSetOutputsNeedsEntries(t *testing.T) {
	ctx, _, _, _, _ := evalCtx(t)
	_, err := (&Node{}).Execute(ctx, workflow.NodeInput{Items: items(map[string]interface{}{})}, map[string]interface{}{"operation": "setOutputs"})
	if err == nil || !strings.Contains(err.Error(), "no outputs to set") {
		t.Errorf("err %v", err)
	}
}

// With a Google Sheet dataset, setOutputs adds missing columns to the
// header row, then writes each output into the case's sheet row.
func TestSetOutputsToSheet(t *testing.T) {
	ctx, _, _, _, _ := evalCtx(t)
	ctx = workflow.WithTrigger(ctx, workflow.TriggerTypeEvaluation, map[string]interface{}{
		workflow.EvaluationKey: map[string]interface{}{"run_id": "r", "case_id": "c", "row_id": "5", "row_number": 5.0}})
	var calls []map[string]interface{}
	old := sheetsCall
	sheetsCall = func(_ context.Context, cfg map[string]interface{}) ([]workflow.Item, error) {
		calls = append(calls, cfg)
		if cfg["operation"] == "read_rows" {
			return items(map[string]interface{}{"A": "question", "B": "answer", "_row_index": 1}), nil
		}
		return items(map[string]interface{}{}), nil
	}
	t.Cleanup(func() { sheetsCall = old })
	err := writeSheetRow(ctx, map[string]interface{}{"source": "googleSheets", "spreadsheet_id": "S", "sheet_name": "Cases",
		"access_token": "tok", "operation": "setOutputs"}, 5, []pair{{"answer", "Paris"}, {"score", 3.0}})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 {
		t.Fatalf("calls: %v", calls)
	}
	if calls[0]["range"] != "1:1" || calls[0]["access_token"] != "tok" || calls[0]["sheet_name"] != "Cases" {
		t.Errorf("header read: %v", calls[0])
	}
	if calls[1]["range"] != "A1" || len(calls[1]["values"].([]interface{})[0].([]interface{})) != 3 {
		t.Errorf("header write: %v", calls[1])
	}
	if calls[2]["range"] != "B5" || calls[3]["range"] != "C5" || calls[2]["operation"] != "update_rows" {
		t.Errorf("cells: %v %v", calls[2], calls[3])
	}
	if _, kept := calls[2]["outputs"]; kept {
		t.Errorf("evaluation config leaked into the sheets call: %v", calls[2])
	}
}

func TestColumnLetter(t *testing.T) {
	for i, want := range map[int]string{0: "A", 25: "Z", 26: "AA", 27: "AB", 701: "ZZ", 702: "AAA"} {
		if got := columnLetter(i); got != want {
			t.Errorf("columnLetter(%d) = %s, want %s", i, got, want)
		}
	}
}
