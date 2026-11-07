package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Wails v2.17.0 answers a Question dialog with the clicked button's text only on macOS; Linux (GTK) returns "Yes"/"No"
// and Windows (MessageBox) "Yes"/"No", whatever Buttons were given. One helper maps "the person confirmed" per OS.
func TestDialogConfirmedPerOS(t *testing.T) {
	q, w := wailsruntime.QuestionDialog, wailsruntime.WarningDialog
	for _, c := range []struct {
		goos          string
		typ           wailsruntime.DialogType
		choice        string
		want          bool
		whatItIsAbout string
	}{
		{"linux", q, "Yes", true, ""}, {"linux", q, "No", false, ""}, {"linux", q, "Delete anyway", false, "GTK never returns a custom label"},
		{"linux", q, "", false, ""}, {"linux", w, "Ok", true, ""}, {"linux", w, "Cancel", false, ""},
		{"freebsd", q, "Yes", true, "GTK too"}, {"freebsd", q, "No", false, ""},
		{"windows", q, "Yes", true, ""}, {"windows", q, "No", false, ""}, {"windows", q, "Delete anyway", false, ""},
		{"windows", w, "Ok", true, ""}, {"windows", w, "Cancel", false, ""}, {"windows", w, "No", false, ""},
		{"darwin", q, "Delete anyway", true, "macOS returns the button text"}, {"darwin", q, "Cancel", false, ""},
		{"darwin", q, "Yes", false, "not a button of this dialog on macOS"}, {"darwin", q, "", false, ""},
		{"darwin", w, "Delete anyway", true, ""}, {"darwin", w, "Cancel", false, ""},
		{"linux", wailsruntime.InfoDialog, "Ok", false, "an Info dialog confirms nothing"},
	} {
		if got := dialogConfirmed(c.goos, c.typ, "Delete anyway", c.choice); got != c.want {
			t.Errorf("%s %s %q: got %v, want %v (%s)", c.goos, c.typ, c.choice, got, c.want, c.whatItIsAbout)
		}
	}
}

// A dialog is answered per OS through the real callers: "Delete anyway" and the folder upload confirm on every OS.
func TestConfirmAbandonConfirmsWithTheAnswerOfEachOS(t *testing.T) {
	oldRun, oldOS := dialogRun, dialogGOOS
	t.Cleanup(func() { dialogRun, dialogGOOS = oldRun, oldOS })
	for _, c := range []struct {
		goos, choice string
		want         bool
	}{
		{"linux", "Yes", true}, {"linux", "No", false}, {"windows", "Yes", true}, {"windows", "No", false},
		{"darwin", "Delete anyway", true}, {"darwin", "Cancel", false},
	} {
		dialogGOOS = c.goos
		dialogRun = func(context.Context, wailsruntime.MessageDialogOptions) (string, error) { return c.choice, nil }
		if got, err := confirmAbandon(&App{}, "t", "m"); err != nil || got != c.want {
			t.Errorf("%s %q: got %v %v, want %v", c.goos, c.choice, got, err, c.want)
		}
	}
}

func TestFolderUploadConfirmsWithTheAnswerOfEachOS(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldRun, oldOS := dialogRun, dialogGOOS
	t.Cleanup(func() { dialogRun, dialogGOOS = oldRun, oldOS })
	for _, c := range []struct {
		goos, choice string
		want         int
	}{
		{"linux", "Yes", 1}, {"linux", "No", 0}, {"windows", "Yes", 1}, {"windows", "No", 0},
		{"darwin", "Upload", 1}, {"darwin", "Cancel", 0},
	} {
		dialogGOOS = c.goos
		dialogRun = func(context.Context, wailsruntime.MessageDialogOptions) (string, error) { return c.choice, nil }
		files, err := (&App{}).confirmFolderUpload(dir)
		if err != nil || len(files) != c.want {
			t.Errorf("%s %q: got %v %v, want %d files", c.goos, c.choice, files, err, c.want)
		}
	}
}

// The Wails runtime calls log.Fatalf (the app exits, no recover) when the context is nil or has no frontend.
func TestRealDialogRunRefusesAContextWithoutAFrontend(t *testing.T) {
	for _, ctx := range []context.Context{nil, context.Background()} {
		if _, err := realDialogRun(ctx, wailsruntime.MessageDialogOptions{}); !errors.Is(err, errNoNativeDialog) {
			t.Errorf("ctx %v: err = %v, want errNoNativeDialog", ctx, err)
		}
	}
	// confirmAbandon on an app with no window says so instead of ending the process
	dialogRunSaved := dialogRun
	dialogRun = realDialogRun
	t.Cleanup(func() { dialogRun = dialogRunSaved })
	if ok, err := confirmAbandon(&App{}, "t", "m"); ok || !errors.Is(err, errNoNativeDialog) {
		t.Errorf("confirmAbandon = %v %v", ok, err)
	}
	if _, err := (&App{}).confirmFolderUpload(t.TempDir()); !errors.Is(err, errNoNativeDialog) && err != nil {
		t.Errorf("folder upload err = %v", err)
	}
}

// A dialog that is busy is said so where the person asked: Delete anyway names it per chat, the folder upload returns it.
func TestDeleteAnywayAndFolderUploadSayTheDialogIsBusy(t *testing.T) {
	a, log := aitaskApp(t, anywayScript)
	old := confirmAbandon
	confirmAbandon = func(*App, string, string) (bool, error) { return false, errDialogBusy }
	t.Cleanup(func() { confirmAbandon = old })
	a.DeleteChatConversations([]string{"running"})
	got, err := a.DeleteChatConversationsAnyway([]string{"running"})
	if err != nil || !strings.Contains(got.Failed["running"], errDialogBusy.Error()) || abandoningCalls(t, log) != 0 {
		t.Fatalf("busy not shown: %+v %v", got, err)
	}
	again, _ := a.DeleteChatConversationsAnyway([]string{"running"}) // the offer survives, asking again is allowed
	if !strings.Contains(again.Failed["running"], errDialogBusy.Error()) {
		t.Fatalf("the offer was lost: %+v", again)
	}

	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.md"), []byte("x"), 0o600)
	oldRun := dialogRun
	dialogRun = func(context.Context, wailsruntime.MessageDialogOptions) (string, error) { return "", errDialogBusy }
	t.Cleanup(func() { dialogRun = oldRun })
	if files, err := (&App{}).confirmFolderUpload(dir); len(files) != 0 || !errors.Is(err, errDialogBusy) {
		t.Fatalf("folder upload: %v %v", files, err)
	}
}

// The tail of the stderr keeps a line it can keep: it is cut at a line boundary only when it begins in the middle of a line.
func TestHeadTailBufferTailEdges(t *testing.T) {
	// the tail begins exactly at a line start: its first line is valid and stays
	b := &headTailBuffer{head: 4, tail: 11}
	b.Write([]byte("HEAD" + strings.Repeat("x", 50) + "\nfirst line\n"))
	if s := b.String(); !strings.HasSuffix(s, "first line\n") {
		t.Errorf("a valid first line was dropped: %q", s)
	}
	// the only newline is the last byte of the tail: the tail is not emptied
	c := &headTailBuffer{head: 4, tail: 10}
	c.Write([]byte("HEAD" + strings.Repeat("y", 50) + "\n"))
	if s := c.String(); !strings.HasSuffix(s, "yyyyyyyyy\n") {
		t.Errorf("the tail was emptied: %q", s)
	}
	// a tail that begins mid-line is still cut to the next line
	d := &headTailBuffer{head: 4, tail: 20}
	d.Write([]byte("HEAD" + strings.Repeat("z", 50) + "\nwhole line\n"))
	if s := d.String(); strings.Contains(s, "zz") || !strings.HasSuffix(s, "whole line\n") {
		t.Errorf("mid-line start kept: %q", s)
	}
}
