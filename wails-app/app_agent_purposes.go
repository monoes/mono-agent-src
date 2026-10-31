package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ─── Agent purposes ─────────────────────────────────────────────────────────
//
// Which validated (runtime, model) pairs may do which kind of work, which
// `monoagentcli agent purposes list|set|reset` owns (monoes/mono-agent-src#1150).
// These bindings build its argv and return its JSON, or its {"error","code"}
// refusal. The app runs as the operator, so set and reset are allowed from
// here; an agent's shell is refused by the CLI. Everything the user chose goes
// in --flag=value form so none of it can read as a flag, and bad input is
// refused here before the CLI runs.

var (
	purposeIDRe      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	purposeRuntimeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,39}$`)
	purposeEffortRe  = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,19}$`)
)

const maxPurposePairs = 64

// AgentPurposesList is `agent purposes list --json`: {purposes:[...], roster:[...]}.
func (a *App) AgentPurposesList() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("agent", "purposes", "list")
}

// AgentPurposeSet saves one purpose in full: `agent purposes set ID
// --pair=RUNTIME:MODEL… --rule=… [--effort=…] --different-from-author=on|off
// --fallback=… [--budget-share=PCT]`. pairs are "runtime:model" strings (the
// model may contain further colons); an empty list means "every validated
// model". effort "" and budgetShare 0 mean none and are not sent.
func (a *App) AgentPurposeSet(id string, pairs []string, rule, effort string, differentFromAuthor bool, fallback string, budgetShare float64) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	args, err := purposeSetArgs(id, pairs, rule, effort, differentFromAuthor, fallback, budgetShare)
	if err != nil {
		return aiError(err)
	}
	return a.jsonResult(args...)
}

// AgentPurposeReset is `agent purposes reset ID`: back to "every validated model".
func (a *App) AgentPurposeReset(id string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if !purposeIDRe.MatchString(id) {
		return aiError(fmt.Errorf("invalid purpose id %q", id))
	}
	return a.jsonResult("agent", "purposes", "reset", id)
}

func purposeSetArgs(id string, pairs []string, rule, effort string, differentFromAuthor bool, fallback string, budgetShare float64) ([]string, error) {
	if !purposeIDRe.MatchString(id) {
		return nil, fmt.Errorf("invalid purpose id %q", id)
	}
	if len(pairs) > maxPurposePairs {
		return nil, fmt.Errorf("too many models (%d, at most %d)", len(pairs), maxPurposePairs)
	}
	switch rule {
	case "any", "order", "cheapest":
	default:
		return nil, fmt.Errorf("invalid rule %q (any, order or cheapest)", rule)
	}
	switch fallback {
	case "any", "none":
	default:
		return nil, fmt.Errorf("invalid fallback %q (any or none)", fallback)
	}
	if effort != "" && !purposeEffortRe.MatchString(effort) {
		return nil, fmt.Errorf("invalid effort %q", effort)
	}
	if budgetShare != budgetShare || budgetShare < 0 || budgetShare > 100 {
		return nil, fmt.Errorf("budget share must be between 0 and 100")
	}
	args := []string{"agent", "purposes", "set", id}
	seen := map[string]bool{}
	for _, p := range pairs {
		runtimeID, model, ok := strings.Cut(p, ":")
		if !ok || !purposeRuntimeRe.MatchString(runtimeID) || strings.TrimSpace(model) == "" || len(model) > 200 {
			return nil, fmt.Errorf("invalid model %q (want runtime:model)", p)
		}
		for _, r := range model {
			if r < 0x20 || r == 0x7f {
				return nil, fmt.Errorf("invalid model %q (control character)", p)
			}
		}
		if seen[p] {
			return nil, fmt.Errorf("model %q is listed twice", p)
		}
		seen[p] = true
		args = append(args, "--pair="+p)
	}
	args = append(args, "--rule="+rule, "--fallback="+fallback)
	if differentFromAuthor {
		args = append(args, "--different-from-author=on")
	} else {
		args = append(args, "--different-from-author=off")
	}
	if effort != "" {
		args = append(args, "--effort="+effort)
	}
	if budgetShare > 0 {
		args = append(args, "--budget-share="+strconv.FormatFloat(budgetShare, 'f', -1, 64))
	}
	return args, nil
}
