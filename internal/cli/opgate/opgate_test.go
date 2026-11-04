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

// SetForTest replaces BOTH flavours: a recovery body registered earlier (the real tree's busy-aware check) is
// gone for the rest of the test and comes back afterwards, so a test's stand-in body is never bypassed by it.
func TestSetForTestClearsTheRecoveryBodyOfAnEarlierRegistration(t *testing.T) {
	prev := recoveryBody
	recoveryBody = func() (string, error) { return "", errBusyStandIn }
	t.Cleanup(func() { recoveryBody = prev })
	outer := recoveryBody
	t.Run("inside", func(t *testing.T) {
		SetForTest(t, func(recovery bool) string { return "stand-in" })
		if recoveryBody != nil {
			t.Fatal("SetForTest left the earlier recovery body in place")
		}
		if why, busy := RecoveryCheck(); why != "stand-in" || busy != nil {
			t.Fatalf("RecoveryCheck = %q, %v; want the stand-in body's answer", why, busy)
		}
	})
	if recoveryBody == nil || outer == nil {
		t.Fatal("the earlier recovery body was not restored")
	}
}

var errBusyStandIn = &standInErr{}

type standInErr struct{}

func (*standInErr) Error() string { return "busy stand-in" }
