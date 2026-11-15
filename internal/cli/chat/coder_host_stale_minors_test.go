//go:build !windows

package chat

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// procArgs2 builds what macOS's kern.procargs2 returns: argc, the executable path, NUL padding, argv, then the environment.
func procArgs2(exec string, argv ...string) []byte {
	raw := binary.LittleEndian.AppendUint32(nil, uint32(len(argv)))
	raw = append(raw, exec...)
	raw = append(raw, 0, 0, 0)
	for _, a := range argv {
		raw = append(raw, a...)
		raw = append(raw, 0)
	}
	return append(raw, "HOME=/Users/a b\x00PATH=/bin\x00"...)
}

// #1662: a binary and a database under paths with spaces are read from the NUL-separated vector and pass for the real host;
// an impostor under such a path is still refused.
func TestMacArgvWithSpacesIsReadWholeAndTheCheckStaysStrict(t *testing.T) {
	const exe = "/Applications/Mono Agent.app/Contents/MacOS/monoagentcli"
	const db = "/Users/a b/Library/Application Support/MonoAgent/monoagent.db"
	argv := []string{exe, "--db-path", db, "--profile", "default", "coder", "host", "run"}
	got, err := parseProcArgs2(procArgs2(exe, argv...))
	if err != nil || fmt.Sprint(got) != fmt.Sprint(argv) {
		t.Fatalf("parsed %q, %v", got, err)
	}
	if err := verifyHostArgs(got, db); err != nil {
		t.Fatalf("the real host under a path with spaces is refused: %v", err)
	}
	if err := verifyHostArgs(got, "/Users/a b/other.db"); err == nil {
		t.Fatal("a host of another database passed")
	}
	imp := []string{"/Applications/Mono Agent.app/Contents/MacOS/sh", "-c", "monoagentcli --db-path x coder host run"}
	got, err = parseProcArgs2(procArgs2(imp[0], imp...))
	if err != nil || verifyHostArgs(got, db) == nil {
		t.Fatalf("an impostor passed: %q %v", got, err)
	}
	for name, raw := range map[string][]byte{"empty": nil, "short": {1, 0}, "argc beyond the data": append(binary.LittleEndian.AppendUint32(nil, 9), "/x\x00a\x00"...)} {
		if _, err := parseProcArgs2(raw); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

// #1662: --db-path is compared after it is made absolute (against the host's own directory) and symlinks are resolved.
func TestHostDbPathIsComparedResolved(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(real, "monoagent.db")
	host := func(path string) []string {
		return []string{"/usr/bin/monoagentcli", "--db-path", path, "coder", "host", "run"}
	}
	for name, c := range map[string]struct{ path, cwd string }{
		"through a symlinked directory": {filepath.Join(link, "monoagent.db"), ""},
		"relative to the host's cwd":    {"real/monoagent.db", root},
		"with dot segments":             {filepath.Join(real, "..", "real", "monoagent.db"), ""},
		"relative and symlinked":        {"link/monoagent.db", root},
	} {
		if err := verifyHostArgsIn(host(c.path), db, c.cwd); err != nil {
			t.Errorf("%s refused: %v", name, err)
		}
	}
	for name, c := range map[string]struct{ path, cwd string }{
		"relative with no known cwd":      {"real/monoagent.db", ""},
		"relative from another directory": {"real/monoagent.db", real},
		"another file":                    {filepath.Join(real, "other.db"), ""},
	} {
		if err := verifyHostArgsIn(host(c.path), db, c.cwd); err == nil {
			t.Errorf("%s passed", name)
		}
	}
}
