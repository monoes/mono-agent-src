package dynorg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/monomind"
)

// session_spawn for the lead (docs/mastermind/plans/2026-10-28-mission-research-directions.md, section 3): the lead of a
// mission, or of a direction, creates a child coder session with a named task session of its own below the lead's. The
// host does the work and holds every guard (the lead scope, the advisory, the depth, the caps, the access and budget
// never above the lead's, the decisions ledger); this file is the tool, offered with the lead's other mission tools and
// not to the workers and helpers the lead starts (childToolSpecs names the few tools those have).

// ToolMissionSessionSpawn is the lead's tool.
const ToolMissionSessionSpawn = "mission_session_spawn"

// Why a spawn was not made, beyond the host's own text.
var (
	// ErrSpawnAdvised: the work is doable in the lead's own session; repeat with a reason that applies.
	ErrSpawnAdvised = errors.New("doable in this session")
	// ErrSpawnDepth: the lead is at the working depth. The work runs in this turn with workers.
	ErrSpawnDepth = errors.New("at the working depth")
)

// SessionSpawnArgs are the tool's arguments.
type SessionSpawnArgs struct {
	Title     string  `json:"title"`
	Goal      string  `json:"goal"`
	Kind      string  `json:"kind"`
	Reason    string  `json:"reason"`
	TaskIDs   []int64 `json:"task_ids"`
	BudgetUSD float64 `json:"budget_usd"`
	Access    string  `json:"access"`
	Runtime   string  `json:"runtime"`
	Model     string  `json:"model"`
}

// SessionSpawnResult is what the host made.
type SessionSpawnResult struct {
	ConversationID string  `json:"conversation_id"`
	TaskSessionID  int64   `json:"task_session_id"`
	Kind           string  `json:"kind"`
	Title          string  `json:"title"`
	Depth          int     `json:"depth"`
	Access         string  `json:"access"`
	BudgetUSD      float64 `json:"budget_usd"`
	Note           string  `json:"note"`
}

// SessionSpawner is what the tool needs from the coder host; the ChildSpawner of a chat that is a coder session also
// is one. internal/cli/chat implements it over coderhost with the lead's verified session token.
type SessionSpawner interface {
	// SpawnSession errors wrap ErrSpawnAdvised or ErrSpawnDepth when that is why nothing was made.
	SpawnSession(ctx context.Context, a SessionSpawnArgs) (SessionSpawnResult, error)
}

func missionSessionSpawnSpec() monomind.ToolSpec {
	return monomind.ToolSpec{Name: ToolMissionSessionSpawn,
		Description: "Start a session of its own for part of the mission: a coder session (a whole conversation that works on its own) with a named task session below yours. " +
			"Spawn only when it pays: reason is required (parallel: independent work that can run at the same time; long_running; isolation: risky or destructive work that deserves its own writer, branch and verifier; " +
			"different_access_or_model; context_overflow). Work that one session can do (at most 3 open tasks of size s or smaller, one writer, no parallel branch) is refused once with \"doable in this session\": run it here, " +
			"or call again with a reason that applies. task_ids are the tasks of your session the new one takes; the size of the work is read from them. " +
			"A session at the working depth (default 3 levels, the operator's setting) cannot spawn: run the work with workers of this turn. The new session's access and budget never exceed yours, and its plan waits for the operator's approval. " +
			"Every spawn and refusal is written to your decisions ledger.",
		Schema: obj(map[string]any{
			"title":      str("The new session's name, in a line."),
			"goal":       str("What it must achieve; this is also its first message."),
			"kind":       str("direction, research or task."),
			"reason":     str("parallel, long_running, isolation, different_access_or_model or context_overflow."),
			"task_ids":   map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Tasks of your session the new one takes."},
			"budget_usd": map[string]any{"type": "number", "description": "Its spend cap, carved from your own budget."},
			"access":     str("Optional: coding or workspace, at most your own."),
			"runtime":    str("Optional: another runtime."),
			"model":      str("Optional: another model."),
		}, "title", "kind", "reason")}
}

// handleMissionSessionSpawn runs the tool.
func (c *Conductor) handleMissionSessionSpawn(ctx context.Context, args json.RawMessage) (string, error) {
	var a SessionSpawnArgs
	if err := json.Unmarshal(orEmpty(args), &a); err != nil {
		return "", fmt.Errorf("bad arguments: %v", err)
	}
	mc, err := c.missions()
	if err != nil {
		return "", err
	}
	if m := c.cfg.Mission; m != nil && !m.Planning() {
		return "", fmt.Errorf("missions are not on for this conversation: the user has not confirmed one")
	}
	sp, ok := mc.Children.(SessionSpawner)
	if !ok {
		return "", errors.New("this chat cannot start sessions of its own: run the work with workers of this turn")
	}
	sid, err := c.sessionOf(ctx, mc, 0)
	if err != nil {
		return "", err
	}
	res, err := sp.SpawnSession(ctx, a)
	if err != nil {
		if errors.Is(err, ErrSpawnDepth) {
			c.childFallbackNotice(sid, "this session is at the working depth, so it cannot start more sessions of its own")
		}
		return "", err
	}
	res.Note = "its plan is a draft until the operator approves it; the first message is queued for the host"
	return marshal(res)
}
