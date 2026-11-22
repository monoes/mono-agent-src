### Task 2: The board watcher and the badge's first value

**Files:**
- Create: `wails-app/app_tasks_watch.go`
- Modify: `wails-app/app.go` (two fields in `App`, one line in `startup`, one in `shutdown`), `wails-app/app_profiles.go` (one line in `SwitchProfile`)
- Test: `wails-app/app_tasks_watch_test.go`

**Interfaces:**
- Consumes: `tasks.NewStore`, `(*Store).Watch`, `(*Store).Profile`, `(*Store).Rev`, `(*Store).Counts`, `(*Store).Add`, `tasks.Change`, `tasks.AddInput`, `tasks.Actor`, `tasks.Human` (Task 0 confirmed them); `folderEventFunc` (`func(name string, data map[string]interface{})`) and `(*App).emitFolderEvent` (emits once the Wails runtime is up) from `app_documents_watch.go`; `(*App).emitLog(source, level, message string)` (adds the entry to the app's log, and emits it once the Wails runtime is up) and `(*App).GetLogs() []LogEntry` from `app.go`; test helpers `eventRecorder` (with `emit` and `snapshot()`) and `waitEvents(t, rec, n)` from `app_documents_watch_test.go` (`!windows`), `newTestApp(t)`, and Task 1's `addTestTask(t, a, profileID, title)` (`app_tasks_test.go`).
- Produces:
  - `(*App).restartTaskWatcher()`, `(*App).stopTaskWatcher()`
  - `(*App).startTaskWatcher(profileID string, interval time.Duration, emit folderEventFunc) (stop func())`: `stop` returns only once the watcher's goroutine has ended, so nothing of the old profile is emitted after a restart. When `Watch` returns an error (the profile is not there: it never was, or it was deleted while watched) the goroutine writes one warning to the app's log and ends; it does not start over.
  - `taskPulseData(profileID string, c tasks.Change) map[string]interface{}`: `{"profile_id", "rev" (int64), "inbox" (int), "review" (int)}`, the `tasks:changed` payload.
  - `(*App).TaskPulse() map[string]interface{}`: the same document for the active profile, read now; `{}` without a database, for a profile that is not there, or on a failed read.
  - `App` fields `taskWatchMu sync.Mutex`, `taskWatchStop func()`, `taskWatchProfile string` (the profile being watched, `""` when none).

- [ ] **Step 1: Write the failing tests**

Create `wails-app/app_tasks_watch_test.go`:

```go
//go:build !windows

package main

import (
	"strings"
	"testing"
	"time"
)

// waitTaskWarning waits until the app's log holds a warning that contains want.
func waitTaskWarning(t *testing.T, a *App, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range a.GetLogs() {
			if e.Level == "WARN" && strings.Contains(e.Message, want) {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no warning holding %q in the log: %+v", want, a.GetLogs())
}

// The watcher reports the board once at the start and after each write,
// with the Inbox and Review counts the sidebar badge shows.
func TestTaskWatcherEmitsTheStartAndEachChange(t *testing.T) {
	a := newTestApp(t)
	rec := &eventRecorder{}
	stop := a.startTaskWatcher("default", 10*time.Millisecond, rec.emit)
	defer stop()
	names, evts := waitEvents(t, rec, 1)
	if names[0] != "tasks:changed" || evts[0]["profile_id"] != "default" || evts[0]["rev"] != int64(0) || evts[0]["inbox"] != 0 || evts[0]["review"] != 0 {
		t.Fatalf("first event = %s %+v", names[0], evts[0])
	}
	addTestTask(t, a, "default", "first")
	_, evts = waitEvents(t, rec, 2)
	if evts[1]["rev"] != int64(1) || evts[1]["inbox"] != 1 {
		t.Fatalf("after an add: %+v", evts[1])
	}
	time.Sleep(100 * time.Millisecond)
	if names, _ := rec.snapshot(); len(names) != 2 {
		t.Fatalf("no write, no event: %v", names)
	}
}

// Once stop has returned, a write emits nothing: after a profile switch the
// page never hears the old profile's board.
func TestTaskWatcherIsSilentOnceStopped(t *testing.T) {
	a := newTestApp(t)
	rec := &eventRecorder{}
	stop := a.startTaskWatcher("default", 10*time.Millisecond, rec.emit)
	waitEvents(t, rec, 1)
	stop()
	addTestTask(t, a, "default", "after the stop")
	time.Sleep(100 * time.Millisecond)
	if names, _ := rec.snapshot(); len(names) != 1 {
		t.Fatalf("a stopped watcher emitted: %v", names)
	}
}

// A watcher reports its own profile's board only (spec D9).
func TestTaskWatcherWatchesItsOwnProfile(t *testing.T) {
	a := newTestApp(t)
	if _, err := a.db.Exec(`INSERT INTO profiles (id, name) VALUES ('work', 'Work')`); err != nil {
		t.Fatal(err)
	}
	rec := &eventRecorder{}
	stop := a.startTaskWatcher("work", 10*time.Millisecond, rec.emit)
	defer stop()
	waitEvents(t, rec, 1)
	addTestTask(t, a, "default", "on the other board")
	time.Sleep(100 * time.Millisecond)
	if names, _ := rec.snapshot(); len(names) != 1 {
		t.Fatalf("a write to another profile's board emitted: %v", names)
	}
	addTestTask(t, a, "work", "on this board")
	_, evts := waitEvents(t, rec, 2)
	if evts[1]["profile_id"] != "work" || evts[1]["inbox"] != 1 {
		t.Fatalf("event = %+v", evts[1])
	}
}

// A watcher on a profile that does not exist ends at once and says so in the
// app's log: Watch returns an error for it (ErrInvalid), where a watcher that
// went on would report revision 0 and an empty board for ever. It emits
// nothing and does not start over, so a profile that appears later is not
// watched until the next restart. A profile deleted while it is watched ends
// Watch the same way, with an error that wraps ErrNotFound (P1's
// TestWatchEndsWhenItsProfileIsDeleted pins that); the branch here is the same.
func TestTaskWatcherEndsWithAWarningForAProfileThatDoesNotExist(t *testing.T) {
	a := newTestApp(t)
	rec := &eventRecorder{}
	stop := a.startTaskWatcher("nobody", 10*time.Millisecond, rec.emit)
	waitTaskWarning(t, a, `unknown profile "nobody"`)
	if _, err := a.db.Exec(`INSERT INTO profiles (id, name) VALUES ('nobody', 'Nobody')`); err != nil {
		t.Fatal(err)
	}
	addTestTask(t, a, "nobody", "after the watcher ended")
	time.Sleep(100 * time.Millisecond)
	if names, _ := rec.snapshot(); len(names) != 0 {
		t.Fatalf("a watcher that had ended reported: %v", names)
	}
	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not return for a watcher that had ended")
	}
}

// TaskPulse answers what the watcher would emit, read now: the badge asks
// once when it mounts, because the first event fired before it listened.
func TestTaskPulseReadsTheActiveBoard(t *testing.T) {
	a := newTestApp(t)
	addTestTask(t, a, "default", "one")
	addTestTask(t, a, "default", "two")
	got := a.TaskPulse()
	if got["profile_id"] != "default" || got["rev"] != int64(2) || got["inbox"] != 2 || got["review"] != 0 {
		t.Fatalf("TaskPulse = %+v", got)
	}
	if _, err := a.db.Exec(`INSERT INTO profiles (id, name) VALUES ('work', 'Work')`); err != nil {
		t.Fatal(err)
	}
	a.setActiveProfileID("work")
	if got := a.TaskPulse(); got["profile_id"] != "work" || got["inbox"] != 0 {
		t.Fatalf("for the active profile, which has no task: %+v", got)
	}
	// A profile that is not there has no board: {}, as for any failed read, not
	// revision 0 with no cards, which the badge would take for a real, empty
	// board (Rev and Counts do not check the profile; Profile does).
	a.setActiveProfileID("nobody")
	if got := a.TaskPulse(); got == nil || len(got) != 0 {
		t.Fatalf("for a profile that does not exist: %#v, want an empty map", got)
	}
	a.db = nil
	if got := a.TaskPulse(); got == nil || len(got) != 0 {
		t.Fatalf("without a database: %#v, want an empty map", got)
	}
}

// restartTaskWatcher watches the active profile (a switch must move the
// badge and the board to the new board) and stopTaskWatcher ends it.
func TestRestartAndStopTaskWatcher(t *testing.T) {
	a := newTestApp(t)
	a.restartTaskWatcher()
	if a.taskWatchStop == nil || a.taskWatchProfile != "default" {
		t.Fatalf("after a restart: stop set %v, profile %q", a.taskWatchStop != nil, a.taskWatchProfile)
	}
	a.setActiveProfileID("work")
	a.restartTaskWatcher()
	if a.taskWatchStop == nil || a.taskWatchProfile != "work" {
		t.Fatalf("after a switch: stop set %v, profile %q", a.taskWatchStop != nil, a.taskWatchProfile)
	}
	a.stopTaskWatcher()
	if a.taskWatchStop != nil || a.taskWatchProfile != "" {
		t.Fatal("the watcher survived stopTaskWatcher")
	}
	a.db = nil
	a.restartTaskWatcher()
	if a.taskWatchStop != nil {
		t.Fatal("a watcher started without a database")
	}
}

// stop waits for a report that is being emitted: once it returns, nothing
// of the old board can reach the page.
func TestTaskWatcherStopWaitsForAReportInFlight(t *testing.T) {
	a := newTestApp(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	stop := a.startTaskWatcher("default", 10*time.Millisecond, func(string, map[string]interface{}) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	})
	<-entered // the first report is inside emit now
	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("stop returned while a report was still being emitted")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stop did not return once the report was done")
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go -C wails-app test -run 'TestTaskWatcher|TestTaskPulse|TestRestartAndStopTaskWatcher' -count=1 .`
Expected: FAIL to compile (`a.startTaskWatcher undefined`, `a.taskWatchStop undefined`).

- [ ] **Step 3: Write `wails-app/app_tasks_watch.go`**

```go
// wails-app/app_tasks_watch.go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// restartTaskWatcher stops the running task board watcher and starts one on
// the active profile's board, at startup and on SwitchProfile (shutdown
// calls stopTaskWatcher), as restartDocumentWatcher does. The watcher reads
// the board revision, a primary-key read, every two seconds, and the counts
// when it moves (spec D12); tasks:changed {profile_id, rev, inbox, review}
// feeds the sidebar badge and makes the Tasks page read the board again.
func (a *App) restartTaskWatcher() {
	a.taskWatchMu.Lock()
	defer a.taskWatchMu.Unlock()
	if a.taskWatchStop != nil {
		a.taskWatchStop()
		a.taskWatchStop = nil
	}
	a.taskWatchProfile = ""
	if a.db == nil {
		return
	}
	profileID := a.getActiveProfileID()
	a.taskWatchStop = a.startTaskWatcher(profileID, 0, a.emitFolderEvent)
	a.taskWatchProfile = profileID
}

// stopTaskWatcher ends the watcher for good (shutdown).
func (a *App) stopTaskWatcher() {
	a.taskWatchMu.Lock()
	defer a.taskWatchMu.Unlock()
	if a.taskWatchStop != nil {
		a.taskWatchStop()
		a.taskWatchStop = nil
	}
	a.taskWatchProfile = ""
}

// startTaskWatcher watches profileID's board revision (interval <= 0 is the
// store's two seconds) and emits tasks:changed at the start and at each move.
// stop returns only once the watcher's goroutine has ended, and a report
// that races the stop is dropped, so no event of the old profile follows a
// restart. Watch returns an error only for a profile that is not there (it
// never was, or it was deleted while watched): the watcher then writes one
// warning to the app's log and ends. It does not start over, since the
// profile would still be missing; the next SwitchProfile or start begins a
// new one.
func (a *App) startTaskWatcher(profileID string, interval time.Duration, emit folderEventFunc) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	store := tasks.NewStore(a.db)
	go func() {
		defer close(done)
		err := store.Watch(ctx, profileID, interval, func(c tasks.Change) {
			if ctx.Err() != nil {
				return
			}
			emit("tasks:changed", taskPulseData(profileID, c))
		})
		if err != nil {
			a.emitLog("SYSTEM", "WARN", fmt.Sprintf("profile %s: task board watcher stopped: %v", profileID, err))
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// taskPulseData is the tasks:changed payload and TaskPulse's answer.
func taskPulseData(profileID string, c tasks.Change) map[string]interface{} {
	return map[string]interface{}{
		"profile_id": profileID,
		"rev":        c.Rev,
		"inbox":      c.Counts.Inbox,
		"review":     c.Counts.Review,
	}
}

// TaskPulse is the active profile's board revision with its Inbox and Review
// counts, read now in process the way the watcher reads them. The sidebar
// asks once when it mounts: the watcher's first tasks:changed fires during
// startup, before any page listens. {} without a database, for a profile
// that is not there, or on a failed read.
func (a *App) TaskPulse() map[string]interface{} {
	if a.db == nil {
		return map[string]interface{}{}
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	profileID := a.getActiveProfileID()
	store := tasks.NewStore(a.db)
	// Rev and Counts do not check the profile: an unknown one reads revision 0
	// and no cards, a board that never existed. Resolve it first, as Watch does.
	if _, err := store.Profile(ctx, profileID); err != nil {
		return map[string]interface{}{}
	}
	rev, err := store.Rev(ctx, profileID)
	if err != nil {
		return map[string]interface{}{}
	}
	counts, err := store.Counts(ctx, profileID)
	if err != nil {
		return map[string]interface{}{}
	}
	return taskPulseData(profileID, tasks.Change{Rev: rev, Counts: counts})
}
```

- [ ] **Step 4: Wire it into the App**

In `wails-app/app.go`, in the `App` struct, after the two image watcher lines:

```go
	imgWatchMu sync.Mutex
	imgWatcher *imagescan.Watcher // polls the active profile's whole folder for images; see restartImageWatcher
```
add:
```go

	taskWatchMu      sync.Mutex
	taskWatchStop    func() // stops the active profile's task board watcher; see restartTaskWatcher
	taskWatchProfile string // the profile that watcher watches
```

In `startup`, change
```go
	a.restartOrgWatcher()
	a.restartDocumentWatcher()
	a.restartImageWatcher()

	a.emitLog("SYSTEM", "INFO", "Mono Agent UI connected to "+a.dbPath)
```
to
```go
	a.restartOrgWatcher()
	a.restartDocumentWatcher()
	a.restartImageWatcher()
	a.restartTaskWatcher()

	a.emitLog("SYSTEM", "INFO", "Mono Agent UI connected to "+a.dbPath)
```

In `shutdown`, change
```go
	a.imgWatchMu.Unlock()

	a.stopRunningCmds()
```
to
```go
	a.imgWatchMu.Unlock()

	a.stopTaskWatcher()

	a.stopRunningCmds()
```

In `wails-app/app_profiles.go`, in `SwitchProfile`, change
```go
	a.restartOrgWatcher()
	a.restartDocumentWatcher()
	a.restartImageWatcher()
	a.emitLog("SYSTEM", "INFO", "Switched to profile: "+switched)
```
to
```go
	a.restartOrgWatcher()
	a.restartDocumentWatcher()
	a.restartImageWatcher()
	a.restartTaskWatcher()
	a.emitLog("SYSTEM", "INFO", "Switched to profile: "+switched)
```
`MoveProfileFolder` restarts the folder watchers only; the board does not depend on the folder, so leave it.

- [ ] **Step 5: Run the tests to see them pass**

Run: `gofmt -l wails-app/*.go` (not the whole folder: `frontend/node_modules` holds Go files of its own) then `go -C wails-app vet .` then `go -C wails-app test -run 'TestTaskWatcher|TestTaskPulse|TestRestartAndStopTaskWatcher|TestDocumentWatcher' -count=1 .`
Expected: nothing from `gofmt` (if it lists `app.go`, run `gofmt -w` on it: the struct block realigns), vet clean, PASS (seven new tests and the three document watcher tests).
Then, because the watcher's goroutine and its stop are this phase's only concurrency (spec §14 asks for `-race` on what a phase touches): `go -C wails-app test -race -run 'TestTask|TestRestartAndStopTaskWatcher' -count=1 .` — expected: PASS with no race report.

- [ ] **Step 6: Check that the rules are pinned**

Each change must make the named test FAIL, then undo it:
1. In `startTaskWatcher`'s stop function, delete `<-done` → `TestTaskWatcherStopWaitsForAReportInFlight`.
2. In `restartTaskWatcher`, `profileID := a.getActiveProfileID()` becomes `profileID := "default"` → `TestRestartAndStopTaskWatcher`.
3. In `TaskPulse`, delete the `store.Profile` check → `TestTaskPulseReadsTheActiveBoard` (a profile that is not there answers a zero board).
4. In `startTaskWatcher`'s goroutine, `if err != nil` becomes `if err == nil` → `TestTaskWatcherEndsWithAWarningForAProfileThatDoesNotExist` (no warning reaches the log).

No test reaches the three call sites (`startup`, `SwitchProfile`, `shutdown`): the reviewer checks those lines by reading. Without the `SwitchProfile` one, the badge and the board stop following the board after a profile switch.

- [ ] **Step 7: Commit**

```
git add wails-app/app_tasks_watch.go wails-app/app_tasks_watch_test.go wails-app/app.go wails-app/app_profiles.go
```
then
```
git commit -m "feat(tasks): the app watches the board revision and emits tasks:changed" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

