//go:build linux

package operator

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/testmode"
	"github.com/monoes/mono-agent/internal/workflow"
)

// ancestorMarker names what marks one of this process's parents, below
// init: processMarker, or ancestorTooDeep; "" when none does. A parent
// whose /proc entries can't be read (another user's) ends the walk.
func ancestorMarker() string {
	if testmode.On && !ancestorsInTests {
		return ""
	}
	return walkAncestors(os.Getppid(), processMarker, parentOf)
}

// unapprovedAncestor reports whether one of this process's parents carries
// workflow.EnvUnapprovedRun (or the chain can't be read to its end): a child that
// dropped its own copy still comes from an unapproved run.
func unapprovedAncestor() bool {
	if testmode.On && !ancestorsInTests {
		return false
	}
	return walkUnapproved(os.Getppid(), func(pid int) (bool, error) {
		env, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
		return err == nil && bytes.Contains(append([]byte{0}, env...), []byte("\x00"+workflow.EnvUnapprovedRun+"=1\x00")), err
	}, parentOf)
}

// walkUnapproved climbs from pid and reports whether a parent has the mark.
// It fails closed on a broken chain: a parent that has gone or whose own
// parent can't be found, and a chain deeper than ancestorMaxDepth. A parent
// whose environment is closed to this user (permission denied) is not
// broken: a marked workflow child is this user's own process, and its
// environment is readable, so what can't be read is systemd --user, sshd's
// privsep children or a root-owned service, never a workflow run. Reaching
// init (or pid 0 of a container) through links like these is clean. The
// limit: a process a double fork handed to init has lost its real parents,
// and looks clean; only a per-run approval record closes that.
func walkUnapproved(pid int, marked func(int) (bool, error), parent func(int) (int, bool)) bool {
	for depth := 0; pid > 1 && pid != ancestorsStopAt; depth++ {
		if depth == ancestorMaxDepth {
			return true
		}
		m, err := marked(pid)
		if m || (err != nil && !errors.Is(err, fs.ErrPermission)) {
			return true
		}
		ppid, ok := parent(pid)
		if !ok {
			return true
		}
		pid = ppid
	}
	return false
}

// walkAncestors climbs from pid through parent and names the first process
// marker marks; "" when it reaches init (pid 1), ancestorsStopAt or a
// parent it can't read first. A chain still climbing after ancestorMaxDepth
// processes counts as marked (ancestorTooDeep): nesting that many clean
// shells is how a marked caller would put its mark out of reach.
func walkAncestors(pid int, marker func(int) string, parent func(int) (int, bool)) string {
	for depth := 0; pid > 1 && pid != ancestorsStopAt; depth++ {
		if depth == ancestorMaxDepth {
			return ancestorTooDeep
		}
		if m := marker(pid); m != "" {
			return m
		}
		ppid, ok := parent(pid)
		if !ok {
			return ""
		}
		pid = ppid
	}
	return ""
}

// processMarker names what marks process pid: any variable Marker reads
// (MarkerEnv: a workflow run, the chat, CLAUDECODE, MONOAGENT_ACTOR and the
// other agents' markers) in the environment it was started with, or a
// `monomind agent exec` command line; "" when none does.
func processMarker(pid int) string {
	dir := "/proc/" + strconv.Itoa(pid)
	if env, err := os.ReadFile(dir + "/environ"); err == nil {
		set := map[string]bool{}
		for _, kv := range bytes.Split(env, []byte{0}) {
			if k, v, ok := bytes.Cut(kv, []byte("=")); ok && len(bytes.TrimSpace(v)) > 0 {
				set[string(k)] = true
			}
		}
		for k, m := range map[string]string{workflow.EnvWorkflowRun: ancestorWorkflowRun, browserctl.EnvAICaller: ancestorAICaller} {
			if set[k] {
				return m
			}
		}
		for _, k := range MarkerEnv() {
			if set[k] {
				return ancestorPrefix + "'s " + k
			}
		}
	}
	if cmd, err := os.ReadFile(dir + "/cmdline"); err == nil && isAgentExec(bytes.Split(cmd, []byte{0})) {
		return ancestorAgentExec
	}
	return ""
}

// isAgentExec reports whether a command line runs `monomind agent exec`: an
// argument naming monomind, then agent, then exec ("node …/monomind agent
// exec …" for the script the kernel hands to node).
func isAgentExec(args [][]byte) bool {
	for i := 0; i+2 < len(args); i++ {
		if bytes.Contains([]byte(filepath.Base(string(args[i]))), []byte("monomind")) &&
			string(args[i+1]) == "agent" && string(args[i+2]) == "exec" {
			return true
		}
	}
	return false
}

// parentOf is pid's parent, from /proc/<pid>/status.
func parentOf(pid int) (int, bool) {
	status, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, false
	}
	for _, line := range bytes.Split(status, []byte{'\n'}) {
		if v, ok := bytes.CutPrefix(line, []byte("PPid:")); ok {
			ppid, err := strconv.Atoi(string(bytes.TrimSpace(v)))
			return ppid, err == nil
		}
	}
	return 0, false
}
