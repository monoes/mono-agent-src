//go:build !windows

package clicore

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

// A command outlives its caller's deadline, in a session of its own, and can
// still write its output.
func TestRunSurvivingOutlivesTheWait(t *testing.T) {
	dir := t.TempDir()
	done, pid := filepath.Join(dir, "done"), filepath.Join(dir, "pid")
	cmd := exec.Command("/bin/sh", "-c", `echo $$ > "$2"; read line; sleep 1; echo "late $line"; echo err >&2; echo "$line" > "$1"`, "sh", done, pid)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, _, err := RunSurviving(ctx, cmd, []byte("input\n")); !errors.Is(err, ErrStillRunning) {
		t.Fatalf("%v", err)
	}
	raw, err := os.ReadFile(pid)
	if err != nil {
		t.Fatal(err)
	}
	child, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	// A signal to the caller's process group does not reach it.
	if group, err := syscall.Getpgid(child); err != nil || group == syscall.Getpgrp() {
		t.Fatalf("the command shares the caller's group: %d %v", group, err)
	}
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if b, err := os.ReadFile(done); err == nil && string(b) == "input\n" {
			return
		}
		if time.Now().After(end) {
			t.Fatal("the command did not finish after the wait ended")
		}
	}
}

func TestRunSurvivingReturnsTheOutputOfACommandThatEnds(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", `read line; echo "out $line"; echo bad >&2; exit 3`)
	out, errOut, err := RunSurviving(context.Background(), cmd, []byte("x\n"))
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 || string(out) != "out x\n" || string(errOut) != "bad\n" {
		t.Fatalf("%q %q %v", out, errOut, err)
	}
}
