package doomsday

import (
	"os"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/testhome"
)

// The finish tests run the criteria as a child of this test binary, which has to become the limits helper when it is
// re-executed that way, as the command's run function does in production.
func TestMain(m *testing.M) {
	finish.RunLimitsHelperIfRequested(os.Args[1:])
	testhome.Main(m)
}
