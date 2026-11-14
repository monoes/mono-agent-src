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
	base := shortTempDir(t)
	old := shortSockBase
	shortSockBase = func() string { return base }
	t.Cleanup(func() { shortSockBase = old })
	d := filepath.Join(base, strings.Repeat("h", 60), strings.Repeat("p", 60))
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return RunDir{Dir: d}
}

// A run directory too long for a socket address must still get a working anchor service: the socket moves to a short
// private directory and the dialer finds it by the same rule.
func TestAnchorServiceWorksWhenTheRunDirectoryIsTooLongForASocketAddress(t *testing.T) {
	dir := longRunDir(t)
	if n := len(dir.Dir + "/anchor.sock"); n < 108 {
		t.Fatalf("the test directory is not long enough (%d bytes)", n)
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

func TestSocketPathStaysNaturalWhenItFitsAndShortWhenItDoesNot(t *testing.T) {
	r := RunDir{Dir: "/tmp/x"}
	if got := r.AnchorSockPath(); got != "/tmp/x/anchor.sock" {
		t.Fatalf("a path that fits moved to %s", got)
	}
	long := longRunDir(t)
	for _, p := range []string{long.AnchorSockPath(), long.SockPath(18446744073709551615)} {
		if len(p) > sockMax {
			t.Fatalf("%s is %d bytes, over %d", p, len(p), sockMax)
		}
	}
	if long.AnchorSockPath() != long.AnchorSockPath() || filepath.Dir(long.AnchorSockPath()) != filepath.Dir(long.SockPath(1)) {
		t.Fatal("the listener and the dialer would not agree")
	}
}

func TestShortSocketDirectoryIsRefusedWhenNotPlainPrivateAndOurs(t *testing.T) {
	long := longRunDir(t)
	short := filepath.Dir(long.AnchorSockPath())
	start := func() error {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		return (&AnchorServer{Dir: long, UID: 1, OS: sessionFakePG{newFakePG()}}).Start(ctx)
	}
	// open to others
	mustNoErr(t, os.Mkdir(short, 0o755))
	if err := os.Chmod(short, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := start(); err == nil {
		t.Fatal("a short directory open to others was used")
	}
	mustNoErr(t, os.Remove(short))
	// a symlink
	target := t.TempDir()
	mustNoErr(t, os.Symlink(target, short))
	if err := start(); err == nil {
		t.Fatal("a symlinked short directory was used")
	}
	mustNoErr(t, os.Remove(short))
	// another user's
	mustNoErr(t, os.Mkdir(short, 0o700))
	old := fileOwnedByMe
	fileOwnedByMe = func(os.FileInfo) bool { return false }
	t.Cleanup(func() { fileOwnedByMe = old })
	if err := start(); err == nil {
		t.Fatal("another user's short directory was used")
	}
}

// The start-time self-test binds where the real sockets will be, so a path that cannot be used shows there.
func TestProbeUsesTheShortDirectoryAndRefusesAnUnusableOne(t *testing.T) {
	long := longRunDir(t)
	dir, err := probeSockDir(long.Dir)
	mustNoErr(t, err)
	if dir == long.Dir || len(dir)+len("/e18446744073709551615.sock") > sockMax {
		t.Fatalf("the probe would bind in %s", dir)
	}
	mustNoErr(t, peerSelfTest(dir, peerUID))
	mustNoErr(t, os.Chmod(dir, 0o755))
	if _, err := probeSockDir(long.Dir); err == nil {
		t.Fatal("the probe accepted an unusable short directory")
	}
	if d, err := probeSockDir("/tmp/x"); err != nil || d != "/tmp/x" {
		t.Fatalf("a run directory that fits changed: %s %v", d, err)
	}
}
