//go:build linux

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/procbind"
)

// builtBinary compiles the real command once per test binary run.
func builtBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "monoagentcli")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// poisonedPath is a FIFO standing in for the database path. opened fires when anything opens it for reading: the
// recorder holds the write end open, which only succeeds once a reader appears.
func poisonedPath(t *testing.T) (path string, opened <-chan struct{}) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "poisoned.db")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	ch := make(chan struct{})
	go func() {
		f, err := os.OpenFile(path, os.O_WRONLY, 0) // blocks until a reader opens the FIFO
		if err == nil {
			f.Close()
			close(ch)
		}
	}()
	t.Cleanup(func() { // release the recorder if nothing opened the FIFO, so the goroutine ends
		if f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
			f.Close()
		}
	})
	return path, ch
}

type runResult struct {
	code   int
	stderr string
	took   time.Duration
}

// runWithFrame starts the binary with args and a frame pipe on descriptor 3 announced by the marker. frame is written
// to the pipe; closeWriter says whether the write end is closed afterwards (false: a writer that never finishes).
func runWithFrame(t *testing.T, bin string, frame []byte, closeWriter bool, args ...string) runResult {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if len(frame) > 0 {
		if _, err := w.Write(frame); err != nil {
			t.Fatal(err)
		}
	}
	if closeWriter {
		w.Close()
	} else {
		defer w.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.ExtraFiles = []*os.File{r}
	cmd.Env = append(os.Environ(), "MONOAGENT_CRED_FD=3", "HOME="+t.TempDir())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	start := time.Now()
	err = cmd.Run()
	res := runResult{stderr: stderr.String(), took: time.Since(start)}
	if ee, ok := err.(*exec.ExitError); ok {
		res.code = ee.ExitCode()
	} else if err != nil {
		res.code = -1
	}
	return res
}

// A turn launched with a credential frame reads it before anything else. A missing, late or malformed frame ends the
// process with the frame exit code before any flag, config, account check or database is touched: the poisoned
// database path (a FIFO whose opening is recorded) is never opened. The control run, with a good frame, does open
// it, which proves the recorder would have seen a premature open.
func TestTheBinaryReadsTheFrameBeforeTouchingAnything(t *testing.T) {
	bin := builtBinary(t)
	good, err := procbind.CredFrame{SessionToken: []byte("mst2-" + string(bytes.Repeat([]byte("a"), 43)))}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		frame  []byte
		closed bool
		maxSec float64
	}{
		"malformed frame":            {frame: []byte("this is not a frame at all"), closed: true, maxSec: 8},
		"truncated frame":            {frame: good[:len(good)-4], closed: true, maxSec: 8},
		"no frame, writer closed":    {frame: nil, closed: true, maxSec: 8},
		"a frame that never arrives": {frame: nil, closed: false, maxSec: 20},
	}
	for name, c := range cases {
		path, opened := poisonedPath(t)
		res := runWithFrame(t, bin, c.frame, c.closed, "--db-path", path, "coder", "status")
		if res.code != exitCredFrame {
			t.Errorf("%s: exit %d (stderr %q), want %d", name, res.code, res.stderr, exitCredFrame)
		}
		if res.took.Seconds() > c.maxSec {
			t.Errorf("%s: took %v", name, res.took)
		}
		select {
		case <-opened:
			t.Errorf("%s: the poisoned path was opened before the frame was judged", name)
		case <-time.After(200 * time.Millisecond):
		}
		for _, secret := range []string{"mst-"} {
			if bytes.Contains([]byte(res.stderr), []byte(secret)) {
				t.Errorf("%s: stderr names a credential: %q", name, res.stderr)
			}
		}
	}

	// control: with a good frame the same command goes on and reaches the path
	path, opened := poisonedPath(t)
	done := make(chan runResult, 1)
	go func() { done <- runWithFrame(t, bin, good, true, "--db-path", path, "coder", "status") }()
	select {
	case <-opened:
	case res := <-done:
		t.Fatalf("control: the process ended (exit %d, stderr %q) without opening the path, so this test cannot tell a premature open", res.code, res.stderr)
	case <-time.After(30 * time.Second):
		t.Fatal("control: nothing opened the path")
	}
	select { // the control process ends by itself (its context kills it at the latest)
	case <-done:
	case <-time.After(45 * time.Second):
		t.Error("the control process did not end")
	}
}
