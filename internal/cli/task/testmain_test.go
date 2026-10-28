package task

import (
	"os"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent or the OS keychain.
func TestMain(m *testing.M) {
	keyring.MockInit()
	// The evidence checker reads the working directory: outside a git work tree the
	// placeholder commit references these tests attach stay accepted.
	if err := os.Chdir(os.TempDir()); err != nil {
		panic(err)
	}
	testhome.Main(m)
}
