package chromium

import (
	"os"
	"path/filepath"
)

// binaryNames are the usual commands of each browser on Linux. Arc has no
// Linux build, so it has none.
var binaryNames = map[string][]string{
	"chrome":   {"google-chrome", "google-chrome-stable"},
	"edge":     {"microsoft-edge", "microsoft-edge-stable"},
	"brave":    {"brave-browser", "brave"},
	"vivaldi":  {"vivaldi", "vivaldi-stable"},
	"opera":    {"opera"},
	"chromium": {"chromium", "chromium-browser"},
}

// candidatePaths are id's commands in every absolute PATH directory, in
// PATH order: what exec.LookPath would search, spelled out so that Detect's
// exists check decides (and a test can stand in for the disk).
func candidatePaths(id string) []string {
	names, ok := binaryNames[id]
	if !ok {
		return nil
	}
	var paths []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if !filepath.IsAbs(dir) {
			continue // never a command from the working directory
		}
		for _, name := range names {
			paths = append(paths, filepath.Join(dir, name))
		}
	}
	return paths
}

// launchArgs runs the detected command by its absolute path, never a name
// looked up again through PATH.
func launchArgs(path string) (string, []string) {
	return path, nil
}

// installed reports whether a candidate is there: an executable regular
// file, so a directory or a plain file of the same name does not stop the
// search before a real browser further along PATH.
func installed(fi os.FileInfo) bool { return fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 }
