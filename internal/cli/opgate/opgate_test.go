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
