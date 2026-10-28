package procchain

import (
	"testing"

	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent (the package does not reach the vault or the keychain).
func TestMain(m *testing.M) {
	testhome.Main(m)
}
