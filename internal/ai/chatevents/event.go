// Package chatevents defines the GUI chat journal's event contract — pure
// types, envelope construction, and (in normalize.go) protocol-to-event
// normalization. No Wails imports, no database connection, no dependency on
// the parent ai package: it is a leaf package both internal/ai (the store)
// and its users (cmd/monoagentcli, which journals turns, and wails-app,
// which relays them) import without a cycle.
//
// See docs/mastermind/plans/2026-09-11-interactive-agent-chat.md §"Event
// contract" for the authoritative field-by-field spec this file implements.
package chatevents

import (
	"encoding/json"
	"fmt"
	"time"
)

// Version is the event envelope's schema version (the "version" field on
// every Event). Bump only on a breaking payload-shape change.
const Version = 1

// EventType names one of the fixed set of GUI chat journal event types.
type EventType string

const (
	EventTurnStarted    EventType = "turn.started"
	EventSessionBound   EventType = "session.bound"
	EventAssistantDelta EventType = "assistant.delta"
	EventToolStarted    EventType = "tool.started"
	EventToolCompleted  EventType = "tool.completed"
	EventUsageUpdated   EventType = "usage.updated"
	EventNotice         EventType = "notice"
	EventTurnFinished   EventType = "turn.finished"

	// Dynamic org (monoes/mono-agent#226): the workers a coder chat's lead
	// agent spawns. Their tool calls reuse tool.started/tool.completed with
	// AgentID set; the lead's own events carry no AgentID.
	EventAgentSpawned    EventType = "agent.spawned"
	EventAgentStatus     EventType = "agent.status"
	EventAgentMessage    EventType = "agent.message"
	EventAgentReassigned EventType = "agent.reassigned"
	EventAgentFinished   EventType = "agent.finished"

	// Missions (docs/mastermind/plans/2026-10-25-coder-missions-task-sessions.md):
	// the coder lead proposes a mission for the user to confirm, and shows
	// its draft plan as a card the operator approves.
	EventMissionPropose EventType = "mission.propose"
	EventMissionPlan    EventType = "mission.plan"
	// EventMissionReport is the final report of a finished mission.
	EventMissionReport EventType = "mission.report"
)

// Event is one journaled/emitted line — the outer envelope. Payload is kept
// as raw JSON so the envelope itself never needs to know every payload
// shape; callers marshal/unmarshal it via the typed Payload* structs below,
// keyed by Type.
type Event struct {
	Version        int             `json:"version"`
	ProfileID      string          `json:"profileId"`
	ConversationID string          `json:"conversationId"`
	TurnID         string          `json:"turnId"`
	Seq            int64           `json:"seq"`
	At             string          `json:"at"` // RFC3339 with millisecond precision, UTC
	Type           EventType       `json:"type"`
	Payload        json.RawMessage `json:"payload"`
}

// FormatAt renders t as the millisecond-precision UTC timestamp the event
// contract uses for "at" (e.g. "2026-09-11T09:00:00.000Z").
func FormatAt(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// MaxSafeSeq is the largest Seq value guaranteed to round-trip exactly
// through JSON into a JavaScript Number (2^53-1, Number.MAX_SAFE_INTEGER).
// The Wails bridge serializes Seq as a plain JSON number and the frontend
// reducer does numeric seq comparisons on whatever JSON.parse hands back, so
// a larger value risks silent precision loss. Real per-turn seq values are
// small monotonic counters far below this; MaxSafeSeq exists as a sentinel
// for an event that was never allocated a real seq by the store (e.g.
// a turn.finished whose FinalizeTurn write itself
// fails) but must still compare as newer than anything the frontend has
// already applied for its turn.
const MaxSafeSeq int64 = (1 << 53) - 1

// New builds a complete Event by marshaling payload into the envelope. seq
// and at are supplied by the caller (the store allocates seq transactionally;
// see internal/ai/chat_events.go) rather than computed here, since this
// package has no database connection and must stay pure/deterministic.
func New(profileID, conversationID, turnID string, seq int64, at time.Time, typ EventType, payload any) (Event, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("chatevents: marshal %s payload: %w", typ, err)
	}
	return Event{
		Version:        Version,
		ProfileID:      profileID,
		ConversationID: conversationID,
		TurnID:         turnID,
		Seq:            seq,
		At:             FormatAt(at),
		Type:           typ,
		Payload:        b,
	}, nil
}

// ── Payload types, one per EventType ────────────────────────────────────

// TurnStartedPayload is turn.started's payload — sequence 1 for a turn,
// emitted before the runtime process is even launched, so a crash before
// any other event still leaves a record of what was asked. Turns recorded
// by the removed provider backend carry backend "provider" and a
// "provider" key this struct no longer reads.
type TurnStartedPayload struct {
	Backend string `json:"backend"` // "agent"
	Runtime string `json:"runtime,omitempty"`
	Model   string `json:"model,omitempty"`
	Text    string `json:"text"` // the accepted user message
	// Attachments are the files attached to a coder turn (`chat --attach`),
	// as copied into its folder; absent on every other turn.
	Attachments []TurnAttachment `json:"attachments,omitempty"`
}

// TurnAttachment is one file attached to a turn: its name, the copy the
// agent reads, its media type and its size in bytes.
type TurnAttachment struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
}

// SessionBoundPayload is session.bound's payload — the runtime-assigned
// resumable session id, used only for that runtime's own --resume. Absence
// of this event for a conversation must not prevent it from appearing in
// history; a session binds only once the runtime actually reports one.
type SessionBoundPayload struct {
	Runtime   string `json:"runtime"`
	SessionID string `json:"sessionId"`
}

// AssistantDeltaPayload is assistant.delta's payload. PartID is a locally
// assigned identifier for one contiguous run of assistant prose — adjacent
// deltas sharing a PartID join into one growing text block; a tool-start
// event closes the current part and any further assistant text opens a new
// PartID, so text/tool ordering survives even though tool events flow on a
// separate lane.
type AssistantDeltaPayload struct {
	// AgentID is the dynamic-org worker that wrote the text (#258); "" for
	// the lead. A worker's part ids are its own ("w1:p1").
	AgentID string `json:"agentId,omitempty"`
	PartID  string `json:"partId"`
	Text    string `json:"text"`
}

// ToolStartedPayload is tool.started's payload — one call, appended once as
// a new timeline step. CallID (not name, not array position) is the only
// stable handle a later tool.completed uses to update this same step:
// "Tool identity is (turnId,callId), never array position or name."
type ToolStartedPayload struct {
	// AgentID is the dynamic-org worker that made the call; "" for the lead.
	AgentID   string          `json:"agentId,omitempty"`
	CallID    string          `json:"callId"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	// Native marks one of the agent's own tools (Bash, Edit, …) in a coder
	// turn; ParentCallID nests a call made inside a subagent call.
	Native       bool   `json:"native,omitempty"`
	ParentCallID string `json:"parentCallId,omitempty"`
	// FileExisted says, for a file-writing tool, whether its target existed
	// when the call started (a Write that creates vs. overwrites).
	FileExisted *bool `json:"fileExisted,omitempty"`
	// Kind is a native call's normalized tool kind (shell, edit, write,
	// read, search, web, mcp, task, todo, patch, other); Arguments then use
	// that kind's canonical keys. "" when unknown.
	Kind string `json:"kind,omitempty"`
}

// ToolCompletedPayload is tool.completed's payload. OK is nullable (some
// adapters never report explicit success/failure); Result is always
// present once this event fires — an empty string is a valid result, not a
// missing one, so it is a plain string rather than a pointer.
type ToolCompletedPayload struct {
	AgentID    string `json:"agentId,omitempty"`
	CallID     string `json:"callId"`
	OK         *bool  `json:"ok"`
	Result     string `json:"result"`
	Truncated  bool   `json:"truncated,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
	Denied     bool   `json:"denied,omitempty"`
	Cancelled  bool   `json:"cancelled,omitempty"`
	// ExitCode is a shell command's exit status, when known.
	ExitCode *int `json:"exitCode,omitempty"`
}

// UsageUpdatedPayload is usage.updated's payload. Every metric is nullable
// and independently optional — "missing cost means unavailable, not $0" —
// so each is a pointer, never defaulted to zero. Source names which
// underlying protocol event reported this snapshot (e.g. "assistant" |
// "result"), since usage semantics differ by source and must not be summed
// across sources without verified delta semantics.
type UsageUpdatedPayload struct {
	// AgentID is the dynamic-org worker this snapshot is for (#257): its
	// running total across its execs so far. "" is the lead's own usage;
	// a worker's never counts toward it.
	AgentID      string   `json:"agentId,omitempty"`
	InputTokens  *int64   `json:"inputTokens"`
	OutputTokens *int64   `json:"outputTokens"`
	CostUSD      *float64 `json:"costUsd"`
	// CostEstimated: a worker's cost is (partly) estimated from its tokens,
	// because its runtime reports none (#230).
	CostEstimated bool   `json:"costEstimated,omitempty"`
	Source        string `json:"source"`
}

// NoticeSeverity classifies a notice event for display — a warning/info
// notice must never erase already-observed work the way a fatal error does.
type NoticeSeverity string

const (
	SeverityInfo    NoticeSeverity = "info"
	SeverityWarning NoticeSeverity = "warning"
	SeverityError   NoticeSeverity = "error"
)

// NoticePayload is notice's payload — a nonfatal condition worth surfacing
// (a malformed tool-call fence, a stale-stop, a persistence warning) without
// terminating the turn.
type NoticePayload struct {
	Code     string         `json:"code"`
	Message  string         `json:"message"`
	Severity NoticeSeverity `json:"severity"`
	// Pids and Processes list the processes a coder turn left running
	// (coder.background): Processes adds each one's identity at the time,
	// so a later stop never hits a reused pid.
	Pids      []int        `json:"pids,omitempty"`
	Processes []ProcessRef `json:"processes,omitempty"`
}

// ProcessRef is a pid plus what identified that process when recorded.
type ProcessRef struct {
	Pid      int    `json:"pid"`
	Identity string `json:"identity,omitempty"`
	Command  string `json:"command,omitempty"`
}

// TurnStatus is turn.finished's terminal classification — see the
// supervisor's precedence rules (Stop > fatal error > missing terminal
// evidence > completed) in ComputeTurnStatus.
type TurnStatus string

const (
	StatusCompleted   TurnStatus = "completed"
	StatusFailed      TurnStatus = "failed"
	StatusCancelled   TurnStatus = "cancelled"
	StatusInterrupted TurnStatus = "interrupted"
)

// TurnFinishedPayload is turn.finished's payload — emitted exactly once per
// turn, after the supervisor has fully determined the outcome. ExitCode is
// nullable since a provider-backend turn has no process exit code at all.
// HistorySaved false means the live transcript is authoritative for this
// turn but durable persistence fell behind (see chat_events.go's
// last_committed_seq / historySaved semantics).
type TurnFinishedPayload struct {
	Status TurnStatus `json:"status"`
	Reason string     `json:"reason,omitempty"`
	// Code classifies a failure the UI can act on: "agent_not_setup"
	// (monomind.AgentNotSetupCode) when the AI agent is not installed or not
	// logged in. Empty otherwise.
	Code         string `json:"code,omitempty"`
	ExitCode     *int   `json:"exitCode"`
	HistorySaved bool   `json:"historySaved"`
	// Sandbox is the sandbox the turn ran in (monomind.SandboxStatus*:
	// "sandboxed", "unsupported", "needs-monomind", "off"); empty when the
	// turn asked for none (coder mode) or never got that far.
	Sandbox string `json:"sandbox,omitempty"`
}

// Record is an Event as `monoagentcli chat` prints it: a snake_case
// envelope around the payload, which is passed through verbatim (it is the
// stored, versioned payload, keyed as the payload types above define).
type Record struct {
	Version        int             `json:"version"`
	ProfileID      string          `json:"profile_id"`
	ConversationID string          `json:"conversation_id"`
	TurnID         string          `json:"turn_id"`
	Seq            int64           `json:"seq"`
	At             string          `json:"at"`
	Type           EventType       `json:"type"`
	Payload        json.RawMessage `json:"payload"`
}

// Record converts e to its CLI shape.
func (e Event) Record() Record {
	return Record{
		Version: e.Version, ProfileID: e.ProfileID, ConversationID: e.ConversationID, TurnID: e.TurnID,
		Seq: e.Seq, At: e.At, Type: e.Type, Payload: e.Payload,
	}
}

// Event converts a CLI record back.
func (r Record) Event() Event {
	return Event{
		Version: r.Version, ProfileID: r.ProfileID, ConversationID: r.ConversationID, TurnID: r.TurnID,
		Seq: r.Seq, At: r.At, Type: r.Type, Payload: r.Payload,
	}
}

// AgentSpawnedPayload is agent.spawned's payload: a worker the lead added,
// with how it was staffed. Why says who chose what ("lead chose the model;
// role from pick"). The confidences are nil when that step didn't ask.
type AgentSpawnedPayload struct {
	AgentID  string `json:"agentId"`
	ParentID string `json:"parentId,omitempty"` // "" = the lead
	// Veteran: a worker of an earlier turn of the conversation, loaded
	// idle for the lead to message (#230). AllowSpawn: the lead let it
	// spawn sub-workers.
	Veteran    bool     `json:"veteran,omitempty"`
	AllowSpawn bool     `json:"allowSpawn,omitempty"`
	Role       string   `json:"role"`
	AgentType  string   `json:"agentType,omitempty"` // monomind agent id, or "native" for a Claude subagent
	Skills     []string `json:"skills,omitempty"`
	Runtime    string   `json:"runtime,omitempty"`
	Model      string   `json:"model,omitempty"`
	Effort     string   `json:"effort,omitempty"`
	Access     string   `json:"access,omitempty"` // coding, qa, automation, research
	// Fidelity is the runtime's tool-activity fidelity (#259): "full",
	// "start-only" (tool starts, never their ends) or "none"; "" unknown.
	Fidelity       string   `json:"fidelity,omitempty"`
	Brief          string   `json:"brief,omitempty"`
	Why            string   `json:"why,omitempty"`
	PickConfidence *float64 `json:"pickConfidence,omitempty"`
	JevConfidence  *float64 `json:"jevConfidence,omitempty"`
	// Branch is the git branch an isolated writer works on (#230).
	Branch string `json:"branch,omitempty"`
}

// Worker statuses (agent.status's To).
const (
	AgentQueued       = "queued"
	AgentStarting     = "starting"
	AgentWorking      = "working"
	AgentWaitingLease = "waiting_lease"
	AgentWaitingUser  = "waiting_user" // asked the user a question (#256)
	AgentIdle         = "idle"
	AgentDone         = "done"
	AgentFailed       = "failed"
	AgentCancelled    = "cancelled"
)

// AgentStatusPayload is agent.status's payload.
type AgentStatusPayload struct {
	AgentID string `json:"agentId"`
	From    string `json:"from,omitempty"`
	To      string `json:"to"`
	Detail  string `json:"detail,omitempty"` // e.g. which lease it waits for
	// Leases are the leases the worker holds at this status ("write",
	// "browser"): the org stage shows who holds the pen and the browser
	// from them (#228).
	Leases []string `json:"leases,omitempty"`
	// Branch is the git branch an isolated writer works on (#230).
	Branch string `json:"branch,omitempty"`
	// Confinement says what keeps a research worker from editing on its
	// current run (Confinement* constants); "" for the other profiles.
	Confinement string `json:"confinement,omitempty"`
}

// A research worker's confinement (#230): agent exec --access read, else a
// read-only sandbox, else the prompt fallback for an older monomind or a
// runtime with neither — full access, told to stay read-only, holding the
// write lease so it never runs beside a writer.
const (
	ConfinementAccessRead = "access-read"
	ConfinementSandbox    = "sandbox-read-only"
	ConfinementWriteLease = "write-lease"
)

// AgentMessagePayload is agent.message's payload: a brief, a result, a
// follow-up or a question passing between the lead and a worker.
type AgentMessagePayload struct {
	AgentID   string `json:"agentId"`
	Direction string `json:"direction"` // brief | result | followup | question
	// QuestionID names a worker's question ("q1") and, on the user's
	// answer (a followup from "user"), the question it answers.
	QuestionID string `json:"questionId,omitempty"`
	From       string `json:"from"`
	To         string `json:"to"`
	Text       string `json:"text"`
	Truncated  bool   `json:"truncated,omitempty"`
}

// AgentReassignedPayload is agent.reassigned's payload: the chosen model
// could not run (sign-in, quota, unknown model) and the next one took over.
type AgentReassignedPayload struct {
	AgentID     string `json:"agentId"`
	FromRuntime string `json:"fromRuntime"`
	FromModel   string `json:"fromModel"`
	ToRuntime   string `json:"toRuntime"`
	ToModel     string `json:"toModel"`
	Reason      string `json:"reason"`
	// Fidelity is the new runtime's tool-activity fidelity, as in
	// agent.spawned.
	Fidelity string `json:"fidelity,omitempty"`
}

// AgentFinishedPayload is agent.finished's payload.
type AgentFinishedPayload struct {
	AgentID       string   `json:"agentId"`
	Outcome       string   `json:"outcome"` // done | failed | cancelled
	Summary       string   `json:"summary,omitempty"`
	InputTokens   *int64   `json:"inputTokens,omitempty"`
	OutputTokens  *int64   `json:"outputTokens,omitempty"`
	CostUSD       *float64 `json:"costUsd,omitempty"`
	CostEstimated bool     `json:"costEstimated,omitempty"`
	DurationMs    int64    `json:"durationMs,omitempty"`
	FilesChanged  []string `json:"filesChanged,omitempty"`
}

// MissionProposePayload is mission.propose's payload: the lead thinks the
// goal is big enough for a planned mission and asks the user to confirm it
// with one click.
type MissionProposePayload struct {
	Title  string `json:"title"`
	Goal   string `json:"goal"`
	Reason string `json:"reason,omitempty"`
}

// MissionPlanPayload is mission.plan's payload, the plan card: the draft
// session the operator can approve. It is emitted again, with the same
// SessionID, each time the plan changes; the latest one is the card.
type MissionPlanPayload struct {
	SessionID int64  `json:"sessionId"`
	Title     string `json:"title"`
	Goal      string `json:"goal"`
	TaskCount int    `json:"taskCount"`
	// EstimatedBudgetUSD is a rough estimate from the tasks' size labels.
	EstimatedBudgetUSD float64 `json:"estimatedBudgetUsd"`
	// Round counts the review rounds applied so far (0 = the first draft).
	Round int `json:"round"`
}

// MissionReportPayload is mission.report's payload: the final report of a mission
// whose tasks are all verified or done, emitted once when the session is closed as
// done. Report is the full text (markdown), also attached to the integration task as
// a report artifact.
type MissionReportPayload struct {
	SessionID int64  `json:"sessionId"`
	Title     string `json:"title"`
	State     string `json:"state"`
	TaskCount int    `json:"taskCount"`
	Done      int    `json:"done"`
	Verified  int    `json:"verified"`
	// Unfinished lists task ids that are neither done nor verified (empty when the
	// session closed as done).
	Unfinished []int64 `json:"unfinished"`
	// Artifacts counts the evidence artifacts across the mission's tasks.
	Artifacts int     `json:"artifacts"`
	SpentUSD  float64 `json:"spentUsd"`
	BudgetUSD float64 `json:"budgetUsd"`
	Report    string  `json:"report"`
}
