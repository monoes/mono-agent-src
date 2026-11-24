package update

import (
	"testing"

	"github.com/monoes/mono-agent/internal/release"

	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent.
// The fixtures here serve the GitHub-API (legacy) path; shipped builds have it
// off (release.AllowLegacyGitHubUpdates), so the tests opt in.
func TestMain(m *testing.M) {
	release.AllowLegacyGitHubUpdates = true
	testhome.Main(m)
}
