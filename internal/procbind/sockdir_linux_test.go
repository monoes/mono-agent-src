//go:build linux

package procbind

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// shortRoot is a runtime root with a short path (sockets have a ~108 byte limit), owned by the
// test user and private.
func shortRoot(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "sd")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeTree(d) })
	return d
}

// removeTree removes what these tests created: a few known files and directories, one level at a time.
func removeTree(root string) {
	for _, p := range []string{"monoagent/p1/bind.sock", "monoagent/p1/bind.lock", "monoagent/p1", "monoagent"} {
		_ = os.Remove(filepath.Join(root, p))
	}
	entries, _ := os.ReadDir(filepath.Join(root, "monoagent"))
	for _, e := range entries {
		sub := filepath.Join(root, "monoagent", e.Name())
		inner, _ := os.ReadDir(sub)
		for _, f := range inner {
			_ = os.Remove(filepath.Join(sub, f.Name()))
		}
		_ = os.Remove(sub)
	}
	_ = os.Remove(filepath.Join(root, "monoagent"))
	_ = os.Remove(root)
}

func opts(root string) SocketOptions { return SocketOptions{Root: root, Profile: "p1"} }

func TestSecureListenerCreatesPrivateDirsAndServes(t *testing.T) {
	root := shortRoot(t)
	l, err := ListenSecure(opts(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"monoagent", "monoagent/p1"} {
		fi, err := os.Lstat(filepath.Join(root, p))
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v %v", p, fi, err)
		}
	}
	fi, err := os.Lstat(filepath.Join(root, "monoagent/p1/bind.sock"))
	if err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("socket: %v %v", fi, err)
	}
	// end to end: the secure dial reaches a service on the secure listener
	self := open(t, os.Getpid())
	s := &Server{Profile: tProfile, Generation: tGen, Key: tKey, Registry: register(t, self), Procs: lp(t), UID: os.Getuid(), Timeout: 3 * time.Second}
	skipWithoutPeerPidfd(t)
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Serve(l.UnixListener) }()
	conn, err := DialSecure(opts(root))
	if err != nil {
		t.Fatal(err)
	}
	v, err := goodClient().Verify(conn, tToken, tScope)
	_ = conn.Close()
	if err != nil || v != VerdictAllow {
		t.Errorf("through the secure dial: %v %v", v, err)
	}
	if err := l.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	<-done
	if _, err := os.Lstat(filepath.Join(root, "monoagent/p1/bind.sock")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the socket was left behind: %v", err)
	}
	// a clean close releases the lock: a new host can start
	l2, err := ListenSecure(opts(root))
	if err != nil {
		t.Fatalf("after a clean close: %v", err)
	}
	_ = l2.Close()
}

func TestSecureListenerRefusesUnsafeDirectories(t *testing.T) {
	cases := map[string]func(t *testing.T, root string) SocketOptions{
		"root writable by the group": func(t *testing.T, root string) SocketOptions {
			_ = os.Chmod(root, 0o770)
			return opts(root)
		},
		"root owned by someone else": func(t *testing.T, root string) SocketOptions {
			o := opts(root)
			o.UID = os.Getuid() + 1
			return o
		},
		"root is a symlink": func(t *testing.T, root string) SocketOptions {
			link := root + "-link"
			if err := os.Symlink(root, link); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Remove(link) })
			return opts(link)
		},
		"intermediate directory is a symlink": func(t *testing.T, root string) SocketOptions {
			other := shortRoot(t)
			if err := os.Symlink(other, filepath.Join(root, "monoagent")); err != nil {
				t.Fatal(err)
			}
			return opts(root)
		},
		"intermediate symlink that stays inside the root": func(t *testing.T, root string) SocketOptions {
			real := filepath.Join(root, "elsewhere")
			if err := os.Mkdir(real, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("elsewhere", filepath.Join(root, "monoagent")); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = os.Remove(filepath.Join(real, "p1", "bind.sock"))
				_ = os.Remove(filepath.Join(real, "p1", "bind.lock"))
				_ = os.Remove(filepath.Join(real, "p1"))
				_ = os.Remove(real)
			})
			return opts(root)
		},
		"leaf is a symlink": func(t *testing.T, root string) SocketOptions {
			other := shortRoot(t)
			_ = os.Mkdir(filepath.Join(root, "monoagent"), 0o700)
			if err := os.Symlink(other, filepath.Join(root, "monoagent", "p1")); err != nil {
				t.Fatal(err)
			}
			return opts(root)
		},
		"leaf directory has the wrong mode": func(t *testing.T, root string) SocketOptions {
			_ = os.Mkdir(filepath.Join(root, "monoagent"), 0o700)
			_ = os.Mkdir(filepath.Join(root, "monoagent", "p1"), 0o755)
			_ = os.Chmod(filepath.Join(root, "monoagent", "p1"), 0o755)
			return opts(root)
		},
		"a regular file where the socket goes": func(t *testing.T, root string) SocketOptions {
			_ = os.Mkdir(filepath.Join(root, "monoagent"), 0o700)
			_ = os.Mkdir(filepath.Join(root, "monoagent", "p1"), 0o700)
			if err := os.WriteFile(filepath.Join(root, "monoagent", "p1", "bind.sock"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			return opts(root)
		},
		"a symlink where the socket goes": func(t *testing.T, root string) SocketOptions {
			_ = os.Mkdir(filepath.Join(root, "monoagent"), 0o700)
			_ = os.Mkdir(filepath.Join(root, "monoagent", "p1"), 0o700)
			if err := os.Symlink("/etc/passwd", filepath.Join(root, "monoagent", "p1", "bind.sock")); err != nil {
				t.Fatal(err)
			}
			return opts(root)
		},
		"relative root": func(t *testing.T, root string) SocketOptions { return SocketOptions{Root: "rel/dir", Profile: "p1"} },
		"empty profile": func(t *testing.T, root string) SocketOptions { return SocketOptions{Root: root} },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			root := shortRoot(t)
			o := setup(t, root)
			l, err := ListenSecure(o)
			if err == nil {
				_ = l.Close()
				t.Fatal("the listener started on an unsafe directory")
			}
			// nothing the attacker pointed at was written to
			if _, err := os.Lstat("/etc/passwd.sock"); err == nil {
				t.Fatal("unexpected file")
			}
		})
	}
}

func TestCheckDirRefusesAnotherOwnerOrMode(t *testing.T) {
	root := shortRoot(t)
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := checkDir(fd, os.Getuid(), "root"); err != nil {
		t.Fatalf("a private directory of ours: %v", err)
	}
	if err := checkDir(fd, os.Getuid()+1, "root"); err == nil {
		t.Error("a directory of another owner was accepted")
	}
	_ = os.Chmod(root, 0o750)
	if err := checkDir(fd, os.Getuid(), "root"); err == nil {
		t.Error("a directory with mode 0750 was accepted")
	}
}

func TestSecureListenerReplacesAStaleSocketAndRefusesASecondHost(t *testing.T) {
	root := shortRoot(t)
	first, err := ListenSecure(opts(root))
	if err != nil {
		t.Fatal(err)
	}
	// a second host while the first holds the lock
	if l, err := ListenSecure(opts(root)); err == nil {
		_ = l.Close()
		t.Fatal("two hosts on one socket")
	} else if !errors.Is(err, ErrAnotherHost) {
		t.Errorf("second host: %v", err)
	}
	// the first "crashes": its lock goes away without removing the socket file
	// the first host "crashes": it stops listening and its lock goes away, but the socket file stays
	_ = first.UnixListener.Close()
	_ = unix.Close(first.lockfd)
	first.lockfd = -1
	stale := filepath.Join(root, "monoagent/p1/bind.sock")
	if fi, err := os.Lstat(stale); err != nil || fi.Mode()&os.ModeSocket == 0 {
		t.Fatalf("test needs a stale socket file: %v %v", fi, err)
	}
	l, err := ListenSecure(opts(root))
	if err != nil {
		t.Fatalf("a stale socket was not replaced: %v", err)
	}
	_ = l.Close()
	_ = first.Close()
}

func TestSocketLeafIsOneSafeComponent(t *testing.T) {
	for _, p := range []string{"p1", "My-Profile_2.x", "a"} {
		if got := socketLeaf(p); got != p {
			t.Errorf("%q became %q", p, got)
		}
	}
	for _, p := range []string{"../x", "a/b", "..", ".", "", "/abs", "a\x00b", strings.Repeat("x", 200), "p 1", "~tilde", "-flag"} {
		got := socketLeaf(p)
		if got == p || strings.ContainsAny(got, "/\x00") || got == "." || got == ".." || got == "" || len(got) > 80 || !strings.HasPrefix(got, "~") {
			t.Errorf("%q became %q: not a hashed single component", p, got)
		}
		if got != socketLeaf(p) {
			t.Errorf("%q: not deterministic", p)
		}
	}
	if socketLeaf("a/b") == socketLeaf("a/c") {
		t.Error("two different profiles share a leaf")
	}
}

func TestResolveRoot(t *testing.T) {
	good := shortRoot(t)
	home := "/home/u"
	if got := ResolveRoot(good, home, os.Getuid()); got != good {
		t.Errorf("a private XDG_RUNTIME_DIR: %q", got)
	}
	want := filepath.Join(home, ".monoagent", "run")
	_ = os.Chmod(good, 0o755)
	for name, xdg := range map[string]string{"unset": "", "relative": "run/user", "not private": good, "missing": "/nonexistent-xdg"} {
		if got := ResolveRoot(xdg, home, os.Getuid()); got != want {
			t.Errorf("%s: %q, want the fallback %q", name, got, want)
		}
	}
	if got := ResolveRoot(good, home, os.Getuid()+1); got != want {
		t.Errorf("a runtime dir owned by someone else: %q", got)
	}
}

func TestDialSecureRefusesUnsafeDirectoriesAndAbstractNames(t *testing.T) {
	root := shortRoot(t)
	if _, err := DialSecure(opts(root)); err == nil {
		t.Error("dialled with nothing there")
	}
	l, err := ListenSecure(opts(root))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_ = os.Chmod(filepath.Join(root, "monoagent/p1"), 0o755)
	if c, err := DialSecure(opts(root)); err == nil {
		_ = c.Close()
		t.Error("dialled through a directory with the wrong mode")
	}
	_ = os.Chmod(filepath.Join(root, "monoagent/p1"), 0o700)
	o := opts(root)
	o.UID = os.Getuid() + 1
	if c, err := DialSecure(o); err == nil {
		_ = c.Close()
		t.Error("dialled through a directory owned by someone else")
	}
	name := "@procbind-test-" + strconv.Itoa(os.Getpid())
	al, err := net.ListenUnix("unix", &net.UnixAddr{Name: name, Net: "unix"})
	if err != nil {
		t.Skipf("abstract sockets are not available here: %v", err)
	}
	defer al.Close()
	reached := make(chan struct{}, 1)
	go func() {
		if c, err := al.AcceptUnix(); err == nil {
			_ = c.Close()
			reached <- struct{}{}
		}
	}()
	for _, bad := range []string{name, "\x00" + name[1:]} {
		if c, err := DialPath(bad); err == nil {
			_ = c.Close()
			t.Errorf("dialled the abstract name %q", bad)
		}
	}
	select {
	case <-reached:
		t.Error("a live abstract listener was reached")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestSecureListenerLeaksNoDescriptors(t *testing.T) {
	root := shortRoot(t)
	before := fdCount(t)
	for i := 0; i < 5; i++ {
		l, err := ListenSecure(opts(root))
		if err != nil {
			t.Fatal(err)
		}
		_ = l.Close()
	}
	// a second host refused while the first runs must not leak either
	first, err := ListenSecure(opts(root))
	if err != nil {
		t.Fatal(err)
	}
	base := fdCount(t)
	for i := 0; i < 10; i++ {
		if l, err := ListenSecure(opts(root)); err == nil {
			_ = l.Close()
			t.Fatal("two hosts")
		}
	}
	if after := fdCount(t); after > base {
		t.Errorf("refusing a second host leaked descriptors: %d before, %d after", base, after)
	}
	_ = first.Close()
	// a refused start must not leak either
	_ = os.Chmod(root, 0o777)
	for i := 0; i < 5; i++ {
		if l, err := ListenSecure(opts(root)); err == nil {
			_ = l.Close()
			t.Fatal("started on a world-writable root")
		}
	}
	if after := settledFDs(t); after > before {
		t.Errorf("descriptors: %d before, %d after", before, after)
	}
}
