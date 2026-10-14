package integrations

import (
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent, ~/.claude, ~/.codex or the OS keychain: every
// test that writes a runtime's config sets HOME (and CODEX_HOME, XDG_CONFIG_HOME) to its own.
func TestMain(m *testing.M) {
	keyring.MockInit()
	testhome.Main(m)
}
