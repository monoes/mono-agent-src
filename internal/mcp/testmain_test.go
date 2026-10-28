package mcp

import (
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/automations"
	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent. The official automation packages
// no longer ship in the app; tests read them from the repo's automations/.
// Nothing here reaches the OS keychain: tokens are signed through the mock keyring.
func TestMain(m *testing.M) {
	keyring.MockInit()
	action.SetTestFallback(automations.FS())
	testhome.Main(m)
}
