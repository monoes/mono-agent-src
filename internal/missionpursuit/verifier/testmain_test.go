package verifier

import (
	"os"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent or the OS keychain. The binary is also the finish sandbox's limits helper, as the
// command's run function is in production.
func TestMain(m *testing.M) {
	finish.RunLimitsHelperIfRequested(os.Args[1:])
	keyring.MockInit()
	testhome.Main(m)
}
