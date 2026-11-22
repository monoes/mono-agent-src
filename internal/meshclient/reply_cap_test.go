//go:build !windows

package meshclient

import (
	"bufio"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// A reply with no newline must not be read without bound (R08-09): the client stops at its cap.
func TestCallCapsAReplyWithoutANewline(t *testing.T) {
	dir, err := os.MkdirTemp("", "mc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(dir, "hub.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var written atomic.Int64
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = bufio.NewReader(c).ReadBytes('\n')
		chunk := make([]byte, 64*1024)
		for i := 0; i < 1024; i++ { // up to 64 MiB, no newline
			n, err := c.Write(chunk)
			written.Add(int64(n))
			if err != nil {
				return
			}
		}
	}()
	_, err = (Client{Dir: dir, Timeout: 5 * time.Second}).Call(map[string]any{"op": "who"})
	if !errors.Is(err, ErrOffline) {
		t.Fatalf("an unterminated reply: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if w := written.Load(); w > 8<<20 {
		t.Fatalf("the client kept reading: the hub wrote %d bytes", w)
	}
}
