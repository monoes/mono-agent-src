package ai

import (
	"errors"
	"time"
)

// ErrTurnActive is returned by DeleteConversation when a turn is still
// active — deletion while a supervisor may be mid-write would race the
// journal and could leave a running process pointed at a vanished
// conversation.
var ErrTurnActive = errors.New("chat: conversation has an active turn")

// ErrConversationNotFound / ErrTurnNotFound distinguish "doesn't exist or
// isn't yours" from a generic sql.ErrNoRows leaking scoping details to
// callers — every lookup here is profile-scoped, so a wrong profileID and a
// truly missing ID look identical on purpose.
var (
	ErrConversationNotFound = errors.New("chat: conversation not found")
	ErrTurnNotFound         = errors.New("chat: turn not found")
)

// ErrTurnOwnedByOtherInstance is returned by CreateTurn when the target
// conversation already has an active turn owned by a DIFFERENT, identified
// live app instance. Two GUI processes sharing one ~/.monoagent database
// must not each admit and run a turn against the same conversation —
// whichever finished last would otherwise silently overwrite the other's
// --resume session binding via BindConversationSession. See
// docs/mastermind/plans/2026-09-12-interactive-agent-chat-followups.md,
// "OwnerInstanceID is written and read back but never compared to anything".
var ErrTurnOwnedByOtherInstance = errors.New("chat: conversation has an active turn owned by another instance")

// Conversation is one server-created app-level chat conversation — see the
// plan's "three distinct identifiers" section. HistoryKey is the opaque
// provider model-context bucket; it is deliberately excluded from JSON so
// it never reaches the frontend or a Wails response.
type Conversation struct {
	ID              string `json:"id"`
	ProfileID       string `json:"profileId"`
	Backend         string `json:"backend"` // "agent", or "provider" for a read-only legacy conversation
	WorkflowContext string `json:"workflowContext"`
	RuntimeID       string `json:"runtimeId,omitempty"`
	ProviderID      string `json:"providerId,omitempty"`
	Model           string `json:"model,omitempty"`
	Effort          string `json:"effort,omitempty"`
	SessionID       string `json:"sessionId,omitempty"`
	// Mode is "assistant" or "coder". A coder conversation runs every turn
	// with full access inside Cwd, fixed at creation: Claude Code keys its
	// sessions by folder, so resuming needs the same one.
	Mode string `json:"mode"`
	Cwd  string `json:"cwd"`
	// OrgMode is a coder conversation's org: "solo" (the agent works
	// alone) or "dynamic" (it can spawn workers, #226).
	OrgMode    string `json:"orgMode,omitempty"`
	HistoryKey string `json:"-"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
}

// Turn is one client-registered turn within a conversation — the mutable
// projection the supervisor updates as a run progresses and finalizes.
type Turn struct {
	ID               string `json:"id"`
	ConversationID   string `json:"conversationId"`
	ProfileID        string `json:"profileId"`
	OwnerInstanceID  string `json:"-"`
	Prompt           string `json:"prompt"`
	Status           string `json:"status"` // active|completed|failed|cancelled|interrupted
	Reason           string `json:"reason,omitempty"`
	LastCommittedSeq int64  `json:"lastCommittedSeq"`
	CreatedAt        string `json:"createdAt"`
	UpdatedAt        string `json:"updatedAt"`
}

const turnStatusActive = "active"

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

// ConversationRecord is a Conversation as `monoagentcli chat history`
// prints it: snake_case keys, and no history key.
type ConversationRecord struct {
	ID              string `json:"id"`
	ProfileID       string `json:"profile_id"`
	Backend         string `json:"backend"`
	WorkflowContext string `json:"workflow_context"`
	RuntimeID       string `json:"runtime_id"`
	ProviderID      string `json:"provider_id"`
	Model           string `json:"model"`
	Effort          string `json:"effort,omitempty"`
	SessionID       string `json:"session_id"`
	Mode            string `json:"mode"`
	Cwd             string `json:"cwd"`
	OrgMode         string `json:"org_mode,omitempty"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

// Record converts c to its CLI shape.
func (c Conversation) Record() ConversationRecord {
	return ConversationRecord{
		ID: c.ID, ProfileID: c.ProfileID, Backend: c.Backend, WorkflowContext: c.WorkflowContext,
		RuntimeID: c.RuntimeID, ProviderID: c.ProviderID, Model: c.Model, Effort: c.Effort, SessionID: c.SessionID,
		Mode: c.Mode, Cwd: c.Cwd, OrgMode: c.OrgMode, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

// Conversation converts a CLI record back (HistoryKey stays empty).
func (r ConversationRecord) Conversation() Conversation {
	return Conversation{
		ID: r.ID, ProfileID: r.ProfileID, Backend: r.Backend, WorkflowContext: r.WorkflowContext,
		RuntimeID: r.RuntimeID, ProviderID: r.ProviderID, Model: r.Model, Effort: r.Effort, SessionID: r.SessionID,
		Mode: r.Mode, Cwd: r.Cwd, OrgMode: r.OrgMode, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// TurnRecord is a Turn as the CLI prints it. Unlike Turn's own JSON it
// carries the owner instance id, so the app can tell its own active turns
// from another window's.
type TurnRecord struct {
	ID               string `json:"id"`
	ConversationID   string `json:"conversation_id"`
	ProfileID        string `json:"profile_id"`
	OwnerInstanceID  string `json:"owner_instance_id"`
	Prompt           string `json:"prompt"`
	Status           string `json:"status"`
	Reason           string `json:"reason"`
	LastCommittedSeq int64  `json:"last_committed_seq"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

// Record converts t to its CLI shape.
func (t Turn) Record() TurnRecord {
	return TurnRecord{
		ID: t.ID, ConversationID: t.ConversationID, ProfileID: t.ProfileID, OwnerInstanceID: t.OwnerInstanceID,
		Prompt: t.Prompt, Status: t.Status, Reason: t.Reason, LastCommittedSeq: t.LastCommittedSeq,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

// Turn converts a CLI record back.
func (r TurnRecord) Turn() Turn {
	return Turn{
		ID: r.ID, ConversationID: r.ConversationID, ProfileID: r.ProfileID, OwnerInstanceID: r.OwnerInstanceID,
		Prompt: r.Prompt, Status: r.Status, Reason: r.Reason, LastCommittedSeq: r.LastCommittedSeq,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}
