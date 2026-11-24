package orgdecide

import (
	"path/filepath"
	"strings"
)

// processLockPath is the file a process holds while it processes org.
func processLockPath(root, org string) string {
	safe := strings.NewReplacer("/", "_", "\\", "_").Replace(org)
	return filepath.Join(root, ".monomind", "org-process-"+safe+".lock")
}
