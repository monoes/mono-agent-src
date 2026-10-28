package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/taskbind"
)

// Lead tools for mission child sessions (phase P-C of docs/mastermind/plans/2026-10-27-mission-child-sessions.md):
// the lead of a mission marks tasks to run in a coder session of their own and sets its own ceiling. The caller is
// the session whose token this process holds, and the mission is that session's own (there is no session
// argument). The scope is built only by coderhost.NewLeadScope from the verified session token and the binding
// of this process as the database verifies it: a process that holds any task-bound binding (a worker or verifier
// of a mission, or a token that does not verify) or whose binding cannot be read is refused. Every caller that
// is not the lead of the task gets the same refusal, whether the task exists or not.
//
// What these tools never do: change a setting (max_children, max_concurrent, access, a budget), turn on mode all,
// lift an operator's floor or cap, start a session, approve, or touch a task row.

const missionChildIntro = "Mission child sessions: you lead a mission and may run some of its tasks in a coder session of their own. The caller is the session this process runs for; there is no argument to choose the mission. " +
	"Refusals: not_yours (not a task of a mission you lead, whatever the reason), operator_only, limit, invalid_input, no_session. A bound worker of a mission cannot use these tools. "

// maxMarkBudgetUSD caps the budget a lead may wish for one task's session, as the plan field does.
const maxMarkBudgetUSD = 50.0

// leadScope opens the store and builds the lead's scope. An unreadable binding is a bound one.
func (s *Server) leadScope(ctx context.Context) (*coderhost.Store, coderhost.LeadScope, string, error) {
	rt, err := s.runtime()
	if err != nil {
		return nil, coderhost.LeadScope{}, "", err
	}
	profile := profileOf(ctx)
	store := coderhost.NewStore(rt.db.DB)
	c, err := store.CallerFor(os.Getenv(coderhost.SessionTokenEnv), profile)
	if err != nil {
		return nil, coderhost.LeadScope{}, "", sessionToolErr(err)
	}
	binding := coderhost.BindingTask // fail closed: only a verified "no binding" passes
	if taskbind.Resolve(ctx, rt.db.DB, profile, os.Getenv(agenttokens.EnvMissionToken)).State == taskbind.None {
		binding = coderhost.BindingNone
	}
	return store, coderhost.NewLeadScope(c, binding), profile, nil
}

func missionChildTools() []tool {
	ro := map[string]bool{"readOnlyHint": true, "idempotentHint": true}
	write := map[string]bool{"readOnlyHint": false, "destructiveHint": false}
	return []tool{
		{
			name: "mission_child_mark", mutating: true, annotations: write, handler: toolMissionChildMark,
			description: missionChildIntro + "Marks a task of your mission to run in a coder session of its own. The mission's policy and ceiling still decide whether a session starts; " +
				"a mark in a draft mission changes nothing until the operator approves the plan. size is large (shown as a bubble) or small; budget_usd is the session's spend cap, carved from your own budget " +
				"(default: derived from your share). Marking a task whose session ended queues another try. Returns the task's row {taskId, state, size, budgetUsd, attempts}.",
			schema: objSchema(map[string]interface{}{
				"task_id":    intParam("The task of your mission"),
				"size":       strParam("large or small (optional)"),
				"budget_usd": map[string]interface{}{"type": "number", "description": "The session's spend cap in USD (optional)"},
			}, "task_id"),
		},
		{
			name: "mission_child_ceiling", mutating: true, annotations: write, handler: toolMissionChildCeiling,
			description: missionChildIntro + "Sets how many of your mission's sessions may run at once (max_children) and what share of your remaining budget they may carve (share_pct). " +
				"You may always lower them; you may raise them only up to the operator's limits (the person's own max_children setting, 80 percent, and any value the operator wrote for this mission). " +
				"Nothing here changes a setting, an access level or your own budget. Returns the policy.",
			schema: objSchema(map[string]interface{}{
				"max_children": intParam("Sessions of your mission that may run at once"),
				"share_pct":    intParam("Percent of your remaining budget the sessions may carve (at most 80)"),
			}),
		},
		{
			name: "mission_child_status", annotations: ro, handler: toolMissionChildStatus,
			description: missionChildIntro + "Reads your mission's child sessions: the policy, the room left, the counts, each marked task's row (numbers and states, never task text) and the newest audit rows.",
			schema:      objSchema(nil),
		},
	}
}

func toolMissionChildMark(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		TaskID    int64   `json:"task_id"`
		Size      string  `json:"size"`
		BudgetUSD float64 `json:"budget_usd"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	if a.BudgetUSD < 0 || a.BudgetUSD > maxMarkBudgetUSD || a.BudgetUSD != a.BudgetUSD {
		return nil, codedErr{text: fmt.Sprintf("invalid_input: budget_usd must be between 0 and %.0f", maxMarkBudgetUSD), err: coderhost.ErrInvalid}
	}
	store, scope, profile, err := s.leadScope(ctx)
	if err != nil {
		return nil, err
	}
	sid, err := store.LeadMission(scope, profile)
	if err != nil {
		return nil, sessionToolErr(err)
	}
	row, err := store.Mark(scope, profile, sid, a.TaskID, coderhost.MarkOptions{Size: a.Size, BudgetUSD: a.BudgetUSD})
	if err != nil {
		return nil, sessionToolErr(err)
	}
	out := map[string]any{"taskId": row.TaskID, "state": row.State, "size": row.Size, "budgetUsd": row.BudgetUSD, "attempts": row.Attempts}
	if !coderhost.MissionChildrenReady() {
		out["note"] = "the mark is stored, but sessions of their own are not available yet: the task runs in this turn as a worker"
	}
	return out, nil
}

func toolMissionChildCeiling(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		MaxChildren *int `json:"max_children"`
		SharePct    *int `json:"share_pct"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	store, scope, profile, err := s.leadScope(ctx)
	if err != nil {
		return nil, err
	}
	sid, err := store.LeadMission(scope, profile)
	if err != nil {
		return nil, sessionToolErr(err)
	}
	pol, err := store.SetPolicy(scope, profile, sid, coderhost.PolicyPatch{MaxChildren: a.MaxChildren, SharePct: a.SharePct})
	if err != nil {
		return nil, sessionToolErr(err)
	}
	return pol, nil
}

func toolMissionChildStatus(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct{}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	store, scope, profile, err := s.leadScope(ctx)
	if err != nil {
		return nil, err
	}
	st, err := store.LeadStatusOf(scope, profile)
	if err != nil {
		return nil, sessionToolErr(err)
	}
	return st, nil
}
