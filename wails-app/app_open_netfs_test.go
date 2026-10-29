package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run on every OS: the matching and the walk are pure, and the resolver is
// driven through its seams.

type fakeInfo struct{ m os.FileMode }

func (f fakeInfo) Name() string       { return "x" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() os.FileMode  { return f.m }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.m == 0 }
func (f fakeInfo) Sys() any           { return nil }

func TestLinuxNetworkFSMagicList(t *testing.T) {
	for _, m := range []uint32{0x6969, 0x517B, 0xFF534D42, 0xFE534D42, 0x5346414F, 0x00C36400, 0x01021997} {
		if !isLinuxNetworkFSMagic(m) {
			t.Errorf("%#x is a network filesystem", m)
		}
	}
	// ext4, tmpfs, btrfs, xfs, overlayfs and FUSE stay allowed
	for _, m := range []uint32{0xEF53, 0x01021994, 0x9123683E, 0x58465342, 0x794C7630, 0x65735546} {
		if isLinuxNetworkFSMagic(m) {
			t.Errorf("%#x is not a network filesystem", m)
		}
	}
}

func TestDarwinNetworkFSNamesAndRemoteDrive(t *testing.T) {
	for _, n := range []string{"nfs", "smbfs", "afpfs", "webdav", "NFS"} {
		if !isDarwinNetworkFSName(n) {
			t.Errorf("%q is a network filesystem", n)
		}
	}
	for _, n := range []string{"apfs", "hfs", "msdos", "", "exfat"} {
		if isDarwinNetworkFSName(n) {
			t.Errorf("%q is local", n)
		}
	}
	if !isRemoteDriveType(4) || isRemoteDriveType(3) || isRemoteDriveType(2) || isRemoteDriveType(0) {
		t.Fatal("only DRIVE_REMOTE (4) is a network drive")
	}
}

func TestResolveChatTokenRefusesAResolvedPathOnANetworkFilesystem(t *testing.T) {
	root, _ := chatTree(t)
	old := chatOnNetworkFS
	defer func() { chatOnNetworkFS = old }()
	var asked []string
	chatOnNetworkFS = func(p string) bool { asked = append(asked, p); return true }
	if _, _, err := resolveChatToken(root, "docs/plan.md"); err == nil {
		t.Fatal("a path on a network filesystem must be refused")
	}
	if g := resolveChatPathInfo(root, "docs/plan.md"); g.Kind != "missing" {
		t.Fatalf("%+v", g)
	}
	if _, err := readChatMarkdown(root, "docs/plan.md"); err == nil {
		t.Fatal("no preview from a network filesystem")
	}
	if err := revealChatPath(chatOpener{goos: "linux"}, root, "docs/plan.md"); err == nil {
		t.Fatal("no reveal on a network filesystem")
	}
	if len(asked) == 0 || !strings.HasSuffix(asked[0], filepath.Join("docs", "plan.md")) {
		t.Fatalf("the probe gets the resolved path: %v", asked)
	}
	chatOnNetworkFS = func(string) bool { return false }
	if g := resolveChatPathInfo(root, "docs/plan.md"); g.Kind != "file" {
		t.Fatalf("%+v", g)
	}
}

// The link walk reads link text without following it.
func TestWalkChatLinksRefusesLinksToSharesAndOtherDrives(t *testing.T) {
	links := map[string]string{
		"/w/share": `\\h\s\dir`, "/w/slashshare": `//h/s`, "/w/devpath": `\\?\C:\x`, "/w/other": `D:\data`,
		"/w/samedrive": `\rooted`, "/w/rel": `sub\dir`, "/w/abs": `/elsewhere/dir`,
	}
	lstat := func(p string) (os.FileInfo, error) {
		if _, ok := links[p]; ok {
			return fakeInfo{os.ModeSymlink}, nil
		}
		if p == "/w" || strings.HasPrefix(p, "/w/") {
			return fakeInfo{0}, nil
		}
		return nil, os.ErrNotExist
	}
	readlink := func(p string) (string, error) { return links[p], nil }
	for name, wantRefuse := range map[string]bool{
		"share": true, "slashshare": true, "devpath": true, "other": true,
		"samedrive": false, "rel": false, "abs": false,
	} {
		err := walkChatLinks("/w/"+name+"/deeper/x.md", lstat, readlink)
		if (err != nil) != wantRefuse {
			t.Errorf("%s: refuse=%v err=%v", name, wantRefuse, err)
		}
	}
	// a missing component ends the walk quietly (the lookup reports it)
	if err := walkChatLinks("/nowhere/share/x.md", lstat, readlink); err != nil {
		t.Fatal(err)
	}
	// an unreadable link text fails closed
	if err := walkChatLinks("/w/share/x", lstat, func(string) (string, error) { return "", os.ErrPermission }); err == nil {
		t.Fatal("unreadable link must be refused")
	}
}

func TestLinkLeavesVolume(t *testing.T) {
	for target, want := range map[string]bool{
		`\\h\s`: true, `//h/s`: true, `/\h`: true, `D:\x`: true, `d:x`: true, `C:\x`: false, `c:\x`: false,
		`rel\x`: false, `\x`: false, `/x`: false, ``: false,
	} {
		if got := linkLeavesVolume(target, "C:"); got != want {
			t.Errorf("%q: %v", target, got)
		}
	}
}

// With the guard wired in, a link whose text names a share is refused before
// any symlink lookup, so the lookup never runs.
func TestResolveChatTokenWalksLinksBeforeTheLookup(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(`\\127.0.0.1\nope\dir`, filepath.Join(root, "share")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if err := os.Symlink("sub", filepath.Join(root, "ok")); err != nil {
		t.Fatal(err)
	}
	_ = os.Mkdir(filepath.Join(root, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "sub", "n.md"), []byte("x"), 0o644)
	oldGuard, oldEval := chatLinkGuard, chatEvalSymlinks
	defer func() { chatLinkGuard, chatEvalSymlinks = oldGuard, oldEval }()
	chatLinkGuard = func(p string) error { return walkChatLinks(p, os.Lstat, os.Readlink) }
	evals := 0
	chatEvalSymlinks = func(p string) (string, error) { evals++; return oldEval(p) }
	if _, _, err := resolveChatToken(root, "share/n.md"); err == nil {
		t.Fatal("a link to a share must be refused")
	}
	if evals != 0 {
		t.Fatalf("the symlink lookup ran %d times before the refusal", evals)
	}
	if _, _, err := resolveChatToken(root, "ok/n.md"); err != nil || evals != 1 {
		t.Fatalf("a local link resolves: %v (%d lookups)", err, evals)
	}
}
