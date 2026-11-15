package chat

import (
	"os"
	"os/signal"
	"syscall"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent or the OS keychain.
func TestMain(m *testing.M) {
	if os.Getenv("CHAT_FAKE_HOST") == "1" { // a stand-in for a running coder host: leaves on SIGTERM like the real one
		term := make(chan os.Signal, 1)
		signal.Notify(term, syscall.SIGTERM)
		<-term
		os.Exit(0)
	}
	keyring.MockInit()
	testhome.Main(m)
}
