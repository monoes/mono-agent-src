package mcp

import (
	"os"
	"testing"

	"github.com/monoes/mono-agent/automations"
	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent. The official automation packages
// no longer ship in the app; tests read them from the repo's automations/.
func TestMain(m *testing.M) {
	action.SetTestFallback(automations.FS())
	// The evidence checker reads the working directory: outside a git work tree the
	// placeholder commit references these tests attach stay accepted.
	if err := os.Chdir(os.TempDir()); err != nil {
		panic(err)
	}
	testhome.Main(m)
}
