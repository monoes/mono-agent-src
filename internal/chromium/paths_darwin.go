package chromium

import (
	"os"
	"path/filepath"
)

// appNames are the macOS application bundles, by browser id.
var appNames = map[string]string{
	"chrome":   "Google Chrome.app",
	"edge":     "Microsoft Edge.app",
	"brave":    "Brave Browser.app",
	"arc":      "Arc.app",
	"vivaldi":  "Vivaldi.app",
	"opera":    "Opera.app",
	"chromium": "Chromium.app",
}

// candidatePaths are where id's app bundle may be: /Applications, then
// ~/Applications.
func candidatePaths(id string) []string {
	app, ok := appNames[id]
	if !ok {
		return nil
	}
	paths := []string{filepath.Join("/Applications", app)}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, "Applications", app))
	}
	return paths
}

// launchArgs opens the detected bundle with `open -a`, which brings an
// already running browser to the front instead of starting a second one.
func launchArgs(path string) (string, []string) {
	return "open", []string{"-a", path}
}
