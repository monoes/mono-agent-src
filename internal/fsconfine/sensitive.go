package fsconfine

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

// homeSecretDirs are folders of a user's home that hold credentials or
// session data. Paths are slash-separated and relative to the home.
var homeSecretDirs = []string{
	".ssh", ".aws", ".gnupg", ".kube", ".docker", ".azure", ".config/gcloud", ".config/gh",
	".config/google-chrome", ".config/chromium", ".config/microsoft-edge", ".config/BraveSoftware",
	".config/vivaldi", ".config/opera", ".mozilla", ".var/app", "snap",
	"Library/Application Support/Google/Chrome", "Library/Application Support/Microsoft Edge",
	"Library/Application Support/BraveSoftware", "Library/Application Support/Firefox",
	"Library/Keychains", "AppData/Local/Google/Chrome", "AppData/Local/Microsoft/Edge",
	"AppData/Local/BraveSoftware", "AppData/Roaming/Mozilla",
}

// homeSecretFiles are single credential files directly in the home.
var homeSecretFiles = []string{".netrc", ".git-credentials", ".npmrc", ".pgpass", ".pypirc"}

// SensitivePath reports whether p, after symlinks are resolved, is a place
// that holds credentials: the SSH, cloud, GnuPG and browser-profile folders
// of any home on this machine, mono-agent's own state folder (except its
// profile folders), root's home, the system password and sudo files, and a
// process's environment, command line, memory, descriptors or mappings.
// vault.Register* refuses these for every caller, so a workflow cannot copy
// a key file into the vault and read it back.
func SensitivePath(p string) bool {
	abs, err := filepath.Abs(p)
	if err != nil {
		return true
	}
	if procSecret(filepath.Clean(abs)) { // before symlinks: /proc/self/fd/3 resolves to its target
		return true
	}
	resolved, err := resolveExisting(filepath.Clean(abs))
	if err != nil {
		return true // cannot say where it points: refuse
	}
	return sensitiveResolved(resolved)
}

func sensitiveResolved(p string) bool {
	if procSecret(p) || systemSecret(p) {
		return true
	}
	for _, h := range sensitiveHomes() {
		if homeSecret(h, p) {
			return true
		}
	}
	return false
}

func sensitiveHomes() []string {
	var out []string
	add := func(h string) {
		if h == "" {
			return
		}
		if r, err := filepath.EvalSymlinks(h); err == nil {
			h = r
		}
		for _, o := range out {
			if o == h {
				return
			}
		}
		out = append(out, h)
	}
	if h, err := os.UserHomeDir(); err == nil {
		add(h)
	}
	if u, err := user.Current(); err == nil {
		add(u.HomeDir)
	}
	return out
}

func homeSecret(home, p string) bool {
	if !in(home, p) {
		return false
	}
	rel, _ := filepath.Rel(home, p)
	rel = filepath.ToSlash(rel)
	for _, d := range homeSecretDirs {
		if fold(rel) == fold(d) || strings.HasPrefix(fold(rel), fold(d)+"/") {
			return true
		}
	}
	for _, f := range homeSecretFiles {
		if rel == f {
			return true
		}
	}
	if rel == ".monoagent" {
		return true
	}
	if strings.HasPrefix(rel, ".monoagent/") {
		// The profile folders are the user's own files.
		return !strings.HasPrefix(rel, ".monoagent/profiles/")
	}
	return false
}

// systemSecret is the machine-wide credential stores outside any home.
func systemSecret(p string) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	switch p {
	case "/root", "/etc/shadow", "/etc/gshadow", "/etc/sudoers", "/etc/master.passwd",
		"/etc/ssh/ssh_host_rsa_key", "/etc/ssh/ssh_host_ed25519_key", "/etc/ssh/ssh_host_ecdsa_key":
		return true
	}
	return strings.HasPrefix(p, "/root/") || strings.HasPrefix(p, "/etc/sudoers.d/") ||
		strings.HasPrefix(p, "/etc/shadow") || strings.HasPrefix(p, "/etc/security/")
}
