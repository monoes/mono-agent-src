// Package imagescan provides background polling and change detection for media files.
package imagescan

import (
	"sync"
	"time"
)

// DefaultPollInterval polls every 5 seconds, matching docscan.DefaultPollInterval.
const DefaultPollInterval = 5 * time.Second

type fileState struct {
	modTime int64
	size    int64
}

// Watcher polls root recursively and invokes onChange with the FULL
// current matching-file snapshot whenever that snapshot differs from the
// previous tick (add/modify/remove of any tracked file).
//
// It watches images, and audio and video too while IncludeAV (set before
// Start) says so -- the profile's `media scan` setting. With it unset or off
// no audio or video file is even walked into the snapshot.
//
// The first scan emits unconditionally, so pre-existing images on disk
// are discovered immediately upon starting the watcher.
type Watcher struct {
	root     string
	interval time.Duration
	onChange func([]FileInfo)

	// IncludeAV is asked on every walk whether audio and video count; nil
	// means no. Set it before Start.
	IncludeAV func() bool

	mu      sync.Mutex
	files   map[string]fileState // keyed by path
	scanned bool

	stopCh chan struct{}
	doneCh chan struct{}
}

// NewWatcher creates a Watcher for root. An interval <= 0 uses DefaultPollInterval.
func NewWatcher(root string, interval time.Duration, onChange func([]FileInfo)) *Watcher {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	return &Watcher{
		root:     root,
		interval: interval,
		onChange: onChange,
		files:    make(map[string]fileState),
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

// Start scans immediately (emitting if anything is found) and then polls in
// a background goroutine.
func (w *Watcher) Start() {
	w.scan(true)
	go func() {
		defer close(w.doneCh)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-w.stopCh:
				return
			case <-ticker.C:
				w.scan(true)
			}
		}
	}()
}

// Stop ends polling and waits for the background goroutine to exit.
func (w *Watcher) Stop() {
	close(w.stopCh)
	<-w.doneCh
}

// scan walks root once, diffs against the last known snapshot, and invokes
// onChange with the full current snapshot if it differs.
func (w *Watcher) scan(emit bool) {
	scan := Scan
	if w.IncludeAV != nil && w.IncludeAV() {
		scan = ScanMedia
	}
	found, err := scan(w.root)
	if err != nil {
		return // transient read error -- try again next tick
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	// First scan always emits so the database reconciles pre-existing files on disk.
	if !w.scanned {
		w.scanned = true
		for _, f := range found {
			w.files[f.Path] = fileState{modTime: f.ModTime, size: f.SizeBytes}
		}
		if emit && w.onChange != nil {
			w.onChange(found)
		}
		return
	}

	// Subsequent ticks: compare with previous snapshot.
	changed := len(found) != len(w.files)
	next := make(map[string]fileState, len(found))
	for _, f := range found {
		next[f.Path] = fileState{modTime: f.ModTime, size: f.SizeBytes}
		if !changed {
			prev, ok := w.files[f.Path]
			if !ok || prev.modTime != f.ModTime || prev.size != f.SizeBytes {
				changed = true
			}
		}
	}

	if changed {
		w.files = next
		if emit && w.onChange != nil {
			w.onChange(found)
		}
	}
}
