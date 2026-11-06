package main

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// wails v2.17.0 hands a dialog's message to gtk_message_dialog_new as its printf FORMAT with no arguments, so a '%' in a
// name, a path, an error or a charter garbles it or reads memory. On Linux every '%' is doubled; macOS and Windows show
// text as it is, and doubling there would show "%%".
func TestDialogTextDoublesPercentWhereGTKReadsAFormat(t *testing.T) {
	for _, in := range []string{"%s%n%x", "100%", "%", "%%", "90% of files", "ends with %", "no percent", "%d %s %p", ""} {
		got := dialogTextFor("linux", in)
		if strings.Count(got, "%") != 2*strings.Count(in, "%") {
			t.Errorf("%q -> %q", in, got)
		}
		// what GTK will do with it: format it with no arguments. It must come out as the original text.
		if shown := fmt.Sprintf(got, []any{}...); shown != in {
			t.Errorf("%q is shown as %q", in, shown)
		}
		for _, goos := range []string{"windows", "darwin"} {
			if dialogTextFor(goos, in) != in {
				t.Errorf("%s changed %q", goos, in)
			}
		}
	}
	for _, goos := range []string{"freebsd", "openbsd", "netbsd"} { // GTK too
		if dialogTextFor(goos, "5%") != "5%%" {
			t.Errorf("%s does not double", goos)
		}
	}
}

// Every Wails dialog goes through showDialog (one app-wide mutex, escaping of the message only): the module has exactly
// one call of MessageDialog, inside app_dialog.go. The scan reads every non-test Go file under wails-app (sub-packages
// too) and fails on a call anywhere else, on a dot-import of the wails runtime, and on the function used as a value
// (an alias or a variable) which a call-only scan would miss.
func TestMessageDialogIsCalledOnlyInsideTheHelper(t *testing.T) {
	calls := 0
	_ = filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if n := d.Name(); n == "frontend" || n == "node_modules" || n == "build" || n == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		inHelper := filepath.Base(path) == "app_dialog.go"
		for _, imp := range file.Imports {
			if imp.Name != nil && imp.Name.Name == "." && strings.Contains(imp.Path.Value, "wails") {
				t.Errorf("%s: a dot-import of the wails runtime hides dialog calls from this scan", fset.Position(imp.Pos()))
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.SelectorExpr:
				if v.Sel.Name == "MessageDialog" {
					calls++
					if !inHelper {
						t.Errorf("%s: MessageDialog outside app_dialog.go: use showDialog", fset.Position(v.Pos()))
					}
				}
			case *ast.Ident:
				if v.Name == "MessageDialog" && v.Obj == nil && !inHelper {
					t.Errorf("%s: a bare MessageDialog: use showDialog", fset.Position(v.Pos()))
				}
			}
			return true
		})
		return nil
	})
	if calls != 1 {
		t.Fatalf("found %d uses of MessageDialog, want exactly one (in showDialog)", calls)
	}
}

// showDialog escapes the MESSAGE (GTK formats it) and not the title (GTK sets it as plain text), and passes the result
// through the seam of the runtime.
func TestShowDialogEscapesTheMessageAndNotTheTitle(t *testing.T) {
	var got wailsruntime.MessageDialogOptions
	old, oldOS := dialogRun, dialogGOOS
	dialogRun = func(_ context.Context, o wailsruntime.MessageDialogOptions) (string, error) {
		got = o
		return "Yes", nil
	}
	dialogGOOS = "linux"
	t.Cleanup(func() { dialogRun, dialogGOOS = old, oldOS })
	if c, err := showDialog(&App{}, wailsruntime.MessageDialogOptions{Title: "100% title", Message: "goal 90%s%n"}); err != nil || c != "Yes" {
		t.Fatalf("%q %v", c, err)
	}
	if got.Title != "100% title" || got.Message != "goal 90%%s%%n" {
		t.Fatalf("title %q message %q", got.Title, got.Message)
	}
	dialogGOOS = "windows"
	_, _ = showDialog(&App{}, wailsruntime.MessageDialogOptions{Title: "t%", Message: "m%"})
	if got.Title != "t%" || got.Message != "m%" {
		t.Fatalf("windows: %q %q", got.Title, got.Message)
	}
}

// wails on Linux has one shared channel for all dialog results, so two dialogs open at once can swap answers: the
// helper shows one at a time, each caller gets its own answer, and one that waits too long is refused, not queued.
func TestDialogsAreShownOneAtATimeAndAnswersNeverCross(t *testing.T) {
	var inFlight, maxInFlight atomic.Int32
	old := dialogRun
	dialogRun = func(_ context.Context, o wailsruntime.MessageDialogOptions) (string, error) {
		n := inFlight.Add(1)
		for { // record the highest concurrency seen
			m := maxInFlight.Load()
			if n <= m || maxInFlight.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		inFlight.Add(-1)
		return o.Title, nil // the answer is the caller's own title
	}
	t.Cleanup(func() { dialogRun = old })
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			title := fmt.Sprintf("dialog %d", i)
			if c, err := showDialog(&App{}, wailsruntime.MessageDialogOptions{Title: title}); err != nil || c != title {
				t.Errorf("%s got %q %v", title, c, err)
			}
		}(i)
	}
	wg.Wait()
	if maxInFlight.Load() != 1 {
		t.Fatalf("%d dialogs were open at once", maxInFlight.Load())
	}
}

func TestADialogThatNeverReturnsRefusesTheNextOneInsteadOfHangingIt(t *testing.T) {
	stuck := make(chan struct{})
	old, oldWait := dialogRun, dialogWait
	dialogRun = func(context.Context, wailsruntime.MessageDialogOptions) (string, error) { <-stuck; return "Yes", nil }
	dialogWait = 150 * time.Millisecond
	var first sync.WaitGroup
	first.Add(1)
	go func() { defer first.Done(); showDialog(&App{}, wailsruntime.MessageDialogOptions{Title: "first"}) }()
	time.Sleep(50 * time.Millisecond)
	began := time.Now()
	_, err := showDialog(&App{}, wailsruntime.MessageDialogOptions{Title: "second"})
	close(stuck)
	first.Wait() // the seams are put back only after the dialog goroutine is done with them
	dialogRun, dialogWait = old, oldWait
	if !errors.Is(err, errDialogBusy) || time.Since(began) > 2*time.Second {
		t.Fatalf("err %v after %v", err, time.Since(began))
	}
}

// A runtime with no window panics; the helper turns that into an error and still gives the mutex back.
func TestAPanickingRuntimeIsAnErrorAndFreesTheDialogLock(t *testing.T) {
	old := dialogRun
	dialogRun = func(context.Context, wailsruntime.MessageDialogOptions) (string, error) { panic("no frontend") }
	t.Cleanup(func() { dialogRun = old })
	if _, err := showDialog(&App{}, wailsruntime.MessageDialogOptions{}); !errors.Is(err, errNoNativeDialog) {
		t.Fatalf("%v", err)
	}
	dialogRun = func(context.Context, wailsruntime.MessageDialogOptions) (string, error) { return "ok", nil }
	if c, err := showDialog(&App{}, wailsruntime.MessageDialogOptions{}); err != nil || c != "ok" {
		t.Fatalf("the lock was not freed: %q %v", c, err)
	}
}
