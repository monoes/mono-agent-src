package agent

import (
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
)

// FakeTurnsForTest exposes fakeTurns to the agent_test package.
func FakeTurnsForTest(t *testing.T, reply func(opts monomind.ExecOptions) (*monomind.TurnResult, error)) {
	fakeTurns(t, reply)
}
