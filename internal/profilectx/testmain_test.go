package profilectx

import (
	"os"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent or the OS keychain.
func TestMain(m *testing.M) {
	keyring.MockInit()
	// the extension probe must never reach the real bridge on its default ports
	os.Setenv(extension.ExtensionPortEnv, "1")
	testhome.Main(m)
}
