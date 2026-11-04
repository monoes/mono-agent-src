package monomind

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// A detached stream reader must outlive the calling CLI, just like the org.
// Node is monomind's existing runtime; use its pinned interpreter. The real
// monomind process still starts normally, so its PID and cancellation group
// are unchanged. The helper never runs project code or resolves project tools.
const startDrainScript = `
const fs = require('fs');
let tail = Buffer.alloc(0);
fs.ftruncateSync(1, 0);
fs.writeSync(2, 'ready\n');
process.stdin.on('data', chunk => {
  tail = Buffer.concat([tail, chunk]).subarray(-4096);
  fs.ftruncateSync(1, 0);
  fs.writeSync(1, tail);
});
`

// drainReadyWait caps the wait for the helper's "ready" handshake. Node's
// startup takes well under a second normally but many seconds on a loaded
// machine, so this is a hang guard, not a latency budget.
var drainReadyWait = 30 * time.Second

// drainExitGrace is how long finishDrain waits for the helper to commit the
// final tail after monomind exits before killing it (a descendant holding
// stdio). Under load the helper may need longer than a second to flush.
var drainExitGrace = 10 * time.Second

func (c *startCapture) startDrain(ctx context.Context, bin, root string) error {
	node := ""
	if dir := nodeDirFor(bin); dir != "" {
		node = filepath.Join(dir, "node")
		if runtime.GOOS == "windows" {
			node += ".exe"
		}
	} else {
		// Non-Node binaries are allowed as MONOMIND_BIN overrides. PATH already
		// includes monoagent's managed Node when the system one is unsuitable.
		found, err := exec.LookPath("node")
		if err != nil {
			return fmt.Errorf("Node.js is required to capture detached org output: %w", err)
		}
		node, err = filepath.Abs(found)
		if err != nil {
			return err
		}
		node, err = pinPath(node, "node")
		if err != nil {
			return err
		}
	}
	if err := CheckOutside(node, root); err != nil {
		return err
	}
	input, output, err := os.Pipe()
	if err != nil {
		return err
	}
	defer input.Close()
	readyRead, readyWrite, err := os.Pipe()
	if err != nil {
		output.Close()
		return err
	}
	defer readyRead.Close()
	defer readyWrite.Close()
	helper := exec.Command(node, "-e", startDrainScript)
	helper.Env = PinEnvIn(ScrubDestinations(os.Environ()), bin, root)
	helper.Stdin, helper.Stdout, helper.Stderr = input, c.f, readyWrite
	helper, err = startDetached(helper)
	if err != nil {
		output.Close()
		return err
	}
	c.stream, c.drainer, c.drained = output, helper, make(chan struct{})
	go func() { c.drainErr = helper.Wait(); close(c.drained) }()
	// Verify the helper can use the capture before giving monomind a pipe.
	// A started Node process alone does not guarantee it parsed the script or
	// opened its descriptors. Do not leave a real run writing to a dead reader.
	ready := make(chan error, 1)
	go func() {
		b := make([]byte, 6)
		_, err := io.ReadFull(readyRead, b)
		if err == nil && string(b) != "ready\n" {
			err = fmt.Errorf("unexpected output capture handshake")
		}
		ready <- err
	}()
	timer := time.NewTimer(drainReadyWait)
	defer timer.Stop()
	select {
	case err = <-ready:
	case <-c.drained:
		err = fmt.Errorf("output capture exited before readiness")
	case <-timer.C:
		err = fmt.Errorf("output capture readiness timed out")
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err != nil {
		output.Close()
		killProcessGroup(helper, helper.Process.Pid)
		<-c.drained
		return err
	}
	return nil
}

func (c *startCapture) finishDrain() {
	if c.drainer == nil {
		return
	}
	// monomind's exit normally closes its stream and the helper exits after
	// committing the final tail. A descendant retaining stdio must not hold
	// the caller open indefinitely.
	timer := time.NewTimer(drainExitGrace)
	defer timer.Stop()
	select {
	case <-c.drained:
	case <-timer.C:
		killProcessGroup(c.drainer, c.drainer.Process.Pid)
		<-c.drained
	}
}
