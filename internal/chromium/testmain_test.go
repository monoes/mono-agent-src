package chromium

import (
	"testing"

	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent (candidatePaths reads the home
// directory on macOS).
func TestMain(m *testing.M) {
	testhome.Main(m)
}
