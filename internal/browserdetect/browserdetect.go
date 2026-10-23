// Package browserdetect finds the Chromium-family browser and the MonoAgent
// extension on this machine — used by the CLI before driving the browser
// and by `monoagentcli doctor`.
package browserdetect

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// pathNames are the browser executables looked up on PATH when none of the
// well-known install paths exists (a distro that puts them in /usr/local,
// /opt/…/bin on PATH, a Flatpak/Nix wrapper).
var pathNames = []string{
	"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
	"brave-browser", "brave", "microsoft-edge", "microsoft-edge-stable",
}

// FindBrowser returns the path to the local Chrome binary, or empty string.
func FindBrowser() string {
	names := pathNames
	if runtime.GOOS == "windows" {
		names = []string{"chrome.exe", "msedge.exe", "brave.exe"}
	}
	return findBrowser(browserCandidates(), names)
}

// findBrowser returns the first existing candidate, else the first of
// names found on PATH.
func findBrowser(candidates, names []string) string {
	for _, p := range candidates {
		if p != "" {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			if abs, err := filepath.Abs(p); err == nil {
				return abs
			}
			return p
		}
	}
	return ""
}

// browserCandidates are the well-known install paths on this OS.
func browserCandidates() []string {
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
	case "windows":
		localAppData := os.Getenv("LOCALAPPDATA")
		programFiles := os.Getenv("ProgramFiles")
		programFilesX86 := os.Getenv("ProgramFiles(x86)")
		candidates = []string{
			filepath.Join(localAppData, `Google\Chrome\Application\chrome.exe`),
			filepath.Join(programFiles, `Google\Chrome\Application\chrome.exe`),
			filepath.Join(programFilesX86, `Google\Chrome\Application\chrome.exe`),
			filepath.Join(localAppData, `Microsoft\Edge\Application\msedge.exe`),
			filepath.Join(programFiles, `Microsoft\Edge\Application\msedge.exe`),
			filepath.Join(programFilesX86, `Microsoft\Edge\Application\msedge.exe`),
		}
	default: // linux, bsd, etc.
		candidates = []string{
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/google-chrome-beta",
			"/usr/bin/google-chrome-unstable",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/usr/bin/brave-browser",
			"/usr/bin/microsoft-edge",
			"/usr/bin/microsoft-edge-stable",
			"/usr/bin/microsoft-edge-beta",
			"/snap/bin/chromium",
		}
	}
	return candidates
}

// IsBrowserRunning checks if a supported Chrome/Chromium browser process is already running.
func IsBrowserRunning() bool {
	switch runtime.GOOS {
	case "darwin", "linux":
		// Match the main executable binary paths to avoid false positives on helpers
		// like chrome-native-host. -i makes the match case-insensitive so
		// "Chromium"/"Google Chrome Canary"/"Brave Browser" (macOS) and the
		// lowercase Linux binary names all match one pattern.
		pattern := "(Google Chrome( Canary)?\\.app/Contents/MacOS/|" +
			"Chromium\\.app/Contents/MacOS/|" +
			"Brave Browser\\.app/Contents/MacOS/|" +
			"Microsoft Edge\\.app/Contents/MacOS/|" +
			"google-chrome|chromium|brave-browser|microsoft-edge|msedge)"
		cmd := exec.Command("pgrep", "-i", "-f", pattern)
		out, err := cmd.Output()
		if err == nil && len(strings.TrimSpace(string(out))) > 0 {
			return true
		}
		return false
	case "windows":
		cmd := exec.Command("tasklist")
		out, err := cmd.Output()
		if err != nil {
			return false
		}
		lower := strings.ToLower(string(out))
		return strings.Contains(lower, "chrome.exe") || strings.Contains(lower, "msedge.exe") || strings.Contains(lower, "brave.exe")
	default:
		return false
	}
}

// ExtensionDir returns the path to the chrome-extension directory to display in instructions.
func ExtensionDir() string {
	// 1. Try finding the chrome-extension directory relative to current working dir
	cwd, err := os.Getwd()
	if err == nil {
		p := filepath.Join(cwd, "chrome-extension")
		if _, err := os.Stat(filepath.Join(p, "manifest.json")); err == nil {
			return p
		}
	}
	// 2. Try ~/.monoagent/extension, where `monoagentcli extension install` unpacks it
	home, err := os.UserHomeDir()
	if err == nil {
		p := filepath.Join(home, ".monoagent", "extension")
		if _, err := os.Stat(filepath.Join(p, "manifest.json")); err == nil {
			return p
		}
	}
	// 3. Fallback
	return "chrome-extension"
}

// UserDataDirs returns possible Chrome/Chromium user data directories.
func UserDataDirs() []string {
	var dirs []string

	if custom := os.Getenv("CHROME_USER_DATA_DIR"); custom != "" {
		dirs = append(dirs, custom)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return dirs
	}

	switch runtime.GOOS {
	case "darwin":
		dirs = append(dirs,
			filepath.Join(home, "Library", "Application Support", "Google", "Chrome"),
			filepath.Join(home, "Library", "Application Support", "Google", "Chrome Canary"),
			filepath.Join(home, "Library", "Application Support", "Chromium"),
			filepath.Join(home, "Library", "Application Support", "BraveSoftware", "Brave-Browser"),
			filepath.Join(home, "Library", "Application Support", "Microsoft Edge"),
		)
	case "windows":
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData != "" {
			dirs = append(dirs,
				filepath.Join(localAppData, "Google", "Chrome", "User Data"),
				filepath.Join(localAppData, "Chromium", "User Data"),
				filepath.Join(localAppData, "Microsoft", "Edge", "User Data"),
				filepath.Join(localAppData, "BraveSoftware", "Brave-Browser", "User Data"),
			)
		}
	default: // linux, etc.
		dirs = append(dirs,
			filepath.Join(home, ".config", "google-chrome"),
			filepath.Join(home, ".config", "google-chrome-beta"),
			filepath.Join(home, ".config", "google-chrome-unstable"),
			filepath.Join(home, ".config", "chromium"),
			filepath.Join(home, ".config", "BraveSoftware", "Brave-Browser"),
			filepath.Join(home, ".config", "microsoft-edge"),
		)
	}
	return dirs
}

// ExtensionInstalled scans browser profiles to verify if the MonoAgent
// extension is installed. checked reports whether the scan actually got to
// look: on macOS, ~/Library/Application Support/<browser> is gated by Full
// Disk Access, and a process without it gets a permission error reading a
// profile directory that plainly exists — treating that the same as "not
// found" would tell someone whose extension is installed and working to go
// reinstall it. checked is false whenever every existing profile directory
// was unreadable; found is only meaningful when checked is true. A browser
// that simply isn't installed (no such directory) doesn't count against
// checked — that candidate has nothing to fail to read.
func ExtensionInstalled() (found, checked bool) {
	userDataDirs := UserDataDirs()
	sawUnreadable := false
	for _, baseDir := range userDataDirs {
		if _, err := os.Stat(baseDir); err != nil {
			continue
		}
		entries, err := os.ReadDir(baseDir)
		if err != nil {
			sawUnreadable = true
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			profileDir := filepath.Join(baseDir, entry.Name())

			// 1. Check Preferences and Secure Preferences JSON files
			for _, prefName := range []string{"Secure Preferences", "Preferences"} {
				prefPath := filepath.Join(profileDir, prefName)
				if inPreferencesFile(prefPath) {
					return true, true
				}
			}

			// 2. Check Extensions directory
			extDir := filepath.Join(profileDir, "Extensions")
			if inExtensionsDir(extDir) {
				return true, true
			}
		}
	}
	return false, !sawUnreadable
}

// nameLooksLikeMonoAgent applies the same name/description substring match
// both call sites below need.
func nameLooksLikeMonoAgent(name, description string) bool {
	name = strings.ToLower(name)
	if strings.Contains(name, "monoagent") || strings.Contains(name, "mono-agent") || strings.Contains(name, "mono agent") {
		return true
	}
	desc := strings.ToLower(description)
	return strings.Contains(desc, "monoagent") || strings.Contains(desc, "mono-agent")
}

func inPreferencesFile(prefPath string) bool {
	data, err := os.ReadFile(prefPath)
	if err != nil {
		return false
	}
	var root struct {
		Extensions struct {
			Settings map[string]struct {
				Path     string `json:"path"`
				Manifest struct {
					Name        string `json:"name"`
					Description string `json:"description"`
				} `json:"manifest"`
			} `json:"settings"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return false
	}

	for _, setting := range root.Extensions.Settings {
		if nameLooksLikeMonoAgent(setting.Manifest.Name, setting.Manifest.Description) {
			return true
		}
		// Chromium/Edge does not cache a "manifest" object at all for
		// extensions loaded via "Load unpacked" (verified against a real
		// profile: the entry has path set but no manifest key whatsoever) —
		// so the check above can never match a dev-mode install, which is
		// exactly how AGENTS.md/README tell users to install this extension.
		// Read the real manifest.json from disk instead. Only for an
		// absolute path (an unpacked extension's own chosen folder) — a
		// packed extension's "path" is a relative "<id>/<version>" segment
		// under the profile's own Extensions dir, already covered by
		// inExtensionsDir, and must never be joined against an
		// arbitrary base here.
		if setting.Path == "" || !filepath.IsAbs(setting.Path) {
			continue
		}
		mData, err := os.ReadFile(filepath.Join(setting.Path, "manifest.json"))
		if err != nil {
			continue
		}
		var m struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if json.Unmarshal(mData, &m) == nil && nameLooksLikeMonoAgent(m.Name, m.Description) {
			return true
		}
	}
	return false
}

func inExtensionsDir(extDir string) bool {
	entries, err := os.ReadDir(extDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		extSubDir := filepath.Join(extDir, entry.Name())
		verEntries, err := os.ReadDir(extSubDir)
		if err != nil {
			continue
		}
		for _, verEntry := range verEntries {
			if !verEntry.IsDir() {
				continue
			}
			manifestPath := filepath.Join(extSubDir, verEntry.Name(), "manifest.json")
			mData, err := os.ReadFile(manifestPath)
			if err != nil {
				continue
			}
			var m struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(mData, &m); err == nil {
				name := strings.ToLower(m.Name)
				if strings.Contains(name, "monoagent") || strings.Contains(name, "mono-agent") || strings.Contains(name, "mono agent") {
					return true
				}
			}
		}
	}
	return false
}
