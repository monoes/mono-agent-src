package clicore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
)

// ErrStillRunning: RunSurviving stopped waiting; the command goes on.
var ErrStillRunning = errors.New("the command is still running")

// RunSurviving runs cmd so that nothing that happens to its caller ends it: no
// deadline, no closed window, no parent that exits. It is for a command that
// performs something that must not be left half done, such as an approved send.
// cmd must have no context of its own. It starts detached (DetachStart), with
// its output in files, so a parent that is gone cannot break its writes.
//
// RunSurviving waits until the command ends or ctx does; then it returns
// ErrStillRunning and leaves the command running.
func RunSurviving(ctx context.Context, cmd *exec.Cmd, stdin []byte) (stdout, stderr []byte, err error) {
	var files [2]*os.File
	drop := func() {
		for _, f := range files {
			if f != nil {
				f.Close()
				os.Remove(f.Name())
			}
		}
	}
	for i := range files {
		if files[i], err = os.CreateTemp("", "monoagent-run-*"); err != nil {
			drop()
			return nil, nil, err
		}
	}
	// Input is a pipe, so what it carries is not written to disk.
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.Stdout, cmd.Stderr = files[0], files[1]
	if err := DetachStart(cmd); err != nil {
		drop()
		return nil, nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
		read := func(f *os.File) []byte {
			if _, serr := f.Seek(0, io.SeekStart); serr != nil {
				return nil
			}
			b, _ := io.ReadAll(f)
			return b
		}
		stdout, stderr = read(files[0]), read(files[1])
		drop()
		return stdout, stderr, err
	case <-ctx.Done():
		go func() { <-done; drop() }()
		return nil, nil, ErrStillRunning
	}
}
