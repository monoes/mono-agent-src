//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The watcher walks the folder itself and runs `media sync` for the
// watcher's profile only when the folder differs from its last walk (and
// once at start). images:changed follows only a sync that reports a change.
func TestImageWatcherSyncsThroughTheCLIOnlyOnChange(t *testing.T) {
	log := syncCLI(t, syncChanged, syncUnchanged, syncChanged)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "logo.png"), []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestApp(t)
	a.selectForTest("other") // the watcher's profile wins over the active one
	rec := &eventRecorder{}
	w := a.startImageWatcher("work", root, 20*time.Millisecond, rec.emit)
	defer w.Stop()

	// Start: one sync, which changed rows, so one event.
	waitCalls(t, log, 1)
	names, evts := waitEvents(t, rec, 1)
	if names[0] != "images:changed" || evts[0]["profileID"] != "work" || evts[0]["added"] != 1 {
		t.Fatalf("event = %s %+v", names[0], evts[0])
	}

	// Idle: many walks, no CLI call.
	time.Sleep(200 * time.Millisecond)
	if got := callsSoFar(log); len(got) != 1 {
		t.Fatalf("idle watcher called the CLI: %v", got)
	}

	// A new image: one sync; it reports no change, so no event.
	if err := os.WriteFile(filepath.Join(root, "b.jpg"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitCalls(t, log, 2)
	time.Sleep(100 * time.Millisecond)
	if names, _ := rec.snapshot(); len(names) != 1 {
		t.Fatalf("an unchanged sync emitted: %v", names)
	}

	// A file that is not an image changes nothing the watcher tracks.
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := callsSoFar(log); len(got) != 2 {
		t.Fatalf("a non-image file triggered a sync: %v", got)
	}

	// Another new image: the sync reports a change, so a second event.
	if err := os.WriteFile(filepath.Join(root, "c.webp"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitCalls(t, log, 3)
	waitEvents(t, rec, 2)

	for _, got := range callsSoFar(log) {
		if got != "--profile work --json media sync" {
			t.Fatalf("CLI call = %q", got)
		}
	}
}

// A failed sync is logged, not emitted.
func TestImageWatcherLogsAFailedSync(t *testing.T) {
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, "echo 'database is locked' >&2; exit 1\n"))
	a := newTestApp(t)
	rec := &eventRecorder{}
	w := a.startImageWatcher("work", t.TempDir(), time.Hour, rec.emit)
	defer w.Stop()
	deadline := time.Now().Add(5 * time.Second)
	for {
		a.logsMu.Lock()
		n := len(a.logs)
		var msg string
		if n > 0 {
			msg = a.logs[n-1].Message
		}
		a.logsMu.Unlock()
		if strings.Contains(msg, "media discovery") && strings.Contains(msg, "database is locked") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no warning logged (last %q)", msg)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if names, _ := rec.snapshot(); len(names) != 0 {
		t.Fatalf("failed sync emitted: %v", names)
	}
}

// restartImageWatcher, end to end with the real CLI: an image already in
// the active profile's folder is registered by the start-up `media sync`,
// in the CLI's database — the app itself writes no vault_images rows.
func TestRestartImageWatcher_DiscoversProjectImages(t *testing.T) {
	cliBin := buildTestCLI(t) // before HOME moves, so the build cache stays put
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MONOAGENTCLI_BIN", cliBin)
	a := newTestApp(t)

	projDir := filepath.Join(home, ".monoagent", "profiles", "default")
	testImg := filepath.Join(projDir, "banner.webp")
	if err := os.MkdirAll(projDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testImg, []byte("webp-binary-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	a.restartImageWatcher()
	defer func() {
		a.imgWatchMu.Lock()
		if a.imgWatcher != nil {
			a.imgWatcher.Stop()
			a.imgWatcher = nil
		}
		a.imgWatchMu.Unlock()
	}()

	var found bool
	for start := time.Now(); time.Since(start) < 30*time.Second && !found; time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(home, ".monoagent", "monoagent.db")); err != nil {
			continue
		}
		var count int
		db := cliHomeDB(t, home)
		if err := db.DB.QueryRow(`SELECT COUNT(*) FROM vault_images WHERE path = ? AND source = 'discovered'`, testImg).Scan(&count); err == nil && count > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the image watcher's sync to discover %s", testImg)
	}
	var appRows int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM vault_images`).Scan(&appRows); err != nil || appRows != 0 {
		t.Fatalf("the app wrote vault_images itself: %d rows, %v", appRows, err)
	}
}
