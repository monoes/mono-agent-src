package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// The lead's one budget tool (phase P-F of docs/mastermind/plans/2026-10-27-mission-child-sessions.md): when a
// mission's budget is used up the lead may ask the person for more. The request waits for the person with no
// timeout; only the person's answer (the operator's `coder sessions budget grant`) raises a budget, once, by
// at most the amount asked, on the one session the request names. The caller is the lead of the mission, built
// the same way as for mission_child_mark (a verified session token whose process holds no task binding).
//
// What this tool never does: raise a budget, answer, decline or reopen a request, start a turn, or read a
// request's answer back as an instruction. There is no grant tool: the answer path is the operator's CLI.

func missionBudgetTools() []tool {
	write := map[string]bool{"readOnlyHint": false, "destructiveHint": false}
	return []tool{{
		name: "mission_budget_request", mutating: true, annotations: write, handler: toolMissionBudgetRequest,
		description: missionChildIntro + "Asks the person for more budget when yours (or one child session's cap) is used up. It does not raise anything: " +
			"it files a request that waits for the person for as long as it takes, and only their answer grants it, once, by at most amount_usd, " +
			"for the one session named. Nothing changes until then: keep working inside what you have left and say in your report what is waiting. " +
			"Without task_id the request is for your own session budget; with the task_id of one of your running child sessions, for that session's cap. " +
			"One open request per session, a few an hour. Returns {id, target, amountUsd, budgetUsd, spentUsd, status}.",
		schema: objSchema(map[string]interface{}{
			"amount_usd": map[string]interface{}{"type": "number", "description": "How much more, in USD (above 0, at most 50)"},
			"reason":     strParam("A short reason for the person (cut to 200 characters)"),
			"task_id":    intParam("The task of one of your running child sessions (optional: omit it to ask for your own budget)"),
		}, "amount_usd"),
	}}
}

func toolMissionBudgetRequest(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		AmountUSD float64 `json:"amount_usd"`
		Reason    string  `json:"reason"`
		TaskID    int64   `json:"task_id"`
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
	target := coderhost.TargetLead
	if a.TaskID != 0 {
		target = coderhost.TargetChild
	}
	b, err := store.RequestBudget(scope, profile, sid, target, a.TaskID, a.AmountUSD, a.Reason)
	if err != nil {
		return nil, sessionToolErr(err)
	}
	return map[string]any{
		"id": b.ID, "target": b.Target, "taskId": b.TaskID, "amountUsd": b.AmountUSD, "budgetUsd": b.BudgetUSD, "spentUsd": b.SpentUSD,
		"status": "waiting for the person",
		"note":   fmt.Sprintf("nothing changes until the person answers; your budget stays $%.2f", b.BudgetUSD),
	}, nil
}
