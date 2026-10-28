// wails-app/app_images_watch.go
package main

import (
	"context"
	"time"

	"github.com/monoes/mono-agent/internal/imagescan"
	"github.com/monoes/mono-agent/internal/vault"
)

// restartImageWatcher stops any existing image watcher and starts a new one
// scoped to the active profile's folder. Mirrors restartDocumentWatcher in
// shape and call sites (startup, MoveProfileFolder, SwitchProfile,
// shutdown).
//
// The watcher only detects change: imagescan.Watcher walks the folder every
// few seconds and compares (path, size, mtime) with its last walk, which
// costs no subprocess. On a difference (and once at start, since nothing
// else knows what is already on disk) it runs `image sync`, which owns the
// vault_images writes; images:changed is emitted only when that sync
// reports it changed something.
func (a *App) restartImageWatcher() {
	a.imgWatchMu.Lock()
	defer a.imgWatchMu.Unlock()

	if a.imgWatcher != nil {
		a.imgWatcher.Stop()
		a.imgWatcher = nil
	}

	root := a.documentRootForActiveProfile()
	profileID, err := a.profileID()
	if root == "" || err != nil {
		return
	}
	a.imgWatcher = a.startImageWatcher(profileID, root, 0, a.emitFolderEvent)
}

// startImageWatcher starts an imagescan.Watcher on root that syncs
// profileID's discovered images through the CLI whenever the folder differs
// from its last walk. interval <= 0 uses imagescan.DefaultPollInterval.
func (a *App) startImageWatcher(profileID, root string, interval time.Duration, emit folderEventFunc) *imagescan.Watcher {
	s := newFolderSyncer(func() {
		if changed := a.syncFolder(profileID, "media discovery", "media", "sync"); changed != nil {
			emit("images:changed", folderEventData(profileID, changed))
		}
	})
	w := imagescan.NewWatcher(root, interval, func([]imagescan.FileInfo) { s.trigger() })
	// Audio and video count only while the profile's `media scan` is on; the
	// setting is read on every walk, so switching it changes the next one.
	w.IncludeAV = func() bool {
		if a.db == nil {
			return false
		}
		on, _ := vault.ScanAVEnabled(context.Background(), a.db, profileID)
		return on
	}
	w.Start()
	return w
}
