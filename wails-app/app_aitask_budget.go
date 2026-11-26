package main

import "strconv"

// ─── AI task budgets ────────────────────────────────────────────────────────
//
// Spend caps for AI tasks (global, per kind, per conversation or role), which
// `monoagentcli aitask budget` owns. These bindings build its argv and return
// its JSON, or its {"error","code"}. The app runs as the operator, so `set` is
// allowed from here; an agent's shell is refused by the CLI. Values that came
// from the user go in --flag=value form, so none can read as a flag.

// AITaskBudgets is `aitask budget show`: {operatorUsd, caps:[{scope,key,capUsd,
// usedUsd,updatedBy,updatedAt}], spentUsd}.
func (a *App) AITaskBudgets() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("aitask", "budget", "show")
}

// SetAITaskBudget sets a cap (0 means no spend, never unlimited): `aitask
// budget set --scope S --key K --usd N`. scope is global, kind, conversation or
// role; key is empty for global.
func (a *App) SetAITaskBudget(scope, key string, usd float64) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	args := []string{"aitask", "budget", "set", "--scope=" + scope, "--usd=" + strconv.FormatFloat(usd, 'f', -1, 64)}
	if key != "" {
		args = append(args, "--key="+key)
	}
	return a.jsonResult(args...)
}

// ClearAITaskBudget removes a cap: `aitask budget set --scope S --key K --clear`.
func (a *App) ClearAITaskBudget(scope, key string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	args := []string{"aitask", "budget", "set", "--scope=" + scope, "--clear"}
	if key != "" {
		args = append(args, "--key="+key)
	}
	return a.jsonResult(args...)
}

// ChatSpendCap is `chat history spend-cap`: {bounded, capUsd, source,
// totalUsd, usedUp, taskId, unenforceableOn, workersCapUsd}, the cap the
// conversation's next message runs under.
func (a *App) ChatSpendCap(conversationID string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("chat", "history", "spend-cap", "--", conversationID)
}

// AITaskBudgetFor is `aitask budget show --task ID`: {taskId, capUsd,
// spentUsd, reported}, for a task's header.
func (a *App) AITaskBudgetFor(taskID string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("aitask", "budget", "show", "--task="+taskID)
}
