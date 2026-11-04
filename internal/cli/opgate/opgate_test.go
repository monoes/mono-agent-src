package opgate

import (
	"testing"

	"github.com/monoes/mono-agent/internal/operator"
)

func TestWithoutARegisteredBodyTheAnswerIsTheOperatorMarkerCheck(t *testing.T) {
	SetForTest(t, nil)
	t.Setenv("CLAUDECODE", "1")
	want := operator.Refusal()
	if want == "" {
		t.Fatal("test setup: CLAUDECODE did not mark the process")
	}
	if Refusal() != want || RecoveryRefusal() != want {
		t.Fatalf("default answers %q / %q, want %q", Refusal(), RecoveryRefusal(), want)
	}
}

func TestARegisteredBodyAnswersAndTheRecoveryFlavourIsPassedOn(t *testing.T) {
	SetForTest(t, func(recovery bool) string {
		if recovery {
			return "recovery"
		}
		return "plain"
	})
	if Refusal() != "plain" || RecoveryRefusal() != "recovery" {
		t.Fatalf("got %q / %q", Refusal(), RecoveryRefusal())
	}
}

func TestRegisterRefusesAnyCallerOutsideCmdMonoagentcli(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Register from another package did not panic")
		}
	}()
	Register(func(bool) string { return "" })
}

// The registered body is also the turn check operator.ApprovingContext consults; a body that panics
// counts as inside a turn, and clearing the body (or the test's cleanup) removes the check.
func TestTheRegisteredBodyIsTheApprovalTurnCheck(t *testing.T) {
	SetForTest(t, func(bool) string { return "in a turn" })
	if operator.InTurn() == "" {
		t.Fatal("the registered body is not the approval turn check")
	}
	SetForTest(t, func(bool) string { panic("no database path") })
	if operator.InTurn() == "" {
		t.Fatal("a panicking body did not fail to the in-a-turn answer")
	}
	SetForTest(t, nil)
	if operator.InTurn() != "" {
		t.Fatal("with no body the turn check is still installed")
	}
}

func TestSetForTestRestoresTheApprovalTurnCheck(t *testing.T) {
	t.Run("inner", func(t *testing.T) { SetForTest(t, func(bool) string { return "x" }) })
	if operator.InTurn() != "" {
		t.Fatal("the cleanup left the turn check installed")
	}
}
