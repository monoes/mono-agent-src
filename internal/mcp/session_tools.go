package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// Master tools for coder sessions (docs/mastermind/plans/2026-10-26-coder-session-host.md, 3.2):
// a master session creates child sessions and controls its descendants. The caller is the
// session whose token this process holds (coderhost.SessionTokenEnv, put in the environment by
// the coder host for the turn that started this server): the tools take no caller argument, so
// a session cannot act as another one, and a call from anywhere else (no token, a forged or
// stale one, or one that names another profile than the token's) is refused.

const coderSessionIntro = "Coder-session tools: a master coder session creates child sessions and controls its descendants (never a peer, an ancestor or itself, except session_status). " +
	"The caller is the session this process runs for; there is no argument to choose it. Refusals: not_yours (the target is not your descendant), operator_only, limit, access, invalid_input, no_session (this is not a coder session's turn). "

// sessionCaller opens the store and identifies the caller from the token.
func (s *Server) sessionCaller(ctx context.Context) (*coderhost.Store, coderhost.Caller, error) {
	rt, err := s.runtime()
	if err != nil {
		return nil, coderhost.Caller{}, err
	}
	store := coderhost.NewStore(rt.db.DB)
	c, err := store.CallerFor(os.Getenv(coderhost.SessionTokenEnv), profileOf(ctx))
	if err != nil {
		return nil, coderhost.Caller{}, sessionToolErr(err)
	}
	return store, c, nil
}

// sessionToolErr gives a master-tool error its code, in the style of the task tools.
func sessionToolErr(err error) error {
	code := ""
	switch {
	case err == nil:
		return nil
	case errors.Is(err, coderhost.ErrSessionToken):
		code = "no_session"
	case errors.Is(err, coderhost.ErrNotYours):
		code = "not_yours"
	case errors.Is(err, coderhost.ErrOperatorOnly):
		code = "operator_only"
	case errors.Is(err, coderhost.ErrLimit), errors.Is(err, coderhost.ErrQueueFull):
		code = "limit"
	case errors.Is(err, coderhost.ErrAccess):
		code = "access"
	case errors.Is(err, coderhost.ErrInvalid):
		code = "invalid_input"
	default:
		return err
	}
	return codedErr{text: code + ": " + err.Error(), err: err}
}

func sessionTarget() map[string]interface{} {
	return strParam("The descendant session's conversation id, from session_list")
}

func sessionTools() []tool {
	ro := map[string]bool{"readOnlyHint": true, "idempotentHint": true}
	write := map[string]bool{"readOnlyHint": false, "destructiveHint": false}
	byTarget := func(name, desc string, mutating bool, h toolHandler) tool {
		t := tool{name: name, description: coderSessionIntro + desc, mutating: mutating, annotations: write, handler: h,
			schema: objSchema(map[string]interface{}{"session": sessionTarget()}, "session")}
		if !mutating {
			t.annotations = ro
		}
		return t
	}
	return []tool{
		{
			name: "session_spawn", mutating: true, annotations: write, handler: toolSessionSpawn,
			description: coderSessionIntro + "Creates a child session of this master: same runtime, model and folder (or a folder inside it), access at most yours and never full, " +
				"a budget carved out of your remaining one. Limited by max_children, max_depth and a spawn rate. Returns {conversationId, access, cwd, budgetUsd, commandId, turnId}. " +
				"The child's first message (optional) starts its work once the coder host picks it up.",
			schema: objSchema(map[string]interface{}{
				"message":    strParam("The child's first message"),
				"access":     strParam("coding or workspace, at most your own (default: yours, capped at workspace)"),
				"cwd":        strParam("A folder inside yours (default: yours)"),
				"budget_usd": map[string]interface{}{"type": "number", "description": "Spend cap for the child, carved out of your remaining budget (required when you have a budget)"},
			}),
		},
		{
			name: "session_list", annotations: ro, handler: toolSessionList,
			description: coderSessionIntro + "Lists your descendants with their state, access, folder, budget, waiting commands and last turn: {sessions}. Records of the host only, never text an agent wrote.",
			schema:      objSchema(nil),
		},
		byTarget("session_status", "Status of your own session or of a descendant (state, last turn, budget carved and left, settings).", false, toolSessionStatus),
		{
			name: "session_send", mutating: true, annotations: write, handler: toolSessionSend,
			description: coderSessionIntro + "Queues a message for a descendant; it runs as a turn when the session is free (messages wait in order). It is not a person's message: it can never confirm a mission, " +
				"and after a run of host-made turns with no person in between the session waits for one. Returns {commandId, turnId}.",
			schema: objSchema(map[string]interface{}{"session": sessionTarget(), "message": strParam("What to tell the session")}, "session", "message"),
		},
		byTarget("session_pause", "Starts no new turn in a descendant until session_resume (a running turn finishes).", true, toolSessionControl(coderhost.KindPause)),
		byTarget("session_resume", "Lifts a pause of a descendant.", true, toolSessionControl(coderhost.KindResume)),
		byTarget("session_stop", "Stops a descendant's running turn and drops the messages queued before the stop.", true, toolSessionControl(coderhost.KindStop)),
		{
			name: "session_set", mutating: true, annotations: write, handler: toolSessionSet,
			description: coderSessionIntro + "Changes agent-adjustable settings of a descendant: " + joinNames(coderhost.AgentAdjustable) +
				". Every other setting (limits, autonomy, who may message it, role, access, budget, unattended) is the operator's and is refused. Returns {changed, settings}.",
			schema: objSchema(map[string]interface{}{
				"session": sessionTarget(),
				"patch":   map[string]interface{}{"type": "object", "description": "The settings to change, by name; the others keep their value"},
			}, "session", "patch"),
		},
	}
}

func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}

func toolSessionSpawn(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		Message   string  `json:"message"`
		Access    string  `json:"access"`
		Cwd       string  `json:"cwd"`
		BudgetUSD float64 `json:"budget_usd"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	store, c, err := s.sessionCaller(ctx)
	if err != nil {
		return nil, err
	}
	out, err := store.Spawn(c, coderhost.SpawnRequest{Message: a.Message, Access: a.Access, Cwd: a.Cwd, BudgetUSD: a.BudgetUSD})
	if err != nil {
		return nil, sessionToolErr(err)
	}
	return out, nil
}

func toolSessionList(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct{}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	store, c, err := s.sessionCaller(ctx)
	if err != nil {
		return nil, err
	}
	kids, err := store.ListChildren(c)
	if err != nil {
		return nil, sessionToolErr(err)
	}
	return map[string]any{"sessions": kids}, nil
}

type sessionTargetArgs struct {
	Session string `json:"session"`
}

func toolSessionStatus(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a sessionTargetArgs
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	store, c, err := s.sessionCaller(ctx)
	if err != nil {
		return nil, err
	}
	st, err := store.StatusOf(c, a.Session)
	if err != nil {
		return nil, sessionToolErr(err)
	}
	return st, nil
}

func toolSessionSend(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		Session string `json:"session"`
		Message string `json:"message"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	store, c, err := s.sessionCaller(ctx)
	if err != nil {
		return nil, err
	}
	cmd, err := store.SendToChild(c, a.Session, a.Message)
	if err != nil {
		return nil, sessionToolErr(err)
	}
	return map[string]any{"commandId": cmd.ID, "turnId": cmd.TurnID, "session": cmd.ConversationID}, nil
}

func toolSessionControl(kind coderhost.Kind) toolHandler {
	return func(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
		var a sessionTargetArgs
		if err := decodeTaskArgs(args, &a); err != nil {
			return nil, err
		}
		store, c, err := s.sessionCaller(ctx)
		if err != nil {
			return nil, err
		}
		cmd, err := store.ControlChild(c, a.Session, kind)
		if err != nil {
			return nil, sessionToolErr(err)
		}
		return map[string]any{"commandId": cmd.ID, "kind": kind, "session": cmd.ConversationID}, nil
	}
}

func toolSessionSet(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		Session string          `json:"session"`
		Patch   json.RawMessage `json:"patch"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	store, c, err := s.sessionCaller(ctx)
	if err != nil {
		return nil, err
	}
	next, keys, err := store.SetChild(c, a.Session, a.Patch)
	if err != nil {
		return nil, sessionToolErr(err)
	}
	return map[string]any{"session": a.Session, "changed": keys, "settings": next}, nil
}
