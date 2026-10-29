package main

import (
	"regexp"
	"strconv"
)

// ─── Agent purposes ─────────────────────────────────────────────────────────
//
// Which systems and models agents use for each kind of work, which
// `monoagentcli agent purposes` owns (list, set, reset). These bindings build
// its argv and return its JSON, or its {"error","code"}. The app runs as the
// operator, so `set` and `reset` are allowed from here; an agent's shell is
// refused by the CLI. Values that came from the user go in --flag=value form
// and the purpose id after "--", so none can read as a flag.

var (
	purposeIDPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	purposePairPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*/?[A-Za-z0-9._:/@+-]*$`)
	purposeRules       = map[string]bool{"any": true, "order": true, "cheapest": true}
)

func purposeInvalid(msg string) string {
	return `{"error":"` + msg + `","code":"invalid_input"}`
}

// AgentPurposes is `agent purposes list`: {purposes:[{id,title,description,
// categories,configured,pairs,rule,effort,different_from_author,
// budget_share_pct,fallback}], available:[pair]}.
func (a *App) AgentPurposes() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("agent", "purposes", "list")
}

// SetAgentPurpose is `agent purposes set`. It replaces that purpose's settings
// with exactly what is given, so the pane sends its whole state each time. pairs
// are "runtime/model" (a runtime's default model is "runtime/"); no pairs means
// nothing chosen.
func (a *App) SetAgentPurpose(purpose string, pairs []string, rule, effort string, differentModel bool, budgetSharePct int, fallback bool) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if !purposeIDPattern.MatchString(purpose) {
		return purposeInvalid("unknown purpose")
	}
	if !purposeRules[rule] {
		return purposeInvalid("the rule is any, order or cheapest")
	}
	if budgetSharePct < 0 || budgetSharePct > 100 {
		return purposeInvalid("the budget share is 0 to 100")
	}
	args := []string{"agent", "purposes", "set", "--rule=" + rule, "--budget-share=" + strconv.Itoa(budgetSharePct)}
	for _, p := range pairs {
		if !purposePairPattern.MatchString(p) {
			return purposeInvalid("a pair is runtime/model")
		}
		args = append(args, "--pair="+p)
	}
	if effort != "" {
		args = append(args, "--effort="+effort)
	}
	if differentModel {
		args = append(args, "--different-model")
	}
	if fallback {
		args = append(args, "--fallback")
	}
	return a.jsonResult(append(args, "--", purpose)...)
}

// ResetAgentPurpose is `agent purposes reset PURPOSE`: {reset:[id]}.
func (a *App) ResetAgentPurpose(purpose string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if !purposeIDPattern.MatchString(purpose) {
		return purposeInvalid("unknown purpose")
	}
	return a.jsonResult("agent", "purposes", "reset", "--", purpose)
}

// ResetAgentPurposes is `agent purposes reset --all`.
func (a *App) ResetAgentPurposes() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("agent", "purposes", "reset", "--all")
}
