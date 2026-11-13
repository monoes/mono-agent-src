package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/monoes/mono-agent/internal/cli/opgate"
)

// ─── The two switches that widen what a mission may do ──────────────────────
//
// Turning ON mission child sessions or the lead's approval of them lifts a safeguard, so the app asks the PERSON in a native
// dialog of the operating system (a script in the page can raise it, it cannot click it) after the operator gate, before the CLI
// runs; the confirm() in the page is only a courtesy. Turning either OFF narrows authority and needs no dialog. Where no native
// dialog can be shown (the browser end-to-end host) turning on is refused. The CLI keeps its own operator check and its
// validation; TestAuthorityRaisingBindingsAskInANativeDialog holds every binding that raises authority to this shape.

// maxApprovalCeilingUSD is the most a lead's approval ceiling may be set to from the app (the CLI itself allows more): a larger
// number is a deliberate CLI action, not a click.
const maxApprovalCeilingUSD = 1000

const authorityDialogTitle = "Widen what this mission may do"

// authorityConfirm asks the person; it returns "" when they said yes, else the refusal document.
func authorityConfirm(a *App, body string) string {
	yes, returned, err := askNativeTitled(a, authorityDialogTitle, body)
	switch {
	case !returned:
		return aiError(&codedError{msg: "the confirmation did not return in time; nothing changed", code: "declined"})
	case errors.Is(err, errDialogBusy):
		return aiError(&codedError{msg: err.Error(), code: "busy"})
	case err != nil:
		return aiError(&codedError{msg: err.Error(), code: "no_native_dialog"})
	case !yes:
		return aiError(&codedError{msg: "declined; nothing changed", code: "declined"})
	}
	return ""
}

// SetMissionChildren is `coder sessions mission-children enable|disable` (the operator's switch for child sessions).
func (a *App) SetMissionChildren(on bool) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	verb := "disable"
	if on {
		if why := opgate.Refusal(); why != "" {
			return taskRefusal("only the user can turn on mission child sessions: " + why + " (operator_only)")
		}
		if bad := authorityConfirm(a, "Allow mission child sessions? Each one gets its own budget carved from its lead's, and its tasks run in sessions of their own."); bad != "" {
			return bad
		}
		verb = "enable"
	}
	return a.jsonResult("coder", "sessions", "mission-children", verb)
}

// SetAgentApproval turns the lead's approval of child sessions on (with a spend ceiling above 0 and at most maxApprovalCeilingUSD)
// or off through `coder sessions settings set --patch`. The patch is built here from two typed values, never from page text.
func (a *App) SetAgentApproval(conversationID string, on bool, ceilingUSD float64) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	patch := map[string]any{"agent_approval": "off"}
	if on {
		if math.IsNaN(ceilingUSD) || ceilingUSD <= 0 || ceilingUSD > maxApprovalCeilingUSD {
			return taskRefusal(fmt.Sprintf("the lead's approval needs a spend ceiling above 0 and at most %d from the app (use the CLI for more)", maxApprovalCeilingUSD))
		}
		if why := opgate.Refusal(); why != "" {
			return taskRefusal("only the user can let a lead approve child sessions: " + why + " (operator_only)")
		}
		if bad := authorityConfirm(a, fmt.Sprintf("Let the lead of this chat approve child sessions itself, each up to $%.2f? This lifts a safeguard that normally needs you.", ceilingUSD)); bad != "" {
			return bad
		}
		patch = map[string]any{"agent_approval": "lead", "approval_ceiling_usd": ceilingUSD}
	}
	raw, _ := json.Marshal(patch)
	return a.jsonResult("coder", "sessions", "settings", "set", "--patch="+string(raw), "--", conversationID)
}
