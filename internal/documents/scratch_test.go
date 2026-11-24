package documents

import (
	"os"
	"strings"
	"testing"
)

// R11-14: temporary CV/tender files go to a private folder under the
// monoagent home, not the shared temp folder.
func TestScratchDirIsPrivateAndUnderTheMonoagentHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	dir := scratchDir()
	if !strings.HasPrefix(dir, home) || !strings.Contains(dir, ".monoagent") {
		t.Fatalf("scratchDir = %s, home %s", dir, home)
	}
	fi, err := os.Stat(dir)
	if err != nil || fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("scratch dir stat: %v %v", fi, err)
	}
}
