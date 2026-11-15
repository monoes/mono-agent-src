//go:build !windows

package chat

import (
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
	return verifyHostArgs(args, dbPath)
}

// hostGlobalFlags are the flags that may stand between the binary and `coder host run` of a host this program starts.
var hostGlobalFlags = map[string]bool{"--db-path": true, "--profile": true, "--config-dir": true, "--output-dir": true, "--log-file": true, "--log-level": true, "--lang": true}

// verifyHostArgs says whether args are those of a coder host of the database at dbPath: a monoagentcli binary, only known global
// flags, then exactly `coder host run`; --db-path names this database (or is absent and this is the default one).
func verifyHostArgs(args []string, dbPath string) error {
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
	if clicore.ExpandPath(db) != clicore.ExpandPath(dbPath) {
		return notHost
	}
	return nil
}

func processArgs(pid int) ([]string, error) {
	if runtime.GOOS == "linux" {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			return nil, err
		}
		return strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00"), nil
	}
	out := psOutput("-o", "command=", "-p", fmt.Sprint(pid))
	if out == "" {
		return nil, errors.New("ps shows no such process")
	}
	return strings.Fields(out), nil
}

// stopStaleUnpinned checks the process and signals the pid (where no process handle can be had before the checks).
func stopStaleUnpinned(cfg *Deps, info hostInfo) (string, error) {
	if err := verifyHostProcess(info.PID, cfg.DBPath()); err != nil {
		return "", err
	}
	return stopHost(cfg, info)
}
