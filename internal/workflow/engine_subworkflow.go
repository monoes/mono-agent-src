package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/vault"
)

// subWorkflowWakeWindow bounds how long a finished child keeps trying to wake
// a parent that has started it but not yet persisted WAITING. Past it the
// parent's own pause window (the resume poll) still picks it up.
const subWorkflowWakeWindow = 5 * time.Second

// StartSubWorkflow implements SubWorkflowRunner: it persists a QUEUED child
// execution carrying the items, links it to the parent node, and enqueues it.
// The child runs whether or not its workflow is active.
func (e *WorkflowEngine) StartSubWorkflow(ctx context.Context, req SubWorkflowStart) (string, error) {
	parentData := TriggerDataFrom(ctx)
	depth := subWorkflowDepth(parentData) + 1
	if depth > MaxSubWorkflowDepth {
		return "", fmt.Errorf("sub-workflow call depth exceeds %d (a workflow calling itself without end?)", MaxSubWorkflowDepth)
	}
	profileID, perr := vault.ProfileIDFromContext(ctx)
	if perr != nil {
		return "", fmt.Errorf("sub-workflow ran without a profile: %w", perr)
	}
	wf, err := ResolveSubWorkflow(ctx, e.store, req.Workflow, profileID)
	if err != nil {
		return "", err
	}

	raw := make([]interface{}, 0, len(req.Items))
	for _, it := range req.Items {
		raw = append(raw, subWorkflowItemData(it))
	}
	data := map[string]interface{}{
		SubWorkflowItemsKey: raw,
		SubWorkflowParentKey: map[string]interface{}{
			"execution_id": req.ParentExecutionID,
			"node_id":      req.ParentNodeID,
			"depth":        depth,
		},
	}
	// The child inherits the parent's workdir confinement and chain trace,
	// so a call cannot step outside what the parent run was allowed.
	for _, k := range []string{"org", "trace"} {
		if v, ok := parentData[k]; ok {
			data[k] = v
		}
	}
	// It inherits the parent's being an AI run too, so it cannot do what the
	// parent may not. Not the parent's lack of approval itself: the child's
	// own approval decides that, as when the chat runs it directly.
	if mark := subWorkflowAICaller(ctx, parentData); mark != "" {
		data[AICallerKey] = mark
	}
	// And which workflow the user never approved is up the chain, whoever
	// started it (CalledByUnapproved).
	if id, ok := subWorkflowUnapprovedCaller(ctx, parentData); ok {
		data[UnapprovedCallerKey] = id
	}

	// A parent under agent access tells the child which secrets it read, so
	// the child masks them too; the child's stored trigger data is masked.
	stored := data
	if names := secrets.ResolvedNames(ctx); len(names) > 0 {
		data[SecretNamesKey] = names
		stored = maskTriggerData(secrets.ResolvedValues(ctx), data)
	}

	exec := &WorkflowExecution{
		WorkflowID:  wf.ID,
		ProfileID:   profileID,
		Status:      "QUEUED",
		TriggerType: TriggerTypeExecuteWorkflow,
		TriggerData: stored,
	}
	dctx, cancel := dbCtx()
	defer cancel()
	if err := e.store.CreateExecution(dctx, exec); err != nil {
		return "", fmt.Errorf("sub-workflow: create execution: %w", err)
	}
	// Linked before it is enqueued, so the child's end always finds its parent.
	if db := e.store.RawDB(); db != nil {
		if _, err := db.ExecContext(dctx,
			`INSERT INTO subworkflow_runs (parent_execution_id, parent_node_id, child_execution_id, item_index, wait) VALUES (?, ?, ?, ?, ?)`,
			req.ParentExecutionID, req.ParentNodeID, exec.ID, req.ItemIndex, req.Wait); err != nil {
			e.persistExecutionFinished(e.logger, exec.ID, "FAILED", "sub-workflow link not recorded")
			return "", fmt.Errorf("sub-workflow: link execution: %w", err)
		}
	}
	if err := e.queue.Enqueue(ExecutionRequest{
		WorkflowID:    wf.ID,
		ExecutionID:   exec.ID,
		TriggerType:   TriggerTypeExecuteWorkflow,
		TriggerNodeID: subWorkflowTriggerNode(wf),
		TriggerData:   data,
	}); err != nil {
		e.persistExecutionFinished(e.logger, exec.ID, "FAILED", "queue full")
		return exec.ID, fmt.Errorf("sub-workflow: %w", ErrQueueFull)
	}
	return exec.ID, nil
}

// SecretNamesKey is the trigger-data field that names the vault secrets a
// parent run under agent access had read when it called this run (names,
// never values), so this run masks them too. Like UnapprovedCallerKey it
// never reaches the child's items.
const SecretNamesKey = "_agent_secret_names"

// secretNamesOf reads SecretNamesKey's value: a list of strings from the
// queue, of interface values from a stored record.
func secretNamesOf(v interface{}) []string {
	switch l := v.(type) {
	case []string:
		return l
	case []interface{}:
		var out []string
		for _, n := range l {
			if s, ok := n.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// maskTriggerData is data with every value in values masked, for the copy
// of a child's trigger data that is stored; the queue keeps the real one.
func maskTriggerData(values []string, data map[string]interface{}) map[string]interface{} {
	if len(values) == 0 {
		return data
	}
	return maskJSON(values, data)
}

// UnapprovedWorkflowCaller is the AI-caller mark (AICallerKey) of a
// sub-workflow that a workflow the user never approved started.
const UnapprovedWorkflowCaller = "unapproved-workflow"

// UnapprovedCallerKey is the trigger-data field that names the workflow
// the user never approved that called this run, directly or further up the
// chain (CalledByUnapproved), whatever AI-caller mark the run carries. It
// never reaches the child's items, which are SubWorkflowItemsKey's.
const UnapprovedCallerKey = "_unapproved_caller"

// subWorkflowAICaller is the AI-caller mark of a sub-workflow the run in ctx
// starts: the parent's own (an AI's `workflow run`, or an earlier call's),
// "org" (browserctl.KindOrg) when the org side started the parent, and
// UnapprovedWorkflowCaller when the user never approved the parent's
// definition; "" for the user's own run.
func subWorkflowAICaller(ctx context.Context, parentData map[string]interface{}) string {
	if v, _ := parentData[AICallerKey].(string); strings.TrimSpace(v) != "" {
		return v
	}
	switch TriggerTypeFrom(ctx) {
	case TriggerTypeOrgTool, TriggerTypeOrgMessage, TriggerNodeTypeOrg:
		return "org"
	}
	if AIAuthoredFrom(ctx) {
		return UnapprovedWorkflowCaller
	}
	return ""
}

// subWorkflowUnapprovedCaller reports whether a sub-workflow the run in ctx
// starts is called by a workflow the user never approved, and which: the
// one that called the parent, else the parent when its own definition is
// unapproved ("" when the run's context doesn't name it).
func subWorkflowUnapprovedCaller(ctx context.Context, parentData map[string]interface{}) (string, bool) {
	if id, ok := CalledByUnapproved(parentData); ok && id != "" {
		return id, true
	}
	if AIAuthoredFrom(ctx) {
		id, _ := vault.ExecIDsFromContext(ctx)
		return id, true
	}
	_, ok := CalledByUnapproved(parentData)
	return "", ok
}

// CalledByUnapproved reports whether the run with triggerData was called,
// directly or further up the chain, by a workflow the user never approved,
// whoever started it, and that workflow's id when known (a call queued
// before ids were recorded carries only the UnapprovedWorkflowCaller mark).
func CalledByUnapproved(triggerData map[string]interface{}) (workflowID string, ok bool) {
	if v, present := triggerData[UnapprovedCallerKey]; present {
		id, _ := v.(string)
		return strings.TrimSpace(id), true
	}
	mark, _ := triggerData[AICallerKey].(string)
	return "", mark == UnapprovedWorkflowCaller
}

// SubWorkflowReader is what finding a called workflow reads: a workflow by
// ID, and a profile's workflows (the engine's store: files first).
type SubWorkflowReader interface {
	GetWorkflow(ctx context.Context, id string) (*Workflow, error)
	ListWorkflows(ctx context.Context, profileID string) ([]Workflow, error)
}

// ResolveSubWorkflow finds the workflow a call's ref names, as a run's
// core.execute_workflow does: an ID first, then an exact name within
// profileID, which one workflow alone may have. A workflow of another
// profile is never returned.
func ResolveSubWorkflow(ctx context.Context, store SubWorkflowReader, ref, profileID string) (*Workflow, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("%w: sub-workflow: no workflow given", ErrInvalidConfig)
	}
	wf, err := store.GetWorkflow(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("sub-workflow: load workflow: %w", err)
	}
	if wf != nil {
		ok, err := OwnedBy(wf, profileID)
		if err != nil {
			return nil, fmt.Errorf("sub-workflow %q: %w", ref, err)
		}
		if !ok {
			return nil, fmt.Errorf("sub-workflow %q: %w", ref, ErrWorkflowProfileMismatch)
		}
		return wf, nil
	}
	all, err := store.ListWorkflows(ctx, profileID)
	if err != nil {
		return nil, fmt.Errorf("sub-workflow: list workflows: %w", err)
	}
	var match *Workflow
	for i := range all {
		if all[i].Name != ref {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("sub-workflow: more than one workflow is named %q; use its ID", ref)
		}
		match = &all[i]
	}
	if match == nil {
		return nil, fmt.Errorf("sub-workflow %q: %w", ref, ErrWorkflowNotFound)
	}
	// ListWorkflows may leave nodes out; load the full record.
	return store.GetWorkflow(ctx, match.ID)
}

// subWorkflowTriggerNode picks the child's "When called by another workflow"
// trigger so only its branch fires; "" (every trigger fires) when it has none.
func subWorkflowTriggerNode(wf *Workflow) string {
	for _, n := range wf.Nodes {
		if n.Type == TriggerTypeExecuteWorkflow && !n.Disabled {
			return n.ID
		}
	}
	return ""
}

// SubWorkflowChildren implements SubWorkflowRunner.
func (e *WorkflowEngine) SubWorkflowChildren(ctx context.Context, parentExecutionID, parentNodeID string) ([]SubWorkflowChild, error) {
	db := e.store.RawDB()
	if db == nil {
		return nil, errors.New("sub-workflow: database not available")
	}
	rows, err := db.QueryContext(ctx,
		`SELECT child_execution_id, item_index FROM subworkflow_runs
		 WHERE parent_execution_id = ? AND parent_node_id = ? ORDER BY item_index, rowid`,
		parentExecutionID, parentNodeID)
	if err != nil {
		return nil, fmt.Errorf("sub-workflow: list children: %w", err)
	}
	var out []SubWorkflowChild
	for rows.Next() {
		var c SubWorkflowChild
		if err := rows.Scan(&c.ExecutionID, &c.ItemIndex); err != nil {
			rows.Close()
			return nil, fmt.Errorf("sub-workflow: list children: %w", err)
		}
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sub-workflow: list children: %w", err)
	}
	all := len(out) > 0
	ids := make([]string, 0, len(out))
	for i := range out {
		if err := e.fillSubWorkflowChild(ctx, &out[i]); err != nil {
			return nil, err
		}
		all = all && out[i].Done()
		ids = append(ids, out[i].ExecutionID)
	}
	// What the finished children read taints this run as well: their items,
	// or their error text, come back in forms nobody can list (issue #1418).
	// Looked up once, when they have all finished, not on every poll.
	if all {
		if err := e.taintFromChildren(ctx, db, ids); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// taintFromChildren makes this run (under agent access) count the secrets the
// child runs, and every run they called at any depth, read: the audit records
// them by execution, by name.
func (e *WorkflowEngine) taintFromChildren(ctx context.Context, db *sql.DB, childIDs []string) error {
	if !secrets.UnderAgentAccess(ctx) {
		return nil
	}
	profile, err := vault.ProfileIDFromContext(ctx)
	if err != nil {
		return nil
	}
	names, err := descendantSecretNames(ctx, db, profile, childIDs)
	if err != nil {
		return err
	}
	secrets.TaintResolved(ctx, names)
	return nil
}

// descendantSecretNames are the secrets read by the runs ids, and by those
// they called, to MaxSubWorkflowDepth levels: one query per level to find the
// runs, one per batch for the audit (which has no index on the execution).
func descendantSecretNames(ctx context.Context, db *sql.DB, profile string, ids []string) ([]string, error) {
	seen := map[string]bool{}
	var all []string
	level := ids
	for depth := 0; len(level) > 0 && depth <= MaxSubWorkflowDepth; depth++ {
		var fresh []string
		for _, id := range level {
			if !seen[id] {
				seen[id] = true
				fresh = append(fresh, id)
			}
		}
		all = append(all, fresh...)
		level = nil
		for _, batch := range chunkStrings(fresh, 400) {
			rows, err := db.QueryContext(ctx, `SELECT child_execution_id FROM subworkflow_runs WHERE parent_execution_id IN (`+placeholders(len(batch))+`)`, anySlice(batch)...)
			if err != nil {
				return nil, fmt.Errorf("sub-workflow: what the children called: %w", err)
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return nil, err
				}
				level = append(level, id)
			}
			rows.Close()
		}
	}
	var names []string
	for _, batch := range chunkStrings(all, 400) {
		rows, err := db.QueryContext(ctx, `SELECT DISTINCT secret_name FROM profile_agent_secret_audit WHERE profile_id = ? AND execution_id IN (`+placeholders(len(batch))+`)`,
			append([]interface{}{profile}, anySlice(batch)...)...)
		if err != nil {
			return nil, fmt.Errorf("sub-workflow: what the children read: %w", err)
		}
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				rows.Close()
				return nil, err
			}
			names = append(names, n)
		}
		rows.Close()
	}
	return names, nil
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func anySlice(s []string) []interface{} {
	out := make([]interface{}, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

func chunkStrings(s []string, n int) [][]string {
	var out [][]string
	for len(s) > n {
		out, s = append(out, s[:n]), s[n:]
	}
	if len(s) > 0 {
		out = append(out, s)
	}
	return out
}

// fillSubWorkflowChild loads a child's status and, once it has finished, the
// main items of its last successful node (a trigger's items are its input).
func (e *WorkflowEngine) fillSubWorkflowChild(ctx context.Context, c *SubWorkflowChild) error {
	exec, err := e.store.GetExecution(ctx, c.ExecutionID)
	if err != nil {
		return fmt.Errorf("sub-workflow: load execution %s: %w", c.ExecutionID, err)
	}
	if exec == nil {
		c.Status, c.Error = "FAILED", "execution record not found"
		return nil
	}
	c.Status, c.Error = exec.Status, exec.ErrorMessage
	if !c.Done() || c.Failed() {
		return nil
	}
	for i := len(exec.Nodes) - 1; i >= 0; i-- {
		n := exec.Nodes[i]
		if n.Status != "SUCCESS" {
			continue
		}
		c.Items = n.OutputItems
		if len(c.Items) == 0 && e.isTriggerNode(ctx, exec.WorkflowID, n.NodeID) {
			c.Items = n.InputItems
		}
		break
	}
	if c.Items == nil {
		c.Items = []Item{}
	}
	return nil
}

// isTriggerNode reports whether nodeID is a trigger of workflowID. A trigger's
// pass-through record stores its items as input only.
func (e *WorkflowEngine) isTriggerNode(ctx context.Context, workflowID, nodeID string) bool {
	wf, err := e.store.GetWorkflow(ctx, workflowID)
	if err != nil || wf == nil {
		return false
	}
	for _, n := range wf.Nodes {
		if n.ID == nodeID {
			return strings.HasPrefix(n.Type, "trigger.")
		}
	}
	return false
}

// onExecutionFinished wakes a waiting parent when executionID was the last of
// its running sub-workflow children. Called after a terminal status is
// persisted; returns at once for executions no parent waits on.
func (e *WorkflowEngine) onExecutionFinished(executionID string) {
	db := e.store.RawDB()
	if db == nil {
		return
	}
	dctx, cancel := dbCtx()
	defer cancel()
	var parentID, nodeID string
	err := db.QueryRowContext(dctx,
		`SELECT parent_execution_id, parent_node_id FROM subworkflow_runs WHERE child_execution_id = ? AND wait = 1`,
		executionID).Scan(&parentID, &nodeID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) && !strings.Contains(err.Error(), "no such table") {
			e.logger.Warn().Err(err).Str("execution_id", executionID).Msg("engine: sub-workflow parent lookup")
		}
		return
	}
	go e.wakeSubWorkflowParent(parentID, nodeID)
}

// wakeSubWorkflowParent resumes parentID once every child its node started
// has finished. A parent still RUNNING is between starting its children and
// persisting WAITING; it is retried briefly rather than missed.
func (e *WorkflowEngine) wakeSubWorkflowParent(parentID, nodeID string) {
	dctx, cancel := dbCtx()
	children, err := e.SubWorkflowChildren(dctx, parentID, nodeID)
	cancel()
	if err != nil {
		e.logger.Warn().Err(err).Str("execution_id", parentID).Msg("engine: sub-workflow wake")
		return
	}
	for _, c := range children {
		if !c.Done() {
			return // the last child to finish wakes the parent
		}
	}
	deadline := time.Now().Add(subWorkflowWakeWindow)
	for {
		sctx, scancel := dbCtx()
		status, err := e.store.GetExecutionStatus(sctx, parentID)
		scancel()
		if err != nil {
			return
		}
		switch status {
		case "WAITING":
			if err := e.ResumeExecution(parentID); err != nil {
				e.logger.Warn().Err(err).Str("execution_id", parentID).Msg("engine: sub-workflow parent resume")
			}
			return
		case "RUNNING", "QUEUED":
			if time.Now().After(deadline) {
				return
			}
			time.Sleep(25 * time.Millisecond)
		default:
			return
		}
	}
}

// cancelSubWorkflowChildren cancels the unfinished children parentID started
// through cancelExecution, which in turn cancels theirs. A child running in
// another process is not reachable from here; `workflow cancel` reaches it
// (cancelExecution in cmd/monoagentcli signals its process).
func (e *WorkflowEngine) cancelSubWorkflowChildren(parentID string, cause error) {
	db := e.store.RawDB()
	if db == nil {
		return
	}
	dctx, cancel := dbCtx()
	ids, err := UnfinishedSubWorkflowChildren(dctx, db, parentID)
	cancel()
	if err != nil {
		e.logger.Warn().Err(err).Str("execution_id", parentID).Msg("engine: sub-workflow cancel cascade")
		return
	}
	for _, id := range ids {
		e.logger.Info().Str("execution_id", id).Str("parent_execution_id", parentID).Msg("engine: cancelling sub-workflow child with its parent")
		e.cancelExecution(id, cause)
	}
}

// WaitingOnSubWorkflows reports whether executionID is WAITING for its
// sub-workflow children: a node that started children it waits on has not
// completed, and no human review is pending. Such a run needs only a live
// engine (this one runs the children it started, and resumes the parent).
func (e *WorkflowEngine) WaitingOnSubWorkflows(ctx context.Context, executionID string) (bool, error) {
	if e.store.RawDB() == nil {
		return false, nil
	}
	exec, err := e.store.GetExecution(ctx, executionID)
	if err != nil {
		return false, fmt.Errorf("sub-workflow wait: %w", err)
	}
	if exec == nil || exec.Status != "WAITING" {
		return false, nil
	}
	return waitingOnChildren(ctx, e.store, exec)
}

// ExecutionInFlight reports executionID's status and whether a wait for it
// goes on: it is QUEUED or RUNNING, or WAITING only for its sub-workflow
// children (WaitingOnSubWorkflows). A WAITING run is decided on one read of
// its record, so a parent this engine resumes between the status poll and
// that read is in flight, not taken for a run that paused for a person.
func (e *WorkflowEngine) ExecutionInFlight(ctx context.Context, executionID string) (string, bool, error) {
	return ExecutionInFlight(ctx, e.store, executionID)
}

// ExecutionInFlight is WorkflowEngine.ExecutionInFlight over store, for a
// wait that holds the store and not the engine running the execution (the
// MCP server's and the HTTP API's): that engine runs the children and
// resumes the parent all the same.
func ExecutionInFlight(ctx context.Context, store WorkflowStore, executionID string) (string, bool, error) {
	status, err := store.GetExecutionStatus(ctx, executionID)
	if err != nil || status != "WAITING" {
		return status, status == "QUEUED" || status == "RUNNING", err
	}
	exec, err := store.GetExecution(ctx, executionID)
	if err != nil {
		return status, false, fmt.Errorf("sub-workflow wait: %w", err)
	}
	if exec == nil {
		return status, false, nil
	}
	inFlight, err := RecordInFlight(ctx, store, exec)
	return exec.Status, inFlight, err
}

// RecordInFlight is whether a wait for exec, a record already read, goes on:
// it is QUEUED or RUNNING, or WAITING only for its sub-workflow children.
func RecordInFlight(ctx context.Context, store WorkflowStore, exec *WorkflowExecution) (bool, error) {
	switch exec.Status {
	case "QUEUED", "RUNNING":
		return true, nil
	case "WAITING":
		return waitingOnChildren(ctx, store, exec)
	}
	return false, nil
}

// waitingOnChildren is WaitingOnSubWorkflows for exec, a WAITING record.
func waitingOnChildren(ctx context.Context, store WorkflowStore, exec *WorkflowExecution) (bool, error) {
	db := store.RawDB()
	if db == nil {
		return false, nil
	}
	var reviews int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM hil_pending WHERE execution_id = ? AND status = 'pending'`, exec.ID).Scan(&reviews); err != nil {
		return false, fmt.Errorf("sub-workflow wait: %w", err)
	}
	if reviews > 0 {
		return false, nil
	}
	var state executionResumeState
	if exec.ResumeState != "" {
		plain, err := openResumeState(ctx, db, exec.ProfileID, exec.ResumeState)
		if err != nil {
			return false, nil
		}
		if err := json.Unmarshal(plain, &state); err != nil {
			return false, nil
		}
	}
	rows, err := db.QueryContext(ctx,
		`SELECT DISTINCT parent_node_id FROM subworkflow_runs WHERE parent_execution_id = ? AND wait = 1`, exec.ID)
	if err != nil {
		return false, fmt.Errorf("sub-workflow wait: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			return false, fmt.Errorf("sub-workflow wait: %w", err)
		}
		if !state.CompletedNodes[nodeID] {
			return true, nil
		}
	}
	return false, rows.Err()
}
