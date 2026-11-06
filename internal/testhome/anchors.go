package testhome

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// A test that starts a REAL process-group unit (macOS containment) leaves its anchor process running when it forgets to end
// it: an `/bin/sleep 2147483647` renamed monoagent-run-<gen>-<nonce>.service (or monoagent-probe-<hex>), re-parented to
// init. Main compares the anchors before and after the tests on macOS, kills the new ones and fails the run. (Linux cgroup
// units are env-gated tests and hold no process of their own; Windows jobs are not processes.)

// anchorNames are the argv[0] prefixes of the anchors.
var anchorNames = []string{"monoagent-run-", "monoagent-probe-"}

// parseAnchors reads `ps -axo pid=,command=` output and returns the anchors' pids by command.
func parseAnchors(ps string) map[int]string {
	out := map[int]string{}
	sc := bufio.NewScanner(strings.NewReader(ps))
	for sc.Scan() {
		f := strings.TrimSpace(sc.Text())
		sp := strings.IndexAny(f, " \t")
		if sp < 0 {
			continue
		}
		pid, err := strconv.Atoi(f[:sp])
		if err != nil {
			continue
		}
		cmd := strings.TrimSpace(f[sp:])
		for _, p := range anchorNames {
			if strings.HasPrefix(cmd, p) {
				out[pid] = cmd
			}
		}
	}
	return out
}

// newAnchors are the anchors in after that were not in before, by pid.
func newAnchors(before, after map[int]string) []int {
	var left []int
	for pid, cmd := range after {
		if b, ok := before[pid]; !ok || b != cmd {
			left = append(left, pid)
		}
	}
	sort.Ints(left)
	return left
}

func psAnchors() map[int]string {
	if runtime.GOOS != "darwin" {
		return nil
	}
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil
	}
	return parseAnchors(string(out))
}

// checkAnchors kills the anchors the tests left and says so.
func checkAnchors(before map[int]string) string {
	left := newAnchors(before, psAnchors())
	if len(left) == 0 {
		return ""
	}
	for _, pid := range left {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	}
	return fmt.Sprintf("%d anchor process(es) were left running by the tests and have been killed: pids %v", len(left), left)
}
