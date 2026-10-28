// wails-app/app_images_watch.go
package main

import (
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/imagescan"
)

// scanAVCacheTTL is how long the watcher trusts one read of the profile's
// `media scan` setting: the read is a CLI process, the walk is every few
// seconds, and a switch only has to take effect within the half minute.
const (
	scanAVCacheTTL   = 30 * time.Second
	scanAVCLITimeout = 15 * time.Second
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
	// Audio and video count only while the profile's `media scan` is on.
	w.IncludeAV = a.scanAVReader(profileID)
	w.Start()
	return w
}

// scanAVReader answers whether profileID's `media scan` is on, by asking the
// CLI (which owns the setting; the app reads no database rows itself) and
// remembering the answer for scanAVCacheTTL. A CLI that cannot answer means
// off: audio and video are opt-in.
func (a *App) scanAVReader(profileID string) func() bool {
	var (
		mu   sync.Mutex
		at   time.Time
		last bool
	)
	return func() bool {
		mu.Lock()
		defer mu.Unlock()
		if !at.IsZero() && time.Since(at) < scanAVCacheTTL {
			return last
		}
		var out struct {
			ScanAudioVideo bool `json:"scan_audio_video"`
		}
		last = a.runProfileCLI(profileID, scanAVCLITimeout, &out, "media", "scan") == nil && out.ScanAudioVideo
		at = time.Now()
		return last
	}
}
