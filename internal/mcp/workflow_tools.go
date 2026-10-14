package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// Workflow tools: with --workflow-tools (or MONOAGENT_MCP_WORKFLOW_TOOLS) every
// saved workflow of the server's profile, or the ones named, is served as a tool
// of its own, wf_<slug>, whose arguments are the workflow's trigger input (the
// fields `workflow inputs` finds) and whose call runs the workflow and waits, as
// workflow_run does. They are mutating, so they also need --allow-mutations.
// The workflows are read at every tools/list and tools/call, so a workflow saved
// after the server started is served without a restart.

// workflowToolPrefix starts the name of every workflow tool. No native tool's
// name starts with it.
const workflowToolPrefix = "wf_"

// maxToolNameLen is the longest tool name MCP hosts accept.
const maxToolNameLen = 64

// workflowToolTimeoutArg is the one argument of a workflow tool that is not
// trigger input: the wait, as workflow_run's timeout_seconds. It is removed
// from the input before the run.
const workflowToolTimeoutArg = "_timeout_seconds"

// workflowToolsAll is the value of Options.WorkflowTools that serves every
// saved workflow; "1", "true" and "*" mean the same.
const workflowToolsAll = "all"

// workflowToolsSelection reads Options.WorkflowTools: all reports whether every
// workflow is served, names the IDs and names listed otherwise (names compared
// without case). An empty value serves none.
func workflowToolsSelection(v string) (all bool, names map[string]bool) {
	v = strings.TrimSpace(v)
	switch strings.ToLower(v) {
	case "", "0", "false":
		return false, nil
	case workflowToolsAll, "1", "true", "*":
		return true, nil
	}
	names = map[string]bool{}
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			names[p] = true
			names[strings.ToLower(p)] = true
		}
	}
	return false, names
}

// workflowToolsRequested reports whether v (Options.WorkflowTools) serves any
// workflow tool.
func workflowToolsRequested(v string) bool {
	all, names := workflowToolsSelection(v)
	return all || len(names) > 0
}

// workflowSlug turns a workflow name into the part of a tool name after wf_:
// lowercase, every run of characters outside [a-z0-9] one underscore, no
// underscore at either end, "workflow" when nothing is left.
func workflowSlug(name string) string {
	var b strings.Builder
	under := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			under = false
			continue
		}
		if !under && b.Len() > 0 {
			b.WriteByte('_')
			under = true
		}
	}
	s := strings.TrimRight(b.String(), "_")
	if s == "" {
		return "workflow"
	}
	return s
}

// workflowToolName builds wf_<slug>, with suffix (when not empty) after one more
// underscore, cut so that the whole stays within maxToolNameLen.
func workflowToolName(slug, suffix string) string {
	room := maxToolNameLen - len(workflowToolPrefix)
	if suffix != "" {
		room -= len(suffix) + 1
	}
	if len(slug) > room {
		slug = strings.TrimRight(slug[:room], "_")
	}
	if suffix != "" {
		return workflowToolPrefix + slug + "_" + suffix
	}
	return workflowToolPrefix + slug
}

// idSuffix is the lowercase [a-z0-9] characters of a workflow ID, at most n.
func idSuffix(id string, n int) string {
	var b strings.Builder
	// Saved workflow IDs start "wf-"; those letters would be the same in
	// every suffix.
	for _, r := range strings.TrimPrefix(strings.ToLower(id), "wf-") {
		if b.Len() == n {
			break
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// workflowToolNames names the tool of each workflow, keyed by workflow ID.
// Workflows are taken oldest first (then by ID), so the first to claim a slug
// keeps it and a later one with the same slug gets a short ID suffix (six
// characters, more if those collide too, a counter as the last resort).
func workflowToolNames(wfs []workflow.Workflow) map[string]string {
	sorted := append([]workflow.Workflow(nil), wfs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].CreatedAt.Before(sorted[j].CreatedAt)
		}
		return sorted[i].ID < sorted[j].ID
	})
	taken := map[string]bool{}
	out := make(map[string]string, len(sorted))
	for _, wf := range sorted {
		slug := workflowSlug(wf.Name)
		name := workflowToolName(slug, "")
		for n := 6; taken[name] && n <= 24; n += 6 {
			if sfx := idSuffix(wf.ID, n); sfx != "" {
				name = workflowToolName(slug, sfx)
			}
		}
		for i := 2; taken[name]; i++ {
			name = workflowToolName(slug, fmt.Sprintf("%s%d", idSuffix(wf.ID, 6), i))
		}
		taken[name] = true
		out[wf.ID] = name
	}
	return out
}

// workflowInputSchema is the inputSchema of a workflow's tool: one property per
// trigger field its nodes read (a field read through the json function is a
// list, any other a string, with the reading node's schema examples), plus
// _timeout_seconds. No field is required, since a node may read one only on a
// branch, and other fields are passed through as trigger input too.
func workflowInputSchema(fields []workflow.TriggerField) map[string]interface{} {
	props := map[string]interface{}{}
	for _, f := range fields {
		if f.Name == workflowToolTimeoutArg {
			continue
		}
		desc := fmt.Sprintf("Trigger input field %q (the workflow reads it as $json.%s)", f.Name, f.Name)
		if len(f.Nodes) > 0 {
			desc += ", read by " + strings.Join(f.Nodes, ", ")
		}
		prop := map[string]interface{}{"description": desc + "."}
		if f.Structured {
			prop["type"] = "array"
			prop["items"] = map[string]interface{}{}
		} else {
			prop["type"] = "string"
		}
		if len(f.Examples) > 0 {
			ex := make([]interface{}, 0, len(f.Examples))
			for _, e := range f.Examples {
				ex = append(ex, e)
			}
			if f.Structured {
				prop["examples"] = []interface{}{ex}
			} else {
				prop["examples"] = ex
			}
		}
		props[f.Name] = prop
	}
	props[workflowToolTimeoutArg] = map[string]interface{}{
		"type":        "number",
		"description": "Wait limit in seconds (default 120, clamped to [1, 600]); not passed to the workflow",
	}
	return map[string]interface{}{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": true,
	}
}

// workflowToolDescription says what the tool runs and what it returns.
func workflowToolDescription(wf *workflow.Workflow) string {
	desc := fmt.Sprintf("Run the saved workflow %q", wf.Name)
	if d := strings.TrimSpace(wf.Description); d != "" {
		desc += ": " + d
	}
	desc = strings.TrimRight(desc, ".") + "."
	return desc + " The arguments are its trigger input (available as {{ $json.<field> }}). " +
		"Waits (default 120s) and returns {execution_id, status, nodes:[{node_id, type, status, output_items}]}; " +
		"timed_out:true means it is still running (check workflow_status), " +
		"status WAITING means a Human-in-Loop node paused it (hil_list / hil_approve)."
}

// workflowTools is the tool of each active workflow Options.WorkflowTools selects, or
// none when it is empty. A workflow that cannot be read is left out and said on
// stderr, as is a store that cannot be listed.
func (s *Server) workflowTools() []tool {
	if !workflowToolsRequested(s.opts.WorkflowTools) {
		return nil
	}
	all, names := workflowToolsSelection(s.opts.WorkflowTools)
	ctx := context.Background()
	rt, err := s.runtime()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp: workflow tools: %v\n", err)
		return nil
	}
	wfs, err := rt.store.ListWorkflows(ctx, rt.profileID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp: workflow tools: list workflows: %v\n", err)
		return nil
	}
	selected := make([]workflow.Workflow, 0, len(wfs))
	for _, wf := range wfs {
		// Only an active workflow can be run, so only those are tools: an
		// inactive one would be listed and then fail on every call.
		if !wf.IsActive {
			continue
		}
		if all || names[wf.ID] || names[wf.Name] || names[strings.ToLower(wf.Name)] {
			selected = append(selected, wf)
		}
	}
	toolNames := workflowToolNames(selected)
	out := make([]tool, 0, len(selected))
	for i := range selected {
		wf := &selected[i]
		if len(wf.Nodes) == 0 {
			full, err := rt.store.GetWorkflow(ctx, wf.ID)
			if err != nil || full == nil {
				fmt.Fprintf(os.Stderr, "mcp: workflow tools: workflow %s cannot be read, not served\n", wf.ID)
				continue
			}
			wf = full
		}
		id := wf.ID
		out = append(out, tool{
			name:        toolNames[id],
			description: workflowToolDescription(wf),
			schema:      workflowInputSchema(workflow.TriggerInputs(wf.Nodes, wf.Connections, workflow.LoadDefaultSchema)),
			annotations: map[string]bool{"readOnlyHint": false},
			mutating:    true,
			handler: func(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
				return runWorkflowTool(ctx, s, id, args)
			},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// runWorkflowTool runs workflow id with the call's arguments as its trigger
// input, through workflow_run, so the wait and the result are the same.
func runWorkflowTool(ctx context.Context, s *Server, id string, args json.RawMessage) (interface{}, error) {
	input := map[string]interface{}{}
	if err := decodeArgs(args, &input); err != nil {
		return nil, err
	}
	if input == nil { // arguments: null
		input = map[string]interface{}{}
	}
	var timeout float64
	if v, ok := input[workflowToolTimeoutArg]; ok {
		n, isNum := v.(float64)
		if !isNum {
			return nil, fmt.Errorf("%s must be a number", workflowToolTimeoutArg)
		}
		timeout = n
		delete(input, workflowToolTimeoutArg)
	}
	runArgs, err := json.Marshal(map[string]interface{}{"id": id, "input": input, "timeout_seconds": timeout})
	if err != nil {
		return nil, fmt.Errorf("render arguments: %w", err)
	}
	return toolWorkflowRun(ctx, s, runArgs)
}

// ErrWorkflowToolsWithNarrowMode refuses --workflow-tools with --api-only or
// --tasks-only: those serve one family of tools and no workflow tool.
var ErrWorkflowToolsWithNarrowMode = errors.New("--workflow-tools serves saved workflows as tools and cannot be combined with --api-only or --tasks-only (MONOAGENT_MCP_WORKFLOW_TOOLS, MONOAGENT_MCP_API_ONLY=1 and MONOAGENT_MCP_TASKS_ONLY=1 count as the flags)")
