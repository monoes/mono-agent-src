package workflowrun

import (
	"os"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/testhome"
)

// lookalikeEnv, set to 1, makes this test binary a stand-in monoagent
// process that only waits to be signalled (see startMonoagentLookalike).
const lookalikeEnv = "MONOAGENT_TEST_LOOKALIKE"

// Tests never touch the real ~/.monoagent or the OS keychain.
func TestMain(m *testing.M) {
	if os.Getenv(lookalikeEnv) == "1" {
		// No signal handler: a SIGTERM ends the process by that signal.
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	keyring.MockInit()
	testhome.Main(m)
}
