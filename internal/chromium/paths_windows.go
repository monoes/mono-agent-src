package chromium

import (
	"os"
	"path/filepath"
)

// exePaths are each browser's executable, relative to an install root.
// Arc is a Microsoft Store app with no fixed executable path, so it has
// none and is never detected here.
var exePaths = map[string][]string{
	"chrome":   {`Google\Chrome\Application\chrome.exe`},
	"edge":     {`Microsoft\Edge\Application\msedge.exe`},
	"brave":    {`BraveSoftware\Brave-Browser\Application\brave.exe`},
	"vivaldi":  {`Vivaldi\Application\vivaldi.exe`},
	"opera":    {`Programs\Opera\opera.exe`, `Opera\opera.exe`},
	"chromium": {`Chromium\Application\chrome.exe`},
}

// candidatePaths are id's executable under %ProgramFiles%,
// %ProgramFiles(x86)% and %LocalAppData%, in that order. An unset or
// relative variable is skipped, so no path is ever relative.
func candidatePaths(id string) []string {
	rels, ok := exePaths[id]
	if !ok {
		return nil
	}
	var paths []string
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
		root := os.Getenv(env)
		if root == "" || !filepath.IsAbs(root) {
			continue
		}
		for _, rel := range rels {
			paths = append(paths, filepath.Join(root, rel))
		}
	}
	return paths
}

// launchArgs runs the detected executable, an absolute path.
func launchArgs(path string) (string, []string) {
	return path, nil
}

// installed reports whether a candidate is there: a regular file, not a
// directory of the same name.
func installed(fi os.FileInfo) bool { return fi.Mode().IsRegular() }
