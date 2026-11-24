package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/monoes/mono-agent/internal/workflow"
)

// ─────────────────────────────────────────────────────────────────────────────
// Workflow types
// ─────────────────────────────────────────────────────────────────────────────

type WorkflowSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IsActive    bool   `json:"is_active"`
	Version     int    `json:"version"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	// NodeCount is how many nodes the workflow has. The list query leaves
	// Nodes unpopulated on purpose, so the UI had nothing to count and
	// showed "0 nodes" for every workflow.
	NodeCount int `json:"node_count"`
	// Set only in the dashboard's All profiles view.
	ProfileID   string `json:"profile_id,omitempty"`
	ProfileName string `json:"profile_name,omitempty"`
}

type WorkflowNodeData struct {
	ID        string                 `json:"id"`
	NodeType  string                 `json:"node_type"`
	Name      string                 `json:"name"`
	Config    map[string]interface{} `json:"config"`
	PositionX float64                `json:"position_x"`
	PositionY float64                `json:"position_y"`
	Disabled  bool                   `json:"disabled"`
	Schema    *workflow.NodeSchema   `json:"schema,omitempty"`
}

type WorkflowConnectionData struct {
	ID           string `json:"id"`
	SourceNodeID string `json:"source_node_id"`
	SourceHandle string `json:"source_handle"`
	TargetNodeID string `json:"target_node_id"`
	TargetHandle string `json:"target_handle"`
	Position     int    `json:"position"`
}

type WorkflowDetail struct {
	WorkflowSummary
	Nodes       []WorkflowNodeData       `json:"nodes"`
	Connections []WorkflowConnectionData `json:"connections"`
}

type SaveWorkflowRequest struct {
	ID          string                   `json:"id"`
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	IsActive    bool                     `json:"is_active"`
	Nodes       []WorkflowNodeData       `json:"nodes"`
	Connections []WorkflowConnectionData `json:"connections"`
}

type WorkflowExecutionSummary struct {
	ID           string `json:"id"`
	WorkflowID   string `json:"workflow_id"`
	WorkflowName string `json:"workflow_name"`
	Status       string `json:"status"`
	TriggerType  string `json:"trigger_type"`
	StartedAt    string `json:"started_at"`
	FinishedAt   string `json:"finished_at"`
	Error        string `json:"error"`
	CreatedAt    string `json:"created_at"`
	// Set only in the dashboard's All profiles view.
	ProfileID   string `json:"profile_id,omitempty"`
	ProfileName string `json:"profile_name,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Workflow CRUD
// ─────────────────────────────────────────────────────────────────────────────

// workflowToDetail converts `workflow get`'s document into the editor's shape.
func workflowToDetail(wf *workflow.Workflow) *WorkflowDetail {
	detail := &WorkflowDetail{
		WorkflowSummary: workflowSummaryOf(wf),
		Nodes:           []WorkflowNodeData{},
		Connections:     []WorkflowConnectionData{},
	}
	for _, n := range wf.Nodes {
		detail.Nodes = append(detail.Nodes, WorkflowNodeData{
			ID:        n.ID,
			NodeType:  n.Type,
			Name:      n.Name,
			Config:    n.Config,
			PositionX: n.PositionX,
			PositionY: n.PositionY,
			Disabled:  n.Disabled,
			Schema:    n.Schema,
		})
	}
	for _, c := range wf.Connections {
		detail.Connections = append(detail.Connections, WorkflowConnectionData{
			ID:           c.ID,
			SourceNodeID: c.SourceNodeID,
			SourceHandle: c.SourceHandle,
			TargetNodeID: c.TargetNodeID,
			TargetHandle: c.TargetHandle,
			Position:     c.Position,
		})
	}
	return detail
}

// ListWorkflowTemplates returns metadata for all bundled, ready-to-use
// workflow templates (e.g. "Outlook Email Sync") shipped with the app.
func (a *App) ListWorkflowTemplates() []workflow.Template {
	if a.signedOut() != nil {
		return nil
	}
	return workflow.ListTemplates()
}

// CreateWorkflowFromTemplate instantiates a bundled template as a new,
// editable workflow for the active profile. Node IDs from the template are
// remapped to fresh UUIDs so multiple instantiations never collide, then
// saved via the same path as a normal SaveWorkflow call.
func (a *App) CreateWorkflowFromTemplate(templateID string) (*WorkflowSummary, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	tmpl, ok := workflow.GetTemplate(templateID)
	if !ok {
		return nil, fmt.Errorf("unknown template %q", templateID)
	}

	idMap := make(map[string]string, len(tmpl.Nodes))
	for _, n := range tmpl.Nodes {
		idMap[n.ID] = uuid.New().String()
	}

	req := SaveWorkflowRequest{Name: tmpl.Name, Description: workflow.SavedTemplateDescription(templateID), IsActive: false}
	for _, n := range tmpl.Nodes {
		config := n.Config
		if config == nil {
			config = map[string]interface{}{}
		}
		// people.sync_outlook_message scopes synced people/messages by its
		// own "profile_id" config field, independent of the workflow's
		// profile — default it to the active profile so the template works
		// correctly out of the box.
		if n.Type == "people.sync_outlook_message" {
			pid, err := a.profileID()
			if err != nil {
				return nil, err
			}
			config["profile_id"] = pid
		}
		req.Nodes = append(req.Nodes, WorkflowNodeData{
			ID:        idMap[n.ID],
			NodeType:  n.Type,
			Name:      n.Name,
			Config:    config,
			PositionX: n.Position.X,
			PositionY: n.Position.Y,
			Disabled:  n.Disabled,
		})
	}
	for _, c := range tmpl.Connections {
		req.Connections = append(req.Connections, WorkflowConnectionData{
			ID:           uuid.New().String(),
			SourceNodeID: idMap[c.Source],
			SourceHandle: c.SourceHandle,
			TargetNodeID: idMap[c.Target],
			TargetHandle: c.TargetHandle,
		})
	}

	return a.SaveWorkflow(req)
}

// ListWorkflows returns `monoagentcli workflow list`: the active profile's
// workflows from the file store and SQLite, with node counts, newest first.
func (a *App) ListWorkflows() ([]WorkflowSummary, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	var rows []struct {
		workflow.Workflow
		NodeCount int `json:"node_count"`
	}
	if err := a.runMonoCLI("", &rows, "workflow", "list"); err != nil {
		return nil, err
	}
	summaries := make([]WorkflowSummary, 0, len(rows))
	for _, r := range rows {
		s := workflowSummaryOf(&r.Workflow)
		s.NodeCount = r.NodeCount
		summaries = append(summaries, s)
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt > summaries[j].UpdatedAt
	})
	return summaries, nil
}

// GetWorkflow returns `workflow get <id>`; another profile's workflow is not
// found.
func (a *App) GetWorkflow(id string) (*WorkflowDetail, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	var wf workflow.Workflow
	if err := a.runMonoCLI("", &wf, "workflow", "get", id); err != nil {
		return nil, err
	}
	return workflowToDetail(&wf), nil
}

// SaveWorkflow hands the editor's document to `workflow save` on stdin. Its
// JSON is the shape `workflow get` prints, so the request goes as it is. An
// existing workflow keeps its activation (that is SetWorkflowActive's job):
// the editor never sends is_active.
func (a *App) SaveWorkflow(req SaveWorkflowRequest) (*WorkflowSummary, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	doc, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var wf workflow.Workflow
	if err := a.runMonoCLI(string(doc), &wf, "workflow", "save"); err != nil {
		return nil, err
	}
	a.emitLog("WORKFLOW", "INFO", fmt.Sprintf("Saved workflow: %s [%s]", wf.Name, wf.ID))
	s := workflowSummaryOf(&wf)
	return &s, nil
}

// DeleteWorkflow runs `workflow delete <id> --yes`: no prompt, but a
// workflow that orgs use is refused with the CLI's explanation.
func (a *App) DeleteWorkflow(id string) error {
	if err := a.signedOut(); err != nil {
		return err
	}
	if err := a.runMonoCLI("", nil, "workflow", "delete", id, "--yes"); err != nil {
		return err
	}
	a.emitLog("WORKFLOW", "WARN", "Deleted workflow: "+id)
	return nil
}

// SetWorkflowActive runs `workflow activate` or `workflow deactivate`.
func (a *App) SetWorkflowActive(id string, active bool) error {
	if err := a.signedOut(); err != nil {
		return err
	}
	if active {
		return a.runMonoCLI("", nil, "workflow", "activate", id)
	}
	// Deactivating can succeed in the database only (an unfinished change left the file as it is):
	// the CLI says so in a note, which goes to the log instead of being dropped.
	var res struct {
		Note string `json:"note"`
	}
	if err := a.runMonoCLI("", &res, "workflow", "deactivate", id); err != nil {
		return err
	}
	if res.Note != "" {
		a.emitLog("WORKFLOW", "WARN", "Workflow "+id+": "+res.Note)
	}
	return nil
}

// workflowSummaryOf is the list/save row for wf, timestamps in RFC 3339.
func workflowSummaryOf(wf *workflow.Workflow) WorkflowSummary {
	return WorkflowSummary{
		ID:          wf.ID,
		Name:        wf.Name,
		Description: wf.Description,
		IsActive:    wf.IsActive,
		Version:     wf.Version,
		CreatedAt:   wf.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   wf.UpdatedAt.Format(time.RFC3339),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Workflow import/export
// ─────────────────────────────────────────────────────────────────────────────

// ─────────────────────────────────────────────────────────────────────────────
// Workflow execution (subprocess)
// ─────────────────────────────────────────────────────────────────────────────

// GetWorkflowTriggerInputs returns `monoagentcli workflow inputs <id> --json`
// verbatim: which trigger fields the workflow reads, and a skeleton payload
// filled from the reading nodes' schema examples.
//
// The detection lives in the CLI, not here — the editor only renders what it
// is told, so the same answer is available to a script, a test, and the
// dialog (see internal/workflow/trigger_inputs.go).
func (a *App) GetWorkflowTriggerInputs(id string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if strings.TrimSpace(id) == "" {
		return aiError(fmt.Errorf("workflow id required"))
	}
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return aiError(err)
	}
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	inputsArgs, err := a.scopedArgs("workflow", "inputs", id)
	if err != nil {
		return aiError(err)
	}
	cmd := exec.CommandContext(ctx, cliBin, inputsArgs...)
	suppressConsole(cmd)
	out, runErr := cmd.Output()
	return cliResultJSON(cliBin, out, runErr)
}

// ─────────────────────────────────────────────────────────────────────────────
