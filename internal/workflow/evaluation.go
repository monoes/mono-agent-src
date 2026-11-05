package workflow

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/account"
)

// Evaluations (n8n's "When fetching a dataset row" trigger and Evaluation
// node): `monoagentcli workflow evaluate` runs a workflow once per row of
// the dataset its trigger.evaluation node names, each run an execution of
// trigger type TriggerTypeEvaluation fired at that node, with the row as
// the trigger's one item. Only such a run fires trigger.evaluation, and
// only in such a run do core.evaluation nodes write outputs back to the
// dataset and record inputs, outputs and metrics (EvaluationFrom).
const (
	// TriggerTypeEvaluation is the trigger node type, and the trigger type
	// of the executions an evaluation run starts.
	TriggerTypeEvaluation = "trigger.evaluation"
	// EvaluationKey is the trigger-data field holding the run's bookkeeping
	// (EvaluationCase); the row itself is the trigger's item.
	EvaluationKey = "evaluation"
)

// EvaluationTriggerSchema documents the trigger.evaluation config the
// evaluation runner (internal/evaluation) reads to load the dataset.
type EvaluationTriggerSchema struct {
	Source string `json:"source" schema:"label=Source,type=select,options=dataTable|googleSheets,default=dataTable,help=Where the dataset rows come from: a data table of this profile or a Google Sheet."`

	Table string `json:"table" schema:"label=Data Table,type=text,placeholder=Eval cases,help=The data table's name or id (monoagentcli table list).,depends_on_key=source,depends_on_values=dataTable"`

	CredentialID string `json:"credential_id" schema:"label=Google Sheets Account,type=text,help=The google_sheets connection that reads the sheet.,depends_on_key=source,depends_on_values=googleSheets"`

	SpreadsheetID string `json:"spreadsheet_id" schema:"label=Spreadsheet ID,type=text,help=The spreadsheet holding the dataset (its first row names the columns).,depends_on_key=source,depends_on_values=googleSheets"`

	SheetName string `json:"sheet_name" schema:"label=Sheet,type=text,default=Sheet1,depends_on_key=source,depends_on_values=googleSheets"`

	LimitRows bool `json:"limit_rows" schema:"label=Limit Rows,type=boolean,help=Run only the first Max Rows rows."`

	MaxRows float64 `json:"max_rows" schema:"label=Max Rows,type=number,default=10,min=1,depends_on_key=limit_rows,depends_on_values=true"`

	Match string `json:"match" schema:"label=Must Match,type=select,options=any|all,default=any,help=Whether a row must match any or all of the filters."`

	Filters string `json:"filters" schema:"label=Row Filters,type=code,language=json,rows=4,placeholder=[{\"column\": \"split\"， \"condition\": \"eq\"， \"value\": \"test\"}],help=Only rows matching these conditions are run. A data table takes the core.data_table conditions; a sheet compares with eq only."`
}

// EvaluationCase is the bookkeeping an evaluation execution carries under
// EvaluationKey: the run and case it belongs to, the dataset row it runs
// and, for a data table dataset, the table's id.
type EvaluationCase struct {
	RunID     string `json:"run_id"`
	CaseID    string `json:"case_id"`
	RowID     string `json:"row_id,omitempty"`
	RowNumber int    `json:"row_number"`
	Table     string `json:"table,omitempty"`
}

// EvaluationFrom returns the case of the running execution when it is a
// run of an evaluation (its trigger type, which only StartEvaluationCase
// sets, is TriggerTypeEvaluation); ok is false for any other run.
func EvaluationFrom(ctx context.Context) (EvaluationCase, bool) {
	if TriggerTypeFrom(ctx) != TriggerTypeEvaluation {
		return EvaluationCase{}, false
	}
	m, _ := TriggerDataFrom(ctx)[EvaluationKey].(map[string]interface{})
	if m == nil {
		return EvaluationCase{}, false
	}
	c := EvaluationCase{}
	c.RunID, _ = m["run_id"].(string)
	c.CaseID, _ = m["case_id"].(string)
	c.RowID, _ = m["row_id"].(string)
	c.Table, _ = m["table"].(string)
	switch n := m["row_number"].(type) {
	case float64:
		c.RowNumber = int(n)
	case int:
		c.RowNumber = n
	}
	return c, c.RunID != "" && c.CaseID != ""
}

// StartEvaluationCase persists and enqueues one execution of an evaluation
// run: workflowID fired at its trigger.evaluation node triggerNodeID, with
// row as the trigger's item. The workflow need not be active, as in n8n.
// It returns the execution id.
func (e *WorkflowEngine) StartEvaluationCase(ctx context.Context, workflowID, triggerNodeID string, row map[string]interface{}, c EvaluationCase) (string, error) {
	if err := account.Require(ctx); err != nil {
		return "", err
	}
	wf, err := e.store.GetWorkflow(ctx, workflowID)
	if err != nil {
		return "", fmt.Errorf("engine: %w", err)
	}
	if wf == nil {
		return "", fmt.Errorf("engine: %w", ErrWorkflowNotFound)
	}
	if err := e.checkWorkflowProfile(wf); err != nil {
		return "", fmt.Errorf("engine: %w", err)
	}
	// The row is data a dataset holds, not the engine's: it carries none of
	// the engine's fields into the case's item.
	item := make(map[string]interface{}, len(row))
	for k, v := range row {
		item[k] = v
	}
	StripEngineKeys(item)
	data := map[string]interface{}{
		SubWorkflowItemsKey: []interface{}{item},
		EvaluationKey: map[string]interface{}{
			"run_id": c.RunID, "case_id": c.CaseID, "row_id": c.RowID, "row_number": c.RowNumber, "table": c.Table,
		},
	}
	data = withRunMark(ctx, data)
	exec := &WorkflowExecution{
		WorkflowID:    workflowID,
		ProfileID:     executionProfileID(wf),
		Status:        "QUEUED",
		TriggerType:   TriggerTypeEvaluation,
		TriggerNodeID: triggerNodeID,
		TriggerData:   data,
		PID:           ownerPID(),
	}
	if err := e.store.CreateExecution(ctx, exec); err != nil {
		return "", fmt.Errorf("engine: create execution: %w", err)
	}
	if err := e.queue.Enqueue(ExecutionRequest{
		WorkflowID:    workflowID,
		ExecutionID:   exec.ID,
		TriggerType:   TriggerTypeEvaluation,
		TriggerNodeID: triggerNodeID,
		TriggerData:   data,
	}); err != nil {
		e.persistExecutionFinished(e.logger, exec.ID, "FAILED", "queue full")
		return exec.ID, fmt.Errorf("engine: %w", ErrQueueFull)
	}
	return exec.ID, nil
}
