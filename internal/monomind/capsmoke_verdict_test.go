package monomind

import (
	"fmt"
	"testing"
)

// incompleteTurn says why a turn is not a completed one, or "".
func incompleteTurn(res *TurnResult, err error, assistantBytes int) string {
	switch {
	case res == nil:
		return redactSecrets(fmt.Sprintf("no result, err=%v", err))
	case err != nil:
		return redactSecrets(fmt.Sprintf("exec error: %v", err))
	case res.Err != nil:
		return redactSecrets(fmt.Sprintf("the runner returned an error result: %v", res.Err))
	case res.ExitCode != 0:
		return fmt.Sprintf("exit code %d", res.ExitCode)
	case !res.SawDone:
		return "no done event"
	case assistantBytes == 0:
		return "no assistant output"
	}
	return ""
}

// overshotAfterFullTurn reports the overshoot case: the turn ran to its done event with output and a reported cost, and
// the runner then failed it with its budget error (the cap is checked after the spend). The real-runner smoke
// (capsmoke_realrunner_test.go, tag realrunner) records it as a verdict instead of calling it inconclusive.
func overshotAfterFullTurn(res *TurnResult, err error, assistantBytes int) bool {
	return res != nil && err == nil && res.Err != nil && res.Err.Code == ErrBudget &&
		res.SawDone && res.HasCostUSD && assistantBytes > 0
}

func TestAFullTurnFailedForCrossingTheCapIsTheOvershootCaseNotInconclusive(t *testing.T) {
	budget := &ProtocolError{Code: ErrBudget, Message: "spend cap exceeded: $0.0106 > --budget-usd 0.01"}
	full := &TurnResult{ExitCode: 1, SawDone: true, HasCostUSD: true, CostUSD: 0.0106, Err: budget}
	if !overshotAfterFullTurn(full, nil, 36088) {
		t.Fatal("a full turn the runner failed for the cap was not recognised as the overshoot case")
	}
	if incompleteTurn(full, nil, 36088) == "" {
		t.Fatal("precondition: the generic check calls this turn incomplete (it was the only check before)")
	}
	for name, r := range map[string]*TurnResult{
		"no done event":    {ExitCode: 1, HasCostUSD: true, Err: budget},
		"no reported cost": {ExitCode: 1, SawDone: true, Err: budget},
		"another error":    {ExitCode: 1, SawDone: true, HasCostUSD: true, Err: &ProtocolError{Code: "auth", Message: "Not logged in"}},
		"no error":         {SawDone: true, HasCostUSD: true},
	} {
		if overshotAfterFullTurn(r, nil, 10) {
			t.Errorf("%s: wrongly treated as the overshoot case", name)
		}
	}
	if overshotAfterFullTurn(full, fmt.Errorf("exec failed"), 10) || overshotAfterFullTurn(full, nil, 0) {
		t.Error("an exec error or an empty turn must stay inconclusive")
	}
}
