package browserdetect

import (
	"fmt"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFindBrowser(t *testing.T) {
	if path := FindBrowser(); path != "" {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("FindBrowser returned non-existent path: %s", path)
		}
	}
}

func TestExtensionInstalledEmptyHome(t *testing.T) {
	empty := t.TempDir()
	t.Setenv("CHROME_USER_DATA_DIR", empty)
	testhome.SetHome(t, empty)
	if found, checked := ExtensionInstalled(); found || !checked {
		t.Fatalf("no browser profiles: want (false, true), got (%v, %v)", found, checked)
	}
}

// TestExtensionInstalledUnreadableProfileIsUnchecked reproduces the bug this
// signature exists to fix: on macOS, ~/Library/Application Support/<browser>
// is gated by Full Disk Access, so a process without it gets a permission
// error reading a profile that plainly exists — that must report
// checked=false, not found=false, or a working, already-connected extension
// gets told it "isn't installed". Uses whichever candidate UserDataDirs
// itself returns for this OS, so it exercises the real path everywhere
// (chmod-based unreadable dirs, not meaningful on Windows, are skipped).
func TestExtensionInstalledUnreadableProfileIsUnchecked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based unreadable dirs aren't meaningful on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	home := t.TempDir()
	t.Setenv("CHROME_USER_DATA_DIR", "")
	testhome.SetHome(t, home)

	dirs := UserDataDirs()
	if len(dirs) == 0 {
		t.Fatal("UserDataDirs returned nothing for this OS")
	}
	profileRoot := dirs[0]
	if err := os.MkdirAll(profileRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(profileRoot, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(profileRoot, 0o755) // let t.TempDir() clean up

	if found, checked := ExtensionInstalled(); found || checked {
		t.Fatalf("unreadable profile dir: want (false, false) — unknown, not a confirmed absence — got (%v, %v)", found, checked)
	}
}

func TestIsExtensionInPreferencesFile(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Valid unpacked MonoAgent extension
	prefUnpacked := filepath.Join(tempDir, "pref_unpacked.json")
	unpackedData := `{
		"extensions": {
			"settings": {
				"abc123": {
					"path": "/path/to/my/project/chrome-extension",
					"manifest": {
						"name": "MonoAgent Bridge"
					}
				}
			}
		}
	}`
	if err := os.WriteFile(prefUnpacked, []byte(unpackedData), 0644); err != nil {
		t.Fatal(err)
	}
	if !inPreferencesFile(prefUnpacked) {
		t.Errorf("expected true for unpacked MonoAgent extension")
	}

	// 2. Packed MonoAgent extension (by manifest name)
	prefPacked := filepath.Join(tempDir, "pref_packed.json")
	packedData := `{
		"extensions": {
			"settings": {
				"xyz789": {
					"path": "/some/other/path",
					"manifest": {
						"name": "MonoAgent Bridge",
						"description": "Browser automation bridge"
					}
				}
			}
		}
	}`
	if err := os.WriteFile(prefPacked, []byte(packedData), 0644); err != nil {
		t.Fatal(err)
	}
	if !inPreferencesFile(prefPacked) {
		t.Errorf("expected true for packed MonoAgent extension")
	}

	// 3. Unrelated extensions only
	prefOther := filepath.Join(tempDir, "pref_other.json")
	otherData := `{
		"extensions": {
			"settings": {
				"other123": {
					"path": "/some/random/ext",
					"manifest": {
						"name": "Random AdBlocker",
						"description": "Blocks ads"
					}
				}
			}
		}
	}`
	if err := os.WriteFile(prefOther, []byte(otherData), 0644); err != nil {
		t.Fatal(err)
	}
	if inPreferencesFile(prefOther) {
		t.Errorf("expected false for unrelated extensions")
	}

	// 4. Corrupted / invalid JSON
	prefInvalid := filepath.Join(tempDir, "pref_invalid.json")
	if err := os.WriteFile(prefInvalid, []byte("{invalid json"), 0644); err != nil {
		t.Fatal(err)
	}
	if inPreferencesFile(prefInvalid) {
		t.Errorf("expected false for invalid json")
	}

	// 5. Non-existent file
	if inPreferencesFile(filepath.Join(tempDir, "non_existent.json")) {
		t.Errorf("expected false for non-existent file")
	}

	// 6. Real-world unpacked shape: Chromium/Edge does not cache a "manifest"
	// object at all for a "Load unpacked" extension (verified against an
	// actual Edge profile — the settings entry has "path" but no "manifest"
	// key whatsoever), so detection must fall back to reading the real
	// manifest.json at that path from disk.
	unpackedDir := filepath.Join(tempDir, "unpacked-ext")
	if err := os.MkdirAll(unpackedDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unpackedDir, "manifest.json"), []byte(`{"name": "MonoAgent Bridge"}`), 0644); err != nil {
		t.Fatal(err)
	}
	prefNoManifestCache := filepath.Join(tempDir, "pref_no_manifest_cache.json")
	noManifestCacheData := fmt.Sprintf(`{
		"extensions": {
			"settings": {
				"nniakjfndopplbofjmjgbbgljnmahpfg": {
					"path": %q
				}
			}
		}
	}`, unpackedDir)
	if err := os.WriteFile(prefNoManifestCache, []byte(noManifestCacheData), 0644); err != nil {
		t.Fatal(err)
	}
	if !inPreferencesFile(prefNoManifestCache) {
		t.Errorf("expected true for unpacked extension with no cached manifest (must read manifest.json from path)")
	}

	// 7. Same shape, but path is a relative "<id>/<version>" segment (how
	// packed extensions record it) — must not be joined against an
	// unrelated base and must not match.
	prefRelativePath := filepath.Join(tempDir, "pref_relative_path.json")
	relativePathData := `{
		"extensions": {
			"settings": {
				"someid": {
					"path": "someid/1.0.0_0"
				}
			}
		}
	}`
	if err := os.WriteFile(prefRelativePath, []byte(relativePathData), 0644); err != nil {
		t.Fatal(err)
	}
	if inPreferencesFile(prefRelativePath) {
		t.Errorf("expected false for a relative packed-extension path with no cached manifest")
	}
}

func TestIsExtensionInExtensionsDir(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Directory with MonoAgent manifest
	extSubDir := filepath.Join(tempDir, "ext1", "1.0.0")
	if err := os.MkdirAll(extSubDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifestData := `{"name": "MonoAgent Bridge", "version": "1.0.0"}`
	if err := os.WriteFile(filepath.Join(extSubDir, "manifest.json"), []byte(manifestData), 0644); err != nil {
		t.Fatal(err)
	}

	if !inExtensionsDir(tempDir) {
		t.Errorf("expected true for valid MonoAgent extension directory")
	}

	// 2. Directory without MonoAgent manifest
	emptyDir := t.TempDir()
	otherSubDir := filepath.Join(emptyDir, "ext2", "1.0.0")
	if err := os.MkdirAll(otherSubDir, 0755); err != nil {
		t.Fatal(err)
	}
	otherManifest := `{"name": "Some Other Extension", "version": "2.0.0"}`
	if err := os.WriteFile(filepath.Join(otherSubDir, "manifest.json"), []byte(otherManifest), 0644); err != nil {
		t.Fatal(err)
	}

	if inExtensionsDir(emptyDir) {
		t.Errorf("expected false for unrelated extension directory")
	}
}

// Edge's Linux package installs /usr/bin/microsoft-edge-stable; browsers
// elsewhere on PATH are found by name when no well-known path exists.
func TestFindBrowserKnowsEdgeStableAndSearchesPATH(t *testing.T) {
	if runtime.GOOS == "linux" {
		found := false
		for _, c := range browserCandidates() {
			found = found || c == "/usr/bin/microsoft-edge-stable"
		}
		if !found {
			t.Error("/usr/bin/microsoft-edge-stable is not a candidate")
		}
	}
	if runtime.GOOS == "windows" {
		t.Skip("shell-less PATH lookup uses .exe names")
	}
	dir := t.TempDir()
	edge := filepath.Join(dir, "microsoft-edge-stable")
	if err := os.WriteFile(edge, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	missing := []string{filepath.Join(t.TempDir(), "google-chrome")}
	if got := findBrowser(missing, pathNames); got != edge {
		t.Fatalf("findBrowser = %q, want %q from PATH", got, edge)
	}
	// A well-known path wins over PATH.
	known := filepath.Join(t.TempDir(), "chrome")
	os.WriteFile(known, []byte("x"), 0o755)
	if got := findBrowser([]string{known}, pathNames); got != known {
		t.Fatalf("findBrowser = %q, want the well-known %q", got, known)
	}
	t.Setenv("PATH", t.TempDir())
	if got := findBrowser(missing, pathNames); got != "" {
		t.Fatalf("nothing installed: %q", got)
	}
}
