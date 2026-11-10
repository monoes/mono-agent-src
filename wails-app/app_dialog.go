package main

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// showDialog is the ONLY way this module shows a native message dialog (TestMessageDialogIsCalledOnlyInsideTheHelper).
//
// Two reasons. Wails on Linux (v2.17.0, internal/frontend/desktop/linux/dialog.go) has one shared, unbuffered channel
// for the result of every message dialog, so two dialogs open at once can swap answers, and an answer meant for another
// Yes/No dialog could confirm a start. So one dialog is open at a time, app-wide: the lock is held while the dialog is
// really on screen (including one a caller gave up waiting for), and another dialog waits at most dialogWait and is then
// refused with errDialogBusy rather than queued for good. And GTK formats the message (not the title), so the message
// is escaped here for the platform and the title is passed as it is.
func showDialog(a *App, o wailsruntime.MessageDialogOptions) (choice string, err error) {
	if !acquireDialog(dialogWait) {
		return "", errDialogBusy
	}
	defer dialogMu.Unlock()
	dialogShown.Store(doomsdayNow().UnixNano()) // on screen from here; a caller's think time runs from this moment
	defer func() {
		if recover() != nil { // no window to show it in
			choice, err = "", errNoNativeDialog
		}
	}()
	o.Message = dialogTextFor(dialogGOOS, o.Message)
	var ctx context.Context
	if a != nil {
		ctx = a.ctx
	}
	return dialogRun(ctx, o)
}

// dialogShown is when the last dialog got the dialog lock (unix nanoseconds, the doomsdayNow clock).
var dialogShown atomic.Int64

func dialogShownAt() time.Time { return time.Unix(0, dialogShown.Load()) }

var (
	dialogMu   sync.Mutex
	dialogWait = 3 * time.Second
	dialogGOOS = runtime.GOOS
	// errDialogBusy is why a dialog was not shown: another one is still open.
	errDialogBusy = errors.New("another dialog is still open: answer or close it first")
	// dialogRun is the Wails runtime call; a test replaces it.
	dialogRun = realDialogRun
)

// realDialogRun is the Wails call. wails runtime.MessageDialog calls log.Fatalf (the app exits, no recover can catch it)
// when the context is nil or carries no frontend, as before the window exists or under a dev server in a browser.
func realDialogRun(ctx context.Context, o wailsruntime.MessageDialogOptions) (string, error) {
	if ctx == nil || ctx.Value("frontend") == nil {
		return "", errNoNativeDialog
	}
	return wailsruntime.MessageDialog(ctx, o)
}

// acquireDialog takes the dialog lock, waiting at most d.
func acquireDialog(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if dialogMu.TryLock() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}
