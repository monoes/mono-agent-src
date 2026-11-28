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
	if filepath.Dir(long.AnchorSockPath()) != filepath.Dir(long.SockPath(1)) {
		t.Fatal("the anchor and the emergency socket do not share the short directory")
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

// The start-time self-test binds where the real sockets will be, so a path that cannot be used shows there. It never makes the
// short directory itself (a show or dry run must not), and a squatted or loose one fails it.
func TestProbeUsesTheShortPlaceWithoutMakingItAndRefusesAnUnusableOne(t *testing.T) {
	long := longRunDir(t)
	short := filepath.Dir(long.AnchorSockPath())
	dir, done, err := probeSockDir(long.Dir)
	mustNoErr(t, err)
	if dir == long.Dir || len(dir)+len("/e18446744073709551615.sock") > sockMax {
		t.Fatalf("the probe would bind in %s", dir)
	}
	mustNoErr(t, peerSelfTest(dir, peerUID))
	done()
	if _, err := os.Lstat(short); err == nil {
		t.Fatal("the probe made the short directory")
	}
	if _, err := os.Lstat(dir); err == nil {
		t.Fatal("the probe left its scratch directory")
	}
	mustNoErr(t, os.Mkdir(short, 0o700))
	mustNoErr(t, os.Chmod(short, 0o755))
	if _, _, err := probeSockDir(long.Dir); err == nil {
		t.Fatal("the probe accepted an unusable short directory")
	}
	if d, _, err := probeSockDir("/tmp/x"); err != nil || d != "/tmp/x" {
		t.Fatalf("a run directory that fits changed: %s %v", d, err)
	}
}

// plantListener plants a listener of this test at path (the short directory made 0700 here), standing for anything that is
// listening there.
func plantListener(t *testing.T, path string, handle func(net.Conn)) {
	t.Helper()
	mustNoErr(t, os.MkdirAll(filepath.Dir(path), 0o700))
	ln, err := net.Listen("unix", path)
	mustNoErr(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); handle(c) }()
		}
	}()
}

func forgedOK(c net.Conn) { _, _ = c.Write([]byte("OK 4242 started:x 4242\nOK\n")) }

// The short directory's name can be guessed, so a dialer must not trust whatever listens there: a directory or socket that is
// not ours, a link, or a peer of another user are all refused before anything is sent or believed.
func TestDialersRefuseWhatIsNotOursInTheShortDirectory(t *testing.T) {
	long := longRunDir(t)
	path := long.SockPath(5)
	plantListener(t, path, forgedOK)
	if c, err := long.dialSock(path); err != nil {
		t.Fatalf("our own socket in our own directory was refused: %v", err)
	} else {
		c.Close()
	}
	// a directory that is another user's
	old := fileOwnedByMe
	fileOwnedByMe = func(os.FileInfo) bool { return false }
	if _, err := long.dialSock(path); err == nil || !strings.Contains(err.Error(), "belongs to") || !strings.Contains(err.Error(), "administrator") {
		t.Fatalf("a foreign directory was not refused with its owner and a remedy: %v", err)
	}
	if err := SendEmergency(long, 5); err == nil {
		t.Fatal("SendEmergency reached a socket in a foreign directory")
	}
	fileOwnedByMe = old
	// a peer that is another user, behind a socket that looks ours
	oldPeer := dialPeerUID
	dialPeerUID = func(net.Conn) (uint32, error) { return uint32(os.Getuid()) + 1, nil }
	t.Cleanup(func() { dialPeerUID = oldPeer })
	if _, err := long.dialSock(path); err == nil {
		t.Fatal("a socket served by another user was accepted")
	}
	dialPeerUID = oldPeer
	// a planted plain file and a link where the socket should be
	mustNoErr(t, os.Remove(path))
	mustNoErr(t, os.WriteFile(path, nil, 0o600))
	if _, err := long.dialSock(path); err == nil {
		t.Fatal("a plain file was taken for the socket")
	}
	mustNoErr(t, os.Remove(path))
	other := filepath.Join(shortTempDir(t), "o.sock")
	plantListener(t, other, forgedOK)
	mustNoErr(t, os.Symlink(other, path))
	if _, err := long.dialSock(path); err == nil {
		t.Fatal("a link to another socket was followed")
	}
	// the whole short directory is a link
	mustNoErr(t, os.Remove(path))
	short := filepath.Dir(path)
	mustNoErr(t, os.Remove(short))
	mustNoErr(t, os.Symlink(filepath.Dir(other), short))
	if _, err := long.dialSock(filepath.Join(short, "o.sock")); err == nil {
		t.Fatal("a linked short directory was followed")
	}
}

// A listener found a socket or a link planted inside an owned short directory (only we can write there, but a leftover
// can be anything): it replaces it, it does not follow it.
func TestListenerReplacesAPlantedLinkInsideOurShortDirectory(t *testing.T) {
	long := longRunDir(t)
	path := long.AnchorSockPath()
	mustNoErr(t, ensurePrivateDir(filepath.Dir(path)))
	target := filepath.Join(t.TempDir(), "victim")
	mustNoErr(t, os.WriteFile(target, []byte("keep"), 0o600))
	mustNoErr(t, os.Symlink(target, path))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv := &AnchorServer{Dir: long, UID: 501, OS: sessionFakePG{newFakePG()}, Peer: func(net.Conn) (uint32, error) { return 501, nil }}
	mustNoErr(t, srv.Start(ctx))
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatalf("the link's target was written through: %q", b)
	}
	if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("the path is not a socket: %v %v", fi, err)
	}
}
