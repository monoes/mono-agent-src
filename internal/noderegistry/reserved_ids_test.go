package noderegistry

import (
	"github.com/monoes/mono-agent/internal/testhome"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
)

// TestBuiltinNamespacesAreReserved: no automation package may take the
// namespace of a built-in node type as its id. A new built-in namespace
// fails here until it is added to automation's reservedIDs.
func TestBuiltinNamespacesAreReserved(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	action.SetDefSource(nil) // no packages: every registered type is built in
	for _, nt := range Build(nil).Types() {
		ns, _, _ := strings.Cut(nt, ".")
		if !automation.ReservedID(ns) {
			t.Errorf("built-in node %q: namespace %q is not in automation's reservedIDs", nt, ns)
		}
	}
	if !automation.ReservedID("trigger") {
		t.Error(`"trigger" (the engine's trigger.* nodes) is not reserved`)
	}
}
