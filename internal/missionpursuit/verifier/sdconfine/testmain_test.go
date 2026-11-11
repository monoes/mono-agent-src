package sdconfine

import (
	"os"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/testhome"
)

// The test binary is also the finish sandbox's limits helper (the canary runs through the real sandbox), as the command's run
// function is in production. Tests never touch the real ~/.monoagent or the OS keychain.
func TestMain(m *testing.M) {
	finish.RunLimitsHelperIfRequested(os.Args[1:])
	keyring.MockInit()
	testhome.Main(m)
}
