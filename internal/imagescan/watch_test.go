package imagescan_test

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/imagescan"
)

// The watcher's snapshot has audio and video in it only while IncludeAV says
// so, so a recording dropped in the folder does not wake a sync when the
// profile has not switched them on.
func TestWatcherIncludesAudioVideoOnlyWhenAsked(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.png"), []byte("p"), 0o644); err != nil {
		t.Fatal(err)
	}
	var last atomic.Value
	snapshot := func() []imagescan.FileInfo {
		v, _ := last.Load().([]imagescan.FileInfo)
		return v
	}
	var av atomic.Bool
	w := imagescan.NewWatcher(dir, 10*time.Millisecond, func(f []imagescan.FileInfo) { last.Store(f) })
	w.IncludeAV = av.Load
	w.Start()
	defer w.Stop()

	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for i := 0; i < 300; i++ {
			if ok() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s; last snapshot %+v", what, snapshot())
	}
	waitFor("the first snapshot", func() bool { return len(snapshot()) == 1 })

	if err := os.WriteFile(filepath.Join(dir, "take.mp3"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if n := len(snapshot()); n != 1 {
		t.Fatalf("with audio/video off the snapshot has %d files, want only the png", n)
	}
	av.Store(true)
	waitFor("the mp3 to join once audio/video is on", func() bool { return len(snapshot()) == 2 })
	av.Store(false)
	waitFor("the mp3 to leave once audio/video is off", func() bool { return len(snapshot()) == 1 })
}
