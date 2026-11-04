package main

import (
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
)

// The hints ensureExtensionConnected hands the wait carry the live operator gate: an adapter that
// dropped Refusal (nil refuses, so the operator would lose the open) or passed a func that always
// answers "" (a model's turn could open the pairing page) is caught here. The wait loops
// themselves are covered in internal/cli/bridge.
func TestExtensionHints_RefusalIsTheOperatorGate(t *testing.T) {
	h := extensionHints()
	if h.Refusal == nil {
		t.Fatal("Hints.Refusal is nil: the pairing page would never open, even for the operator")
	}
	opgate.SetForTest(t, func(bool) string { return "this process runs inside a coder turn" })
	if got := h.Refusal(); got != "this process runs inside a coder turn" {
		t.Fatalf("Refusal() = %q, want the gate's refusal", got)
	}
	opgate.SetForTest(t, func(bool) string { return "" })
	if got := h.Refusal(); got != "" {
		t.Fatalf("Refusal() = %q for the operator, want none", got)
	}
}
