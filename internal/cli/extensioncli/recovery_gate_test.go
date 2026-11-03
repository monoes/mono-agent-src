package extensioncli

import (
	"strings"
	"testing"
)

// `extension reset` asks the recovery gate when one is wired (an unreadable database must not lock the
// operator out of revoking the pairing token); every other gated verb asks Refusal. Without a recovery
// gate reset asks Refusal, so a missing wire is never more open than the ordinary gate.
func TestExtensionResetAsksTheRecoveryGate(t *testing.T) {
	gateHome(t)
	d := testDeps(false)
	d.Refusal = func() string { return "ordinary gate" }
	d.RecoveryRefusal = func() string { return "" }
	if _, err := runExt(t, d, "reset"); err != nil {
		t.Fatalf("reset with a recovery gate that lets the operator through: %v", err)
	}
	if _, err := runExt(t, d, "unbind", "Edge"); !operatorOnlyErr(err) || !strings.Contains(err.Error(), "ordinary gate") {
		t.Fatalf("unbind must ask the ordinary gate: %v", err)
	}

	gateHome(t)
	d.RecoveryRefusal = func() string { return "recovery gate" }
	d.Refusal = func() string { return "" }
	if _, err := runExt(t, d, "reset"); !operatorOnlyErr(err) || !strings.Contains(err.Error(), "recovery gate") {
		t.Fatalf("reset must ask the recovery gate: %v", err)
	}

	d.RecoveryRefusal = nil
	d.Refusal = func() string { return "ordinary gate" }
	if _, err := runExt(t, d, "reset"); !operatorOnlyErr(err) || !strings.Contains(err.Error(), "ordinary gate") {
		t.Fatalf("reset without a recovery gate must fall back to the ordinary gate: %v", err)
	}
}
