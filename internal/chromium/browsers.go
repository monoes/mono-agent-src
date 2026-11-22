// Package chromium finds the Chromium-based browsers installed on this
// machine and builds the command that opens one, for the onboarding's
// "Connect your browser" step. Nothing here takes a path from a caller: a
// browser is named by its id, and the command comes from a detected path:
// an absolute path, run with no arguments from the caller (on macOS by the
// absolute /usr/bin/open).
//
// Some candidates are writable by the user (~/Applications, %LocalAppData%,
// PATH directories such as ~/.local/bin): trusted at the user's own level,
// and checked after the system locations where there are both. Never add a
// candidate under a world-writable or shared directory.
package chromium

import (
	"fmt"
	"os"
)

// Browser is one Chromium-based browser as the onboarding shows it.
type Browser struct {
	ID            string `json:"id"` // chrome, edge, brave, arc, vivaldi, opera, chromium
	Name          string `json:"name"`
	Installed     bool   `json:"installed"`
	ExtensionsURL string `json:"extensionsUrl"` // the address to paste: it cannot be opened from outside
	DevModeSide   string `json:"devModeSide"`   // where the Developer mode switch is: "left" or "right"
}

// known lists every browser in the order the onboarding shows them.
var known = []Browser{
	{ID: "chrome", Name: "Google Chrome", ExtensionsURL: "chrome://extensions", DevModeSide: "right"},
	{ID: "edge", Name: "Microsoft Edge", ExtensionsURL: "edge://extensions", DevModeSide: "left"},
	{ID: "brave", Name: "Brave", ExtensionsURL: "brave://extensions", DevModeSide: "right"},
	{ID: "arc", Name: "Arc", ExtensionsURL: "arc://extensions", DevModeSide: "right"},
	{ID: "vivaldi", Name: "Vivaldi", ExtensionsURL: "vivaldi://extensions", DevModeSide: "right"},
	{ID: "opera", Name: "Opera", ExtensionsURL: "opera://extensions", DevModeSide: "right"},
	{ID: "chromium", Name: "Chromium", ExtensionsURL: "chrome://extensions", DevModeSide: "right"},
}

// Known lists all seven browsers, none marked installed.
func Known() []Browser {
	return append([]Browser(nil), known...)
}

// Detect lists all seven browsers, the installed ones first, each group in
// Known's order. exists reports whether a candidate path is there (Exists
// for the real disk).
func Detect(exists func(path string) bool) []Browser {
	var installed, rest []Browser
	for _, b := range known {
		if _, ok := detectedPath(b.ID, exists); ok {
			b.Installed = true
			installed = append(installed, b)
			continue
		}
		rest = append(rest, b)
	}
	return append(installed, rest...)
}

// Exists reports whether a browser is installed at candidate path: an app
// bundle on macOS, an (executable, on Linux) regular file elsewhere.
func Exists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && installed(fi)
}

// LaunchCommand is the program and arguments that open browser id, built
// from the path Detect finds on this machine. An unknown id, or one that is
// not installed, is an error.
func LaunchCommand(id string) (name string, args []string, err error) {
	return launchCommand(id, Exists)
}

func launchCommand(id string, exists func(string) bool) (string, []string, error) {
	if !isKnown(id) {
		return "", nil, fmt.Errorf("%q is not a browser MonoAgent knows", id)
	}
	path, ok := detectedPath(id, exists)
	if !ok {
		return "", nil, fmt.Errorf("%s is not installed on this computer", nameOf(id))
	}
	name, args := launchArgs(path)
	return name, args, nil
}

// detectedPath is the first of id's candidate paths that exists.
func detectedPath(id string, exists func(string) bool) (string, bool) {
	for _, p := range candidatePaths(id) {
		if exists(p) {
			return p, true
		}
	}
	return "", false
}

func isKnown(id string) bool {
	for _, b := range known {
		if b.ID == id {
			return true
		}
	}
	return false
}

func nameOf(id string) string {
	for _, b := range known {
		if b.ID == id {
			return b.Name
		}
	}
	return id
}
