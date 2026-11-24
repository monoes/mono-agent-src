package browserctl

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Files an upload never reads. The bytes go into a page the caller picks, so
// a prompt-injected agent (or a workflow) pointed at a credential store would
// otherwise hand it over. The list is credentials and browser profiles under
// the home directory plus a few system files; it is a deny-list, not a
// sandbox: a file anywhere else that the process can read can still be
// uploaded (to a page that is not local, only after the approval an agent's
// upload already needs).
var uploadDeniedHomeDirs = []string{
	".ssh", ".monoagent", ".aws", ".gnupg", ".kube", ".docker",
	".config/gcloud", ".config/google-chrome", ".config/chromium", ".config/microsoft-edge",
	".config/BraveSoftware", ".mozilla", ".netrc",
	"Library/Application Support/Google/Chrome", "Library/Application Support/Microsoft Edge",
	"Library/Application Support/BraveSoftware", "Library/Application Support/Firefox",
	"AppData/Local/Google", "AppData/Local/Microsoft/Edge", "AppData/Local/BraveSoftware",
	"AppData/Roaming/Mozilla",
}

var uploadDeniedSystemPaths = []string{"/etc/shadow", "/etc/gshadow", "/etc/sudoers", "/etc/sudoers.d", "/etc/ssh", "/root"}

// checkUploadAllowed refuses abs when it, or what it resolves to through
// symlinks, is on the deny-list.
func checkUploadAllowed(abs string) error {
	cands := []string{abs}
	if real, err := filepath.EvalSymlinks(abs); err == nil && real != abs {
		cands = append(cands, real)
	}
	denied := append([]string(nil), uploadDeniedSystemPaths...)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for _, d := range uploadDeniedHomeDirs {
			denied = append(denied, filepath.Join(home, filepath.FromSlash(d)))
		}
		if real, err := filepath.EvalSymlinks(home); err == nil && real != home {
			for _, d := range uploadDeniedHomeDirs {
				denied = append(denied, filepath.Join(real, filepath.FromSlash(d)))
			}
		}
	}
	for _, c := range cands {
		for _, d := range denied {
			if uploadWithin(c, filepath.FromSlash(d)) {
				return fmt.Errorf("upload: %s is a credential or browser-profile location and is never uploaded", abs)
			}
		}
	}
	return nil
}

// uploadWithin reports whether path is dir or below it (case-insensitively: a deny-list
// should not be sidestepped on a case-insensitive file system).
func uploadWithin(path, dir string) bool {
	p, d := strings.ToLower(filepath.Clean(path)), strings.ToLower(filepath.Clean(dir))
	return p == d || strings.HasPrefix(p, d+string(filepath.Separator))
}
