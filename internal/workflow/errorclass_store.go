package workflow

import (
	"context"
	"database/sql"
)

// recordNodeFailure marks an execution node FAILED with err's message, as
// SetExecutionNodeFinished always did, and also stores the failure's class
// and fix hint on the node and, when the execution has none yet, its class
// on the execution. items (the ones a node routed to an unwired "error"
// output, or nil) are kept as the row's output so the failure can be
// inspected. The class write is best-effort: a database without the
// columns (an old test schema) still records the failure itself.
func recordNodeFailure(ctx context.Context, store WorkflowStore, en *WorkflowExecutionNode, err error, items []Item) error {
	ferr := store.SetExecutionNodeFinished(ctx, en.ID, "FAILED", items, err.Error())
	class := ClassifyError(err)
	en.ErrorClass, en.ErrorHint = string(class), ErrorHint(class)
	if db := store.RawDB(); db != nil {
		_, _ = db.ExecContext(ctx,
			`UPDATE workflow_execution_nodes SET error_class = ?, error_hint = ? WHERE id = ?`,
			en.ErrorClass, en.ErrorHint, en.ID)
		setExecutionErrorClass(ctx, db, en.ExecutionID, class, false)
	}
	return ferr
}

// setExecutionErrorClass records class on the execution. Without force it
// only fills an empty class, so the execution keeps its first failure's.
func setExecutionErrorClass(ctx context.Context, db *sql.DB, executionID string, class ErrorClass, force bool) {
	if db == nil || class == "" || executionID == "" {
		return
	}
	q := `UPDATE workflow_executions SET error_class = ? WHERE id = ? AND COALESCE(error_class, '') = ''`
	if force {
		q = `UPDATE workflow_executions SET error_class = ? WHERE id = ?`
	}
	_, _ = db.ExecContext(ctx, q, string(class), executionID)
}

// loadExecutionErrorClass returns the stored class of an execution, "" when
// it has none or the column does not exist.
func loadExecutionErrorClass(ctx context.Context, db *sql.DB, executionID string) string {
	if db == nil {
		return ""
	}
	var c string
	_ = db.QueryRowContext(ctx,
		`SELECT COALESCE(error_class, '') FROM workflow_executions WHERE id = ?`, executionID).Scan(&c)
	return c
}
