//go:build !windows

package dynorg

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// When the placed command cannot be adopted, the command AND what it forked since it started are ended and the error comes back;
// nothing is left running unrecorded. The command here is a git alias that forks a long sleep; the injected adopter waits until the
// child exists, so the group kill (not only the leader's) is what the test needs.
func TestGitRunEndsTheWholeCommandWhenItCannotBeAdopted(t *testing.T) {
	repo := newRepo(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	boom := errors.New("adoption refused")
	old := adoptPlaced
	adoptPlaced = func(cmd *exec.Cmd) error {
		for i := 0; i < 500; i++ {
			if b, err := os.ReadFile(pidFile); err == nil && strings.TrimSpace(string(b)) != "" {
				return boom
			}
			time.Sleep(10 * time.Millisecond)
		}
		return errors.New("the child never started")
	}
	t.Cleanup(func() { adoptPlaced = old })
	ctx := withGitPlacer(context.Background(), func(cmd *exec.Cmd) (func(), error) {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		return func() {}, nil
	})
	alias := `alias.fork=!sleep 60 & echo $! > ` + pidFile + `; wait`
	start := time.Now()
	_, err := gitRun(ctx, repo, "-c", alias, "fork")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 20*time.Second {
		t.Fatal("the wait after the kill was not bounded")
	}
	b, _ := os.ReadFile(pidFile)
	child, perr := strconv.Atoi(strings.TrimSpace(string(b)))
	if perr != nil {
		t.Fatalf("no child pid: %q", b)
	}
	for i := 0; i < 200; i++ { // the child is reparented and reaped by init: a short poll
		if syscall.Kill(child, 0) == syscall.ESRCH {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = syscall.Kill(child, syscall.SIGKILL)
	t.Fatalf("the git child %d was left running after a failed adoption", child)
}
