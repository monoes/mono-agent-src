package decide

import (
	"context"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/testhome"
	"github.com/monoes/mono-agent/internal/vault"
)

// Tests never touch the real ~/.monoagent or the OS keychain.
func TestMain(m *testing.M) {
	keyring.MockInit()
	testhome.Main(m)
}

// testCtx is a context that names the profile the node runs for.
func testCtx() context.Context {
	return vault.ContextWithProfileID(context.Background(), "p1")
}
