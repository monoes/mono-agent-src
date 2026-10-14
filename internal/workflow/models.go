package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Item represents a single data record flowing between nodes.
// JSON field is arbitrary key-value data. Binary is optional base64 content.
type Item struct {
	JSON   map[string]interface{} `json:"json"`
	Binary map[string][]byte      `json:"binary,omitempty"`
}

// NewItem constructs an Item from an arbitrary data map.
func NewItem(data map[string]interface{}) Item {
	return Item{
		JSON: data,
	}
}

// NodeInput is passed to NodeExecutor.Execute.
type NodeInput struct {
	Items       []Item            // items from upstream node
	NodeOutputs map[string][]Item // all outputs indexed by node name (for $node["Name"])
	WorkflowID  string
	ExecutionID string
	NodeID      string
	NodeName    string
	// ItemKeys, when set, holds the idempotency key of each item in Items
	// (see ItemKey). Nil means the keys follow the items' positions.
	ItemKeys []string
	// Location is the workflow's timezone (settings.timezone), for the
	// $now/$today of expressions a node evaluates itself. Nil means UTC.
	Location *time.Location
	// Trigger, Vars, RunIndex and PrevNode are $trigger, $vars, $runIndex
	// and $prevNode for the expressions a node evaluates itself.
	Trigger  map[string]interface{}
	Vars     map[string]interface{}
	RunIndex int
	PrevNode string
}

// ItemContext is the expression context for a node evaluating its own
// expressions against one item: the same variables the engine resolves
// config templates with.
func (in NodeInput) ItemContext(json map[string]interface{}) ExpressionContext {
	return ExpressionContext{
		JSON:        json,
		Node:        in.NodeOutputs,
		WorkflowID:  in.WorkflowID,
		ExecutionID: in.ExecutionID,
		Trigger:     in.Trigger,
		Vars:        in.Vars,
		RunIndex:    in.RunIndex,
		PrevNode:    in.PrevNode,
		Location:    in.Location,
	}
}

// NodeOutput is returned by NodeExecutor.Execute.
// Handle is the output port name: "main", "true", "false", "error", case names, etc.
type NodeOutput struct {
	Handle string
	Items  []Item
}

// NodeExecutor is the single interface every action type must implement.
type NodeExecutor interface {
	// Execute runs the node logic. config is the node's JSON config map (credentials already injected).
	// Returns one or more NodeOutputs (one per output handle used).
	Execute(ctx context.Context, input NodeInput, config map[string]interface{}) ([]NodeOutput, error)
	// Type returns the node type string, e.g. "core.if", "action.instagram.KEYWORD_SEARCH"
	Type() string
}

// TriggerProvider is implemented by nodes that can initiate workflow executions.
type TriggerProvider interface {
	NodeExecutor
	// Activate registers the trigger. When it fires, it calls triggerFn with initial items.
	Activate(ctx context.Context, workflowID string, nodeID string, config map[string]interface{}, triggerFn func(items []Item)) error
	// Deactivate stops the trigger.
	Deactivate(workflowID string, nodeID string) error
}

// PerItemConfigResolver is implemented by nodes that evaluate part of their
// config once per input item rather than once for the whole batch (e.g. a
// filter condition, or a URL template that reads from $json). RunExecution
// normally resolves a node's config templates once, using only the first
// input item — correct for nodes that run once per batch, wrong for these.
// Each returned field spec is either a top-level config key (e.g.
// "condition") or, to protect one sub-key of every element in a top-level
// array-of-objects field, "arrayKey[].subKey" (e.g. "assignments[].value").
// RunExecution holds these fields back from its own resolution pass so the
// node receives them as raw (template-containing) values and can resolve
// each one itself, once per item.
type PerItemConfigResolver interface {
	NodeExecutor
	PerItemConfigFields() []string
}

// Workflow is the top-level entity stored in the workflows table.
type Workflow struct {
	ID          string    `json:"id" db:"id"`
	Name        string    `json:"name" db:"name"`
	Description string    `json:"description" db:"description"`
	IsActive    bool      `json:"is_active" db:"is_active"`
	Version     int       `json:"version" db:"version"`
	ProfileID   string    `json:"profile_id,omitempty" db:"profile_id"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" db:"updated_at"`
	// Populated by LoadWorkflow — not stored directly
	Nodes       []WorkflowNode       `json:"nodes,omitempty"`
	Connections []WorkflowConnection `json:"connections,omitempty"`

	// Settings holds workflow-level options, e.g. "error_workflow" (the id
	// of the workflow to run when this one fails), "parallel_branches",
	// "variables" ($vars), "executionTimeout" (seconds of active running
	// time; see ExecutionTimeoutSetting), "per_item_execution"
	// (PerItemExecutionSetting), "disabled_nodes" (DisabledNodesSetting)
	// and "timezone" (TimezoneSetting).
	Settings map[string]interface{} `json:"settings,omitempty"`
}

// WorkflowNode is one node in the workflow graph.
// The Type field is the node type string (e.g. "core.if", "trigger.schedule").
type WorkflowNode struct {
	ID         string `json:"id" db:"id"`
	WorkflowID string `json:"workflow_id" db:"workflow_id"`
	// Type is the node type identifier; stored as node_type in the database.
	Type      string                 `json:"node_type" db:"node_type"`
	Name      string                 `json:"name" db:"name"`
	Config    map[string]interface{} `json:"config" db:"-"`
	ConfigRaw string                 `json:"-" db:"config"`
	PositionX float64                `json:"position_x" db:"position_x"`
	PositionY float64                `json:"position_y" db:"position_y"`
	Disabled  bool                   `json:"disabled" db:"disabled"`
	Schema    *NodeSchema            `json:"schema,omitempty" db:"-"`
	CreatedAt time.Time              `json:"created_at" db:"created_at"`
	UpdatedAt time.Time              `json:"updated_at" db:"updated_at"`
}

// ParseConfig unmarshals ConfigRaw JSON into Config.
// If ConfigRaw is empty and Config is already populated (e.g. loaded from a
// file-store workflow), the existing Config is left intact.
func (n *WorkflowNode) ParseConfig() error {
	if n.ConfigRaw == "" {
		if n.Config == nil {
			n.Config = make(map[string]interface{})
		}
		return nil
	}
	return json.Unmarshal([]byte(n.ConfigRaw), &n.Config)
}

// MarshalConfig marshals Config into ConfigRaw JSON.
func (n *WorkflowNode) MarshalConfig() error {
	b, err := json.Marshal(n.Config)
	if err != nil {
		return err
	}
	n.ConfigRaw = string(b)
	return nil
}

// WorkflowConnection is a directed edge in the workflow graph.
type WorkflowConnection struct {
	ID           string `json:"id" db:"id"`
	WorkflowID   string `json:"workflow_id" db:"workflow_id"`
	SourceNodeID string `json:"source_node_id" db:"source_node_id"`
	SourceHandle string `json:"source_handle" db:"source_handle"`
	TargetNodeID string `json:"target_node_id" db:"target_node_id"`
	TargetHandle string `json:"target_handle" db:"target_handle"`
	Position     int    `json:"position" db:"position"`
}

// WorkflowExecution is a single run of a workflow.
type WorkflowExecution struct {
	ID         string `json:"id" db:"id"`
	WorkflowID string `json:"workflow_id" db:"workflow_id"`
	// ProfileID is the owning workflow's profile — stamped at creation from
	// the workflow record, not the engine's own (single, possibly ambiguous
	// in a multi-profile daemon) profileID. Left empty, it silently falls
	// back to the profile_id column's SQL default ('default'), which made
	// every scheduled/triggered execution unreachable by GetExecutionDetail
	// (profile-scoped lookup) for any profile other than 'default'.
	ProfileID   string `json:"profile_id" db:"profile_id"`
	Status      string `json:"status" db:"status"` // QUEUED, RUNNING, WAITING, SUCCESS, SUCCESS_WITH_ERRORS, FAILED, REJECTED, CANCELLED
	TriggerType string `json:"trigger_type" db:"trigger_type"`
	// TriggerNodeID is the trigger node that fired this run, persisted so an
	// adopted, resumed or retried run still fires only that node's branch.
	// Empty means every trigger node fires (manual runs).
	TriggerNodeID  string                 `json:"trigger_node_id,omitempty" db:"trigger_node_id"`
	TriggerData    map[string]interface{} `json:"trigger_data" db:"-"`
	TriggerDataRaw string                 `json:"-" db:"trigger_data"`
	StartedAt      *time.Time             `json:"started_at" db:"started_at"`
	FinishedAt     *time.Time             `json:"finished_at" db:"finished_at"`
	ErrorMessage   string                 `json:"error_message" db:"error_message"`
	CreatedAt      time.Time              `json:"created_at" db:"created_at"`
	PID            int                    `json:"pid" db:"pid"`
	// ResumeState is the serialized in-flight state of a paused (WAITING)
	// execution — enough for RunExecution to skip already-completed nodes and
	// continue from the pause point on resume. Empty for normal runs.
	ResumeState string `json:"-" db:"resume_state"`
	// ErrorClass is the class of the run's first failure (see ErrorClass).
	ErrorClass string `json:"error_class,omitempty" db:"error_class"`
	// Populated on load
	Nodes []WorkflowExecutionNode `json:"nodes,omitempty"`
}

// ParseTriggerData unmarshals TriggerDataRaw JSON into TriggerData.
func (e *WorkflowExecution) ParseTriggerData() error {
	if e.TriggerDataRaw == "" {
		e.TriggerData = make(map[string]interface{})
		return nil
	}
	return json.Unmarshal([]byte(e.TriggerDataRaw), &e.TriggerData)
}

// MarshalTriggerData marshals TriggerData into TriggerDataRaw JSON.
func (e *WorkflowExecution) MarshalTriggerData() error {
	b, err := json.Marshal(e.TriggerData)
	if err != nil {
		return err
	}
	e.TriggerDataRaw = string(b)
	return nil
}

// WorkflowExecutionNode is the per-node run record within an execution.
type WorkflowExecutionNode struct {
	ID           string     `json:"id" db:"id"`
	ExecutionID  string     `json:"execution_id" db:"execution_id"`
	NodeID       string     `json:"node_id" db:"node_id"`
	NodeName     string     `json:"node_name" db:"node_name"`
	Status       string     `json:"status" db:"status"` // PENDING, RUNNING, SUCCESS, FAILED, SKIPPED
	InputItems   []Item     `json:"input_items" db:"-"`
	InputRaw     string     `json:"-" db:"input_items"`
	OutputItems  []Item     `json:"output_items" db:"-"`
	OutputRaw    string     `json:"-" db:"output_items"`
	ErrorMessage string     `json:"error_message" db:"error_message"`
	StartedAt    *time.Time `json:"started_at" db:"started_at"`
	FinishedAt   *time.Time `json:"finished_at" db:"finished_at"`
	RetryCount   int        `json:"retry_count" db:"retry_count"`

	// ErrorClass and ErrorHint classify a FAILED node (see ErrorClass).
	ErrorClass string `json:"error_class,omitempty" db:"error_class"`
	ErrorHint  string `json:"error_hint,omitempty" db:"error_hint"`
}

// ParseItems unmarshals InputRaw and OutputRaw JSON into InputItems and OutputItems.
func (en *WorkflowExecutionNode) ParseItems() error {
	if en.InputRaw != "" {
		if err := json.Unmarshal([]byte(en.InputRaw), &en.InputItems); err != nil {
			return err
		}
	}
	if en.OutputRaw != "" {
		if err := json.Unmarshal([]byte(en.OutputRaw), &en.OutputItems); err != nil {
			return err
		}
	}
	return nil
}

// MarshalItems marshals InputItems and OutputItems into InputRaw and OutputRaw JSON.
func (en *WorkflowExecutionNode) MarshalItems() error {
	ib, err := json.Marshal(en.InputItems)
	if err != nil {
		return err
	}
	en.InputRaw = string(ib)

	ob, err := json.Marshal(en.OutputItems)
	if err != nil {
		return err
	}
	en.OutputRaw = string(ob)
	return nil
}

// Credential is a stored secret set for use by nodes.
type Credential struct {
	ID        string                 `json:"id" db:"id"`
	Name      string                 `json:"name" db:"name"`
	Type      string                 `json:"type" db:"type"`
	Data      map[string]interface{} `json:"data" db:"-"`
	DataRaw   string                 `json:"-" db:"data"`
	CreatedAt time.Time              `json:"created_at" db:"created_at"`
	UpdatedAt time.Time              `json:"updated_at" db:"updated_at"`
}

// RetryPolicy defines how a node is retried on failure.
type RetryPolicy struct {
	MaxRetries   int     `json:"max_retries"`   // 0 = no retry, max 10
	BackoffType  string  `json:"backoff_type"`  // "fixed" or "exponential"
	InitialDelay float64 `json:"initial_delay"` // seconds, 1–3600
}

// NodeConfig is the parsed config for a workflow node including retry and error handling.
type NodeConfig struct {
	RetryPolicy    RetryPolicy            `json:"retry_policy"`
	OnError        string                 `json:"on_error"` // "stop", "continue", "error_branch"
	ContinueOnFail bool                   `json:"continue_on_fail"`
	CredentialID   string                 `json:"credential_id"`
	Params         map[string]interface{} `json:"params"`
}

// ExpressionContext is passed to the expression engine for a node execution.
type ExpressionContext struct {
	JSON        map[string]interface{} // current item's JSON
	Node        map[string][]Item      // all node outputs indexed by name
	WorkflowID  string
	ExecutionID string
	Env         map[string]string // env var access

	// Optional — callers may leave these unset.
	Trigger  map[string]interface{} // $trigger: the execution's trigger data
	Vars     map[string]interface{} // $vars: workflow variables (never secrets)
	RunIndex int                    // $runIndex: loop iteration, 0 outside a loop
	PrevNode string                 // $prevNode: name of the node that fed this one
	Location *time.Location         // zone of $now/$today; nil means UTC

	base *exprBase // cached derived data; see Prepared
}

// ValidateConnectionsAgainstNodes checks that every connection's source and
// target node id is present in nodes, returning ErrDanglingConnection
// (naming the connection and the missing id) for the first mismatch found.
//
// This is purely in-memory — no store access — so a caller can (and should)
// run it before ever calling CreateWorkflow/SaveWorkflowNodes/
// SaveWorkflowConnections: catching a dangling reference here means the
// operation touches nothing at all, rather than partially persisting state
// that SaveWorkflowConnections's own (store-level, defense-in-depth) check
// would then have to unwind.
func ValidateConnectionsAgainstNodes(nodes []WorkflowNode, conns []WorkflowConnection) error {
	ids := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		ids[n.ID] = true
	}
	for _, c := range conns {
		if !ids[c.SourceNodeID] {
			return fmt.Errorf("%w: connection %q references unknown source node %q", ErrDanglingConnection, c.ID, c.SourceNodeID)
		}
		if !ids[c.TargetNodeID] {
			return fmt.Errorf("%w: connection %q references unknown target node %q", ErrDanglingConnection, c.ID, c.TargetNodeID)
		}
	}
	return nil
}
