package procbind

import (
	"testing"

	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent (the package reaches neither the vault nor the keychain).
func TestMain(m *testing.M) {
	testhome.Main(m)
}
