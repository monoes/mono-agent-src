package org

import (
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/orgdecide"
	"github.com/monoes/mono-agent/internal/orgdesign"
)

// What is not the operator's own terminal (opgate: an agent, the chat, a
// workflow, a process inside a coder session's turn) may make an org's autonomy stricter or leave it as it is,
// never let it approve more without a person: autonomy full is refused, and
// so is any change autonomyWidening finds. Level manual and mid stay anyone's.

// refuseFullAutonomy refuses level full (full: the change asks for it).
func (env *orgEnv) refuseFullAutonomy(full bool, org string) error {
	if !full {
		return nil
	}
	if why := opgate.Refusal(); why != "" {
		return env.d.InvalidInput("autonomy full lets org %s's decider handle everything with no person involved, which is the user's to choose: %s; "+
			"set manual or mid, or run it yourself from a terminal or the app", org, why)
	}
	return nil
}

// refuseNotOperator refuses what switches an org into a looser mode to what is not the operator's own
// terminal (opgate: an agent, the chat, a workflow, a process inside a coder session's turn), whatever
// the change would be: what is the user's to give is not decided by comparing before and after.
func (env *orgEnv) refuseNotOperator(what string) error {
	if why := opgate.Refusal(); why != "" {
		return env.d.InvalidInput("%s is the user's to do: %s; run it yourself from a terminal or the app", what, why)
	}
	return nil
}

// refuseWidening refuses a change from before to after that would approve
// more without a person.
func (env *orgEnv) refuseWidening(before, after *orgdecide.Autonomy, org string) error {
	why := opgate.Refusal()
	if why == "" {
		return nil
	}
	if w := autonomyWidening(before, after); w != "" {
		return env.d.InvalidInput("%s would let org %s approve more without a person, which is the user's to choose: %s; "+
			"make it stricter or leave it, or run it yourself from a terminal or the app", w, org, why)
	}
	return nil
}

// snapshotAutonomy copies a before the flags change it.
func snapshotAutonomy(a *orgdecide.Autonomy) *orgdecide.Autonomy {
	c := *a
	c.Tiers = maps.Clone(a.Tiers)
	return &c
}

// autonomyWidening names the first part of a change that could approve more
// without a person, "" when none:
//   - the policy, the decider's instructions: any change, since which of two
//     texts lets more through can't be told;
//   - a tier override that is not irreversible (which can never widen) and
//     not stricter than or equal to the override it replaces (with no
//     override before, the class's tier comes from facts in the database);
//   - removing an override that was not routine (removing routine can only
//     make a class stricter);
//   - a higher limit on the decider's decisions or spend per run (0 or less
//     runs as the default, so it counts as the default);
//   - ending a pause sooner (resume, or a shorter --for): a paused org's
//     decisions all go to a person.
func autonomyWidening(before, after *orgdecide.Autonomy) string {
	if strings.TrimSpace(after.Policy) != strings.TrimSpace(before.Policy) {
		return "a new --policy (the decider's instructions)"
	}
	for k, v := range after.Tiers {
		old, had := before.Tiers[k]
		if v == orgdesign.TierIrreversible || had && tierStrictness(v) >= tierStrictness(old) {
			continue
		}
		return fmt.Sprintf("--tier %s=%s", k, v)
	}
	for k, old := range before.Tiers {
		if _, kept := after.Tiers[k]; !kept && old != orgdesign.TierRoutine {
			return fmt.Sprintf("--clear-tier %s (%s)", k, old)
		}
	}
	bn, busd := deciderLimits(before)
	an, ausd := deciderLimits(after)
	if an > bn {
		return fmt.Sprintf("--max-decisions %d (now %d)", after.Limits.MaxDecisionsPerRun, bn)
	}
	if ausd > busd {
		return fmt.Sprintf("--max-decider-usd %g (now %g)", after.Limits.MaxDeciderUSDPerRun, busd)
	}
	if b := before.PausedUntil; b != nil && b.After(time.Now()) && (after.PausedUntil == nil || after.PausedUntil.Before(*b)) {
		return fmt.Sprintf("ending its pause (until %s) sooner", b.UTC().Format(time.RFC3339))
	}
	return ""
}

// pauseUntil is when a pause asked to end at until ends for org a: what is
// not the operator's own terminal never ends a pause sooner, so an org
// already paused for longer keeps its pause (and `pause --all` still pauses
// the rest).
func pauseUntil(a *orgdecide.Autonomy, until time.Time) time.Time {
	if opgate.Refusal() != "" && a.PausedUntil != nil && a.PausedUntil.After(until) {
		return *a.PausedUntil
	}
	return until
}

// deciderLimits are the limits the decider runs with: 0 or less is the default.
func deciderLimits(a *orgdecide.Autonomy) (int, float64) {
	n, usd := a.Limits.MaxDecisionsPerRun, a.Limits.MaxDeciderUSDPerRun
	if n <= 0 {
		n = orgdecide.DefaultMaxDecisions
	}
	if usd <= 0 {
		usd = orgdecide.DefaultMaxDeciderUSD
	}
	return n, usd
}

// tierStrictness orders the tiers, the strictest (a person decides) highest.
func tierStrictness(t string) int {
	switch t {
	case orgdesign.TierRoutine:
		return 0
	case orgdesign.TierConsequential:
		return 1
	}
	return 2
}
