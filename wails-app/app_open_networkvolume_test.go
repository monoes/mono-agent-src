package main

import (
	"runtime"
	"testing"
)

// The resolved path is judged by networkVolume: a result that is a UNC share
// must be refused. Replacing the guard with "false" must fail this test.
func TestResolveChatTokenRefusesAResolvedNetworkVolume(t *testing.T) {
	old := chatEvalSymlinks
	defer func() { chatEvalSymlinks = old }()
	root := "/w"
	if runtime.GOOS == "windows" {
		root = `C:\w` // a rooted path needs a drive there
	}
	for _, resolved := range []string{`\\h\s\x.md`, `//h/s/x.md`, `\\?\UNC\h\s\x.md`, `/\h\s\x.md`} {
		chatEvalSymlinks = func(string) (string, error) { return resolved, nil }
		if _, _, err := resolveChatToken(root, "x.md"); err == nil {
			t.Errorf("a result on %q must be refused", resolved)
		}
	}
	chatEvalSymlinks = func(p string) (string, error) { return p, nil }
	if _, _, err := resolveChatToken(root, "x.md"); err != nil {
		t.Fatalf("a local result is fine: %v", err)
	}
	if !networkVolume(`\\host\share\x`) || !networkVolume(`\\?\UNC\host\share\x`) || networkVolume(`/mnt/share/x`) || networkVolume(`C:\x`) {
		t.Fatal("networkVolume spelling")
	}
}
