package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/hilsuggest"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// HumanInLoopNode pauses workflow execution and waits for a human to review
// and optionally edit the incoming data before approving or rejecting it.
//
// Config fields:
//
//	"readonly_fields"  ([]string): item keys shown in the read-only info section
//	"editable_fields"  ([]string): item keys shown in the editable section
//	"timeout_minutes"  (float64, optional): max wait time, default 0 = unlimited
//	"auto_decide"      (object, optional): {policy, approve_above, reject_above} —
//	                   TypeSafe Jev settles confident items (surface "hil" only)
//
// Outputs: "main" carries approved items. Rejected items go to the opt-in
// "rejected" output when an edge is wired from it; otherwise a rejection
// fails the node with workflow.ErrRejected and the run ends REJECTED.
type HumanInLoopNode struct{}

func (n *HumanInLoopNode) Type() string { return "core.human_in_loop" }

func (n *HumanInLoopNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	db := vault.DBFromContext(ctx)
	if db == nil {
		return nil, fmt.Errorf("human_in_loop: database not available in context")
	}

	profileID := vault.ProfileIDFromContext(ctx)
	if profileID == "" {
		profileID = "default"
	}

	// Optional timeout: pending items older than this are auto-rejected.
	timeoutMinutes := 0.0
	if v, ok := config["timeout_minutes"]; ok {
		switch t := v.(type) {
		case float64:
			timeoutMinutes = t
		case int:
			timeoutMinutes = float64(t)
		}
	}

	// Non-blocking: on first encounter, create one pending row per input item
	// and pause the execution (ErrNodePaused) — the engine persists resume state
	// and suspends without holding a goroutine. On resume, this node re-runs and
	// evaluates the (now possibly resolved) rows. Idempotent by (execution,
	// node): duplicate rows are never created. Rows an earlier run of the node
	// already used (a loop's earlier iterations) are skipped, so each run
	// asks about its own items.
	consumed := hilConsumed(ctx)
	existing, err := loadHILRows(ctx, db, input.ExecutionID, input.NodeID, consumed)
	if err != nil {
		return nil, err
	}

	if len(existing) == 0 {
		decided, err := n.createRows(ctx, db, input, config, profileID)
		if err != nil {
			return nil, err
		}
		if !decided {
			return nil, workflow.ErrNodePaused
		}
		// auto_decide settled some rows: evaluate them exactly as if a
		// person had (all approved ⇒ no pause; any rejected ⇒ fail).
		if existing, err = loadHILRows(ctx, db, input.ExecutionID, input.NodeID, consumed); err != nil {
			return nil, err
		}
	}

	// Expire stale pending rows if a timeout is configured, then re-read.
	if timeoutMinutes > 0 {
		_, _ = db.ExecContext(ctx,
			`UPDATE hil_pending SET status='rejected', updated_at=CURRENT_TIMESTAMP
			 WHERE execution_id=? AND node_id=? AND status='pending' AND created_at < datetime('now', ?)`,
			input.ExecutionID, input.NodeID, fmt.Sprintf("-%d minutes", int(timeoutMinutes)))
		if existing, err = loadHILRows(ctx, db, input.ExecutionID, input.NodeID, consumed); err != nil {
			return nil, err
		}
	}

	anyPending, anyRejected := false, false
	for _, r := range existing {
		switch r.status {
		case "pending":
			anyPending = true
		case "rejected":
			anyRejected = true
		}
	}
	// A rejection fails the node at once unless a `rejected` edge is wired;
	// then the node waits for every decision and routes rejected items there.
	if anyRejected && !rejectedEdgeWired(ctx, db, input) {
		return nil, fmt.Errorf("human_in_loop: %w", workflow.ErrRejected)
	}
	if anyPending {
		return nil, workflow.ErrNodePaused // still awaiting approval
	}

	// Approved items go to main with their row's edited_data applied.
	approvedItems, rejectedItems := splitDecided(input.Items, existing)
	// Outside an execution there is no state to keep: nothing runs again.
	_ = workflow.SetNodeState(ctx, hilState{Consumed: consumed + len(existing)})
	outputs := []workflow.NodeOutput{{Handle: "main", Items: approvedItems}}
	if len(rejectedItems) > 0 {
		outputs = append(outputs, workflow.NodeOutput{Handle: RejectedHandle, Items: rejectedItems})
	}
	return outputs, nil
}

type hilRow struct {
	status string
	edited map[string]interface{}
}

// hilState is the node's state across its runs in one execution: how many
// of its rows (in insertion order) earlier runs already used.
type hilState struct {
	Consumed int `json:"consumed"`
}

// hilConsumed reads hilState.Consumed from the node's state (0 for none).
func hilConsumed(ctx context.Context) int {
	var st hilState
	if raw := workflow.NodeStateFromContext(ctx); raw != nil {
		_ = json.Unmarshal(raw, &st)
	}
	return st.Consumed
}

// loadHILRows returns the HIL rows for one (execution, node) in insertion
// order, skipping the first skip rows (used by earlier runs).
func loadHILRows(ctx context.Context, db *sql.DB, executionID, nodeID string, skip int) ([]hilRow, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT status, edited_data FROM hil_pending WHERE execution_id=? AND node_id=? ORDER BY rowid`,
		executionID, nodeID)
	if err != nil {
		return nil, fmt.Errorf("human_in_loop: load rows: %w", err)
	}
	defer rows.Close()
	var out []hilRow
	for rows.Next() {
		var status, editedRaw string
		if err := rows.Scan(&status, &editedRaw); err != nil {
			return nil, fmt.Errorf("human_in_loop: scan row: %w", err)
		}
		r := hilRow{status: status}
		if editedRaw != "" && editedRaw != "{}" {
			_ = json.Unmarshal([]byte(editedRaw), &r.edited)
		}
		out = append(out, r)
	}
	if skip >= len(out) {
		return nil, rows.Err()
	}
	return out[skip:], rows.Err()
}

// createRows inserts one HIL row per input item, in input order. Rows are
// pending unless auto_decide settled them (see autoDecideRows); decided
// reports whether any row was inserted already approved or rejected.
func (n *HumanInLoopNode) createRows(ctx context.Context, db *sql.DB, input workflow.NodeInput, config map[string]interface{}, profileID string) (decided bool, err error) {
	readonlyFields := stringsFromConfig(config, "readonly_fields")
	editableFields := stringsFromConfig(config, "editable_fields")
	ro := make([]map[string]interface{}, len(input.Items))
	ed := make([]map[string]interface{}, len(input.Items))
	for i, item := range input.Items {
		ro[i] = extractFields(item.JSON, readonlyFields)
		ed[i] = extractFields(item.JSON, editableFields)
		if len(readonlyFields) == 0 && len(editableFields) == 0 {
			ed[i] = copyMap(item.JSON)
		}
	}
	plan, err := autoDecideRows(ctx, db, input, config, profileID, ro, ed)
	if err != nil {
		return false, err
	}
	for i := range input.Items {
		cfg := map[string]interface{}{
			"readonly_fields": readonlyFields,
			"editable_fields": editableFields,
		}
		status := "pending"
		if plan != nil {
			for k, v := range plan[i].extra {
				cfg[k] = v
			}
			if plan[i].status != "" {
				status, decided = plan[i].status, true
			}
		}
		configJSON, _ := json.Marshal(cfg)
		roJSON, _ := json.Marshal(ro[i])
		edJSON, _ := json.Marshal(ed[i])
		if _, err := db.ExecContext(ctx,
			`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, readonly_data, editable_data, edited_data, node_config, profile_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, '{}', ?, ?)`,
			uuid.New().String(), input.ExecutionID, input.WorkflowID, input.NodeID, input.NodeName,
			status, string(roJSON), string(edJSON), string(configJSON), profileID,
		); err != nil {
			return false, fmt.Errorf("human_in_loop: insert pending record: %w", err)
		}
	}
	return decided, nil
}

func stringsFromConfig(config map[string]interface{}, key string) []string {
	raw, ok := config[key]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []string:
		return v
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, s := range v {
			if str, ok := s.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

func extractFields(src map[string]interface{}, keys []string) map[string]interface{} {
	out := make(map[string]interface{}, len(keys))
	for _, k := range keys {
		if v, ok := src[k]; ok {
			out[k] = v
		}
	}
	return out
}

func copyMap(src map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// rowPlan is auto_decide's outcome for one row: a status to insert with
// ("" = pending) and keys merged into the row's node_config.
type rowPlan struct {
	status string
	extra  map[string]interface{}
}

// autoDecideCfg is the parsed auto_decide config.
type autoDecideCfg struct {
	policy       string
	approveAbove float64
	rejectAbove  float64 // 0 = never auto-reject
}

// autoDecideTimeout bounds all Jev calls of one node run.
const autoDecideTimeout = 30 * time.Second

func parseAutoDecide(ctxDB *sql.DB, profileID string, config map[string]interface{}) (*autoDecideCfg, error) {
	raw, ok := config["auto_decide"]
	if !ok || raw == nil {
		return nil, nil
	}
	// The GUI's JSON editor may hand the object over as text.
	if str, ok := raw.(string); ok {
		if strings.TrimSpace(str) == "" {
			return nil, nil
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(str), &parsed); err != nil {
			return nil, fmt.Errorf("%w: human_in_loop: auto_decide is not a JSON object: %v", workflow.ErrInvalidConfig, err)
		}
		raw = parsed
	}
	m, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: human_in_loop: auto_decide must be an object", workflow.ErrInvalidConfig)
	}
	c := &autoDecideCfg{}
	if p, ok := m["policy"].(string); ok {
		c.policy = strings.TrimSpace(p)
	}
	num := func(key string) (float64, bool, error) {
		v, ok := m[key]
		if !ok || v == nil {
			return 0, false, nil
		}
		var f float64
		switch t := v.(type) {
		case float64:
			f = t
		case int:
			f = float64(t)
		default:
			return 0, false, fmt.Errorf("%w: human_in_loop: auto_decide.%s must be a number", workflow.ErrInvalidConfig, key)
		}
		if f <= 0 || f > 1 {
			return 0, false, fmt.Errorf("%w: human_in_loop: auto_decide.%s = %v must be in (0,1]", workflow.ErrInvalidConfig, key, f)
		}
		return f, true, nil
	}
	f, set, err := num("approve_above")
	if err != nil {
		return nil, err
	}
	if set {
		c.approveAbove = f
	} else {
		c.approveAbove = jevconf.Threshold(ctxDB, profileID, jevconf.HIL, jevconf.DefaultThreshold[jevconf.HIL])
	}
	if c.rejectAbove, _, err = num("reject_above"); err != nil {
		return nil, err
	}
	return c, nil
}

// orgStarted reports whether the execution was started by an org (a grant
// or a message to an automation role). Its HIL rows are tier-routed org
// decisions (internal/orgdecide), so auto_decide never settles them.
func orgStarted(ctx context.Context, db *sql.DB, executionID string) bool {
	tt := workflow.TriggerTypeFrom(ctx)
	if tt == "" {
		_ = db.QueryRowContext(ctx, `SELECT COALESCE(trigger_type,'') FROM workflow_executions WHERE id = ?`, executionID).Scan(&tt)
	}
	return tt == workflow.TriggerTypeOrgTool || tt == workflow.TriggerTypeOrgMessage
}

// autoDecideRows applies the optional auto_decide config (plan WS5). It
// returns nil when auto_decide is absent — rows are then exactly today's.
// Otherwise every row gets a plan: skipped (surface off, no key, Jev
// error) ⇒ pending with the reason; confident approve (top p ≥
// approve_above) ⇒ approved; confident reject with reject_above set ⇒
// rejected; anything else ⇒ pending with the suggestion stored. Rows of
// org-started executions only ever get the suggestion.
func autoDecideRows(ctx context.Context, db *sql.DB, input workflow.NodeInput, config map[string]interface{}, profileID string, ro, ed []map[string]interface{}) ([]rowPlan, error) {
	cfg, err := parseAutoDecide(db, profileID, config)
	if err != nil || cfg == nil {
		return nil, err
	}
	plan := make([]rowPlan, len(input.Items))
	all := func(k string, v interface{}) {
		for i := range plan {
			if plan[i].extra == nil {
				plan[i].extra = map[string]interface{}{}
			}
			plan[i].extra[k] = v
		}
	}
	if cfg.policy != "" {
		all("policy", cfg.policy)
	}
	skip := func(reason string) ([]rowPlan, error) {
		zerolog.Ctx(ctx).Warn().Str("execution_id", input.ExecutionID).Str("node_id", input.NodeID).
			Msg("human_in_loop: auto_decide ignored: " + reason)
		all("auto_decide_skipped", reason)
		return plan, nil
	}
	if !jevconf.Enabled(db, profileID, jevconf.HIL) {
		return skip("TypeSafe Jev surface \"hil\" is not enabled for this profile (monoagentcli jev enable hil)")
	}
	client, err := jevconf.NewClient(ctx, db, profileID, "", "", jevconf.HIL)
	if err != nil {
		return skip(err.Error())
	}
	ins := make([]hilsuggest.Input, len(input.Items))
	for i := range ins {
		ins[i] = hilsuggest.Input{Readonly: ro[i], Editable: ed[i], Policy: cfg.policy}
	}
	jctx, cancel := context.WithTimeout(ctx, autoDecideTimeout)
	defer cancel()
	sugs, errs := hilsuggest.SuggestAll(jctx, client, ins)
	var firstErr error
	for i := range plan {
		if errs[i] != nil {
			if firstErr == nil {
				firstErr = errs[i]
			}
			continue
		}
		if plan[i].extra == nil {
			plan[i].extra = map[string]interface{}{}
		}
		plan[i].extra["suggestion"] = sugs[i]
	}
	if firstErr != nil {
		// Any Jev failure leaves every row for a person, as without Jev.
		return skip("jev: " + firstErr.Error())
	}
	if orgStarted(ctx, db, input.ExecutionID) {
		all("auto_decide_skipped", "org-started execution: the org decides its HIL items")
		return plan, nil
	}
	for i, s := range sugs {
		switch {
		// Never auto-approve an item Jev itself rates high-risk: it waits for
		// a human with the suggestion shown.
		case s.Choice == hilsuggest.Approve && s.P >= cfg.approveAbove && s.Risk != "high":
			plan[i].status = "approved"
		case s.Choice == hilsuggest.Reject && cfg.rejectAbove > 0 && s.P >= cfg.rejectAbove:
			plan[i].status = "rejected"
		default:
			continue
		}
		plan[i].extra["decided_by"] = "jev:" + s.Model
		plan[i].extra["p"] = s.P
	}
	return plan, nil
}
