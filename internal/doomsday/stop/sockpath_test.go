package stop

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// longRunDir is a run directory whose natural socket paths do not fit a unix socket address on any platform (the real
// ones are <home>/.monoagent/doomsday/<profile id>, and a long home name pushes them over).
func longRunDir(t *testing.T) RunDir {
	t.Helper()
	d := filepath.Join(shortTempDir(t), strings.Repeat("h", 60), strings.Repeat("p", 60))
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return RunDir{Dir: d}
}

// A run directory too long for a socket address must still get a working anchor service: the socket moves to a short
// private directory and the dialer finds it by the same rule.
func TestAnchorServiceWorksWhenTheRunDirectoryIsTooLongForASocketAddress(t *testing.T) {
	dir := longRunDir(t)
	if len(dir.AnchorSockPath()) < 108 {
		t.Fatalf("the test directory is not long enough (%d bytes)", len(dir.AnchorSockPath()))
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv := &AnchorServer{Dir: dir, UID: 501, OS: sessionFakePG{newFakePG()}, Peer: func(net.Conn) (uint32, error) { return 501, nil }}
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("the anchor service cannot start: %v", err)
	}
	if got := anchorAsk(t, dir, "BOGUS\n"); got != "ERR request" {
		t.Fatalf("the service answered %q", got)
	}
}

func TestEmergencySocketWorksWhenTheRunDirectoryIsTooLongForASocketAddress(t *testing.T) {
	dir := longRunDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &EmergencyServer{Dir: dir, Gen: 3, UID: 501, Peer: func(net.Conn) (uint32, error) { return 501, nil }, OnStop: func(uint64) {}}
	if err := s.Start(ctx); err != nil {
		t.Fatalf("the emergency socket cannot be made: %v", err)
	}
	c, err := net.DialTimeout("unix", dir.SockPath(3), 2*time.Second)
	if err != nil {
		t.Fatalf("the emergency socket cannot be reached: %v", err)
	}
	c.Close()
}
