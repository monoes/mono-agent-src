package account_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/account"
)

// lockExpectingFailure is a Lock that the test expects to fail. Should it succeed
// after all, the lock is released at once, so one failure does not hang every
// test that locks after it.
func lockExpectingFailure(ctx context.Context, st account.Store) error {
	unlock, err := st.Lock(ctx)
	if err == nil {
		unlock()
	}
	return err
}

// A Lock that is cancelled while it waits returns the context's own error, soon,
// and the abandoned wait holds nothing: once the holder lets go the lock is free.
func TestLockHonorsItsContextAndHoldsNothingAfterIt(t *testing.T) {
	a, dir := newStore(t)
	b := account.OpenStore(dir, account.NewMemorySealer())
	unlockA, err := a.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	err = lockExpectingFailure(ctx, b)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a Lock cancelled while it waits: err = %v, want context.Canceled", err)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("a cancelled Lock took %v to return", waited)
	}
	unlockA()
	again, cancelAgain := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelAgain()
	unlockB, err := b.Lock(again)
	if err != nil {
		t.Fatalf("Lock after a cancelled wait and a release: %v", err)
	}
	unlockB()
}

// Goroutines that share one Store exclude each other too (a Guard has one Store
// and several goroutines that refresh).
func TestLockIsExclusiveForGoroutinesSharingOneStore(t *testing.T) {
	st, _ := newStore(t)
	unlock, err := st.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if err := lockExpectingFailure(short, st); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a second Lock on the same Store while held: err = %v, want a deadline", err)
	}
	unlock()
	again, err := st.Lock(context.Background())
	if err != nil {
		t.Fatalf("Lock after unlock: %v", err)
	}
	again()
}

// The unlock function is safe to call at the same time from several goroutines.
// (Under -race an unsynchronized second call is reported.)
func TestUnlockIsSafeFromSeveralGoroutinesAtOnce(t *testing.T) {
	st, _ := newStore(t)
	unlock, err := st.Lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock()
		}()
	}
	wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	again, err := st.Lock(ctx)
	if err != nil {
		t.Fatalf("Lock after the unlocks: %v", err)
	}
	again()
}

// lockHolderEnv marks the child of TestLockAcrossProcesses: it takes the lock on
// the directory the variable names and holds it until its stdin ends.
const lockHolderEnv = "ACCOUNT_LOCK_HOLDER_DIR"

type lockHolder struct {
	cmd     *exec.Cmd
	drained chan struct{}
	once    sync.Once
}

// kill ends the holder the way a crash does: no chance to unlock or to clean up.
func (h *lockHolder) kill() {
	h.once.Do(func() {
		_ = h.cmd.Process.Kill()
		<-h.drained
		_ = h.cmd.Wait()
	})
}

// startLockHolder runs this test binary again as a process that holds the lock on
// dir, and returns once it says so. Its temporary files go under tmp, so a child
// that is killed leaves nothing outside the parent's test directory.
func startLockHolder(t *testing.T, tmp, dir string) *lockHolder {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, exe, "-test.run", "^TestLockAcrossProcesses$", "-test.count=1")
	cmd.Env = append(os.Environ(), lockHolderEnv+"="+dir, "TMPDIR="+tmp, "TMP="+tmp, "TEMP="+tmp)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h := &lockHolder{cmd: cmd, drained: make(chan struct{})}
	t.Cleanup(func() {
		stdin.Close()
		h.kill()
	})
	var lines []string // read only after drained is closed
	holding := make(chan bool, 1)
	go func() {
		defer close(h.drained)
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if sc.Text() == "holding" {
				holding <- true
				break
			}
			lines = append(lines, sc.Text())
		}
		_, _ = io.Copy(io.Discard, stdout)
		select {
		case holding <- false:
		default:
		}
	}()
	select {
	case ok := <-holding:
		if !ok {
			<-h.drained
			t.Fatalf("the lock holder process ended without taking the lock:\n%s", strings.Join(lines, "\n"))
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the lock holder process did not take the lock within 60 seconds")
	}
	return h
}

// The lock excludes other processes, not only other goroutines, and a process
// that dies holding it leaves nothing stale: the system drops the lock with the
// process, and the lock file that stays behind is not a lock.
func TestLockAcrossProcesses(t *testing.T) {
	if dir := os.Getenv(lockHolderEnv); dir != "" {
		unlock, err := account.OpenStore(dir, account.NewMemorySealer()).Lock(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		fmt.Println("holding")
		_, _ = io.Copy(io.Discard, os.Stdin) // until the parent closes our stdin or kills us
		return
	}
	root := t.TempDir()
	tmp := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "account")
	st := account.OpenStore(dir, account.NewMemorySealer())

	holder := startLockHolder(t, tmp, dir)
	short, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	err := lockExpectingFailure(short, st)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Lock while another process holds it: err = %v, want a deadline", err)
	}

	holder.kill()
	if _, err := os.Stat(filepath.Join(dir, "session.lock")); err != nil {
		t.Fatalf("the lock file of the process that died is gone (%v): the premise of this check is a file that stays", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	unlock, err := st.Lock(ctx)
	if err != nil {
		t.Fatalf("Lock after the holder process died: %v: a crash must leave no stale lock", err)
	}
	unlock()
}
