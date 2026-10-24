package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// ErrorWorkflowSetting is the Workflow.Settings key naming the workflow (by
// id) that runs when this one finishes FAILED. The named workflow starts at
// its trigger.error node.
const ErrorWorkflowSetting = "error_workflow"

// DefaultErrorWorkflowConfig is the configs-table key (`monoagentcli config
// set workflow.error_workflow <id>`) of the error workflow used by every
// workflow whose settings name none.
const DefaultErrorWorkflowConfig = "workflow.error_workflow"

// execer is what saveWorkflowSettings writes through: a *sql.DB, or the
// *sql.Tx of a save that must land as a whole (saveDefinition).
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// saveWorkflowSettings writes w.Settings to the workflows.settings column.
// Best-effort: a database without the column (an old test schema) keeps
// the workflow row as it was; the file store holds settings on its own.
func saveWorkflowSettings(ctx context.Context, db execer, w *Workflow) {
	if d, ok := db.(*sql.DB); db == nil || (ok && d == nil) || w == nil || w.ID == "" {
		return
	}
	raw := []byte("{}")
	if len(w.Settings) > 0 {
		b, err := json.Marshal(w.Settings)
		if err != nil {
			return
		}
		raw = b
	}
	_, _ = db.ExecContext(ctx, `UPDATE workflows SET settings = ? WHERE id = ?`, string(raw), w.ID)
}

// loadWorkflowSettings fills w.Settings from the workflows.settings column,
// leaving it nil when the column is missing, empty or "{}".
func loadWorkflowSettings(ctx context.Context, db *sql.DB, w *Workflow) {
	if db == nil || w == nil {
		return
	}
	loadSettingsFrom(ctx, db, w)
}

func loadSettingsFrom(ctx context.Context, db rowQuerier, w *Workflow) {
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(settings, '') FROM workflows WHERE id = ?`, w.ID).Scan(&raw); err != nil {
		return
	}
	var m map[string]interface{}
	if json.Unmarshal([]byte(raw), &m) == nil && len(m) > 0 {
		w.Settings = m
	}
}

// loadWorkflowsSettings fills Settings for each listed workflow with one
// query; best-effort like loadWorkflowSettings.
func loadWorkflowsSettings(ctx context.Context, db *sql.DB, ws []Workflow) {
	if db == nil || len(ws) == 0 {
		return
	}
	rows, err := db.QueryContext(ctx, `SELECT id, COALESCE(settings, '') FROM workflows WHERE COALESCE(settings, '') NOT IN ('', '{}')`)
	if err != nil {
		return
	}
	defer rows.Close()
	byID := map[string]map[string]interface{}{}
	for rows.Next() {
		var id, raw string
		var m map[string]interface{}
		if rows.Scan(&id, &raw) == nil && json.Unmarshal([]byte(raw), &m) == nil && len(m) > 0 {
			byID[id] = m
		}
	}
	for i := range ws {
		if m, ok := byID[ws[i].ID]; ok {
			ws[i].Settings = m
		}
	}
}

// errorWorkflowID returns the id of wf's error workflow: its own
// error_workflow setting, else the configured default, else "".
func errorWorkflowID(ctx context.Context, db *sql.DB, wf *Workflow) string {
	if id, _ := wf.Settings[ErrorWorkflowSetting].(string); strings.TrimSpace(id) != "" {
		return strings.TrimSpace(id)
	}
	if db == nil {
		return ""
	}
	var id string
	_ = db.QueryRowContext(ctx, `SELECT config_data FROM configs WHERE name = ?`, DefaultErrorWorkflowConfig).Scan(&id)
	return strings.TrimSpace(id)
}
