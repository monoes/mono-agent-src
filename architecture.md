# Mono Agent — Workflow System Architecture

> Historical design record (2026-03-10). Paths and Go version below are from that plan and no longer match the code; current guidance is in AGENTS.md.

**Version:** 1.0
**Date:** 2026-03-10
**Module:** `github.com/monoes/mono-agent`
**Go Version:** 1.26 (go.mod; the plan was written for 1.24)

---

## Table of Contents

1. [Package Structure](#1-package-structure)
2. [Core Interfaces](#2-core-interfaces)
3. [Workflow Engine Design](#3-workflow-engine-design)
4. [Expression Engine](#4-expression-engine)
5. [Trigger System](#5-trigger-system)
6. [Job Queue Design](#6-job-queue-design)
7. [Node Implementation Pattern](#7-node-implementation-pattern)
8. [Credentials System](#8-credentials-system)
9. [Database Migration](#9-database-migration)
10. [New Go Dependencies](#10-new-go-dependencies)
11. [Implementation Wave Plan](#11-implementation-wave-plan)

---

## 1. Package Structure

The workflow system adds two new top-level packages under `internal/`, plus extensions to the Wails app layer. All existing packages remain unchanged.

```
github.com/monoes/mono-agent/
├── cmd/monoes/                        (unchanged)
├── data/                              (unchanged — embed.go, actions/)
├── internal/
│   ├── action/                        (unchanged)
│   ├── algorithms/                    (unchanged)
│   ├── auth/                          (unchanged)
│   ├── bot/                           (unchanged base; new sub-packages below)
│   │   ├── adapter.go                 (unchanged)
│   │   ├── browser.go                 (unchanged)
│   │   ├── humanize.go                (unchanged)
│   │   ├── instagram/                 (unchanged)
│   │   ├── linkedin/
│   │   │   ├── bot.go                 (unchanged — extends GetMethodByName)
│   │   │   ├── linkedin.go            (unchanged)
│   │   │   └── actions.go             (NEW — Tier 1 methods: LinkedInKeywordSearch,
│   │   │                               LinkedInProfileInteraction, LinkedInPublishContent)
│   │   ├── tiktok/
│   │   │   ├── bot.go                 (extends GetMethodByName)
│   │   │   ├── tiktok.go              (unchanged)
│   │   │   └── actions.go             (NEW — TikTokKeywordSearch, TikTokProfileFetch,
│   │   │                               TikTokProfileInteraction, TikTokPublishContent)
│   │   ├── x/
│   │   │   ├── bot.go                 (extends GetMethodByName)
│   │   │   ├── x.go                   (unchanged)
│   │   │   └── actions.go             (NEW — XKeywordSearch, XProfileFetch,
│   │   │                               XProfileInteraction, XPublishContent)
│   │   ├── email/                     (unchanged)
│   │   └── telegram/                  (unchanged)
│   ├── config/                        (unchanged — schemas.go gains new schema keys)
│   ├── scheduler/
│   │   └── scheduler.go               (extends: adds ScheduleWorkflow method)
│   ├── storage/                       (unchanged — migration file added)
│   ├── util/                          (unchanged)
│   │
│   ├── workflow/                      (NEW — workflow engine)
│   │   ├── engine.go                  — WorkflowEngine: orchestrates all execution
│   │   ├── dag.go                     — DAG topological sort (Kahn's algorithm), cycle detection
│   │   ├── execution.go               — WorkflowExecution struct, state machine, run loop
│   │   ├── expression.go              — ExpressionEngine: text/template + FuncMap
│   │   ├── queue.go                   — buffered channel job queue + worker pool
│   │   ├── registry.go                — NodeTypeRegistry: maps type string → NodeExecutor factory
│   │   ├── storage.go                 — WorkflowStore interface + SQLite implementation
│   │   ├── trigger_manager.go         — TriggerManager: registers/deregisters all trigger types
│   │   ├── webhook_server.go          — standalone net/http webhook server
│   │   ├── models.go                  — all workflow domain structs (Workflow, WorkflowNode, etc.)
│   │   ├── validator.go               — SaveWorkflow and ActivateWorkflow validation logic
│   │   └── errors.go                  — sentinel errors
│   │
│   └── nodes/                         (NEW — non-browser NodeExecutor implementations)
│       ├── browser_adapter.go         — BrowserNode: wraps ActionExecutor → NodeExecutor
│       ├── control/
│       │   ├── if.go                  — core.if
│       │   ├── switch.go              — core.switch
│       │   ├── merge.go               — core.merge
│       │   ├── split_in_batches.go    — core.split_in_batches
│       │   ├── wait.go                — core.wait
│       │   ├── stop_error.go          — core.stop_error
│       │   ├── set.go                 — core.set
│       │   ├── code.go                — core.code (goja JS engine)
│       │   ├── filter.go              — core.filter
│       │   ├── sort.go                — core.sort
│       │   ├── limit.go               — core.limit
│       │   ├── remove_duplicates.go   — core.remove_duplicates
│       │   ├── compare_datasets.go    — core.compare_datasets
│       │   └── aggregate.go           — core.aggregate
│       ├── data/
│       │   ├── datetime.go            — data.datetime
│       │   ├── crypto.go              — data.crypto (stdlib crypto/*)
│       │   ├── html.go                — data.html (goquery)
│       │   ├── xml.go                 — data.xml (encoding/xml)
│       │   ├── markdown.go            — data.markdown (goldmark)
│       │   ├── spreadsheet.go         — data.spreadsheet (encoding/csv, internal/xlsx)
│       │   ├── compression.go         — data.compression (compress/gzip, archive/zip)
│       │   └── write_binary_file.go   — data.write_binary_file
│       ├── http/
│       │   ├── request.go             — http.request
│       │   ├── ftp.go                 — http.ftp (jlaffaye/ftp)
│       │   └── ssh.go                 — http.ssh (golang.org/x/crypto/ssh)
│       ├── system/
│       │   ├── execute_command.go     — system.execute_command
│       │   └── rss_read.go            — system.rss_read (mmcdole/gofeed)
│       ├── db/
│       │   ├── mysql.go               — db.mysql (go-mysql-org/go-mysql)
│       │   ├── postgres.go            — db.postgres (lib/pq)
│       │   ├── mongodb.go             — db.mongodb (mongo-driver)
│       │   └── redis.go               — db.redis (go-redis/redis)
│       ├── comm/
│       │   ├── email_send.go          — comm.email_send (net/smtp)
│       │   ├── email_read.go          — comm.email_read (emersion/go-imap)
│       │   ├── slack.go               — comm.slack (slack-go/slack)
│       │   ├── telegram.go            — comm.telegram (go-telegram-bot-api)
│       │   ├── discord.go             — comm.discord (bwmarrin/discordgo)
│       │   ├── twilio.go              — comm.twilio (net/http REST)
│       │   └── whatsapp.go            — comm.whatsapp (net/http REST)
│       └── service/
│           ├── github.go              — service.github
│           ├── airtable.go            — service.airtable
│           ├── notion.go              — service.notion
│           ├── jira.go                — service.jira
│           ├── linear.go              — service.linear
│           ├── asana.go               — service.asana
│           ├── stripe.go              — service.stripe
│           ├── shopify.go             — service.shopify
│           ├── salesforce.go          — service.salesforce
│           ├── hubspot.go             — service.hubspot
│           ├── google_sheets.go       — service.google_sheets
│           ├── gmail.go               — service.gmail
│           └── google_drive.go        — service.google_drive
│
├── data/
│   ├── embed.go                       (unchanged)
│   ├── migrations/
│   │   ├── 001_initial.sql            (unchanged)
│   │   ├── ...
│   │   └── 006_workflow_system.sql    (NEW)
│   └── actions/
│       ├── instagram/                 (unchanged)
│       ├── linkedin/
│       │   ├── KEYWORD_SEARCH.json    (NEW)
│       │   ├── PROFILE_INTERACTION.json (NEW)
│       │   └── PUBLISH_CONTENT.json   (NEW)
│       ├── tiktok/
│       │   ├── KEYWORD_SEARCH.json    (NEW)
│       │   ├── PROFILE_FETCH.json     (NEW)
│       │   ├── PROFILE_INTERACTION.json (NEW)
│       │   └── PUBLISH_CONTENT.json   (NEW)
│       └── x/
│           ├── KEYWORD_SEARCH.json    (NEW)
│           ├── PROFILE_FETCH.json     (NEW)
│           ├── PROFILE_INTERACTION.json (NEW)
│           └── PUBLISH_CONTENT.json   (NEW)
│
└── wails-app/
    ├── app.go                         (unchanged)
    ├── workflow.go                    (NEW — Wails bound methods for workflow management)
    ├── main.go                        (extend: start WorkflowEngine + WebhookServer on boot)
    └── frontend/
        └── src/
            ├── pages/
            │   ├── Workflows.jsx      (NEW)
            │   └── WorkflowCanvas.jsx (NEW)
            └── components/
                ├── nodes/             (NEW — per-node-type config panels)
                └── Sidebar.jsx        (extend: add Workflows nav item)
```

---

## 2. Core Interfaces

All interfaces below live in `internal/workflow/` unless otherwise stated.

### 2.1 Item

```go
// Item is the fundamental unit of data flowing between nodes.
// It is an alias, not a named type, to avoid unnecessary wrapping.
// An Item carries arbitrary key-value pairs.
type Item = map[string]interface{}
```

### 2.2 NodeExecutor

Every node type — browser action, control node, data transform, HTTP call, etc. — implements `NodeExecutor`. The engine calls `Execute` once per node activation during a workflow run.

```go
// File: internal/workflow/models.go

// NodeExecutor is the single interface all node types must implement.
type NodeExecutor interface {
    // Execute receives the full set of input items from the upstream node
    // (all items on one handle), the resolved node config (with expressions
    // already evaluated by the engine before this call), and a context for
    // cancellation/timeout.
    //
    // It returns zero or more NodeOutput values — one per output handle that
    // should emit data. Returning an empty slice means the node emits nothing
    // (terminal node). Returning an error with no outputs means the node
    // failed; the engine applies the on_error policy.
    Execute(ctx context.Context, input NodeInput, config map[string]interface{}) ([]NodeOutput, error)
}

// NodeInput is what the engine passes into Execute.
type NodeInput struct {
    Items    []Item // items from the upstream node's output handle
    SourceID string // UUID of the source node
}

// NodeOutput is what a node returns for one output handle.
type NodeOutput struct {
    Handle string // "main" | "true" | "false" | "error" | "done" | "loop_item" | custom
    Items  []Item // items to pass to nodes connected on this handle
}

// NodeError carries structured failure information.
type NodeError struct {
    Message   string
    NodeID    string
    NodeName  string
    Timestamp time.Time
}
```

### 2.3 WorkflowStore

The storage abstraction used by the engine. The SQLite implementation lives in `internal/workflow/storage.go`.

```go
// File: internal/workflow/storage.go

type WorkflowStore interface {
    // Workflow CRUD
    CreateWorkflow(ctx context.Context, w *Workflow) error
    GetWorkflow(ctx context.Context, id string) (*Workflow, error)
    ListWorkflows(ctx context.Context) ([]*Workflow, error)
    UpdateWorkflow(ctx context.Context, w *Workflow) error
    DeleteWorkflow(ctx context.Context, id string) error

    // Node + connection bulk save (transactional)
    SaveWorkflowGraph(ctx context.Context, workflowID string, nodes []*WorkflowNode, conns []*WorkflowConnection) error

    // Nodes
    ListNodes(ctx context.Context, workflowID string) ([]*WorkflowNode, error)

    // Connections
    ListConnections(ctx context.Context, workflowID string) ([]*WorkflowConnection, error)

    // Trigger cache
    UpsertTrigger(ctx context.Context, t *WorkflowTrigger) error
    DeleteTriggers(ctx context.Context, workflowID string) error
    ListActiveTriggers(ctx context.Context) ([]*WorkflowTrigger, error)

    // Executions
    CreateExecution(ctx context.Context, e *WorkflowExecution) error
    GetExecution(ctx context.Context, id string) (*WorkflowExecution, error)
    UpdateExecution(ctx context.Context, e *WorkflowExecution) error
    ListExecutions(ctx context.Context, workflowID string, limit, offset int) ([]*WorkflowExecution, error)
    RecoverStaleExecutions(ctx context.Context) error // sets RUNNING → FAILED on startup

    // Execution node records
    CreateNodeRun(ctx context.Context, r *WorkflowExecutionNode) error
    UpdateNodeRun(ctx context.Context, r *WorkflowExecutionNode) error
    ListNodeRuns(ctx context.Context, executionID string) ([]*WorkflowExecutionNode, error)

    // Credentials
    CreateCredential(ctx context.Context, c *Credential) error
    GetCredential(ctx context.Context, id string) (*Credential, error)
    ListCredentials(ctx context.Context, nodeType string) ([]*Credential, error)
    UpdateCredential(ctx context.Context, c *Credential) error
    DeleteCredential(ctx context.Context, id string) error

    // Settings
    GetSetting(ctx context.Context, key string) (string, error)
}
```

### 2.4 WorkflowRunner

The public API surface consumed by the Wails layer and the trigger system.

```go
// File: internal/workflow/engine.go

type WorkflowRunner interface {
    // Lifecycle
    Start(ctx context.Context) error  // starts worker pool + webhook server + re-registers triggers
    Stop()                            // drains queue, shuts down webhook server

    // Workflow management
    ActivateWorkflow(ctx context.Context, workflowID string) error
    DeactivateWorkflow(ctx context.Context, workflowID string) error

    // Execution
    TriggerWorkflow(ctx context.Context, workflowID, triggerType string, inputData Item) (string, error)
    CancelExecution(ctx context.Context, executionID string) error
    RetryExecution(ctx context.Context, executionID string) (string, error)

    // Query
    GetExecutionStatus(ctx context.Context, executionID string) (*WorkflowExecution, error)
}
```

### 2.5 TriggerProvider

Each trigger type implements this interface. The `TriggerManager` holds a registry of providers.

```go
// File: internal/workflow/trigger_manager.go

type TriggerProvider interface {
    // TriggerType returns the string constant this provider handles,
    // e.g. "trigger.schedule", "trigger.webhook".
    TriggerType() string

    // Register activates this trigger for the given workflow node.
    // Called during ActivateWorkflow.
    Register(ctx context.Context, workflowID string, node *WorkflowNode) error

    // Deregister removes the trigger for the given workflow.
    // Called during DeactivateWorkflow and DeleteWorkflow.
    Deregister(workflowID string) error

    // ReregisterAll re-registers all active triggers on startup,
    // reading from the workflow_triggers table.
    ReregisterAll(ctx context.Context, triggers []*WorkflowTrigger) error
}
```

### 2.6 Domain Models

```go
// File: internal/workflow/models.go

// Workflow is the top-level definition record.
type Workflow struct {
    ID              string
    Name            string
    Description     string
    IsActive        bool
    Version         int
    TimeoutSeconds  int
    ErrorWorkflowID string
    CreatedAt       time.Time
    UpdatedAt       time.Time
}

// WorkflowNode is a single node in the DAG.
type WorkflowNode struct {
    ID         string
    WorkflowID string
    NodeType   string
    Name       string
    Config     map[string]interface{} // deserialized from JSON blob
    PositionX  float64
    PositionY  float64
    Disabled   bool
    CreatedAt  time.Time
    UpdatedAt  time.Time
}

// WorkflowConnection is a directed edge from one node's output handle
// to another node's input handle.
type WorkflowConnection struct {
    ID           string
    WorkflowID   string
    SourceNodeID string
    SourceHandle string
    TargetNodeID string
    TargetHandle string
    Position     int
}

// WorkflowTrigger is the cached trigger registration for fast restart re-registration.
type WorkflowTrigger struct {
    ID          string
    WorkflowID  string
    NodeID      string
    TriggerType string
    Config      map[string]interface{}
    CreatedAt   time.Time
}

// WorkflowExecution is one run of a workflow.
type WorkflowExecution struct {
    ID          string
    WorkflowID  string
    TriggerType string
    Status      string // "RUNNING" | "COMPLETED" | "FAILED" | "CANCELLED"
    InputData   Item
    ErrorMsg    string
    StartedAt   time.Time
    FinishedAt  *time.Time
    cancelFunc  context.CancelFunc // unexported; held in memory only
}

// WorkflowExecutionNode is one node's run record within an execution.
type WorkflowExecutionNode struct {
    ID          string
    ExecutionID string
    NodeID      string
    NodeName    string
    NodeType    string
    RunIndex    int
    Status      string // "SUCCESS" | "FAILED" | "SKIPPED"
    InputItems  []Item
    OutputItems []Item
    ErrorMsg    string
    RetryCount  int
    StartedAt   time.Time
    FinishedAt  *time.Time
}

// ExpressionContext is the data available to the expression engine
// when evaluating a node's config fields.
type ExpressionContext struct {
    JSON      Item                        // current item fields ($json.*)
    Node      map[string]NodeOutputSnapshot // $node["Name"].json.*
    Workflow  WorkflowSnapshot
    Execution ExecutionSnapshot
}

type NodeOutputSnapshot struct {
    JSON Item // last output item from the named node
}

type WorkflowSnapshot struct {
    ID   string
    Name string
}

type ExecutionSnapshot struct {
    ID          string
    TriggerType string
}
```

---

## 3. Workflow Engine Design

### 3.1 Engine Struct

```go
// File: internal/workflow/engine.go

type WorkflowEngine struct {
    store          WorkflowStore
    queue          *ExecutionQueue
    registry       *NodeTypeRegistry
    triggerMgr     *TriggerManager
    webhookServer  *WebhookServer
    expr           *ExpressionEngine
    logger         zerolog.Logger
    cancelFuncs    sync.Map        // executionID → context.CancelFunc
    cancelFuncsMu  sync.RWMutex   // guards cancelFuncs for status-check operations
    settings       settingsReader
}
```

### 3.2 DAG Representation and Topological Sort

**File:** `internal/workflow/dag.go`

The DAG is built in-memory from `[]WorkflowNode` + `[]WorkflowConnection` at the start of each execution and at save time for validation. The structure:

```go
type DAG struct {
    Nodes    map[string]*WorkflowNode   // nodeID → node
    InEdges  map[string][]Edge          // nodeID → incoming edges
    OutEdges map[string][]Edge          // nodeID → outgoing edges
}

type Edge struct {
    SourceNodeID string
    SourceHandle string
    TargetNodeID string
    TargetHandle string
}

func BuildDAG(nodes []*WorkflowNode, conns []*WorkflowConnection) *DAG

// TopologicalSort performs Kahn's algorithm.
// Returns (ordered node IDs, nil) on success.
// Returns (nil, error) if a cycle is detected, with the cycle nodes named.
func (d *DAG) TopologicalSort() ([]string, error)

// TriggerNodes returns all nodes whose NodeType starts with "trigger.".
func (d *DAG) TriggerNodes() []*WorkflowNode

// Successors returns the outgoing edges from a given node and handle.
func (d *DAG) Successors(nodeID, handle string) []Edge

// Predecessors returns all edges pointing into a given node.
func (d *DAG) Predecessors(nodeID string) []Edge
```

**Kahn's Algorithm Implementation:**

```
1. Compute in-degree for every node.
2. Initialize queue Q with all nodes having in-degree == 0.
3. While Q is not empty:
   a. Pop node N from Q; append to sorted list.
   b. For each outgoing edge from N to M: decrement in-degree of M.
      If M's in-degree reaches 0, push M onto Q.
4. If sorted list length != total node count: cycle detected.
   Identify cycle nodes as those still with in-degree > 0.
```

### 3.3 Execution Algorithm

**File:** `internal/workflow/execution.go`

Each execution runs in a single goroutine dispatched from the worker pool. The goroutine executes the following algorithm:

```go
func (e *WorkflowEngine) runExecution(ctx context.Context, exec *WorkflowExecution) {
    // 1. Load workflow graph from DB.
    nodes, _ := e.store.ListNodes(ctx, exec.WorkflowID)
    conns, _ := e.store.ListConnections(ctx, exec.WorkflowID)
    dag := BuildDAG(nodes, conns)

    // 2. Build node output snapshot map (for $node["Name"] expressions).
    nodeOutputs := make(map[string]NodeOutputSnapshot) // keyed by node NAME

    // 3. Build the execution stack. Initial entry is the trigger node
    //    with the trigger's input data.
    type StackEntry struct {
        Node      *WorkflowNode
        InputItems []Item
    }
    stack := []StackEntry{{
        Node:      triggerNode,
        InputItems: []Item{exec.InputData},
    }}

    // 4. Main execution loop.
    for len(stack) > 0 {
        // Check cancellation.
        select {
        case <-ctx.Done():
            setFailed(exec, "execution cancelled")
            return
        default:
        }

        entry := stack[0]
        stack = stack[1:]   // pop from front (BFS order preserves topo sort)

        if entry.Node.Disabled {
            continue
        }

        // 5. Evaluate config expressions with current item context.
        resolvedConfig := e.expr.ResolveConfig(entry.Node.Config, entry.InputItems, nodeOutputs, exec)

        // 6. Retrieve the NodeExecutor for this node type.
        executor, _ := e.registry.Get(entry.Node.NodeType)

        // 7. Execute the node with retry policy.
        nodeInput := NodeInput{Items: entry.InputItems, SourceID: entry.Node.ID}
        outputs, err := e.executeWithRetry(ctx, executor, nodeInput, resolvedConfig, entry.Node)

        // 8. Persist node run record.
        e.persistNodeRun(ctx, exec.ID, entry.Node, nodeInput.Items, outputs, err)

        // 9. Update node output snapshot (for $node["Name"] usage).
        if len(outputs) > 0 && len(outputs[0].Items) > 0 {
            nodeOutputs[entry.Node.Name] = NodeOutputSnapshot{JSON: outputs[0].Items[0]}
        }

        // 10. Handle error per node on_error policy.
        if err != nil {
            onError := resolvedConfig["on_error"].(string) // "stop" | "continue" | "error_branch"
            switch onError {
            case "stop":
                setFailed(exec, err.Error())
                return
            case "continue":
                outputs = []NodeOutput{{Handle: "main", Items: entry.InputItems}}
            case "error_branch":
                errItem := Item{
                    "error":          err.Error(),
                    "failedNodeId":   entry.Node.ID,
                    "failedNodeName": entry.Node.Name,
                    "originalInput":  entry.InputItems,
                }
                outputs = []NodeOutput{{Handle: "error", Items: []Item{errItem}}}
            }
        }

        // 11. For each output handle, push connected nodes onto stack.
        for _, out := range outputs {
            successors := dag.Successors(entry.Node.ID, out.Handle)
            for _, edge := range successors {
                targetNode := dag.Nodes[edge.TargetNodeID]
                stack = append(stack, StackEntry{
                    Node:       targetNode,
                    InputItems: out.Items,
                })
            }
        }
    }

    setCompleted(exec)
}
```

### 3.4 Parallel Branch Execution

**When to parallelize:** Branches that diverge from an IF node, SWITCH node, or any node with multiple outgoing handles are pushed onto the stack sequentially in the main goroutine's BFS loop. They execute sequentially in v1.

**True parallelism (for MERGE node):** The `control.merge` node requires waiting for multiple upstream branches. The engine handles this with a per-execution merge state map:

```go
// Inside WorkflowEngine
mergeState sync.Map // executionID+"#"+nodeID → *mergeAccumulator

type mergeAccumulator struct {
    mu       sync.Mutex
    expected int          // number of incoming connections to this merge node
    received int
    branches [][]Item
    ready    chan struct{} // closed when received == expected
}
```

When a node pushes output to a MERGE node's input handle:
1. The engine increments `received` in the accumulator.
2. If `received < expected`, the push is stored but the MERGE node is NOT added to the execution stack.
3. When `received == expected`, the MERGE node IS added to the stack with all branch data combined.

This avoids spawning goroutines for branches unless they are IO-bound. For workflows that have explicit parallelism needs, the merge accumulator pattern handles the synchronization without requiring a goroutine per branch.

**Goroutine parallelism for browser actions:** Browser action nodes are inherently slow (seconds to minutes). The execution model keeps each workflow execution in one goroutine. Concurrent workflows use the worker pool (section 6). Do NOT parallelize branches within a single workflow execution in v1.

### 3.5 Retry Policy

**File:** `internal/workflow/execution.go`

```go
func (e *WorkflowEngine) executeWithRetry(
    ctx context.Context,
    executor NodeExecutor,
    input NodeInput,
    config map[string]interface{},
    node *WorkflowNode,
) ([]NodeOutput, error) {
    policy := extractRetryPolicy(config) // maxRetries, backoff, initialDelay, maxDelay
    var lastErr error

    for attempt := 0; attempt <= policy.MaxRetries; attempt++ {
        if attempt > 0 {
            delay := computeDelay(policy, attempt)
            select {
            case <-ctx.Done():
                return nil, ctx.Err()
            case <-time.After(delay):
            }
        }

        outputs, err := executor.Execute(ctx, input, config)
        if err == nil {
            return outputs, nil
        }
        lastErr = err
        e.logger.Warn().
            Err(err).
            Str("node_id", node.ID).
            Int("attempt", attempt+1).
            Int("max_retries", policy.MaxRetries).
            Msg("node execution failed, will retry")
    }
    return nil, lastErr
}

// computeDelay calculates the wait time before retry attempt N (1-indexed).
func computeDelay(p RetryPolicy, attempt int) time.Duration {
    if p.Backoff == "exponential" {
        d := p.InitialDelay * time.Duration(math.Pow(2, float64(attempt-1)))
        if d > p.MaxDelay {
            d = p.MaxDelay
        }
        return d
    }
    // fixed
    return p.InitialDelay
}
```

---

## 4. Expression Engine

**File:** `internal/workflow/expression.go`

### 4.1 Design

The expression engine uses Go's `text/template` package with a custom `template.FuncMap`. Expressions appear inside `{{ }}` delimiters in node config string fields.

The engine does NOT evaluate the entire config as a template document. Instead, it scans each string value in the config map for `{{...}}` patterns and evaluates them individually using `template.Execute` with the `ExpressionContext` as the data object.

### 4.2 ExpressionEngine Struct

```go
type ExpressionEngine struct {
    logger zerolog.Logger
}

func NewExpressionEngine(logger zerolog.Logger) *ExpressionEngine

// ResolveConfig walks every string field in the config map recursively
// and evaluates any {{ }} expressions found. Non-string values are left
// unchanged. Returns a new config map; the original is not mutated.
func (e *ExpressionEngine) ResolveConfig(
    config map[string]interface{},
    items []Item,
    nodeOutputs map[string]NodeOutputSnapshot,
    exec *WorkflowExecution,
) map[string]interface{}

// EvaluateString evaluates a single string that may contain {{ }} expressions.
// Returns the evaluated string. On template parse or execute error, returns
// empty string "" and logs a warning.
func (e *ExpressionEngine) EvaluateString(
    tmplStr string,
    ctx ExpressionContext,
) string

// EvaluateBool evaluates a string expression and parses the result as bool.
// "true", "1", "yes" → true; anything else → false.
func (e *ExpressionEngine) EvaluateBool(tmplStr string, ctx ExpressionContext) bool
```

### 4.3 Template Data Binding

The `ExpressionContext` is passed as the template `.` (dot) data. However, the expressions use `$json`, `$node`, `$workflow`, `$execution` variable names. This is achieved by defining these as template variables in a wrapper template:

```go
const wrapperPrefix = `
{{- $json := .JSON -}}
{{- $node := .Node -}}
{{- $workflow := .Workflow -}}
{{- $execution := .Execution -}}
`

func (e *ExpressionEngine) buildTemplate(expr string) (*template.Template, error) {
    full := wrapperPrefix + expr
    return template.New("expr").Funcs(e.funcMap()).Parse(full)
}
```

### 4.4 Variable Access Mechanics

**`{{$json.fieldName}}`** — Works via standard template dot-notation. `$json` is `map[string]interface{}`, so `$json.fieldName` evaluates to `$json["fieldName"]` via the template engine's map indexing.

**`{{$json.nested.field}}`** — Also works natively: template engine traverses `map[string]interface{}` chains. If any intermediate key is missing, the template returns `<no value>`, which the engine converts to `""`.

**`{{$node["NodeName"].json.field}}`** — `$node` is `map[string]NodeOutputSnapshot`. Template map indexing with string key returns the `NodeOutputSnapshot` struct. `.json` accesses the struct field `JSON` (which must be exported). `.field` then accesses the map. The struct field must be tagged or exported as `JSON`:

```go
type NodeOutputSnapshot struct {
    JSON Item `template:"json"` // accessed as .JSON or .json via FuncMap
}
```

Because Go templates access exported struct fields by name (case-sensitive), `$node["NodeName"].JSON.field` works natively. To support lowercase `.json`, register a template function:

```go
// In FuncMap: "nodeOutput" provides $node["Name"].json access
"nodeJSON": func(snapshots map[string]NodeOutputSnapshot, name string) Item {
    if s, ok := snapshots[name]; ok {
        return s.JSON
    }
    return Item{}
},
```

Then the expression `{{(index $node "NodeName").JSON.fieldName}}` works natively. For the n8n-style `$node["Name"].json.field` syntax, document that in this system the canonical form is `{{(index $node "NodeName").JSON.fieldName}}`.

**`{{$workflow.id}}`** — `$workflow` is `WorkflowSnapshot{ID, Name}`. Template accesses `.ID` field.

**`{{$execution.id}}`** — `$execution` is `ExecutionSnapshot{ID, TriggerType}`.

### 4.5 FuncMap — Permitted Functions Only

```go
func (e *ExpressionEngine) funcMap() template.FuncMap {
    return template.FuncMap{
        // Standard template builtins are included automatically:
        // len, index, print, printf, println, html, urlquery,
        // eq, ne, lt, gt, le, ge, and, or, not, call

        // String operations
        "upper":      strings.ToUpper,
        "lower":      strings.ToLower,
        "trim":       strings.TrimSpace,
        "trimPrefix": strings.TrimPrefix,
        "trimSuffix": strings.TrimSuffix,
        "contains":   strings.Contains,
        "hasPrefix":  strings.HasPrefix,
        "hasSuffix":  strings.HasSuffix,
        "replace":    strings.ReplaceAll,
        "split":      strings.Split,
        "join":       strings.Join,
        "sprintf":    fmt.Sprintf,

        // Math
        "add":   func(a, b float64) float64 { return a + b },
        "sub":   func(a, b float64) float64 { return a - b },
        "mul":   func(a, b float64) float64 { return a * b },
        "div":   func(a, b float64) float64 { return a / b },
        "mod":   func(a, b int) int { return a % b },
        "floor": math.Floor,
        "ceil":  math.Ceil,
        "abs":   math.Abs,

        // Date/time
        "now":        func() time.Time { return time.Now().UTC() },
        "formatTime": func(t time.Time, layout string) string { return t.Format(layout) },
        "parseTime":  func(layout, value string) time.Time { t, _ := time.Parse(layout, value); return t },

        // Type conversion
        "toString": fmt.Sprint,
        "toInt":    func(v interface{}) int { /* strconv.Atoi or cast */ },
        "toFloat":  func(v interface{}) float64 { /* strconv.ParseFloat or cast */ },
        "toBool":   func(v interface{}) bool { /* cast */ },

        // Environment (read-only)
        "env": os.Getenv,

        // Map/slice helpers
        "keys":    func(m map[string]interface{}) []string { /* extract keys */ },
        "values":  func(m map[string]interface{}) []interface{} { /* extract values */ },
        "default": func(def, val interface{}) interface{} { if val == nil { return def }; return val },

        // EXPLICITLY NOT INCLUDED:
        // os.Exec, exec.Command, net/http, os.Open, os.Create — no IO or OS exec
    }
}
```

### 4.6 Missing Field Behavior

When a template expression accesses a missing map key, Go's `text/template` renders `<no value>` by default. The engine sets `template.Option("missingkey=zero")` on every template, which renders missing keys as the zero value of the type (empty string for `interface{}`). This prevents noisy `<no value>` output.

```go
tmpl, _ := template.New("expr").
    Option("missingkey=zero").
    Funcs(e.funcMap()).
    Parse(full)
```

---

## 5. Trigger System

**File:** `internal/workflow/trigger_manager.go`

### 5.1 TriggerManager

```go
type TriggerManager struct {
    providers map[string]TriggerProvider  // trigger type → provider
    store     WorkflowStore
    engine    WorkflowRunner
    logger    zerolog.Logger
}

func NewTriggerManager(store WorkflowStore, engine WorkflowRunner, logger zerolog.Logger) *TriggerManager

// Register calls the appropriate TriggerProvider.Register for every trigger
// node in the workflow. Called by ActivateWorkflow.
func (tm *TriggerManager) RegisterWorkflow(ctx context.Context, workflow *Workflow,
    nodes []*WorkflowNode) error

// Deregister removes all trigger registrations for a workflow.
func (tm *TriggerManager) DeregisterWorkflow(workflowID string) error

// ReregisterAll is called on startup to restore all active trigger registrations.
func (tm *TriggerManager) ReregisterAll(ctx context.Context) error
```

### 5.2 Manual Trigger

**Provider:** `ManualTriggerProvider`

- `TriggerType()` returns `"trigger.manual"`.
- `Register`: no-op — manual triggers do not require registration. The trigger fires only when `WorkflowRunner.TriggerWorkflow` is called directly.
- `Deregister`: no-op.
- `ReregisterAll`: no-op.

**Trigger data construction:**

```go
func buildManualTriggerData(inputData Item, sampleData Item) Item {
    merged := make(Item)
    for k, v := range sampleData {
        merged[k] = v
    }
    for k, v := range inputData { // caller-supplied overrides sample_data
        merged[k] = v
    }
    merged["triggeredBy"] = "manual"
    merged["timestamp"] = time.Now().UTC().Format(time.RFC3339)
    return merged
}
```

### 5.3 Schedule Trigger

**Provider:** `ScheduleTriggerProvider`

```go
type ScheduleTriggerProvider struct {
    scheduler *scheduler.Scheduler  // existing robfig/cron wrapper
    engine    WorkflowRunner
    jobs      map[string]string     // workflowID → cronExpr (for deregistration)
    mu        sync.Mutex
    logger    zerolog.Logger
}
```

- `TriggerType()` returns `"trigger.schedule"`.
- `Register(ctx, workflowID, node)`:
  1. Parse `node.Config["cron_expression"].(string)`. If empty or invalid → error.
  2. Parse `node.Config["timezone"].(string)`. If non-empty, call `time.LoadLocation(tz)`. If fails → error.
  3. Build a 5-field cron expression (no seconds). The existing `scheduler.Scheduler` uses `cron.WithSeconds()` — for workflow triggers, prepend `"0 "` to the 5-field expression to make it 6-field and consistent with the scheduler's parser.
  4. Call `scheduler.ScheduleWorkflow(workflowID, cronExpr, timezone)` — a new method added to the existing `Scheduler` struct that calls `engine.TriggerWorkflow` instead of `ActionExecutor.RunSingle`.
  5. Store `workflowID → entryID` in a local map.
- `Deregister(workflowID)`: removes the cron entry.
- `ReregisterAll(ctx, triggers)`: iterates the trigger list and calls `Register` for each schedule trigger.

**New method on existing Scheduler (non-breaking addition):**

```go
// ScheduleWorkflow registers a workflow trigger cron job.
// cronExpr must be a 6-field expression (with leading seconds field).
// timezone is an IANA timezone name; empty string means UTC.
func (s *Scheduler) ScheduleWorkflow(
    workflowID string,
    cronExpr string,
    timezone string,
    runner WorkflowRunner,
    logger zerolog.Logger,
) error
```

This method is added to `internal/scheduler/scheduler.go` and calls `runner.TriggerWorkflow` in the cron callback.

**Trigger data:**

```go
Item{
    "triggeredBy":    "schedule",
    "timestamp":      time.Now().UTC().Format(time.RFC3339),
    "cronExpression": cronExpr,
}
```

### 5.4 Webhook Trigger

**File:** `internal/workflow/webhook_server.go`

```go
type WebhookServer struct {
    port     int
    server   *http.Server
    routes   sync.Map   // path_suffix (string) → webhookRoute
    engine   WorkflowRunner
    logger   zerolog.Logger
}

type webhookRoute struct {
    WorkflowID    string
    NodeID        string
    RequireAuth   bool
    WebhookSecret string
    HTTPMethod    string
}

func NewWebhookServer(port int, engine WorkflowRunner, logger zerolog.Logger) *WebhookServer

func (ws *WebhookServer) Start() error   // binds to 127.0.0.1:<port>, runs in goroutine
func (ws *WebhookServer) Stop(ctx context.Context) error

// Register stores a route for the given workflow webhook node config.
func (ws *WebhookServer) Register(workflowID, nodeID string, config map[string]interface{}) error

// Deregister removes the route for the given workflow.
func (ws *WebhookServer) Deregister(workflowID string)
```

**Request handler flow:**

```go
func (ws *WebhookServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    pathSuffix := strings.TrimPrefix(r.URL.Path, "/webhook/")
    route, ok := ws.routes.Load(pathSuffix)
    if !ok {
        writeJSON(w, 404, map[string]string{"error": "webhook not found"})
        return
    }
    wh := route.(webhookRoute)

    switch r.Method {
    case http.MethodOptions:
        w.Header().Set("Access-Control-Allow-Origin", "*")
        w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
        w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Monoes-Token")
        w.WriteHeader(http.StatusNoContent)
        return

    case http.MethodGet:
        writeJSON(w, 200, map[string]string{"status": "active", "workflowId": wh.WorkflowID})
        return

    case http.MethodPost:
        // Auth check
        if wh.RequireAuth {
            token := r.Header.Get("X-Monoes-Token")
            if !validateHMAC(r, token, wh.WebhookSecret) {
                writeJSON(w, 401, map[string]string{"error": "invalid token"})
                return
            }
        }

        // Body parsing (max 1MB)
        body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
        if err != nil || int64(len(body)) >= 1<<20 {
            writeJSON(w, 413, map[string]string{"error": "request too large"})
            return
        }

        var parsedBody interface{}
        if err := json.Unmarshal(body, &parsedBody); err != nil {
            parsedBody = map[string]interface{}{"rawBody": string(body)}
        }

        // Build headers map
        headers := make(map[string]interface{})
        for k, vs := range r.Header {
            headers[k] = strings.Join(vs, ", ")
        }

        triggerData := Item{
            "triggeredBy": "webhook",
            "timestamp":   time.Now().UTC().Format(time.RFC3339),
            "method":      r.Method,
            "headers":     headers,
            "body":        parsedBody,
        }

        execID, err := ws.engine.TriggerWorkflow(r.Context(), wh.WorkflowID, "webhook", triggerData)
        if err != nil {
            if errors.Is(err, ErrQueueFull) {
                writeJSON(w, 503, map[string]string{"error": "execution queue full"})
                return
            }
            writeJSON(w, 500, map[string]string{"error": err.Error()})
            return
        }
        writeJSON(w, 202, map[string]string{"executionId": execID})
    }
}
```

**HMAC validation:**

```go
func validateHMAC(r *http.Request, token, secret string) bool {
    // Read body for HMAC; body was already read into 'body' slice before this call.
    // The engine passes body bytes in context or re-reads from a bytes.Reader.
    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write(bodyBytes)
    expected := hex.EncodeToString(mac.Sum(nil))
    return hmac.Equal([]byte(token), []byte(expected))
}
```

### 5.5 Startup Re-registration

Called from `WorkflowEngine.Start()`:

```go
func (e *WorkflowEngine) reregisterTriggers(ctx context.Context) error {
    triggers, err := e.store.ListActiveTriggers(ctx)
    if err != nil {
        return err
    }
    return e.triggerMgr.ReregisterAll(ctx, triggers)
}
```

---

## 6. Job Queue Design

**File:** `internal/workflow/queue.go`

### 6.1 Queue Struct

```go
const QueueCapacity = 1000

type WorkflowExecutionRequest struct {
    WorkflowID  string
    TriggerType string
    InputData   Item
    ResultChan  chan<- WorkflowExecutionResult // nil = fire-and-forget
}

type WorkflowExecutionResult struct {
    ExecutionID string
    Error       error
}

type ExecutionQueue struct {
    ch      chan *WorkflowExecutionRequest
    workers int
    wg      sync.WaitGroup
    logger  zerolog.Logger
}

func NewExecutionQueue(workers int, logger zerolog.Logger) *ExecutionQueue {
    return &ExecutionQueue{
        ch:      make(chan *WorkflowExecutionRequest, QueueCapacity),
        workers: workers,
        logger:  logger,
    }
}
```

### 6.2 Worker Pool Start/Stop

```go
// Start launches 'workers' goroutines. Each goroutine reads from the channel
// and calls processFn for each request. processFn is engine.processRequest.
func (q *ExecutionQueue) Start(processFn func(*WorkflowExecutionRequest)) {
    for i := 0; i < q.workers; i++ {
        q.wg.Add(1)
        go func() {
            defer q.wg.Done()
            for req := range q.ch {
                processFn(req)
            }
        }()
    }
}

// Stop closes the channel and waits for all workers to finish draining.
func (q *ExecutionQueue) Stop() {
    close(q.ch)
    q.wg.Wait()
}

// Enqueue attempts a non-blocking send onto the channel.
// Returns ErrQueueFull if the channel buffer is full.
func (q *ExecutionQueue) Enqueue(req *WorkflowExecutionRequest) error {
    select {
    case q.ch <- req:
        return nil
    default:
        return ErrQueueFull
    }
}
```

### 6.3 Worker Process Flow

```go
func (e *WorkflowEngine) processRequest(req *WorkflowExecutionRequest) {
    // 1. Create execution record in DB.
    exec := &WorkflowExecution{
        ID:          uuid.NewString(),
        WorkflowID:  req.WorkflowID,
        TriggerType: req.TriggerType,
        Status:      "RUNNING",
        InputData:   req.InputData,
        StartedAt:   time.Now(),
    }
    if err := e.store.CreateExecution(context.Background(), exec); err != nil {
        e.logger.Error().Err(err).Msg("failed to create execution record")
        if req.ResultChan != nil {
            req.ResultChan <- WorkflowExecutionResult{Error: err}
        }
        return
    }

    // 2. Create a cancellable context with timeout.
    workflow, _ := e.store.GetWorkflow(context.Background(), req.WorkflowID)
    timeout := time.Duration(workflow.TimeoutSeconds) * time.Second
    ctx, cancel := context.WithTimeout(context.Background(), timeout)
    e.cancelFuncs.Store(exec.ID, cancel)
    defer func() {
        cancel()
        e.cancelFuncs.Delete(exec.ID)
    }()

    // 3. Notify caller of execution ID (non-blocking).
    if req.ResultChan != nil {
        req.ResultChan <- WorkflowExecutionResult{ExecutionID: exec.ID}
    }

    // 4. Run the workflow.
    e.runExecution(ctx, exec)
}
```

### 6.4 Cancellation via cancelFuncs Map

```go
func (e *WorkflowEngine) CancelExecution(ctx context.Context, executionID string) error {
    val, ok := e.cancelFuncs.Load(executionID)
    if !ok {
        return fmt.Errorf("execution '%s' is not currently running", executionID)
    }
    cancel := val.(context.CancelFunc)
    cancel()

    // Background goroutine: if execution hasn't exited after 30s, force-set FAILED.
    go func() {
        deadline := time.After(30 * time.Second)
        ticker := time.NewTicker(2 * time.Second)
        defer ticker.Stop()
        for {
            select {
            case <-deadline:
                // Force update status
                e.store.UpdateExecution(ctx, &WorkflowExecution{
                    ID:     executionID,
                    Status: "CANCELLED",
                })
                return
            case <-ticker.C:
                // Check if cancelFuncs still holds this ID
                if _, stillRunning := e.cancelFuncs.Load(executionID); !stillRunning {
                    return // already cleaned up by the worker goroutine
                }
            }
        }
    }()
    return nil
}
```

### 6.5 Startup Recovery

```go
// Called from WorkflowEngine.Start() before starting workers.
func (e *WorkflowEngine) recoverStaleExecutions(ctx context.Context) {
    // Sets all RUNNING executions to FAILED with message "execution interrupted by process restart"
    e.store.RecoverStaleExecutions(ctx)
}
```

---

## 7. Node Implementation Pattern

### 7.1 Browser Action Adapter

**File:** `internal/nodes/browser_adapter.go`

The `BrowserNode` adapter wraps the existing `ActionExecutor` so that browser automation nodes are first-class `NodeExecutor` implementations. The engine calls `Execute` on `BrowserNode`; `BrowserNode` builds a `StorageAction` from the config, acquires the Rod page for the session, constructs an `ActionExecutor`, and runs it.

```go
package nodes

import (
    "context"
    "fmt"

    "github.com/monoes/mono-agent/internal/action"
    "github.com/monoes/mono-agent/internal/workflow"
)

// BrowserNode implements workflow.NodeExecutor by delegating to action.ActionExecutor.
type BrowserNode struct {
    sessionProvider SessionProvider   // returns *rod.Page for platform+username
    db              action.StorageInterface
    configMgr       action.ConfigInterface
    botRegistry     BotRegistry
    logger          zerolog.Logger
}

// SessionProvider abstracts acquiring a browser page for a given platform session.
type SessionProvider interface {
    GetPage(ctx context.Context, platform, username string) (*rod.Page, error)
}

// BotRegistry abstracts looking up a BotAdapter by platform name.
type BotRegistry interface {
    Get(platform string) (action.BotAdapter, error)
}

func (b *BrowserNode) Execute(
    ctx context.Context,
    input workflow.NodeInput,
    config map[string]interface{},
) ([]workflow.NodeOutput, error) {
    platform := config["platform"].(string)
    actionType := config["action_type"].(string)
    sessionUsername, _ := config["session_username"].(string)

    page, err := b.sessionProvider.GetPage(ctx, platform, sessionUsername)
    if err != nil {
        return nil, fmt.Errorf("acquiring browser session for %s/%s: %w", platform, sessionUsername, err)
    }

    botAdapter, err := b.botRegistry.Get(platform)
    if err != nil {
        return nil, fmt.Errorf("getting bot adapter for platform %s: %w", platform, err)
    }

    events := make(chan action.ExecutionEvent, 100)
    go func() {
        for range events {} // drain events; can forward to workflow logger
    }()

    executor := action.NewActionExecutor(ctx, page, b.db, b.configMgr, events, botAdapter, b.logger)

    // Seed params from workflow node config + first input item
    params, _ := config["params"].(map[string]interface{})
    for k, v := range params {
        executor.SetVariable(k, v)
    }
    if len(input.Items) > 0 {
        for k, v := range input.Items[0] {
            executor.SetVariable(k, v)
        }
    }

    storageAction := &action.StorageAction{
        ID:             "wf-" + uuid.NewString(), // synthetic ID for progress tracking
        Type:           actionType,
        TargetPlatform: platform,
        Params:         params,
    }

    result, err := executor.Execute(storageAction)
    if err != nil {
        return nil, err
    }

    // Convert ExtractedItems to workflow Items
    outputItems := make([]workflow.Item, len(result.ExtractedItems))
    for i, extracted := range result.ExtractedItems {
        outputItems[i] = extracted
    }

    return []workflow.NodeOutput{{Handle: "main", Items: outputItems}}, nil
}
```

### 7.2 Non-Browser Node Template

Every non-browser node lives in `internal/nodes/<category>/<name>.go` and follows this pattern:

```go
package control   // or data, http, system, db, comm, service

import (
    "context"
    "fmt"

    "github.com/monoes/mono-agent/internal/workflow"
)

// IfNode implements the core.if control node.
// It evaluates a boolean expression and routes items to "true" or "false" output handles.
type IfNode struct {
    expr *workflow.ExpressionEngine
}

// NewIfNode is the factory function registered with NodeTypeRegistry.
func NewIfNode(expr *workflow.ExpressionEngine) workflow.NodeExecutor {
    return &IfNode{expr: expr}
}

// Execute evaluates the condition expression for each input item and routes it.
func (n *IfNode) Execute(
    ctx context.Context,
    input workflow.NodeInput,
    config map[string]interface{},
) ([]workflow.NodeOutput, error) {
    condition, ok := config["condition"].(string)
    if !ok || condition == "" {
        return nil, fmt.Errorf("core.if: 'condition' config field is required")
    }

    var trueItems, falseItems []workflow.Item

    for _, item := range input.Items {
        exprCtx := workflow.ExpressionContext{JSON: item}
        result := n.expr.EvaluateBool(condition, exprCtx)
        if result {
            trueItems = append(trueItems, item)
        } else {
            falseItems = append(falseItems, item)
        }
    }

    outputs := []workflow.NodeOutput{}
    if len(trueItems) > 0 {
        outputs = append(outputs, workflow.NodeOutput{Handle: "true", Items: trueItems})
    }
    if len(falseItems) > 0 {
        outputs = append(outputs, workflow.NodeOutput{Handle: "false", Items: falseItems})
    }
    return outputs, nil
}
```

**Config parsing pattern:** Always extract config fields with typed assertions and provide clear error messages:

```go
func extractStringConfig(config map[string]interface{}, key string, required bool) (string, error) {
    val, ok := config[key]
    if !ok || val == nil {
        if required {
            return "", fmt.Errorf("required config field '%s' is missing", key)
        }
        return "", nil
    }
    s, ok := val.(string)
    if !ok {
        return "", fmt.Errorf("config field '%s' must be a string, got %T", key, val)
    }
    if required && s == "" {
        return "", fmt.Errorf("required config field '%s' resolved to empty string", key)
    }
    return s, nil
}
```

### 7.3 NodeTypeRegistry

**File:** `internal/workflow/registry.go`

```go
// NodeExecutorFactory is a function that produces a NodeExecutor.
// Dependencies (ExpressionEngine, etc.) are injected via closure.
type NodeExecutorFactory func() workflow.NodeExecutor

type NodeTypeRegistry struct {
    factories map[string]NodeExecutorFactory
}

func NewNodeTypeRegistry() *NodeTypeRegistry {
    return &NodeTypeRegistry{factories: make(map[string]NodeExecutorFactory)}
}

func (r *NodeTypeRegistry) Register(nodeType string, factory NodeExecutorFactory) {
    r.factories[nodeType] = factory
}

func (r *NodeTypeRegistry) Get(nodeType string) (workflow.NodeExecutor, error) {
    factory, ok := r.factories[nodeType]
    if !ok {
        return nil, fmt.Errorf("unknown node type: %s", nodeType)
    }
    return factory(), nil
}
```

**Registration in engine initialization:**

```go
func (e *WorkflowEngine) registerNodes(browserNode *nodes.BrowserNode) {
    expr := e.expr

    // Trigger nodes (no-op executors; the trigger system handles firing)
    e.registry.Register("trigger.manual",   func() NodeExecutor { return &noopExecutor{} })
    e.registry.Register("trigger.schedule", func() NodeExecutor { return &noopExecutor{} })
    e.registry.Register("trigger.webhook",  func() NodeExecutor { return &noopExecutor{} })

    // Control
    e.registry.Register("control.if",               func() NodeExecutor { return control.NewIfNode(expr) })
    e.registry.Register("control.switch",            func() NodeExecutor { return control.NewSwitchNode(expr) })
    e.registry.Register("control.merge",             func() NodeExecutor { return control.NewMergeNode() })
    e.registry.Register("control.split_in_batches",  func() NodeExecutor { return control.NewSplitInBatchesNode() })
    e.registry.Register("control.wait",              func() NodeExecutor { return control.NewWaitNode() })
    e.registry.Register("control.stop_error",        func() NodeExecutor { return control.NewStopErrorNode(expr) })
    e.registry.Register("control.noop",              func() NodeExecutor { return control.NewNoopNode() })
    e.registry.Register("core.set",                  func() NodeExecutor { return control.NewSetNode(expr) })
    e.registry.Register("core.code",                 func() NodeExecutor { return control.NewCodeNode() })
    e.registry.Register("core.filter",               func() NodeExecutor { return control.NewFilterNode(expr) })
    e.registry.Register("core.sort",                 func() NodeExecutor { return control.NewSortNode() })
    e.registry.Register("core.limit",                func() NodeExecutor { return control.NewLimitNode() })
    e.registry.Register("core.remove_duplicates",    func() NodeExecutor { return control.NewRemoveDuplicatesNode() })
    e.registry.Register("core.compare_datasets",     func() NodeExecutor { return control.NewCompareDatasetsNode() })
    e.registry.Register("core.aggregate",            func() NodeExecutor { return control.NewAggregateNode() })

    // Data
    e.registry.Register("data.datetime",             func() NodeExecutor { return data.NewDatetimeNode() })
    e.registry.Register("data.crypto",               func() NodeExecutor { return data.NewCryptoNode() })
    e.registry.Register("data.html",                 func() NodeExecutor { return data.NewHTMLNode() })
    e.registry.Register("data.xml",                  func() NodeExecutor { return data.NewXMLNode() })
    e.registry.Register("data.markdown",             func() NodeExecutor { return data.NewMarkdownNode() })
    e.registry.Register("data.spreadsheet",          func() NodeExecutor { return data.NewSpreadsheetNode() })
    e.registry.Register("data.compression",          func() NodeExecutor { return data.NewCompressionNode() })
    e.registry.Register("data.write_binary_file",    func() NodeExecutor { return data.NewWriteBinaryFileNode() })

    // HTTP / System
    e.registry.Register("http.request",              func() NodeExecutor { return httpnodes.NewRequestNode() })
    e.registry.Register("http.ftp",                  func() NodeExecutor { return httpnodes.NewFTPNode() })
    e.registry.Register("http.ssh",                  func() NodeExecutor { return httpnodes.NewSSHNode() })
    e.registry.Register("system.execute_command",    func() NodeExecutor { return system.NewExecuteCommandNode() })
    e.registry.Register("system.rss_read",           func() NodeExecutor { return system.NewRSSReadNode() })

    // Database
    e.registry.Register("db.mysql",                  func() NodeExecutor { return db.NewMySQLNode() })
    e.registry.Register("db.postgres",               func() NodeExecutor { return db.NewPostgresNode() })
    e.registry.Register("db.mongodb",                func() NodeExecutor { return db.NewMongoDBNode() })
    e.registry.Register("db.redis",                  func() NodeExecutor { return db.NewRedisNode() })

    // Communication
    e.registry.Register("comm.email_send",           func() NodeExecutor { return comm.NewEmailSendNode() })
    e.registry.Register("comm.email_read",           func() NodeExecutor { return comm.NewEmailReadNode() })
    e.registry.Register("comm.slack",                func() NodeExecutor { return comm.NewSlackNode() })
    e.registry.Register("comm.telegram",             func() NodeExecutor { return comm.NewTelegramNode() })
    e.registry.Register("comm.discord",              func() NodeExecutor { return comm.NewDiscordNode() })
    e.registry.Register("comm.twilio",               func() NodeExecutor { return comm.NewTwilioNode() })
    e.registry.Register("comm.whatsapp",             func() NodeExecutor { return comm.NewWhatsAppNode() })

    // Services
    e.registry.Register("service.github",            func() NodeExecutor { return service.NewGitHubNode() })
    e.registry.Register("service.airtable",          func() NodeExecutor { return service.NewAirtableNode() })
    e.registry.Register("service.notion",            func() NodeExecutor { return service.NewNotionNode() })
    e.registry.Register("service.jira",              func() NodeExecutor { return service.NewJiraNode() })
    e.registry.Register("service.linear",            func() NodeExecutor { return service.NewLinearNode() })
    e.registry.Register("service.asana",             func() NodeExecutor { return service.NewAsanaNode() })
    e.registry.Register("service.stripe",            func() NodeExecutor { return service.NewStripeNode() })
    e.registry.Register("service.shopify",           func() NodeExecutor { return service.NewShopifyNode() })
    e.registry.Register("service.salesforce",        func() NodeExecutor { return service.NewSalesforceNode() })
    e.registry.Register("service.hubspot",           func() NodeExecutor { return service.NewHubSpotNode() })
    e.registry.Register("service.google_sheets",     func() NodeExecutor { return service.NewGoogleSheetsNode() })
    e.registry.Register("service.gmail",             func() NodeExecutor { return service.NewGmailNode() })
    e.registry.Register("service.google_drive",      func() NodeExecutor { return service.NewGoogleDriveNode() })

    // Browser action nodes — all action.<platform>.<TYPE> map to BrowserNode
    // Register is called with a wildcard prefix matcher. The registry checks
    // prefix "action." and routes all such types to the BrowserNode factory.
    e.registry.RegisterPrefix("action.", func() NodeExecutor { return browserNode })
}
```

**Prefix registration:** The `NodeTypeRegistry` adds a `RegisterPrefix` method that is consulted as a fallback when exact type lookup fails:

```go
func (r *NodeTypeRegistry) Get(nodeType string) (workflow.NodeExecutor, error) {
    if factory, ok := r.factories[nodeType]; ok {
        return factory(), nil
    }
    for prefix, factory := range r.prefixFactories {
        if strings.HasPrefix(nodeType, prefix) {
            return factory(), nil
        }
    }
    return nil, fmt.Errorf("unknown node type: %s", nodeType)
}
```

---

## 8. Credentials System

### 8.1 Purpose

Credentials allow nodes (HTTP, database, communication, service) to store sensitive values (API keys, passwords, tokens, connection strings) encrypted in the database rather than in node config fields in plaintext.

### 8.2 Encryption Scheme

**Algorithm:** AES-256-GCM (authenticated encryption).
**Key source:** 32-byte key derived from the environment variable `MONOES_CREDENTIAL_KEY` using HKDF-SHA256 with a fixed salt:

```go
const credentialSalt = "monoes-credential-v1"

func deriveCredentialKey(envKey string) ([]byte, error) {
    raw := []byte(envKey)
    if len(raw) == 0 {
        return nil, errors.New("MONOES_CREDENTIAL_KEY env variable is not set")
    }
    // HKDF with SHA-256
    reader := hkdf.New(sha256.New, raw, []byte(credentialSalt), nil)
    key := make([]byte, 32)
    if _, err := io.ReadFull(reader, key); err != nil {
        return nil, err
    }
    return key, nil
}
```

**Encryption of a credential value:**

```go
func encryptValue(key []byte, plaintext string) (string, error) {
    block, err := aes.NewCipher(key)
    if err != nil {
        return "", err
    }
    gcm, err := cipher.NewGCM(block)
    if err != nil {
        return "", err
    }
    nonce := make([]byte, gcm.NonceSize())
    if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
        return "", err
    }
    ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
    return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func decryptValue(key []byte, encoded string) (string, error) {
    ciphertext, err := base64.StdEncoding.DecodeString(encoded)
    if err != nil {
        return "", err
    }
    block, _ := aes.NewCipher(key)
    gcm, _ := cipher.NewGCM(block)
    nonceSize := gcm.NonceSize()
    if len(ciphertext) < nonceSize {
        return "", errors.New("ciphertext too short")
    }
    nonce, ct := ciphertext[:nonceSize], ciphertext[nonceSize:]
    plaintext, err := gcm.Open(nil, nonce, ct, nil)
    if err != nil {
        return "", err
    }
    return string(plaintext), nil
}
```

### 8.3 Credential Model

```go
type Credential struct {
    ID        string
    Name      string              // user-facing label, e.g. "My GitHub Token"
    NodeType  string              // e.g. "service.github", "db.mysql" — for filtering
    Data      map[string]string   // field name → encrypted value
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

The `Data` field is stored as a JSON object in the `credentials` table with each value individually encrypted. On read, the credential manager decrypts all values before returning.

### 8.4 How Nodes Receive Credentials

Nodes that require credentials include a `credential_id` string field in their config:

```json
{
  "credential_id": "cred-uuid",
  "operation": "create_issue",
  "repo": "owner/repo"
}
```

Before calling `NodeExecutor.Execute`, the engine resolves the credential:

```go
func (e *WorkflowEngine) resolveCredentials(
    ctx context.Context,
    config map[string]interface{},
) (map[string]interface{}, error) {
    credID, _ := config["credential_id"].(string)
    if credID == "" {
        return config, nil
    }
    cred, err := e.store.GetCredential(ctx, credID)
    if err != nil {
        return nil, fmt.Errorf("loading credential '%s': %w", credID, err)
    }
    // Decrypt all credential fields
    decrypted, err := e.credMgr.DecryptAll(cred)
    if err != nil {
        return nil, fmt.Errorf("decrypting credential '%s': %w", credID, err)
    }
    // Merge into config under "credential" key
    merged := make(map[string]interface{}, len(config)+1)
    for k, v := range config {
        merged[k] = v
    }
    merged["credential"] = decrypted
    return merged, nil
}
```

Inside the node's `Execute`, credentials are accessed as:

```go
cred, _ := config["credential"].(map[string]string)
apiKey := cred["api_key"]
```

### 8.5 Credential Table Schema

```sql
CREATE TABLE IF NOT EXISTS credentials (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    node_type  TEXT NOT NULL DEFAULT '',
    data       TEXT NOT NULL DEFAULT '{}',  -- JSON: field_name → AES-256-GCM encrypted base64
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_credentials_node_type ON credentials(node_type);
```

### 8.6 Wails Bound Methods for Credentials

These are added to `wails-app/workflow.go`:

```go
// CreateCredential stores a new encrypted credential.
// plainData is the raw field values; the backend encrypts before storing.
func (a *App) CreateCredential(name, nodeType string, plainData map[string]string) (*CredentialSummary, error)

// UpdateCredential replaces credential data (all fields re-encrypted).
func (a *App) UpdateCredential(id string, plainData map[string]string) error

// ListCredentials returns credentials filtered by node type.
// plainData is NOT included in the response (never sent to frontend unencrypted).
func (a *App) ListCredentials(nodeType string) []CredentialSummary

// DeleteCredential removes a credential.
func (a *App) DeleteCredential(id string) error

type CredentialSummary struct {
    ID        string   `json:"id"`
    Name      string   `json:"name"`
    NodeType  string   `json:"node_type"`
    Fields    []string `json:"fields"`    // field names only, no values
    CreatedAt string   `json:"created_at"`
}
```

---

## 9. Database Migration

**File:** `data/migrations/006_workflow_system.sql`

This migration file is additive only. All tables use `CREATE TABLE IF NOT EXISTS` and `CREATE INDEX IF NOT EXISTS` to be idempotent.

```sql
-- ============================================================
-- Migration 006: Workflow System
-- ============================================================

-- Core workflow definition
CREATE TABLE IF NOT EXISTS workflows (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    description       TEXT NOT NULL DEFAULT '',
    is_active         INTEGER NOT NULL DEFAULT 0,
    version           INTEGER NOT NULL DEFAULT 1,
    timeout_seconds   INTEGER NOT NULL DEFAULT 3600,
    error_workflow_id TEXT REFERENCES workflows(id) ON DELETE SET NULL,
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_workflows_is_active ON workflows(is_active);
CREATE INDEX IF NOT EXISTS idx_workflows_updated   ON workflows(updated_at DESC);

-- Nodes within a workflow (the DAG vertices)
CREATE TABLE IF NOT EXISTS workflow_nodes (
    id          TEXT PRIMARY KEY,
    workflow_id TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    node_type   TEXT NOT NULL,
    name        TEXT NOT NULL,
    config      TEXT NOT NULL DEFAULT '{}',
    position_x  REAL NOT NULL DEFAULT 0,
    position_y  REAL NOT NULL DEFAULT 0,
    disabled    INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_workflow_nodes_workflow ON workflow_nodes(workflow_id);
CREATE INDEX IF NOT EXISTS idx_workflow_nodes_type     ON workflow_nodes(node_type);

-- Edges between nodes (the DAG edges)
CREATE TABLE IF NOT EXISTS workflow_connections (
    id              TEXT PRIMARY KEY,
    workflow_id     TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    source_node_id  TEXT NOT NULL REFERENCES workflow_nodes(id) ON DELETE CASCADE,
    source_handle   TEXT NOT NULL DEFAULT 'main',
    target_node_id  TEXT NOT NULL REFERENCES workflow_nodes(id) ON DELETE CASCADE,
    target_handle   TEXT NOT NULL DEFAULT 'main',
    position        INTEGER NOT NULL DEFAULT 0,
    UNIQUE(source_node_id, source_handle, target_node_id, target_handle)
);

CREATE INDEX IF NOT EXISTS idx_workflow_connections_workflow ON workflow_connections(workflow_id);
CREATE INDEX IF NOT EXISTS idx_workflow_connections_source   ON workflow_connections(source_node_id);
CREATE INDEX IF NOT EXISTS idx_workflow_connections_target   ON workflow_connections(target_node_id);

-- Cached trigger registrations for fast startup re-registration
CREATE TABLE IF NOT EXISTS workflow_triggers (
    id           TEXT PRIMARY KEY,
    workflow_id  TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    node_id      TEXT NOT NULL REFERENCES workflow_nodes(id) ON DELETE CASCADE,
    trigger_type TEXT NOT NULL,
    config       TEXT NOT NULL DEFAULT '{}',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(node_id)
);

CREATE INDEX IF NOT EXISTS idx_workflow_triggers_workflow ON workflow_triggers(workflow_id);
CREATE INDEX IF NOT EXISTS idx_workflow_triggers_type     ON workflow_triggers(trigger_type);

-- One record per workflow execution instance
CREATE TABLE IF NOT EXISTS workflow_executions (
    id            TEXT PRIMARY KEY,
    workflow_id   TEXT NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
    trigger_type  TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'RUNNING',
    input_data    TEXT NOT NULL DEFAULT '{}',
    error_message TEXT NOT NULL DEFAULT '',
    started_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at   TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_workflow_executions_workflow ON workflow_executions(workflow_id);
CREATE INDEX IF NOT EXISTS idx_workflow_executions_status   ON workflow_executions(status);
CREATE INDEX IF NOT EXISTS idx_workflow_executions_started  ON workflow_executions(started_at DESC);

-- Per-node run records within an execution (includes retry attempts as separate rows)
CREATE TABLE IF NOT EXISTS workflow_execution_nodes (
    id           TEXT PRIMARY KEY,
    execution_id TEXT NOT NULL REFERENCES workflow_executions(id) ON DELETE CASCADE,
    node_id      TEXT NOT NULL,
    node_name    TEXT NOT NULL,
    node_type    TEXT NOT NULL,
    run_index    INTEGER NOT NULL DEFAULT 0,
    status       TEXT NOT NULL,
    input_items  TEXT NOT NULL DEFAULT '[]',
    output_items TEXT NOT NULL DEFAULT '[]',
    error_msg    TEXT NOT NULL DEFAULT '',
    retry_count  INTEGER NOT NULL DEFAULT 0,
    started_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at  TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_exec_nodes_execution ON workflow_execution_nodes(execution_id);
CREATE INDEX IF NOT EXISTS idx_exec_nodes_node      ON workflow_execution_nodes(node_id);
CREATE INDEX IF NOT EXISTS idx_exec_nodes_status    ON workflow_execution_nodes(status);

-- Encrypted credential storage
CREATE TABLE IF NOT EXISTS credentials (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    node_type  TEXT NOT NULL DEFAULT '',
    data       TEXT NOT NULL DEFAULT '{}',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_credentials_node_type ON credentials(node_type);
```

### 9.1 Migration Integration

The existing storage package reads numbered SQL files from `data/migrations/` at startup. The new file `006_workflow_system.sql` must be embedded alongside the existing migration files via the `data/embed.go` embed directive. No changes to the migration runner are required — it reads all `*.sql` files in numeric order.

### 9.2 Execution History Retention

A background goroutine started by `WorkflowEngine.Start()` runs every 60 seconds and prunes old executions:

```go
func (e *WorkflowEngine) pruneExecutions(ctx context.Context) {
    ticker := time.NewTicker(60 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            // For each workflow, delete oldest non-RUNNING executions
            // beyond the 500-execution retention limit.
            e.store.PruneExecutions(ctx, 500)
        }
    }
}
```

```sql
-- PruneExecutions implementation per workflow:
DELETE FROM workflow_executions
WHERE workflow_id = ?
  AND status != 'RUNNING'
  AND id NOT IN (
      SELECT id FROM workflow_executions
      WHERE workflow_id = ?
      ORDER BY started_at DESC
      LIMIT 500
  );
```

### 9.3 Data Size Limits

Before inserting `input_items` or `output_items` into `workflow_execution_nodes`, the engine checks the serialized size:

```go
func truncateItems(items []Item) []Item {
    data, _ := json.Marshal(items)
    if len(data) <= 1<<20 { // 1MB
        return items
    }
    // Truncate to first 100 items
    if len(items) > 100 {
        items = items[:100]
    }
    items = append(items, Item{"_truncated": true})
    return items
}
```

---

## 10. New Go Dependencies

The requirements mandate minimal new dependencies. The workflow engine core uses only the existing dependency set. Additional dependencies are needed only for the non-browser node library.

### 10.1 Core Workflow Engine (zero new dependencies)

The workflow engine (`internal/workflow/`) uses only:
- `text/template` (stdlib) — expression engine
- `net/http` (stdlib) — webhook server
- `sync`, `context`, `time`, `crypto/hmac`, `crypto/sha256`, `crypto/aes`, `crypto/cipher`, `encoding/json` (stdlib) — all standard
- `github.com/robfig/cron/v3` — already in `go.mod`
- `github.com/google/uuid` — already in `go.mod`
- `github.com/rs/zerolog` — already in `go.mod`
- `modernc.org/sqlite` — already in `go.mod`
- `golang.org/x/crypto` (for HKDF) — already transitively present; explicitly `go get` if needed

### 10.2 Node Library Dependencies

```bash
# Control nodes
go get github.com/dop251/goja@latest                    # core.code: JavaScript execution engine

# Data nodes
go get github.com/PuerkitoBio/goquery@latest            # data.html: HTML parsing
go get github.com/yuin/goldmark@latest                  # data.markdown: Markdown→HTML

# HTTP/System nodes
go get github.com/jlaffaye/ftp@latest                   # http.ftp
go get golang.org/x/crypto@latest                       # http.ssh (golang.org/x/crypto/ssh) + HKDF

# System nodes
go get github.com/mmcdole/gofeed@latest                 # system.rss_read: RSS/Atom parsing

# Database nodes
go get github.com/go-mysql-org/go-mysql@latest          # db.mysql
go get github.com/lib/pq@latest                         # db.postgres
go get go.mongodb.org/mongo-driver@latest               # db.mongodb
go get github.com/redis/go-redis/v9@latest              # db.redis

# Communication nodes
go get github.com/slack-go/slack@latest                 # comm.slack
go get github.com/go-telegram-bot-api/telegram-bot-api/v5@latest  # comm.telegram
go get github.com/bwmarrin/discordgo@latest             # comm.discord
# comm.twilio and comm.whatsapp use net/http REST only — no new deps

# Frontend
# (added via npm in wails-app/frontend, not go get)
# npm install @xyflow/react        (React Flow — MIT licensed canvas library)
```

### 10.3 Complete `go get` Commands

```bash
cd /Users/morteza/Desktop/monoes/mono-agent/newmonoes

go get github.com/dop251/goja@latest
go get github.com/PuerkitoBio/goquery@latest
go get github.com/yuin/goldmark@latest
go get github.com/jlaffaye/ftp@latest
go get golang.org/x/crypto@latest
go get github.com/mmcdole/gofeed@latest
go get github.com/go-mysql-org/go-mysql@latest
go get github.com/lib/pq@latest
go get go.mongodb.org/mongo-driver@latest
go get github.com/redis/go-redis/v9@latest
go get github.com/slack-go/slack@latest
go get github.com/go-telegram-bot-api/telegram-bot-api/v5@latest
go get github.com/bwmarrin/discordgo@latest

go mod tidy
```

---

## 11. Implementation Wave Plan

Waves are ordered by dependency. Items within a wave can be implemented in parallel by independent Builder agents. Each wave must be complete and tested before the next wave begins.

---

### Wave 1 — Foundation (prerequisite for everything)

**All items in this wave can be built in parallel.**

| Item | File(s) | Description |
|------|---------|-------------|
| W1-A | `internal/workflow/models.go` | All domain structs: `Workflow`, `WorkflowNode`, `WorkflowConnection`, `WorkflowTrigger`, `WorkflowExecution`, `WorkflowExecutionNode`, `ExpressionContext`, `NodeInput`, `NodeOutput`, `NodeExecutor` interface, `WorkflowRunner` interface, `TriggerProvider` interface |
| W1-B | `internal/workflow/errors.go` | Sentinel errors: `ErrQueueFull`, `ErrWorkflowNotFound`, `ErrExecutionNotFound`, `ErrCycleDetected`, `ErrInvalidConfig`, `ErrNodeTypeUnknown` |
| W1-C | `data/migrations/006_workflow_system.sql` | Full SQL migration as specified in section 9 |
| W1-D | `internal/workflow/dag.go` | `DAG` struct, `BuildDAG`, `TopologicalSort` (Kahn's), `TriggerNodes`, `Successors`, `Predecessors` + unit tests for all cycle/diamond cases |
| W1-E | `internal/workflow/expression.go` | `ExpressionEngine`, `EvaluateString`, `EvaluateBool`, `ResolveConfig`, FuncMap with all safe functions + unit tests |
| W1-F | `internal/workflow/storage.go` | `WorkflowStore` interface + SQLite implementation for all CRUD methods including `RecoverStaleExecutions` and `PruneExecutions` |
| W1-G | `internal/workflow/registry.go` | `NodeTypeRegistry`, `Register`, `RegisterPrefix`, `Get` |

---

### Wave 2 — Queue, Engine Shell, Webhook Server

**Depends on Wave 1. Items W2-A and W2-B can be built in parallel.**

| Item | File(s) | Description |
|------|---------|-------------|
| W2-A | `internal/workflow/queue.go` | `ExecutionQueue`, `WorkflowExecutionRequest/Result`, `NewExecutionQueue`, `Start`, `Stop`, `Enqueue` with unit tests |
| W2-B | `internal/workflow/webhook_server.go` | `WebhookServer`, `Register`, `Deregister`, `ServeHTTP` (all routes), HMAC validation, CORS headers, 1MB limit |
| W2-C | `internal/workflow/trigger_manager.go` | `TriggerManager`, `ManualTriggerProvider`, `ScheduleTriggerProvider` (extending existing scheduler), `WebhookTriggerProvider` (delegating to WebhookServer) + extend `internal/scheduler/scheduler.go` with `ScheduleWorkflow` |
| W2-D | `internal/workflow/engine.go` | `WorkflowEngine` struct, `NewWorkflowEngine`, `Start`, `Stop`, `ActivateWorkflow`, `DeactivateWorkflow`, `TriggerWorkflow`, `CancelExecution`, `RetryExecution`, `recoverStaleExecutions`, `reregisterTriggers`, `pruneExecutions`, `registerNodes` |

---

### Wave 3 — Execution Core

**Depends on Wave 2.**

| Item | File(s) | Description |
|------|---------|-------------|
| W3-A | `internal/workflow/execution.go` | `runExecution` full algorithm, `executeWithRetry`, merge accumulator, `computeDelay`, `persistNodeRun`, execution status transitions using `UPDATE ... WHERE status = 'RUNNING'` pattern |
| W3-B | `internal/workflow/validator.go` | `validateForSave` (no cycles, no duplicate IDs, node count ≤ 200, connection count ≤ 500), `validateForActivation` (trigger exists, action types resolve to JSON files, required fields non-empty) |
| W3-C | `internal/nodes/browser_adapter.go` | `BrowserNode`, `SessionProvider` interface, `BotRegistry` interface, full `Execute` implementation bridging to `action.ActionExecutor` |

---

### Wave 4 — Control and Transform Nodes

**Depends on Wave 1 (models + expression engine). All items are independent.**

| Item | File(s) | Description |
|------|---------|-------------|
| W4-A | `internal/nodes/control/if.go` | `IfNode.Execute` — boolean expression routing to true/false handles |
| W4-B | `internal/nodes/control/switch.go` | `SwitchNode.Execute` — expression value matching to N case handles + default |
| W4-C | `internal/nodes/control/merge.go` | `MergeNode.Execute` — append mode: combines all branch items; first mode: returns first received batch |
| W4-D | `internal/nodes/control/split_in_batches.go` | `SplitInBatchesNode.Execute` — splits input Items slice into sub-slices of N, each emitted separately on main |
| W4-E | `internal/nodes/control/wait.go` | `WaitNode.Execute` — `time.Sleep` with ctx cancellation, 1–3600s range validation |
| W4-F | `internal/nodes/control/stop_error.go` | `StopErrorNode.Execute` — always returns error with configured message |
| W4-G | `internal/nodes/control/set.go` | `SetNode.Execute` — apply assignments list to each item using dot-path setter |
| W4-H | `internal/nodes/control/code.go` | `CodeNode.Execute` — goja JS engine, `$input.all()` returns items, result must be array of objects |
| W4-I | `internal/nodes/control/filter.go` | `FilterNode.Execute` — expression-based item filter |
| W4-J | `internal/nodes/control/sort.go` | `SortNode.Execute` — sort by field, asc/desc |
| W4-K | `internal/nodes/control/limit.go` | `LimitNode.Execute` — return first N items |
| W4-L | `internal/nodes/control/remove_duplicates.go` | `RemoveDuplicatesNode.Execute` — deduplicate by field key |
| W4-M | `internal/nodes/control/compare_datasets.go` | `CompareDatasetsNode.Execute` — diff two item sets (added/removed/changed), emits on separate handles |
| W4-N | `internal/nodes/control/aggregate.go` | `AggregateNode.Execute` — group by field, compute sum/count/avg per group |

---

### Wave 5 — Data, HTTP, System, DB, Comm, Service Nodes

**Depends on Wave 1. All items are independent of each other. Can be distributed across many agents.**

| Item | File(s) | Node Types |
|------|---------|------------|
| W5-A | `internal/nodes/data/datetime.go` | `data.datetime`: parse, format, add/subtract durations |
| W5-B | `internal/nodes/data/crypto.go` | `data.crypto`: MD5, SHA256, HMAC-SHA256, UUID v4, random bytes — stdlib only |
| W5-C | `internal/nodes/data/html.go` | `data.html`: parse with goquery, CSS selector extract, attribute extract, generate HTML |
| W5-D | `internal/nodes/data/xml.go` | `data.xml`: parse XML → map, map → XML marshal via encoding/xml |
| W5-E | `internal/nodes/data/markdown.go` | `data.markdown`: goldmark Markdown→HTML conversion |
| W5-F | `internal/nodes/data/spreadsheet.go` | `data.spreadsheet`: read CSV (encoding/csv), read/write XLSX (internal/xlsx) |
| W5-G | `internal/nodes/data/compression.go` | `data.compression`: gzip/zip compress and decompress (stdlib compress/gzip, archive/zip) |
| W5-H | `internal/nodes/data/write_binary_file.go` | `data.write_binary_file`: write base64-decoded or raw bytes to file path |
| W5-I | `internal/nodes/http/request.go` | `http.request`: full HTTP client — method, URL, headers, body (JSON/form/text), basic/bearer/API-key auth, follow redirects, response as item |
| W5-J | `internal/nodes/http/ftp.go` | `http.ftp`: upload/download via jlaffaye/ftp |
| W5-K | `internal/nodes/http/ssh.go` | `http.ssh`: connect, run command, return stdout/stderr as item |
| W5-L | `internal/nodes/system/execute_command.go` | `system.execute_command`: exec.Command with timeout, capture stdout/stderr |
| W5-M | `internal/nodes/system/rss_read.go` | `system.rss_read`: gofeed fetch URL, return items as workflow Items |
| W5-N | `internal/nodes/db/mysql.go` | `db.mysql`: open connection from credential, execute SELECT/INSERT/UPDATE/DELETE, return rows as items |
| W5-O | `internal/nodes/db/postgres.go` | `db.postgres`: same pattern as mysql using lib/pq |
| W5-P | `internal/nodes/db/mongodb.go` | `db.mongodb`: connect, find/insert/update/delete, return docs as items |
| W5-Q | `internal/nodes/db/redis.go` | `db.redis`: GET/SET/DEL/LPUSH/LRANGE/HSET/HGET/EXPIRE |
| W5-R | `internal/nodes/comm/email_send.go` | `comm.email_send`: net/smtp PLAIN auth, to/cc/bcc/subject/body/attachments |
| W5-S | `internal/nodes/comm/email_read.go` | `comm.email_read`: go-imap fetch inbox, return messages as items |
| W5-T | `internal/nodes/comm/slack.go` | `comm.slack`: post message, upload file, list channels via slack-go/slack |
| W5-U | `internal/nodes/comm/telegram.go` | `comm.telegram`: send message, send photo via go-telegram-bot-api |
| W5-V | `internal/nodes/comm/discord.go` | `comm.discord`: send message to channel via bwmarrin/discordgo |
| W5-W | `internal/nodes/comm/twilio.go` | `comm.twilio`: SMS/voice via Twilio REST API (net/http only) |
| W5-X | `internal/nodes/comm/whatsapp.go` | `comm.whatsapp`: WhatsApp Business API (net/http only) |
| W5-Y | `internal/nodes/service/github.go` | `service.github`: issues CRUD, PRs, repos, releases via GitHub REST API (net/http) |
| W5-Z | `internal/nodes/service/airtable.go` | `service.airtable`: records CRUD via Airtable REST |
| W5-AA | `internal/nodes/service/notion.go` | `service.notion`: pages, databases, blocks via Notion API |
| W5-BB | `internal/nodes/service/jira.go` | `service.jira`: issues, projects via Jira REST |
| W5-CC | `internal/nodes/service/linear.go` | `service.linear`: issues, teams via Linear GraphQL API |
| W5-DD | `internal/nodes/service/asana.go` | `service.asana`: tasks, projects via Asana REST |
| W5-EE | `internal/nodes/service/stripe.go` | `service.stripe`: customers, charges, subscriptions via Stripe API |
| W5-FF | `internal/nodes/service/shopify.go` | `service.shopify`: products, orders via Shopify Admin REST |
| W5-GG | `internal/nodes/service/salesforce.go` | `service.salesforce`: objects CRUD, SOQL query via Salesforce REST |
| W5-HH | `internal/nodes/service/hubspot.go` | `service.hubspot`: contacts, deals, companies via HubSpot REST |
| W5-II | `internal/nodes/service/google_sheets.go` | `service.google_sheets`: read/write rows via Sheets API v4 |
| W5-JJ | `internal/nodes/service/gmail.go` | `service.gmail`: send/read via Gmail API |
| W5-KK | `internal/nodes/service/google_drive.go` | `service.google_drive`: file upload/download/list via Drive API v3 |

---

### Wave 6 — New Browser Platform Actions

**Depends on Wave 3 (browser adapter). All 3 platforms can be built in parallel. Each platform's 4 actions can also be built in parallel.**

| Item | File(s) | Actions |
|------|---------|---------|
| W6-A | `internal/bot/linkedin/actions.go` + `data/actions/linkedin/*.json` + `internal/config/schemas.go` | `LinkedInKeywordSearch`, `LinkedInProfileInteraction`, `LinkedInPublishContent` |
| W6-B | `internal/bot/tiktok/actions.go` + `data/actions/tiktok/*.json` + `internal/config/schemas.go` | `TikTokKeywordSearch`, `TikTokProfileFetch`, `TikTokProfileInteraction`, `TikTokPublishContent` |
| W6-C | `internal/bot/x/actions.go` + `data/actions/x/*.json` + `internal/config/schemas.go` | `XKeywordSearch`, `XProfileFetch`, `XProfileInteraction`, `XPublishContent` |

Each platform item requires, per action:
1. Tier 1 Go bot method in `actions.go` with correct signature.
2. Registration in `bot.go`'s `GetMethodByName`.
3. Schema keys in `internal/config/schemas.go`.
4. Full 3-tier action JSON in `data/actions/<platform>/<TYPE>.json`.
5. Integration test in `internal/action/<platform>_integration_test.go` with `//go:build integration`.

---

### Wave 7 — Wails Binding Layer

**Depends on Wave 2 (engine shell). Can be built in parallel with Waves 4, 5, 6.**

| Item | File(s) | Description |
|------|---------|-------------|
| W7-A | `wails-app/workflow.go` | All Wails bound methods: `GetWorkflows`, `GetWorkflow`, `CreateWorkflow`, `SaveWorkflow`, `DeleteWorkflow`, `ActivateWorkflow`, `DeactivateWorkflow`, `ExecuteWorkflow`, `CancelWorkflowExecution`, `RetryWorkflowExecution`, `GetWorkflowExecutions`, `GetWorkflowExecutionDetail`, credential methods; all DTOs |
| W7-B | `wails-app/main.go` | Extend startup sequence: `WorkflowEngine.Start(ctx)`, webhook server bind, shutdown hook |
| W7-C | `internal/workflow/credentials.go` | `CredentialManager`: `deriveCredentialKey`, `encryptValue`, `decryptValue`, `DecryptAll` |

---

### Wave 8 — Frontend

**Depends on Wave 7. Frontend items can be built in parallel.**

| Item | File(s) | Description |
|------|---------|-------------|
| W8-A | `wails-app/frontend/src/pages/Workflows.jsx` | Workflow list page with table, status toggle, inline run/delete, 10s polling |
| W8-B | `wails-app/frontend/src/pages/WorkflowCanvas.jsx` | Canvas editor using React Flow: node palette, canvas, config panel, execution log panel, undo/redo (50-state history), auto-save (3s debounce), Ctrl+S, zoom/pan |
| W8-C | `wails-app/frontend/src/components/nodes/` | All per-node-type config panel components (one component per node type, as listed in requirements section 8.5) |
| W8-D | `wails-app/frontend/src/components/Sidebar.jsx` | Add "Workflows" nav item with graph icon |

---

### Wave 9 — Tests

**Depends on Waves 1–8.**

| Item | File(s) | Description |
|------|---------|-------------|
| W9-A | `internal/workflow/dag_test.go` | Unit tests: no cycle, direct cycle, indirect cycle, diamond pattern |
| W9-B | `internal/workflow/expression_test.go` | Unit tests: simple field, nested field, node output, missing field, boolean comparison, env function, arithmetic |
| W9-C | `internal/workflow/engine_test.go` | Unit tests with mock store and scheduler: queue dispatch, retry policy, cancellation, startup recovery |
| W9-D | `internal/workflow/webhook_server_test.go` | Unit tests: route registration, HMAC validation, body parsing, 1MB limit, CORS |
| W9-E | `internal/action/linkedin_integration_test.go` | Integration tests (build tag: integration) for all 3 LinkedIn actions |
| W9-F | `internal/action/tiktok_integration_test.go` | Integration tests for all 4 TikTok actions |
| W9-G | `internal/action/x_integration_test.go` | Integration tests for all 4 X actions |

---

## Appendix A — Node Handle Reference

| Node Type | Output Handles |
|-----------|----------------|
| `trigger.manual` | `main` |
| `trigger.schedule` | `main` |
| `trigger.webhook` | `main` |
| `action.*` | `main`, `error` |
| `control.if` | `true`, `false` |
| `control.switch` | one handle per case value + `default` |
| `control.merge` | `main` |
| `control.split_in_batches` | `main` |
| `control.wait` | `main` |
| `control.stop_error` | (none — always errors) |
| `control.noop` | `main` |
| `core.set` | `main` |
| `core.code` | `main` |
| `core.filter` | `main` |
| `core.sort` | `main` |
| `core.limit` | `main` |
| `core.remove_duplicates` | `main` |
| `core.compare_datasets` | `added`, `removed`, `changed`, `unchanged` |
| `core.aggregate` | `main` |
| All `data.*` nodes | `main` |
| All `http.*` nodes | `main`, `error` |
| All `system.*` nodes | `main`, `error` |
| All `db.*` nodes | `main`, `error` |
| All `comm.*` nodes | `main`, `error` |
| All `service.*` nodes | `main`, `error` |

---

## Appendix B — Config Field Resolution Order

When the engine processes a node, config field values are resolved in this order before calling `NodeExecutor.Execute`:

1. **Expression evaluation** — all string fields containing `{{` are evaluated by `ExpressionEngine.ResolveConfig` using the current item context.
2. **Credential injection** — if `credential_id` is present, the referenced credential is decrypted and injected as `config["credential"]`.
3. **Validation** — required fields that resolved to empty string cause immediate node failure with `ErrInvalidConfig`.
4. **Execute** — the resolved config is passed to `NodeExecutor.Execute`.

---

## Appendix C — Error Message Catalog

| Condition | Error Message |
|-----------|---------------|
| Workflow not found | `"workflow not found: <id>"` |
| Execution not running | `"execution '<id>' is not currently running"` |
| Execution not retryable | `"execution '<id>' cannot be retried (status: <status>)"` |
| Queue full | `"workflow execution queue is full; try again later"` |
| Cycle in DAG | `"workflow contains a cycle involving nodes: [<name1>, <name2>]"` |
| Unknown action type | `"node '<name>' references unknown action type: '<platform>/<type>'"` |
| Invalid cron | `"invalid cron expression '<expr>': <parse error>"` |
| Invalid timezone | `"invalid timezone '<value>': <parse error>"` |
| Webhook path conflict | `"webhook path '<path>' is already registered by workflow '<id>'"` |
| Node count exceeded | `"workflow exceeds maximum node limit of 200"` |
| Connection count exceeded | `"workflow exceeds maximum connection limit of 500"` |
| Required config empty | `"required config field '<field>' resolved to empty string"` |
| Workflow name empty | `"workflow name is required"` |
| Timeout | `"execution timed out after <N> seconds"` |
| Process restart | `"execution interrupted by process restart"` |
| Stop error node | configured message from `control.stop_error` node config |
