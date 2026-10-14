package main

import (
	"encoding/json"
	"fmt"
)

// ─────────────────────────────────────────────────────────────────────────────
// Human in Loop (HIL)
// ─────────────────────────────────────────────────────────────────────────────

// HILItem is the data structure returned to the frontend for each pending HIL item.
type HILItem struct {
	ID           string                 `json:"id"`
	ExecutionID  string                 `json:"execution_id"`
	WorkflowID   string                 `json:"workflow_id"`
	WorkflowName string                 `json:"workflow_name"`
	NodeID       string                 `json:"node_id"`
	NodeName     string                 `json:"node_name"`
	Status       string                 `json:"status"`
	ReadonlyData map[string]interface{} `json:"readonly_data"`
	EditableData map[string]interface{} `json:"editable_data"`
	NodeConfig   map[string]interface{} `json:"node_config"`
	CreatedAt    string                 `json:"created_at"`
}

// GetHILItems returns the active profile's pending Human-in-Loop items,
// including the workflow name — `hil list --suggest`. A TypeSafe Jev
// suggestion (when the profile enabled surface hil) is in
// node_config.suggestion; the CLI stores it, so polling never re-pays.
func (a *App) GetHILItems() ([]HILItem, error) {
	items := []HILItem{}
	if err := a.runMonoCLI("", &items, "hil", "list", "--suggest"); err != nil {
		return nil, fmt.Errorf("GetHILItems: %w", err)
	}
	if items == nil {
		items = []HILItem{}
	}
	return items, nil
}

// ApproveHIL approves a pending HIL item with optional edited data (JSON
// string) — `hil approve <id> --data …`.
func (a *App) ApproveHIL(id string, editedDataJSON string) error {
	if err := a.resolveHIL(id, editedDataJSON, true); err != nil {
		return err
	}
	a.emitLog("HIL", "INFO", fmt.Sprintf("HIL item %s approved", id))
	return nil
}

// RejectHIL rejects a pending HIL item, causing the workflow to error out —
// `hil reject <id>`.
func (a *App) RejectHIL(id string) error {
	if err := a.resolveHIL(id, "", false); err != nil {
		return err
	}
	a.emitLog("HIL", "INFO", fmt.Sprintf("HIL item %s rejected", id))
	return nil
}

// resolveHIL runs `hil approve|reject` for the active profile.
func (a *App) resolveHIL(id, editedDataJSON string, approve bool) error {
	if !approve {
		if err := a.runMonoCLI("", nil, "hil", "reject", id); err != nil {
			return fmt.Errorf("RejectHIL: %w", err)
		}
		return nil
	}
	if editedDataJSON == "" {
		editedDataJSON = "{}"
	}
	var check map[string]interface{}
	if err := json.Unmarshal([]byte(editedDataJSON), &check); err != nil {
		return fmt.Errorf("ApproveHIL: editedDataJSON is not valid JSON: %w", err)
	}
	if err := a.runMonoCLI("", nil, "hil", "approve", id, "--data", editedDataJSON); err != nil {
		return fmt.Errorf("ApproveHIL: %w", err)
	}
	return nil
}
