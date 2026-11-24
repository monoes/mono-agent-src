package hil

import (
	"testing"

	"github.com/monoes/mono-agent/internal/hilsend"
)

// The command line installs the operator's ancestor check into the send gate (R10-13).
func TestAncestorCheckInstalled(t *testing.T) {
	if hilsend.AncestorCheck == nil {
		t.Fatal("hilsend.AncestorCheck is not installed")
	}
}
