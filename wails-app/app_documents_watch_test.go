//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// eventRecorder is a folderEventFunc that remembers what was emitted.
type eventRecorder struct {
	mu   sync.Mutex
	evts []map[string]interface{}
	name []string
}

func (r *eventRecorder) emit(name string, data map[string]interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.name = append(r.name, name)
	r.evts = append(r.evts, data)
}

func (r *eventRecorder) snapshot() ([]string, []map[string]interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.name...), append([]map[string]interface{}(nil), r.evts...)
}

// syncCLI is a fake monoagentcli that logs its argv and prints the next
// canned sync report (or the last one once they run out). It returns the
// args log path.
func syncCLI(t *testing.T, reports ...string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "args.log")
	for i, r := range reports {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("r%d", i)), []byte(r), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, fmt.Sprintf(`case "$*" in *"media scan"*) echo '{"scan_audio_video":false}'; exit 0;; esac
echo "$*" >> '%[1]s'
i=$(($(wc -l < '%[1]s') - 1)); [ $i -ge %[2]d ] && i=%[3]d
cat '%[4]s'/r$i
`, log, len(reports), len(reports)-1, dir)))
	return log
}

// callsSoFar is the argv log, or nothing before the first call.
func callsSoFar(log string) []string {
	raw, err := os.ReadFile(log)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// waitCalls waits until the fake CLI has been called n times.
func waitCalls(t *testing.T, log string, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := callsSoFar(log); len(got) >= n {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("CLI called %d times, want %d", len(callsSoFar(log)), n)
	return nil
}

func waitEvents(t *testing.T, r *eventRecorder, n int) ([]string, []map[string]interface{}) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if names, evts := r.snapshot(); len(names) >= n {
			return names, evts
		}
		time.Sleep(10 * time.Millisecond)
	}
	names, _ := r.snapshot()
	t.Fatalf("events = %v, want %d", names, n)
	return nil, nil
}

// dropFile puts a file into root in one step. os.WriteFile creates the file
// empty and then fills it, so a poll between the two sees it twice (size 0,
// then its content): two syncs for one file, and the extra one can report a
// change the test did not expect. Renaming from a staging directory outside
// the watched root makes the file appear complete, as a finished download
// or save does.
func dropFile(t *testing.T, root, name, content string) {
	t.Helper()
	staged := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(staged, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staged, filepath.Join(root, name)); err != nil {
		t.Fatal(err)
	}
}

const (
	syncChanged   = `{"profile_id":"work","added":1,"updated":0,"removed":0,"changed":true,"errors":[]}`
	syncUnchanged = `{"profile_id":"work","added":0,"updated":0,"removed":0,"changed":false,"errors":[]}`
)

// The watcher walks the folder itself and runs `profile documents sync`
// for the watcher's profile only when the folder differs from its last
// walk (and once at start). documents:changed follows only a sync that
// reports a change.
func TestDocumentWatcherSyncsThroughTheCLIOnlyOnChange(t *testing.T) {
	log := syncCLI(t, syncChanged, syncUnchanged, syncChanged)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "cv.md"), []byte("cv"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestApp(t)
	a.selectForTest("other") // the watcher's profile wins over the active one
	rec := &eventRecorder{}
	w := a.startDocumentWatcher("work", root, 20*time.Millisecond, rec.emit)
	defer w.Stop()

	// Start: one sync, which changed rows, so one event.
	waitCalls(t, log, 1)
	names, evts := waitEvents(t, rec, 1)
	if names[0] != "documents:changed" || evts[0]["profileID"] != "work" || evts[0]["added"] != 1 {
		t.Fatalf("event = %s %+v", names[0], evts[0])
	}

	// Idle: many walks, no CLI call.
	time.Sleep(200 * time.Millisecond)
	if got := callsSoFar(log); len(got) != 1 {
		t.Fatalf("idle watcher called the CLI: %v", got)
	}

	// A new file: one sync; it reports no change, so no event.
	dropFile(t, root, "b.txt", "b")
	waitCalls(t, log, 2)
	time.Sleep(100 * time.Millisecond)
	if names, _ := rec.snapshot(); len(names) != 1 {
		t.Fatalf("an unchanged sync emitted: %v", names)
	}

	// Another new file: the sync reports a change, so a second event.
	dropFile(t, root, "c.pdf", "c")
	waitCalls(t, log, 3)
	waitEvents(t, rec, 2)

	for _, got := range callsSoFar(log) {
		if got != "--profile work --json profile documents sync" {
			t.Fatalf("CLI call = %q", got)
		}
	}
}

// Rewriting a document at the same size writes no rows, but an indexed
// document goes Stale, so the page still gets documents:changed.
func TestDocumentWatcherEmitsForAnInPlaceRewrite(t *testing.T) {
	log := syncCLI(t, syncUnchanged)
	root := t.TempDir()
	doc := filepath.Join(root, "cv.md")
	if err := os.WriteFile(doc, []byte("cv"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newTestApp(t)
	rec := &eventRecorder{}
	w := a.startDocumentWatcher("work", root, 20*time.Millisecond, rec.emit)
	defer w.Stop()
	waitCalls(t, log, 1)
	time.Sleep(60 * time.Millisecond)
	if names, _ := rec.snapshot(); len(names) != 0 {
		t.Fatalf("unchanged first sync emitted: %v", names)
	}

	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(doc, later, later); err != nil {
		t.Fatal(err)
	}
	waitCalls(t, log, 2)
	names, evts := waitEvents(t, rec, 1)
	if names[0] != "documents:changed" || evts[0]["profileID"] != "work" {
		t.Fatalf("event = %s %+v", names[0], evts[0])
	}
}

// A failed sync is logged, not emitted.
func TestDocumentWatcherLogsAFailedSync(t *testing.T) {
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, "echo 'database is locked' >&2; exit 1\n"))
	a := newTestApp(t)
	rec := &eventRecorder{}
	w := a.startDocumentWatcher("work", t.TempDir(), time.Hour, rec.emit)
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
		if strings.Contains(msg, "database is locked") {
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
