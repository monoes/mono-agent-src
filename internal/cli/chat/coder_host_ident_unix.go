//go:build !windows

package chat

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// stopStaleProcess ends the stale host named by info after the process has been shown to be this database's coder host. A
// variable so a test can stand in for the process.
var stopStaleProcess = stopStaleProcessOS

// verifyHostProcess reads pid's command line and checks it with verifyHostArgs. host.json is a plain file any process of the user can
// write, so a restart that nobody asked for by hand never signals a pid just because the file names it.
func verifyHostProcess(pid int, dbPath string) error {
	args, err := processArgs(pid)
	if err != nil {
		return fmt.Errorf("the process %d could not be identified as the coder host: %w", pid, err)
	}
	cwd, _ := processCwd(pid) // "" where it cannot be read: a relative --db-path then cannot be resolved and is refused
	return verifyHostArgsIn(args, dbPath, cwd)
}

// hostGlobalFlags are the flags that may stand between the binary and `coder host run` of a host this program starts.
var hostGlobalFlags = map[string]bool{"--db-path": true, "--profile": true, "--config-dir": true, "--output-dir": true, "--log-file": true, "--log-level": true, "--lang": true}

// verifyHostArgs says whether args are those of a coder host of the database at dbPath: a monoagentcli binary, only known global
// flags, then exactly `coder host run`; --db-path names this database (or is absent and this is the default one).
func verifyHostArgs(args []string, dbPath string) error { return verifyHostArgsIn(args, dbPath, "") }

// verifyHostArgsIn is verifyHostArgs for a process whose working directory is cwd ("" when unknown).
func verifyHostArgsIn(args []string, dbPath, cwd string) error {
	notHost := fmt.Errorf("the process is not this database's coder host (it does not run `monoagentcli ... coder host run` for %s)", dbPath)
	if len(args) < 4 || !strings.HasPrefix(strings.TrimSuffix(filepath.Base(args[0]), ".exe"), "monoagentcli") {
		return notHost
	}
	db := ""
	i := 1
	for ; i < len(args) && strings.HasPrefix(args[i], "--"); i++ {
		name, val, hasVal := strings.Cut(args[i], "=")
		if !hostGlobalFlags[name] {
			return notHost
		}
		if !hasVal {
			if i+1 >= len(args) {
				return notHost
			}
			i++
			val = args[i]
		}
		if name == "--db-path" {
			db = val
		}
	}
	if len(args[i:]) != 3 || args[i] != "coder" || args[i+1] != "host" || args[i+2] != "run" {
		return notHost
	}
	if db == "" {
		db = "~/.monoagent/monoagent.db"
	}
	if !sameDBPath(db, dbPath, cwd) {
		return notHost
	}
	return nil
}

// sameDBPath says whether the host's --db-path (relative to the host's working directory cwd) and ours are the same file once both
// are absolute with their symlinks resolved. A relative path with no known cwd is not resolved, so it is not the same.
func sameDBPath(hostDB, ours, cwd string) bool {
	hostDB = clicore.ExpandPath(hostDB)
	if !filepath.IsAbs(hostDB) {
		if cwd == "" {
			return false
		}
		hostDB = filepath.Join(cwd, hostDB)
	}
	a, b := resolvedPath(hostDB), resolvedPath(clicore.ExpandPath(ours))
	return a != "" && a == b
}

// resolvedPath is p made absolute with its symlinks resolved; a file that does not exist yet keeps its name under its resolved directory.
func resolvedPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return abs
	}
	return filepath.Join(dir, filepath.Base(abs))
}

func processArgs(pid int) ([]string, error) {
	switch runtime.GOOS {
	case "linux":
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			return nil, err
		}
		return strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00"), nil
	case "darwin":
		raw, err := procArgsRaw(pid)
		if err != nil {
			return nil, err
		}
		return parseProcArgs2(raw)
	}
	out := psOutput("-o", "command=", "-p", fmt.Sprint(pid)) // other systems: ps cannot keep a space inside one argument
	if out == "" {
		return nil, errors.New("ps shows no such process")
	}
	return strings.Fields(out), nil
}

// processCwd is the working directory of pid where the system tells it without a helper (Linux); a relative --db-path of a host is
// relative to it.
func processCwd(pid int) (string, error) {
	if runtime.GOOS != "linux" {
		return "", errors.New("the working directory of another process is not read on this system")
	}
	return os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
}

// parseProcArgs2 reads the argument vector out of the raw KERN_PROCARGS2 buffer of macOS: an int argc, the executable path, NUL
// padding, then argc NUL-terminated arguments (the environment follows and is not read). Nothing is split on white space.
func parseProcArgs2(raw []byte) ([]string, error) {
	bad := errors.New("the process arguments are not in the expected form")
	if len(raw) < 4 {
		return nil, bad
	}
	argc := int(binary.NativeEndian.Uint32(raw))
	rest := raw[4:]
	if argc <= 0 || argc > len(rest) {
		return nil, bad
	}
	end := bytes.IndexByte(rest, 0) // the executable path
	if end < 0 {
		return nil, bad
	}
	rest = bytes.TrimLeft(rest[end:], "\x00")
	args := make([]string, 0, argc)
	for len(args) < argc {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			return nil, bad
		}
		args = append(args, string(rest[:end]))
		rest = rest[end+1:]
	}
	return args, nil
}

// stopStaleUnpinned checks the process and signals the pid (where no process handle can be had before the checks).
func stopStaleUnpinned(cfg *Deps, info hostInfo) (string, error) {
	if err := verifyHostProcess(info.PID, cfg.DBPath()); err != nil {
		return "", err
	}
	return stopHost(cfg, info)
}
