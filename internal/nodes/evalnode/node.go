// Package evalnode implements core.evaluation, n8n's Evaluation node: in a
// run of an evaluation (`monoagentcli workflow evaluate`, see
// internal/evaluation) it records the case's inputs, writes its outputs
// back to the dataset row and records its metrics; checkIfEvaluating routes
// items by whether the run is one.
//
// String similarity, categorization, tools used and custom metrics are
// computed locally. Correctness and helpfulness are scored 1–5 by a local
// agent runtime through monomind (one turn per item, in the turn sandbox),
// the runtime and model set on the node or by an attached chat model.
package evalnode

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/datatable"
	"github.com/monoes/mono-agent/internal/evaluation"
	"github.com/monoes/mono-agent/internal/nodes/aisub"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

const nodeType = evaluation.NodeType

// Output handles of checkIfEvaluating (n8n's outputs 0 and 1).
const (
	HandleEvaluation = "evaluation"
	HandleNormal     = "normal"
)

// Node is the core.evaluation executor (config: see EvaluationNodeSchema).
type Node struct{}

// Type implements workflow.NodeExecutor.
func (n *Node) Type() string { return nodeType }

// metricFields are the setMetrics fields resolved per item, as n8n reads
// each item's own.
var metricFields = []string{"expected_answer", "actual_answer", "user_query", "expected_tools",
	"intermediate_steps", "metrics", "metric_name", "prompt", "input_prompt"}

// PerItemConfigFields keeps the metric fields from being resolved against
// the first item only.
func (n *Node) PerItemConfigFields() []string { return metricFields }

// RegisterAll registers core.evaluation.
func RegisterAll(r *workflow.NodeTypeRegistry) {
	r.Register(nodeType, func() workflow.NodeExecutor { return &Node{} })
}

func cfgStr(cfg map[string]interface{}, key, def string) string {
	if s, ok := cfg[key].(string); ok && strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	return def
}

// Execute implements workflow.NodeExecutor.
func (n *Node) Execute(ctx context.Context, input workflow.NodeInput, cfg map[string]interface{}) ([]workflow.NodeOutput, error) {
	ev, evaluating := workflow.EvaluationFrom(ctx)
	items := input.Items
	if items == nil {
		items = []workflow.Item{}
	}
	pass := []workflow.NodeOutput{{Handle: "main", Items: items}}
	switch op := cfgStr(cfg, "operation", "setOutputs"); op {
	case "checkIfEvaluating":
		if evaluating {
			return []workflow.NodeOutput{{Handle: HandleEvaluation, Items: items}, {Handle: HandleNormal, Items: []workflow.Item{}}}, nil
		}
		return []workflow.NodeOutput{{Handle: HandleEvaluation, Items: []workflow.Item{}}, {Handle: HandleNormal, Items: items}}, nil
	case "setInputs", "setOutputs":
		// Outside an evaluation they pass the items on and set nothing.
		if !evaluating {
			return pass, nil
		}
		field := map[string]string{"setInputs": "inputs", "setOutputs": "outputs"}[op]
		list, err := pairs(cfg[field], field)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", nodeType, err)
		}
		if len(list) == 0 {
			return nil, fmt.Errorf("%w: %s: no %s to set: add at least one {name, value}", workflow.ErrInvalidConfig, nodeType, field)
		}
		data := make(map[string]interface{}, len(list))
		for _, p := range list {
			if p.name == "" {
				return nil, fmt.Errorf("%w: %s: every entry of %s needs a name", workflow.ErrInvalidConfig, nodeType, field)
			}
			data[p.name] = p.value
		}
		db := vault.DBFromContext(ctx)
		if db == nil {
			return nil, fmt.Errorf("%s: recording an evaluation needs the profile database", nodeType)
		}
		kind := evaluation.KindInputs
		if op == "setOutputs" {
			kind = evaluation.KindOutputs
			if err := writeBack(ctx, cfg, ev, list); err != nil {
				return nil, fmt.Errorf("%s: write outputs back to the dataset: %w", nodeType, err)
			}
		}
		if err := evaluation.Record(ctx, db, ev.RunID, ev.CaseID, kind, data); err != nil {
			return nil, err
		}
		return pass, nil
	case "setMetrics":
		out, err := n.setMetrics(ctx, input, cfg)
		if err != nil {
			return nil, err
		}
		if evaluating && len(out) > 0 {
			db := vault.DBFromContext(ctx)
			if db == nil {
				return nil, fmt.Errorf("%s: recording an evaluation needs the profile database", nodeType)
			}
			if err := evaluation.Record(ctx, db, ev.RunID, ev.CaseID, evaluation.KindMetrics, out[0].JSON); err != nil {
				return nil, err
			}
		}
		return []workflow.NodeOutput{{Handle: "main", Items: out}}, nil
	default:
		return nil, fmt.Errorf("%w: %s: operation %q (use setInputs, setOutputs, setMetrics or checkIfEvaluating)", workflow.ErrInvalidConfig, nodeType, op)
	}
}

// setMetrics computes the metric for each item; each output item holds
// only the metric, by its name.
func (n *Node) setMetrics(ctx context.Context, input workflow.NodeInput, cfg map[string]interface{}) ([]workflow.Item, error) {
	metric := cfgStr(cfg, "metric", evaluation.DefaultMetric)
	if metric != evaluation.MetricCustom && defaultMetricNames[metric] == "" {
		return nil, fmt.Errorf("%w: %s: unknown metric %q (use correctness, helpfulness, stringSimilarity, categorization, toolsUsed or customMetrics)",
			workflow.ErrInvalidConfig, nodeType, metric)
	}
	var j *judge
	if evaluation.NeedsModel(metric) {
		runtime, model := cfgStr(cfg, "runtime", ""), cfgStr(cfg, "model", "")
		if m, ok := aisub.ModelOf(input); ok {
			runtime, model = m.Runtime, m.Model
		}
		timeout := time.Duration(defaultJudgeTimeout) * time.Second
		if f, ok := number(cfg["timeout"]); ok && f > 0 {
			timeout = time.Duration(f * float64(time.Second))
		}
		var err error
		if j, err = newJudge(ctx, runtime, model, timeout, input, vault.DBFromContext(ctx), metric); err != nil {
			return nil, err
		}
	}
	held := map[string]interface{}{}
	for _, k := range metricFields {
		if v, ok := cfg[k]; ok {
			held[k] = v
		}
	}
	engine := workflow.NewExpressionEngine()
	out := make([]workflow.Item, 0, len(input.Items))
	for i, item := range input.Items {
		ic, err := engine.ResolveConfig(held, input.ItemContext(item.JSON))
		if err != nil {
			return nil, fmt.Errorf("%s: item %d: %w", nodeType, i, err)
		}
		m, err := metricValue(ctx, j, metric, ic)
		if err != nil {
			return nil, fmt.Errorf("%s: item %d: %w", nodeType, i, err)
		}
		out = append(out, workflow.Item{JSON: m})
	}
	return out, nil
}

// metricValue computes one item's metric from its resolved fields.
func metricValue(ctx context.Context, j *judge, metric string, ic map[string]interface{}) (map[string]interface{}, error) {
	if metric == evaluation.MetricCustom {
		return customMetrics(ic["metrics"])
	}
	name := cfgStr(ic, "metric_name", defaultMetricNames[metric])
	var v float64
	switch metric {
	case evaluation.MetricCategorization, evaluation.MetricStringSimilarity:
		expected, actual, err := answers(ic)
		if err != nil {
			return nil, err
		}
		if metric == evaluation.MetricCategorization {
			v = categorization(expected, actual)
		} else {
			v = stringSimilarity(expected, actual)
		}
	case evaluation.MetricToolsUsed:
		var err error
		if v, err = toolsUsed(ic["expected_tools"], ic["intermediate_steps"]); err != nil {
			return nil, err
		}
	case evaluation.MetricCorrectness:
		expected, actual, err := answers(ic)
		if err != nil {
			return nil, err
		}
		prompt := fill(cfgStr(ic, "input_prompt", correctnessInput), map[string]string{"expected_answer": expected, "actual_answer": actual})
		if v, err = j.score(ctx, cfgStr(ic, "prompt", correctnessPrompt), prompt, metric); err != nil {
			return nil, err
		}
	case evaluation.MetricHelpfulness:
		query, actual := text(ic["user_query"]), text(ic["actual_answer"])
		if query == "" {
			return nil, fmt.Errorf("%w: user query is missing: fill in user_query", workflow.ErrInvalidConfig)
		}
		if actual == "" {
			return nil, fmt.Errorf("%w: response is missing: fill in actual_answer", workflow.ErrInvalidConfig)
		}
		prompt := fill(cfgStr(ic, "input_prompt", helpfulnessInput), map[string]string{"user_query": query, "actual_answer": actual})
		var err error
		if v, err = j.score(ctx, cfgStr(ic, "prompt", helpfulnessPrompt), prompt, metric); err != nil {
			return nil, err
		}
	}
	return map[string]interface{}{name: v}, nil
}

// writeBack writes the outputs into the case's dataset row: the data table
// row by id (a column the table lacks is added), or the sheet row by
// number (a column the sheet lacks is added to its header row).
func writeBack(ctx context.Context, cfg map[string]interface{}, ev workflow.EvaluationCase, list []pair) error {
	if cfgStr(cfg, "source", evaluation.SourceDataTable) == evaluation.SourceGoogleSheets {
		return writeSheetRow(ctx, cfg, ev.RowNumber, list)
	}
	db := vault.DBFromContext(ctx)
	if db == nil {
		return fmt.Errorf("data tables need the profile database")
	}
	ref := cfgStr(cfg, "table", ev.Table)
	if ref == "" {
		return fmt.Errorf("%w: no data table: set \"table\"", workflow.ErrInvalidConfig)
	}
	id, err := strconv.ParseFloat(ev.RowID, 64)
	if err != nil {
		return fmt.Errorf("the dataset row id %q is not a data table row id", ev.RowID)
	}
	profile, perr := vault.ProfileIDFromContext(ctx)
	if perr != nil {
		return fmt.Errorf("eval node ran without a profile: %w", perr)
	}
	store := datatable.New(db, profile)
	t, err := store.FindTable(ctx, ref)
	if err != nil {
		return err
	}
	data := make(map[string]interface{}, len(list))
	for _, p := range list {
		data[p.name] = p.value
	}
	_, err = store.Update(ctx, &t, datatable.Filter{Conditions: []datatable.Condition{{Column: "id", Op: datatable.OpEq, Value: id}}}, data, false)
	return err
}
