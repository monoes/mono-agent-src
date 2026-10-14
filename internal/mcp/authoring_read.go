package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/summary"
	"github.com/monoes/mono-agent/internal/workflow"
)

// searchHit is one `workflow search --json` row.
type searchHit struct {
	Source  string   `json:"source"` // "template" or "workflow"
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Inputs  []string `json:"inputs,omitempty"`
	Active  bool     `json:"active,omitempty"`
	Command string   `json:"command"`
}

func sortedTemplates() []workflow.Template {
	ts := workflow.ListTemplates()
	sort.Slice(ts, func(i, j int) bool { return ts[i].ID < ts[j].ID })
	return ts
}

// exampleInputJSON is the copy-pasteable --input of a template's command.
func exampleInputJSON(inputs []string) string {
	if len(inputs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(inputs))
	for _, key := range inputs {
		if strings.HasSuffix(key, "s") { // plural keys are arrays in every bundled template
			parts = append(parts, fmt.Sprintf("%q:[\"...\",\"...\"]", key))
		} else {
			parts = append(parts, fmt.Sprintf("%q:\"...\"", key))
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func toolWorkflowSearch(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		Query string `json:"query"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	query := strings.ToLower(a.Query)
	matches := func(fields ...string) bool {
		if query == "" {
			return true
		}
		for _, f := range fields {
			if strings.Contains(strings.ToLower(f), query) {
				return true
			}
		}
		return false
	}
	hits := []searchHit{}
	for _, t := range sortedTemplates() {
		if !matches(t.ID, t.Name, t.Description) {
			continue
		}
		cmd := "monoagentcli workflow templates run " + t.ID
		if ex := exampleInputJSON(t.Inputs); ex != "" {
			cmd += " --input '" + ex + "'"
		}
		hits = append(hits, searchHit{Source: "template", ID: t.ID, Name: t.Name, Inputs: t.Inputs, Command: cmd})
	}
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	saved, err := rt.store.ListWorkflows(ctx, rt.profileID)
	if err != nil {
		return nil, fmt.Errorf("list workflows: %w", err)
	}
	for _, wf := range saved {
		fields := []string{wf.ID, wf.Name, wf.Description}
		for _, n := range wf.Nodes {
			fields = append(fields, n.Type)
		}
		if !matches(fields...) {
			continue
		}
		hits = append(hits, searchHit{Source: "workflow", ID: wf.ID, Name: wf.Name, Active: wf.IsActive, Command: "monoagentcli workflow run " + wf.ID})
	}
	return hits, nil
}

func toolWorkflowInputs(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	wf, err := ownedWorkflow(ctx, rt, a.ID)
	if err != nil {
		return nil, err
	}
	fields := workflow.TriggerInputs(wf.Nodes, wf.Connections, workflow.LoadDefaultSchema)
	if fields == nil {
		fields = []workflow.TriggerField{}
	}
	return map[string]interface{}{
		"v":        1,
		"workflow": wf.ID,
		"fields":   fields,
		"skeleton": workflow.TriggerInputSkeleton(fields),
	}, nil
}

func toolWorkflowExecutions(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		WorkflowID string `json:"workflow_id"`
		Limit      *int   `json:"limit"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	limit := 20
	if a.Limit != nil {
		limit = *a.Limit
	}
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	if a.WorkflowID == "" {
		n := limit
		if n <= 0 {
			n = -1 // SQLite: no limit
		}
		rows, err := summary.RecentExecutions(ctx, rt.db.DB, rt.profileID, n)
		if err != nil {
			return nil, fmt.Errorf("list executions: %w", err)
		}
		if rows == nil {
			rows = []summary.ExecRow{}
		}
		return rows, nil
	}
	if _, err := ownedWorkflow(ctx, rt, a.WorkflowID); err != nil {
		return nil, err
	}
	execs, err := rt.store.ListExecutions(ctx, a.WorkflowID, limit)
	if err != nil {
		return nil, fmt.Errorf("list executions: %w", err)
	}
	if execs == nil {
		execs = []workflow.WorkflowExecution{}
	}
	return execs, nil
}

func toolWorkflowTemplateList(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	return sortedTemplates(), nil
}

func templateArg(args json.RawMessage) (workflow.WorkflowFile, workflow.Template, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return workflow.WorkflowFile{}, workflow.Template{}, err
	}
	if a.ID == "" {
		return workflow.WorkflowFile{}, workflow.Template{}, fmt.Errorf("id is required")
	}
	file, ok := workflow.GetTemplate(a.ID)
	if !ok {
		return file, workflow.Template{}, fmt.Errorf("unknown template %q: workflow_template_list lists them", a.ID)
	}
	for _, t := range workflow.ListTemplates() {
		if t.ID == a.ID {
			return file, t, nil
		}
	}
	return file, workflow.Template{ID: a.ID}, nil
}

func toolWorkflowTemplateShow(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	file, meta, err := templateArg(args)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"id":          meta.ID,
		"name":        file.Name,
		"description": file.Description,
		"inputs":      meta.Inputs,
		"definition":  file,
	}, nil
}

// toolWorkflowTemplateInstantiate is `workflow templates use`: the template's
// placeholder node ids become fresh ones, so every copy stands alone; it
// starts inactive; a failed save leaves nothing behind.
func toolWorkflowTemplateInstantiate(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	file, meta, err := templateArg(args)
	if err != nil {
		return nil, err
	}
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	wf := instantiateTemplate(file, rt.profileID)
	wf.Description = workflow.SavedTemplateDescription(meta.ID)
	if err := workflow.ValidateConnectionsAgainstNodes(wf.Nodes, wf.Connections); err != nil {
		return nil, err
	}
	persistErr := func() error {
		if err := rt.store.CreateWorkflow(ctx, &wf); err != nil {
			return fmt.Errorf("save workflow: %w", err)
		}
		if err := rt.store.SaveWorkflowNodes(ctx, wf.ID, wf.Nodes); err != nil {
			return fmt.Errorf("save nodes: %w", err)
		}
		if err := rt.store.SaveWorkflowConnections(ctx, wf.ID, wf.Connections); err != nil {
			return fmt.Errorf("save connections: %w", err)
		}
		return nil
	}()
	if persistErr != nil {
		if cleanupErr := rt.store.DeleteWorkflow(ctx, wf.ID); cleanupErr != nil {
			return nil, fmt.Errorf("%w (cleaning up the partly created workflow %s also failed: %v)", persistErr, wf.ID, cleanupErr)
		}
		return nil, persistErr
	}
	return wf, nil
}

// instantiateTemplate is cmd/monoagentcli's of the same name: a concrete,
// unsaved workflow of profileID from a bundled template.
func instantiateTemplate(tmpl workflow.WorkflowFile, profileID string) workflow.Workflow {
	idMap := make(map[string]string, len(tmpl.Nodes))
	for _, n := range tmpl.Nodes {
		idMap[n.ID] = uuid.New().String()
	}
	now := time.Now().UTC()
	wf := workflow.Workflow{
		ID: uuid.New().String(), Name: tmpl.Name, Description: tmpl.Description,
		Version: 1, ProfileID: profileID, CreatedAt: now, UpdatedAt: now,
	}
	for _, fn := range tmpl.Nodes {
		config := fn.Config
		if config == nil {
			config = map[string]interface{}{}
		}
		// The Outlook sync node scopes what it syncs by its own profile_id.
		if fn.Type == "people.sync_outlook_message" {
			config["profile_id"] = profileID
		}
		wf.Nodes = append(wf.Nodes, workflow.WorkflowNode{
			ID: idMap[fn.ID], WorkflowID: wf.ID, Type: fn.Type, Name: fn.Name,
			PositionX: fn.Position.X, PositionY: fn.Position.Y, Disabled: fn.Disabled, Config: config,
		})
	}
	for _, fe := range tmpl.Connections {
		wf.Connections = append(wf.Connections, workflow.WorkflowConnection{
			ID: uuid.New().String(), WorkflowID: wf.ID,
			SourceNodeID: idMap[fe.Source], SourceHandle: fe.SourceHandle,
			TargetNodeID: idMap[fe.Target], TargetHandle: fe.TargetHandle,
		})
	}
	return wf
}
