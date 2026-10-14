// Package monomind is the mono-agent client for monomind's Agent Exec
// Protocol (doc/agent-exec-protocol.md in the monomind repo, v1/rev 5): the
// subprocess contract monoagentcli uses to delegate every AI interaction to
// a locally-installed monomind, which in turn drives the installed agent
// CLIs. mono-agent never learns agent-CLI wire formats — only this protocol.
package monomind

import (
	"encoding/json"
)

// Protocol version implemented by this client (handshake-checked).
const ProtocolVersion = 1

// MinMonomindVersion is the minimum monomind release this client requires.
const MinMonomindVersion = "2.10.0"

// RequiredCapabilities are the handshake capabilities mono-agent relies on.
var RequiredCapabilities = []string{"agent-exec", "agent-scan", "org-json-v1"}

// Event is one NDJSON event from `monomind agent exec` (protocol §3.2).
// Exactly one Type* constant is set in Type; unknown event types are
// ignored per the spec's forward-compatibility rule.
type Event struct {
	V    int    `json:"v"`
	Type string `json:"type"`

	// start
	Runtime string `json:"runtime,omitempty"`
	Model   string `json:"model,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	Resume  string `json:"resume,omitempty"`
	Pid     int    `json:"pid,omitempty"`
	// StreamsIncrementally (protocol rev 5): whether this runtime delivers
	// real incremental `assistant` text as a turn streams, vs. only ever a
	// complete message at a step/turn boundary. Deliberately no
	// `omitempty` — unlike the other `start` fields, `false` is a real,
	// common, meaningful value here (most runtimes don't stream), and
	// Event has its own MarshalJSON below; `omitempty` on a bool drops the
	// key entirely on `false`, which would silently defeat any caller that
	// re-encodes a decoded Event (see ScanEntry's own comment on the same
	// hazard, which is actually exercised via ScanAgentRuntimes' re-marshal).
	StreamsIncrementally bool `json:"streams_incrementally"`

	// session
	SessionID string `json:"session_id,omitempty"`

	// assistant
	Text string `json:"text,omitempty"`

	// tool_call / tool_result
	ID     string          `json:"id,omitempty"`
	Name   string          `json:"name,omitempty"`
	Args   json.RawMessage `json:"args,omitempty"`
	OK     *bool           `json:"ok,omitempty"`
	Result *struct {
		Text string `json:"text"`
	} `json:"result,omitempty"`

	// usage / result. Plain scalars (not pointers) so existing Go consumers
	// that compare them directly (e.g. fixtures_test.go's
	// result.CostUSD != 0.0041) keep compiling and working unchanged — the
	// plan is explicit that a metric's presence must be preserved without
	// "changing every scalar to a pointer". Presence is tracked separately
	// in HasInputTokens/HasOutputTokens/HasCostUSD by UnmarshalJSON/
	// MarshalJSON below: a metric reported as exactly 0 is not the same as
	// a metric never reported at all (e.g. cost is genuinely unavailable,
	// not $0).
	InputTokens  int64   `json:"input_tokens,omitempty"`
	OutputTokens int64   `json:"output_tokens,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`

	// HasInputTokens/HasOutputTokens/HasCostUSD are set by UnmarshalJSON to
	// whether the corresponding key was present in the decoded JSON object.
	// Not part of the wire format themselves (see eventJSON).
	HasInputTokens  bool `json:"-"`
	HasOutputTokens bool `json:"-"`
	HasCostUSD      bool `json:"-"`

	// result
	Subtype    string `json:"subtype,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`

	// error
	Code       string `json:"code,omitempty"`
	ErrMessage string `json:"message,omitempty"`
	Fatal      bool   `json:"fatal,omitempty"`

	// done; also a shell tool_activity end's exit status (HasExitCode
	// says whether it was reported, as 0 is a real value there).
	ExitCode    int  `json:"exit_code,omitempty"`
	HasExitCode bool `json:"-"`

	CoderFields
	// start: the sandbox the turn runs in (see sandbox.go).
	SandboxFields
	SubagentFields
	ResultFields
}

// ResultFields are a `result` event's visibility fields (protocol rev 30,
// monomind 2.24.4): what the request actually ran on beside what was
// selected. All optional; older monomind never sends them.
type ResultFields struct {
	// ModelUsage is per model served, child agents included.
	ModelUsage map[string]ModelUsage `json:"model_usage,omitempty"`
	// Effort is the effort the request was sent with.
	Effort string `json:"effort,omitempty"`
	// PeakContextTokens is the largest main-thread context sent in one call.
	PeakContextTokens int64 `json:"peak_context_tokens,omitempty"`
	// ContextWarning: the peak is large enough that every further call
	// re-reads an expensive history.
	ContextWarning bool `json:"context_warning,omitempty"`
	// UnexpectedModels are served models that are not the selected one.
	UnexpectedModels []string `json:"unexpected_models,omitempty"`
	// AgentLaunches counts agent launches this turn (coder mode, claude).
	AgentLaunches *AgentLaunches `json:"agent_launches,omitempty"`
}

// ModelUsage is one model's token usage in ResultFields.ModelUsage.
type ModelUsage struct {
	Input         int64 `json:"input"`
	Output        int64 `json:"output"`
	CacheRead     int64 `json:"cache_read"`
	CacheCreation int64 `json:"cache_creation"`
}

// AgentLaunches counts a turn's agent launches, reviews counted apart.
type AgentLaunches struct {
	Total  int `json:"total"`
	Review int `json:"review"`
}

// CoderFields are the events and fields full-access ("coder") turns add to
// the protocol (monomind#355/#356/#357/#359). Every one is additive: older
// monomind never sends them and older callers ignore them.
type CoderFields struct {
	// start: "scoped" or "full"
	Access string `json:"access,omitempty"`

	// tool_activity: one of the runner's own tools (Bash, Edit, …), not a
	// caller tool. Phase is "start" or "end"; status reuses Phase for
	// "initializing"/"ready".
	Phase string `json:"phase,omitempty"`
	// Kind (tool_activity start) is the normalized tool kind: shell, edit,
	// write, read, search, web, mcp, task, todo, patch or other. Input then
	// uses that kind's canonical keys; Name stays the runtime's own.
	Kind            string          `json:"kind,omitempty"`
	Input           json.RawMessage `json:"input,omitempty"`
	InputTruncated  bool            `json:"input_truncated,omitempty"`
	Output          string          `json:"output,omitempty"`
	OutputTruncated bool            `json:"output_truncated,omitempty"`
	DurationMs      int64           `json:"duration_ms,omitempty"`
	Denied          bool            `json:"denied,omitempty"`
	Cancelled       bool            `json:"cancelled,omitempty"`
	ParentToolUseID string          `json:"parent_tool_use_id,omitempty"`

	// status
	MCPServers []MCPServerStatus `json:"mcp_servers,omitempty"`

	// done: processes the turn started that were still running when it
	// ended normally.
	BackgroundPids []int `json:"background_pids,omitempty"`
}

// SubagentFields are a `subagent` event's fields (monomind#387, protocol
// §3.2.1, capability agent-exec-subagent-events): one lifecycle step of a
// native subagent (claude's Task/Agent tool; synthesized from a task-kind
// call on other runtimes). Phase is "started", "progress" or "finished"
// and ID the subagent's own id; ToolUseID joins it to the tool_activity
// call that started it. A subagent's own text is an assistant event whose
// ParentToolUseID is that call's id.
type SubagentFields struct {
	ToolUseID    string         `json:"tool_use_id,omitempty"`
	SubagentType string         `json:"subagent_type,omitempty"` // started
	Description  string         `json:"description,omitempty"`   // started
	Prompt       string         `json:"prompt,omitempty"`        // started
	Summary      string         `json:"summary,omitempty"`       // progress, finished
	LastTool     string         `json:"last_tool,omitempty"`     // progress
	Status       string         `json:"status,omitempty"`        // finished: completed, failed, stopped (denied when synthesized)
	Usage        *SubagentUsage `json:"usage,omitempty"`         // progress, finished; claude only
}

// SubagentUsage is a subagent's cumulative usage as the Agent SDK reports
// it: a token total, no input/output split and no cost.
type SubagentUsage struct {
	TotalTokens int64 `json:"total_tokens"`
	ToolUses    int   `json:"tool_uses"`
	DurationMs  int64 `json:"duration_ms"`
}

// MCPServerStatus is one entry of a status event's mcp_servers list.
type MCPServerStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// eventJSON mirrors Event's wire shape exactly, except the three optional
// numeric metrics are pointers purely so decoding can detect whether the
// key was present at all. It exists only inside Event's UnmarshalJSON/
// MarshalJSON below — see Event's own field comments for why the public
// struct keeps plain scalars instead of switching to this shape directly.
type eventJSON struct {
	V    int    `json:"v"`
	Type string `json:"type"`

	Runtime              string `json:"runtime,omitempty"`
	Model                string `json:"model,omitempty"`
	Cwd                  string `json:"cwd,omitempty"`
	Resume               string `json:"resume,omitempty"`
	Pid                  int    `json:"pid,omitempty"`
	StreamsIncrementally bool   `json:"streams_incrementally"`

	SessionID string `json:"session_id,omitempty"`

	Text string `json:"text,omitempty"`

	ID     string          `json:"id,omitempty"`
	Name   string          `json:"name,omitempty"`
	Args   json.RawMessage `json:"args,omitempty"`
	OK     *bool           `json:"ok,omitempty"`
	Result *struct {
		Text string `json:"text"`
	} `json:"result,omitempty"`

	InputTokens  *int64   `json:"input_tokens,omitempty"`
	OutputTokens *int64   `json:"output_tokens,omitempty"`
	CostUSD      *float64 `json:"cost_usd,omitempty"`

	Subtype    string `json:"subtype,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
	StopReason string `json:"stop_reason,omitempty"`

	Code       string `json:"code,omitempty"`
	ErrMessage string `json:"message,omitempty"`
	Fatal      bool   `json:"fatal,omitempty"`

	ExitCode *int `json:"exit_code,omitempty"`

	CoderFields
	// start: the sandbox the turn runs in (see sandbox.go).
	SandboxFields
	SubagentFields
	ResultFields
}

// UnmarshalJSON decodes the wire event and records, in HasInputTokens/
// HasOutputTokens/HasCostUSD, whether each optional metric key was actually
// present — a plain struct-tag decode into Event directly cannot preserve
// this distinction since a missing key and an explicit 0 both decode to the
// Go zero value.
func (e *Event) UnmarshalJSON(data []byte) error {
	var w eventJSON
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*e = Event{
		V: w.V, Type: w.Type,
		Runtime: w.Runtime, Model: w.Model, Cwd: w.Cwd, Resume: w.Resume, Pid: w.Pid,
		StreamsIncrementally: w.StreamsIncrementally,
		SessionID:            w.SessionID,
		Text:                 w.Text,
		ID:                   w.ID, Name: w.Name, Args: w.Args, OK: w.OK, Result: w.Result,
		Subtype: w.Subtype, IsError: w.IsError, StopReason: w.StopReason,
		Code: w.Code, ErrMessage: w.ErrMessage, Fatal: w.Fatal,
		CoderFields:    w.CoderFields,
		SandboxFields:  w.SandboxFields,
		SubagentFields: w.SubagentFields,
		ResultFields:   w.ResultFields,
	}
	if w.ExitCode != nil {
		e.ExitCode = *w.ExitCode
		e.HasExitCode = true
	}
	if w.InputTokens != nil {
		e.InputTokens = *w.InputTokens
		e.HasInputTokens = true
	}
	if w.OutputTokens != nil {
		e.OutputTokens = *w.OutputTokens
		e.HasOutputTokens = true
	}
	if w.CostUSD != nil {
		e.CostUSD = *w.CostUSD
		e.HasCostUSD = true
	}
	return nil
}

// MarshalJSON re-encodes the event, omitting a metric key entirely when its
// Has* flag is false — so an event that started as "cost never reported"
// serializes back the same way instead of gaining a fabricated 0. Nothing
// in this codebase currently re-serializes a decoded Event (verified: no
// json.Marshal call site takes one), but this keeps decode/encode
// symmetric, matching the plan's "preserve reported metric presence
// through CLI decoding/serialization" requirement.
func (e Event) MarshalJSON() ([]byte, error) {
	w := eventJSON{
		V: e.V, Type: e.Type,
		Runtime: e.Runtime, Model: e.Model, Cwd: e.Cwd, Resume: e.Resume, Pid: e.Pid,
		StreamsIncrementally: e.StreamsIncrementally,
		SessionID:            e.SessionID,
		Text:                 e.Text,
		ID:                   e.ID, Name: e.Name, Args: e.Args, OK: e.OK, Result: e.Result,
		Subtype: e.Subtype, IsError: e.IsError, StopReason: e.StopReason,
		Code: e.Code, ErrMessage: e.ErrMessage, Fatal: e.Fatal,
		CoderFields:    e.CoderFields,
		SandboxFields:  e.SandboxFields,
		SubagentFields: e.SubagentFields,
		ResultFields:   e.ResultFields,
	}
	if e.HasExitCode || e.ExitCode != 0 {
		w.ExitCode = &e.ExitCode
	}
	if e.HasInputTokens {
		w.InputTokens = &e.InputTokens
	}
	if e.HasOutputTokens {
		w.OutputTokens = &e.OutputTokens
	}
	if e.HasCostUSD {
		w.CostUSD = &e.CostUSD
	}
	return json.Marshal(w)
}

// Event types (protocol §3.2).
const (
	EventStart      = "start"
	EventSession    = "session"
	EventAssistant  = "assistant"
	EventToolCall   = "tool_call"
	EventToolResult = "tool_result"
	EventUsage      = "usage"
	EventResult     = "result"
	EventError      = "error"
	EventDone       = "done"

	EventToolActivity = "tool_activity" // runner-native tool call (monomind#357)
	EventStatus       = "status"        // runner startup progress (monomind#356)
	EventSubagent     = "subagent"      // native subagent lifecycle (monomind#387)
)

// Error codes (protocol §3.4). Unknown codes must be treated as non-fatal.
const (
	ErrAuth  = "auth"
	ErrQuota = "quota"
	// ErrRateLimited is a transient 429 that agent exec already retried
	// (rev 20, up to 3 attempts); quota is used-up credits and never retried.
	ErrRateLimited   = "rate-limited"
	ErrMissingBinary = "missing-binary"
	ErrNoRunner      = "no-runner"
	ErrBudget        = "budget"
	ErrRunnerError   = "runner-error"
	ErrTimeout       = "timeout"
	ErrCancelled     = "cancelled"
	ErrBadFrame      = "bad-frame"
)

// Stop reasons (protocol §3.2 result.stop_reason).
const (
	StopEndTurn      = "end_turn"
	StopMaxTurns     = "max_turns"
	StopToolRoundCap = "tool_round_cap"
	StopCancelled    = "cancelled"
	StopTimeout      = "timeout"
)

// ProtocolError is a terminal `error` event from an exec turn.
type ProtocolError struct {
	Code    string
	Message string
	Fatal   bool
	// ExitCode is the process exit code that accompanied the error, when
	// the turn terminated without a success result.
	ExitCode int
}

func (e *ProtocolError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// ToolSpec is a tool definition for --tools-file (protocol §4.1): JSON
// Schema {type:"object", properties, required}.
type ToolSpec struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Schema      map[string]interface{} `json:"schema,omitempty"`
}

// VersionInfo is the `monomind --version --json` handshake payload (§2).
type VersionInfo struct {
	V            int      `json:"v"`
	Version      string   `json:"version"`
	MinCaller    string   `json:"min_caller"`
	Capabilities []string `json:"capabilities"`
}

// HasCapability reports whether the handshake advertised the capability.
func (vi *VersionInfo) HasCapability(cap string) bool {
	for _, c := range vi.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}
