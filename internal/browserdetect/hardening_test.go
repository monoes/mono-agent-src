package browserdetect

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeManifest(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"manifest_version":3,"name":"` + name + `"}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Running inside someone else's checkout must not make the instructions send
// the person to Load unpacked from a folder that is not MonoAgent's.
func TestExtensionDirPrefersInstalledOverForeignCwdCopy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	installed := filepath.Join(home, ".monoagent", "extension")
	writeManifest(t, installed, "MonoAgent Bridge")

	cwd := t.TempDir()
	writeManifest(t, filepath.Join(cwd, "chrome-extension"), "Totally Innocent Helper")
	t.Chdir(cwd)

	if got := ExtensionDir(); got != installed {
		t.Errorf("ExtensionDir() = %q, want the installed %q", got, installed)
	}
}

func TestExtensionDirUsesOwnCwdCopyWhenNotInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cwd := t.TempDir()
	own := filepath.Join(cwd, "chrome-extension")
	writeManifest(t, own, "MonoAgent Bridge")
	t.Chdir(cwd)
	if got := ExtensionDir(); got != own {
		t.Errorf("ExtensionDir() = %q, want the checkout's own %q", got, own)
	}
}

// pgrep -f matches any command line that mentions a browser's name; the
// linux check matches process names only.
func TestLinuxPgrepMatchesProcessNamesOnly(t *testing.T) {
	args := pgrepArgs("linux")
	if slices.Contains(args, "-f") {
		t.Errorf("pgrep args %v match whole command lines (-f)", args)
	}
	if !slices.Contains(args, "-x") {
		t.Errorf("pgrep args %v do not anchor on the process name (-x)", args)
	}
	if !slices.Contains(pgrepArgs("darwin"), "-f") {
		t.Error("the macOS app-path match must keep -f")
	}
}
