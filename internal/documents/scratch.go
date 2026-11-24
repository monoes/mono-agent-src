package documents

import (
	"os"
	"path/filepath"
)

// maxPDFBytes bounds the PDF stream read back from the browser.
const maxPDFBytes = 128 << 20

// scratchDir is the private folder for the temporary HTML/PDF files and the
// browser profile of one generation: under ~/.monoagent/scratch, mode 0700,
// not the shared temp folder (a CV or tender is personal data). A crashed run
// leaves files there, in a folder only this user can open (R11-14).
func scratchDir() string {
	base := os.TempDir()
	if home, err := os.UserHomeDir(); err == nil {
		base = filepath.Join(home, ".monoagent", "scratch")
	}
	dir := filepath.Join(base, "documents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return os.TempDir()
	}
	return dir
}
