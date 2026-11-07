// wails-app/app_documents_watch.go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monoes/mono-agent/internal/capture"
	"github.com/monoes/mono-agent/internal/capturedocs"
	"github.com/monoes/mono-agent/internal/docscan"
	"github.com/monoes/mono-agent/internal/profiledir"
)

// documentRootForActiveProfile resolves the directory the document watcher
// scans -- the active profile's whole folder. Returns
// profiledir.Root(a.db, <selected profile>) UNMODIFIED (no
// Abs/EvalSymlinks) -- see internal/docscan.Scan's doc comment for why:
// monomind.IngestDocument calls profiledir.Root independently at index
// time and its path-traversal guard requires the two to agree
// byte-for-byte.
func (a *App) documentRootForActiveProfile() string {
	id, err := a.profileID()
	if err != nil {
		return ""
	}
	return profiledir.Root(a.db, id)
}

// restartDocumentWatcher stops any existing document watcher and starts a
// new one scoped to the active profile's folder. Mirrors restartOrgWatcher
// exactly in shape and call sites (startup, MoveProfileFolder,
// SwitchProfile, shutdown).
//
// The watcher only detects change: docscan.Watcher walks the folder every
// few seconds and compares (path, size, mtime) with its last walk, which
// costs no subprocess. On a difference (and once at start, since nothing
// else knows what is already on disk) it runs `profile documents sync`,
// which owns the vault_documents writes; documents:changed is emitted only
// when that sync reports it changed something.
func (a *App) restartDocumentWatcher() {
	a.docWatchMu.Lock()
	defer a.docWatchMu.Unlock()

	if a.docWatcher != nil {
		a.docWatcher.Stop()
		a.docWatcher = nil
	}
	if a.capWatcher != nil {
		a.capWatcher.Stop()
		a.capWatcher = nil
	}

	profileID, err := a.profileID()
	if err != nil {
		return // no profile selected: nothing to watch
	}
	a.startCaptureWatcher(profileID)

	root := a.documentRootForActiveProfile()
	if root == "" {
		return
	}
	a.docWatcher = a.startDocumentWatcher(profileID, root, 0, a.emitFolderEvent)
}

// startDocumentWatcher starts a docscan.Watcher on root that syncs
// profileID's documents through the CLI whenever the folder differs from
// its last walk. interval <= 0 uses docscan.DefaultPollInterval.
//
// A file rewritten in place at the same size changes nothing the sync
// writes, but it can turn an indexed document Stale (computed from the
// file's mtime at list time), so that also emits documents:changed after
// the sync, without a second CLI call.
func (a *App) startDocumentWatcher(profileID, root string, interval time.Duration, emit folderEventFunc) *docscan.Watcher {
	var touched atomic.Bool
	s := newFolderSyncer(func() {
		wasTouched := touched.Swap(false)
		changed := a.syncFolder(profileID, "document discovery", "profile", "documents", "sync")
		if changed != nil || wasTouched {
			emit("documents:changed", folderEventData(profileID, changed))
		}
	})
	var prev map[string]int64 // path -> mtime from the previous walk
	w := docscan.NewWatcher(root, interval, func(files []docscan.FileInfo) {
		cur := make(map[string]int64, len(files))
		for _, f := range files {
			cur[f.Path] = f.ModTime
			if m, ok := prev[f.Path]; ok && m != f.ModTime {
				touched.Store(true)
			}
		}
		prev = cur
		s.trigger()
	})
	w.Start()
	return w
}

// folderSyncCLITimeout bounds one folder sync; a first sync of a large
// profile folder registers every file in it.
const folderSyncCLITimeout = 2 * time.Minute

// folderSyncReport is the --json output of `profile documents sync` and
// `image sync`.
type folderSyncReport struct {
	ProfileID string   `json:"profile_id"`
	Added     int      `json:"added"`
	Updated   int      `json:"updated"`
	Removed   int      `json:"removed"`
	Changed   bool     `json:"changed"`
	Errors    []string `json:"errors"`
}

// folderEventFunc emits one frontend event; tests pass a recorder.
type folderEventFunc func(name string, data map[string]interface{})

// emitFolderEvent emits once the Wails runtime is up (emitLog's guard).
func (a *App) emitFolderEvent(name string, data map[string]interface{}) {
	if a.ctx != nil {
		eventsEmit(a.ctx, name, data)
	}
}

// syncFolder runs `monoagentcli --profile <profileID> --json <args…>` and
// returns its report when the sync changed something, else nil (also on
// failure, which is logged). The profile is the watcher's own, not the
// active one: a sync queued just before a profile switch must not
// reconcile the new profile's folder under the old watcher's name.
func (a *App) syncFolder(profileID, what string, args ...string) *folderSyncReport {
	var rep folderSyncReport
	if err := a.runProfileCLI(profileID, folderSyncCLITimeout, &rep, args...); err != nil {
		a.emitLog("SYSTEM", "WARN", fmt.Sprintf("profile %s: %s: %v", profileID, what, err))
		return nil
	}
	for _, e := range rep.Errors {
		a.emitLog("SYSTEM", "WARN", fmt.Sprintf("profile %s: %s: %s", profileID, what, e))
	}
	if !rep.Changed {
		return nil
	}
	return &rep
}

// folderEventData is the documents:changed / images:changed payload.
// rep is nil when the event is not from a sync that wrote rows.
func folderEventData(profileID string, rep *folderSyncReport) map[string]interface{} {
	data := map[string]interface{}{"profileID": profileID}
	if rep != nil {
		data["added"], data["updated"], data["removed"] = rep.Added, rep.Updated, rep.Removed
	}
	return data
}

// runProfileCLI is cliJSON pinned to profileID instead of the active
// profile.
func (a *App) runProfileCLI(profileID string, timeout time.Duration, result interface{}, args ...string) error {
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return err
	}
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	full, err := scopedCLIArgs(profileID, args...)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, cliBin, full...)
	suppressConsole(cmd)
	stdout, runErr := cmd.Output()
	out := cliResultJSON(cliBin, stdout, runErr)
	var e struct {
		Error *string `json:"error"`
	}
	if json.Unmarshal([]byte(out), &e) == nil && e.Error != nil {
		return errors.New(*e.Error)
	}
	return json.Unmarshal([]byte(out), result)
}

// folderSyncer runs sync off the watcher's goroutine (the first scan fires
// inside Start, which must not block startup or a profile switch on a
// subprocess), one run at a time, and coalesces triggers that arrive while
// a run is already waiting: that run reads the folder fresh, so it covers
// them.
type folderSyncer struct {
	sync   func()
	mu     sync.Mutex // held for the duration of one sync
	queued atomic.Bool
}

func newFolderSyncer(fn func()) *folderSyncer { return &folderSyncer{sync: fn} }

func (s *folderSyncer) trigger() {
	if !s.queued.CompareAndSwap(false, true) {
		return
	}
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.queued.Store(false)
		s.sync()
	}()
}

// startCaptureWatcher watches the profile's browser-capture inbox, which
// the folder scan above skips (it is under .monomind/). It only signals:
// the page answers documents:changed by re-fetching the list, and
// `profile documents list` is what syncs captures into the vault, so the
// GUI never writes capture rows itself. Caller holds docWatchMu.
func (a *App) startCaptureWatcher(profileID string) {
	inbox, err := capture.ProfileInbox(profileID)
	if err != nil {
		return // no usable profile id, so no inbox to watch
	}
	w := capturedocs.NewWatcher(inbox, 0, func() {
		eventsEmit(a.ctx, "documents:changed", map[string]interface{}{"profileID": profileID, "captures": true})
	})
	w.Start()
	a.capWatcher = w
}
