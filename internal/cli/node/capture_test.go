package node

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A command that prints more than the pipe buffer must not hang the capture
// helper (it used to block in fn before the reader started).
func TestCaptureStdoutDrainsOutputLargerThanThePipeBuffer(t *testing.T) {
	big := strings.Repeat("x", 1<<20)
	done := make(chan string, 1)
	go func() {
		done <- captureStdout(t, func() { _, _ = os.Stdout.WriteString(big) })
	}()
	select {
	case got := <-done:
		if len(got) != len(big) {
			t.Fatalf("captured %d bytes, want %d", len(got), len(big))
		}
	case <-time.After(30 * time.Second):
		t.Fatal("captureStdout deadlocked on output larger than the pipe buffer")
	}
}
