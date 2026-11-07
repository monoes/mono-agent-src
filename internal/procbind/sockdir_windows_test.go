//go:build windows

package procbind

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func sdOf(t *testing.T, sddl string) *windows.SECURITY_DESCRIPTOR {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	return sd
}

// accessOK decides on the owner and the access list; the cases are descriptors written out in full.
func TestAccessOKTrustsOnlyTheUserSystemAndAdministrators(t *testing.T) {
	me, err := OwnSID()
	if err != nil {
		t.Fatal(err)
	}
	ok, _, err := trustedSet()
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		sddl string
		want bool
	}{
		"the user and SYSTEM only":          {"O:" + me + "D:P(A;OICI;FA;;;" + me + ")(A;OICI;FA;;;SY)", true},
		"Everyone allowed":                  {"O:" + me + "D:P(A;OICI;FA;;;" + me + ")(A;OICI;FA;;;WD)", false},
		"Authenticated Users allowed":       {"O:" + me + "D:P(A;OICI;FA;;;" + me + ")(A;OICI;0x1200a9;;;AU)", false},
		"Users group allowed":               {"O:" + me + "D:P(A;OICI;FA;;;" + me + ")(A;OICI;FA;;;BU)", false},
		"a foreign owner":                   {"O:WDD:P(A;OICI;FA;;;" + me + ")", false},
		"a deny entry beside the user":      {"O:" + me + "D:P(D;;FA;;;WD)(A;OICI;FA;;;" + me + ")", true},
		"an audit-only list with no allow":  {"O:" + me + "D:P(D;;FA;;;WD)", true},
		"an object entry that is not plain": {"O:" + me + "D:P(OA;;CC;bf967aba-0de6-11d0-a285-00aa003049e2;;WD)", false},
	} {
		if got := accessOK(sdOf(t, c.sddl), ok) == nil; got != c.want {
			t.Errorf("%s: trusted=%v, want %v", name, got, c.want)
		}
	}
}

func TestAccessOKRefusesAMissingAccessList(t *testing.T) {
	me, _ := OwnSID()
	ok, _, _ := trustedSet()
	if err := accessOK(sdOf(t, "O:"+me+"D:NO_ACCESS_CONTROL"), ok); err == nil {
		t.Fatal("an object with no access list (open to everyone) was trusted")
	}
}

func icacls(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("icacls", args...).CombinedOutput(); err != nil {
		t.Fatalf("icacls %v: %v %s", args, err, out)
	}
}

func profileDir(root string) string { return filepath.Join(root, runDirName, "p1") }

func TestListenSecureMakesAPrivateChainAndHoldsIt(t *testing.T) {
	root := shortRoot(t)
	l, err := ListenSecure(SocketOptions{Root: root, Profile: "p1", CreateRoot: true})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	// every directory and the socket pass the same check the dialer applies
	ok, _, _ := trustedSet()
	for _, h := range l.ch.handles {
		if err := checkAccess(h, ok); err != nil {
			t.Errorf("a directory of the chain: %v", err)
		}
	}
	if err := checkSocketFile(l.path); err != nil {
		t.Errorf("the socket: %v", err)
	}
	// the chain is held: a directory with an open handle that shares no delete cannot be renamed away and replaced
	if err := os.Rename(profileDir(root), profileDir(root)+"x"); err == nil {
		t.Fatal("the profile directory was renamed while the service holds it")
	}
	if err := os.Rename(filepath.Join(root, runDirName), filepath.Join(root, runDirName)+"x"); err == nil {
		t.Fatal("the runtime directory was renamed while the service holds it")
	}
	if err := os.Rename(root, root+"x"); err == nil {
		t.Fatal("the runtime root was renamed while the service holds it")
	}
}

func TestListenSecureRefusesADirectoryThatAdmitsAnotherPrincipal(t *testing.T) {
	root := shortRoot(t)
	l, err := ListenSecure(SocketOptions{Root: root, Profile: "p1", CreateRoot: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	icacls(t, profileDir(root), "/grant", "*S-1-1-0:(OI)(CI)R")
	if l, err := ListenSecure(SocketOptions{Root: root, Profile: "p1"}); err == nil || !errors.Is(err, errUnsafeDir) {
		if l != nil {
			l.Close()
		}
		t.Fatalf("a directory open to Everyone was used: %v", err)
	}
}

func TestListenSecureAndDialSecureRefuseAReparsePointInTheChain(t *testing.T) {
	root := shortRoot(t)
	l, err := ListenSecure(SocketOptions{Root: root, Profile: "p1", CreateRoot: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	elsewhere := filepath.Join(filepath.Dir(root), "elsewhere")
	if err := os.MkdirAll(filepath.Join(elsewhere, "p1"), 0o700); err != nil {
		t.Fatal(err)
	}
	// the profile directory becomes a junction to another folder
	if err := os.Remove(profileDir(root) + `\` + lockName); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(profileDir(root)); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", profileDir(root), filepath.Join(elsewhere, "p1")).CombinedOutput(); err != nil {
		t.Skipf("no junction here: %v %s", err, out)
	}
	if l, err := ListenSecure(SocketOptions{Root: root, Profile: "p1"}); err == nil {
		l.Close()
		t.Fatal("a junction in the chain was followed by the listener")
	}
	if c, err := DialSecure(SocketOptions{Root: root, Profile: "p1"}); err == nil {
		c.Close()
		t.Fatal("a junction in the chain was followed by the dialer")
	}
}

func TestSecondHostOfTheSameProfileIsRefused(t *testing.T) {
	root := shortRoot(t)
	l, err := ListenSecure(SocketOptions{Root: root, Profile: "p1", CreateRoot: true})
	if err != nil {
		t.Fatal(err)
	}
	if l2, err := ListenSecure(SocketOptions{Root: root, Profile: "p1"}); !errors.Is(err, ErrAnotherHost) {
		if l2 != nil {
			l2.Close()
		}
		t.Fatalf("a second host got the directory: %v", err)
	}
	_ = l.Close()
	l3, err := ListenSecure(SocketOptions{Root: root, Profile: "p1"})
	if err != nil {
		t.Fatalf("the lock was not released with the first host: %v", err)
	}
	l3.Close()
}

func TestListenSecureReplacesAStaleSocketAndRefusesAnythingElse(t *testing.T) {
	root := shortRoot(t)
	l, err := ListenSecure(SocketOptions{Root: root, Profile: "p1", CreateRoot: true})
	if err != nil {
		t.Fatal(err)
	}
	path := l.path
	_ = l.Close()
	// a socket left by a host that died
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	_ = stale.Close()
	l, err = ListenSecure(SocketOptions{Root: root, Profile: "p1"})
	if err != nil {
		t.Fatalf("a stale socket was not replaced: %v", err)
	}
	_ = l.Close()
	// a regular file at the socket name is not ours to remove
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if l, err := ListenSecure(SocketOptions{Root: root, Profile: "p1"}); err == nil {
		l.Close()
		t.Fatal("a file that is not a socket was replaced")
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "x" {
		t.Fatal("the file at the socket name was touched")
	}
}

func TestAPathLongerThanASocketAddressIsRefusedWithItsCause(t *testing.T) {
	root := shortRoot(t) + strings.Repeat("d", 120)
	_, err := ListenSecure(SocketOptions{Root: root, Profile: "p1", CreateRoot: true})
	if !errors.Is(err, errPathTooLong) || !strings.Contains(err.Error(), "107") {
		// the root itself cannot be made when its parent is missing: make it, then try again
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Skipf("cannot make a long root here: %v", err)
		}
		_, err = ListenSecure(SocketOptions{Root: root, Profile: "p1"})
	}
	if !errors.Is(err, errPathTooLong) {
		t.Fatalf("a long path: %v", err)
	}
}

func TestDialSecureCreatesNothingAndRefusesASocketOthersCanOpen(t *testing.T) {
	root := shortRoot(t)
	if c, err := DialSecure(SocketOptions{Root: root, Profile: "p1"}); err == nil {
		c.Close()
		t.Fatal("dialed a service that is not there")
	}
	if _, err := os.Stat(root); err == nil {
		t.Fatal("the dialer created the runtime root")
	}
	l, err := ListenSecure(SocketOptions{Root: root, Profile: "p1", CreateRoot: true})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.AcceptUnix()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	icacls(t, l.path, "/grant", "*S-1-1-0:R")
	if c, err := DialSecure(SocketOptions{Root: root, Profile: "p1"}); err == nil {
		c.Close()
		t.Fatal("dialed a socket whose access list admits Everyone")
	}
}
