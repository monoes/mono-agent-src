//go:build !windows

package org

import (
	"errors"
	"fmt"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
)

// Issue #235: when the reader of `org events --follow` goes away, the next
// write fails with EPIPE; that must cancel the tail and stop monomind's
// follower, not leave it running.
func TestOrgEventsStopsFollowerOnBrokenStdout(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	bin := filepath.Join(t.TempDir(), "follow-monomind.sh") // outside the project: a project-local one is refused
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--version" ] && [ "$2" = "--json" ]; then
  echo '{"v":1,"version":"2.10.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'
  exit 0
fi
echo $$ > %q
echo '{"v":1,"id":"e1"}'
exec sleep 60
`, pidFile)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monomind.EnvOverride, bin)
	testhome.SetHome(t, t.TempDir())

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	r.Close() // the app is gone
	defer w.Close()
	prev := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = prev }()

	cfg := &testConfig{DBPath: filepath.Join(t.TempDir(), "o.db"), ProfileID: "default"}
	cmd := newOrgCmd(cfg)
	cmd.SetArgs([]string{"--project", dir, "events", "growth", "--follow"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()

	select {
	case err := <-done:
		if !errors.Is(err, syscall.EPIPE) {
			t.Fatalf("org events returned %v, want an EPIPE write error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("org events kept running after its stdout broke")
	}

	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	child, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if child > 0 && syscall.Kill(child, 0) == nil {
		_ = syscall.Kill(child, syscall.SIGKILL)
		t.Fatalf("follower %d still running", child)
	}
}

func TestEventsWriterCancelsOnWriteError(t *testing.T) {
	t.Parallel()
	cancelled := false
	w := &eventsWriter{w: failingWriter{}, cancel: func() { cancelled = true }}
	w.writeLine([]byte(`{"id":"a"}`))
	if !cancelled || !errors.Is(w.err, syscall.EPIPE) {
		t.Fatalf("cancelled=%v err=%v, want cancel on EPIPE", cancelled, w.err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, syscall.EPIPE }
