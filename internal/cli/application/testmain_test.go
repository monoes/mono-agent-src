package application

import (
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent or the OS keychain.
func TestMain(m *testing.M) {
	keyring.MockInit()
	FillStartWiring.RequireOperator = func(string) error { return nil } // the operator by default; tests refuse explicitly
	testhome.Main(m)
}
