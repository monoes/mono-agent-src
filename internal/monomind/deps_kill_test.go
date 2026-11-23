//go:build !windows

package monomind

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A timed-out `monomind deps install` must take its npm child down too: the
// child holds the output pipe, so left alive it keeps DepsInstall blocked.
func TestDepsInstallTimeoutKillsChildren(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "monomind")
	script := "#!/bin/sh\nsleep 30 &\nwait\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- DepsInstall(ctx, bin, func(string) {}) }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a killed install reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("DepsInstall still blocked %s after the deadline: the child outlived it", time.Since(start))
	}
}
