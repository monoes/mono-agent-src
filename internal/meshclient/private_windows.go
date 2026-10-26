//go:build windows

package meshclient

import "os"

// privateDirProblem: Windows has no owner/mode bits to check here; only that it is a directory.
func privateDirProblem(dir string) string {
	fi, err := os.Lstat(dir)
	if err != nil {
		return "mesh directory does not exist"
	}
	if !fi.IsDir() {
		return "mesh path is not a private directory"
	}
	return ""
}
