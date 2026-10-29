package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

type call struct {
	name string
	args []string
}

type recorder struct {
	runs, starts []call
	runErr       error
	hasDbus      bool
}

func (r *recorder) opener(goos string) chatOpener {
	return chatOpener{
		goos: goos,
		lookPath: func(n string) (string, error) {
			if n == "dbus-send" && r.hasDbus {
				return "/usr/bin/dbus-send", nil
			}
			return "", exec.ErrNotFound
		},
		run:   func(n string, a ...string) error { r.runs = append(r.runs, call{n, a}); return r.runErr },
		start: func(n string, a ...string) error { r.starts = append(r.starts, call{n, a}); return nil },
	}
}

func chatTree(t *testing.T) (root, outside string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "work")
	outside = filepath.Join(base, "secret")
	for _, d := range []string{root, filepath.Join(root, "docs"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "docs", "plan.md"), "# Plan\n")
	write(filepath.Join(root, "main.go"), "package main\n")
	write(filepath.Join(outside, "pw.md"), "# secret\n")
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if err := os.Symlink(filepath.Join(outside, "pw.md"), filepath.Join(root, "link.md")); err != nil {
		t.Fatal(err)
	}
	return root, outside
}

func TestChatPathResolve(t *testing.T) {
	root, _ := chatTree(t)
	got := resolveChatPathInfo(root, "docs/plan.md:12:3")
	if got.Kind != "file" || !got.Previewable || got.Line != 12 {
		t.Fatalf("plan.md: %+v", got)
	}
	if g := resolveChatPathInfo(root, "main.go"); g.Kind != "file" || g.Previewable {
		t.Fatalf("main.go must be a non-previewable file: %+v", g)
	}
	if g := resolveChatPathInfo(root, "docs"); g.Kind != "dir" {
		t.Fatalf("docs: %+v", g)
	}
	if g := resolveChatPathInfo(root, "nope.md"); g.Kind != "missing" {
		t.Fatalf("nope: %+v", g)
	}
	// a symlink out of the folder still reveals, but is not previewable
	if g := resolveChatPathInfo(root, "link.md"); g.Kind != "file" || g.Previewable {
		t.Fatalf("escaping link must not be previewable: %+v", g)
	}
	if g := resolveChatPathInfo("", "docs/plan.md"); g.Kind != "missing" {
		t.Fatalf("relative path without a folder: %+v", g)
	}
	if g := resolveChatPathInfo(root, filepath.Join(root, "docs", "plan.md")); g.Kind != "file" || !g.Previewable {
		t.Fatalf("absolute: %+v", g)
	}
}

func TestChatPathRefusesOptionLookingTokens(t *testing.T) {
	root, _ := chatTree(t)
	// a real file whose name looks like an option still must not be passed on
	_ = os.WriteFile(filepath.Join(root, "-x.md"), []byte("x"), 0o644)
	for _, tok := range []string{"-rf", "--help", "-x.md", "-docs/plan.md", "a\x00b", "a\nb", ""} {
		if g := resolveChatPathInfo(root, tok); g.Kind != "missing" {
			t.Errorf("%q resolved: %+v", tok, g)
		}
		if _, err := readChatMarkdown(root, tok); err == nil {
			t.Errorf("%q read", tok)
		}
		rec := &recorder{}
		if err := revealChatPath(rec.opener("linux"), root, tok); err == nil || len(rec.starts)+len(rec.runs) != 0 {
			t.Errorf("%q revealed (err=%v)", tok, err)
		}
	}
}

func TestReadChatMarkdown(t *testing.T) {
	root, _ := chatTree(t)
	if s, err := readChatMarkdown(root, "docs/plan.md"); err != nil || s != "# Plan\n" {
		t.Fatalf("got %q %v", s, err)
	}
	for _, tok := range []string{"main.go", "nope.md", "docs", "../secret/pw.md", "escape/pw.md", "link.md"} {
		if _, err := readChatMarkdown(root, tok); err == nil {
			t.Errorf("%s was readable", tok)
		}
	}
	// absolute path outside the folder
	if _, err := readChatMarkdown(root, filepath.Join(filepath.Dir(root), "secret", "pw.md")); err == nil {
		t.Error("absolute outside path was readable")
	}
	if _, err := readChatMarkdown("", "docs/plan.md"); err == nil {
		t.Error("read without a folder")
	}
	// a sibling whose name shares the root's prefix is outside it
	sib := root + "-evil"
	_ = os.MkdirAll(sib, 0o755)
	_ = os.WriteFile(filepath.Join(sib, "x.md"), []byte("x"), 0o644)
	if _, err := readChatMarkdown(root, filepath.Join(sib, "x.md")); err == nil {
		t.Error("prefix sibling was readable")
	}
}

func TestReadChatMarkdownSizeCap(t *testing.T) {
	root, _ := chatTree(t)
	ok := filepath.Join(root, "ok.md")
	big := filepath.Join(root, "big.md")
	_ = os.WriteFile(ok, []byte(strings.Repeat("a", maxChatPreviewBytes)), 0o644)
	_ = os.WriteFile(big, []byte(strings.Repeat("a", maxChatPreviewBytes+1)), 0o644)
	if _, err := readChatMarkdown(root, "ok.md"); err != nil {
		t.Fatalf("at the cap: %v", err)
	}
	if _, err := readChatMarkdown(root, "big.md"); err == nil {
		t.Fatal("over the cap was read")
	}
	if g := resolveChatPathInfo(root, "big.md"); g.Previewable {
		t.Fatal("over-cap file offered a preview")
	}
}

func TestReadChatMarkdownRefusesBinaryAndFifo(t *testing.T) {
	root, _ := chatTree(t)
	_ = os.WriteFile(filepath.Join(root, "bin.md"), []byte{0xff, 0xfe, 0, 1}, 0o644)
	if _, err := readChatMarkdown(root, "bin.md"); err == nil {
		t.Error("binary read")
	}
	fifo := filepath.Join(root, "pipe.md")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skip("no fifo")
	}
	done := make(chan error, 1)
	go func() { _, err := readChatMarkdown(root, "pipe.md"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("fifo read")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fifo read blocked")
	}
	if g := resolveChatPathInfo(root, "pipe.md"); g.Kind != "missing" {
		t.Errorf("fifo: %+v", g)
	}
	rec := &recorder{}
	if err := revealChatPath(rec.opener("linux"), root, "pipe.md"); err == nil {
		t.Error("fifo revealed")
	}
	if g := resolveChatPathInfo(root, "/dev/null"); g.Kind != "missing" {
		t.Errorf("/dev/null: %+v", g)
	}
}

func TestRevealArgv(t *testing.T) {
	root, _ := chatTree(t)
	file := filepath.Join(root, "docs", "plan.md")
	real, _ := filepath.EvalSymlinks(file)

	rec := &recorder{hasDbus: true}
	if err := revealChatPath(rec.opener("linux"), root, "docs/plan.md:3"); err != nil {
		t.Fatal(err)
	}
	if len(rec.runs) != 1 || rec.runs[0].name != "dbus-send" || len(rec.starts) != 0 {
		t.Fatalf("linux dbus: %+v %+v", rec.runs, rec.starts)
	}
	if !strings.Contains(strings.Join(rec.runs[0].args, " "), "array:string:file://"+real) {
		t.Fatalf("args: %v", rec.runs[0].args)
	}

	rec = &recorder{hasDbus: true, runErr: os.ErrNotExist}
	_ = revealChatPath(rec.opener("linux"), root, "docs/plan.md")
	if len(rec.starts) != 1 || rec.starts[0].name != "xdg-open" || rec.starts[0].args[0] != filepath.Dir(real) {
		t.Fatalf("fallback: %+v", rec.starts)
	}
	rec = &recorder{}
	_ = revealChatPath(rec.opener("linux"), root, "docs")
	if len(rec.starts) != 1 || rec.starts[0].args[0] != filepath.Join(root, "docs") {
		t.Fatalf("dir: %+v", rec.starts)
	}

	rec = &recorder{}
	_ = revealChatPath(rec.opener("darwin"), root, "docs/plan.md")
	if g := rec.starts[0]; g.name != "open" || len(g.args) != 2 || g.args[0] != "-R" || g.args[1] != real {
		t.Fatalf("darwin: %+v", g)
	}
	rec = &recorder{}
	_ = revealChatPath(rec.opener("windows"), root, "docs/plan.md")
	if g := rec.starts[0]; g.name != "explorer" || len(g.args) != 1 || g.args[0] != "/select,"+real {
		t.Fatalf("windows: %+v", g)
	}
}

func TestRevealDbusEscapesCommaInURI(t *testing.T) {
	root, _ := chatTree(t)
	_ = os.WriteFile(filepath.Join(root, "a,b c.md"), []byte("x"), 0o644)
	rec := &recorder{hasDbus: true}
	if err := revealChatPath(rec.opener("linux"), root, "a,b c.md"); err != nil {
		t.Fatal(err)
	}
	last := rec.runs[0].args[len(rec.runs[0].args)-2]
	if strings.Count(last, ",") != 0 || !strings.Contains(last, "%2C") {
		t.Fatalf("uri: %s", last)
	}
}

func TestRevealMissingLaunchesNothing(t *testing.T) {
	root, _ := chatTree(t)
	rec := &recorder{hasDbus: true}
	if err := revealChatPath(rec.opener("linux"), root, "ghost.md"); err == nil || len(rec.runs)+len(rec.starts) != 0 {
		t.Fatalf("err=%v %+v %+v", err, rec.runs, rec.starts)
	}
}

func TestChatPathHomeExpansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	_ = os.WriteFile(filepath.Join(home, "n.md"), []byte("x"), 0o644)
	realHome, _ := filepath.EvalSymlinks(home)
	if g := resolveChatPathInfo("", "~/n.md"); g.Kind != "file" || g.Path != filepath.Join(realHome, "n.md") {
		t.Fatalf("%+v", g)
	}
	if g := resolveChatPathInfo("", "~root/x"); g.Kind != "missing" {
		t.Fatalf("~user: %+v", g)
	}
}

func TestChatTokenRefusesNetworkAndDevicePaths(t *testing.T) {
	for _, tok := range []string{
		`/\h\s\x.md`, `\/h/s/x.md`, `//h/s/x.md`, `\\h\s\x.md`, `\\?\C:\x.md`, `\\.\pipe\x`,
		`\??\C:\x.md`, `C:foo.md`, `c:foo.md:3`, `//h/s/x.md:7`, `docs\\?\x.md`,
	} {
		if _, _, err := parseChatToken(tok); err == nil {
			t.Errorf("%q was accepted by parseChatToken", tok)
		}
		if _, _, err := resolveChatToken(t.TempDir(), tok); err == nil {
			t.Errorf("%q was resolved", tok)
		}
		if g := resolveChatPathInfo(t.TempDir(), tok); g.Kind != "missing" {
			t.Errorf("%q: %+v", tok, g)
		}
	}
	// the ordinary Windows drive form still gets through the syntax check
	for _, tok := range []string{`C:\ok\x.md`, `C:/ok/x.md:3`, `docs/plan.md`, `/abs/x.md`} {
		if _, _, err := parseChatToken(tok); err != nil {
			t.Errorf("%q refused: %v", tok, err)
		}
	}
}

// A .app bundle is a directory: handing it to "open" runs it, so a folder is
// always revealed, never opened, on macOS and Windows.
func TestRevealNeverOpensABundleOrBareDirectory(t *testing.T) {
	root, _ := chatTree(t)
	if err := os.MkdirAll(filepath.Join(root, "X.app", "Contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	realRoot, _ := filepath.EvalSymlinks(root)
	dir := filepath.Join(realRoot, "docs")
	app := filepath.Join(realRoot, "X.app")
	file := filepath.Join(realRoot, "docs", "plan.md")
	for _, c := range []struct {
		goos, token string
		want        call
	}{
		{"darwin", "docs", call{"open", []string{"-R", dir}}},
		{"darwin", "X.app", call{"open", []string{"-R", app}}},
		{"darwin", "docs/plan.md", call{"open", []string{"-R", file}}},
		{"windows", "docs", call{"explorer", []string{"/select," + dir}}},
		{"windows", "X.app", call{"explorer", []string{"/select," + app}}},
		{"windows", "docs/plan.md", call{"explorer", []string{"/select," + file}}},
		{"linux", "docs", call{"xdg-open", []string{dir}}},
		{"linux", "X.app", call{"xdg-open", []string{app}}},
	} {
		rec := &recorder{}
		if err := revealChatPath(rec.opener(c.goos), root, c.token); err != nil {
			t.Fatalf("%s %s: %v", c.goos, c.token, err)
		}
		if len(rec.starts) != 1 || len(rec.runs) != 0 || !reflect.DeepEqual(rec.starts[0], c.want) {
			t.Errorf("%s %s: got %+v / %+v, want %+v", c.goos, c.token, rec.starts, rec.runs, c.want)
		}
	}
	// reveal is handed a directory that is gone or is a file by the time it
	// runs (Linux): it opens the parent folder, never the stale path.
	rec := &recorder{}
	if err := recImpl(rec).reveal(file, true); err != nil || rec.starts[0].args[0] != filepath.Dir(file) {
		t.Fatalf("file passed as dir: %v %+v", err, rec.starts)
	}
	rec = &recorder{}
	if err := recImpl(rec).reveal(filepath.Join(realRoot, "gone"), true); err != nil || rec.starts[0].args[0] != realRoot {
		t.Fatalf("missing dir: %v %+v", err, rec.starts)
	}
}

func recImpl(r *recorder) chatOpener { return r.opener("linux") }
