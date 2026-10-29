package chat

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/storage"
)

func touchFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool { _, err := os.Lstat(path); return err == nil }

// recordStarted records a finished turn of the conversation with the given attachment copies, each
// with the identity it has now (what the host records when it makes the copy).
func recordStarted(t *testing.T, r *accessRig, convID, turnID string, copies ...string) {
	t.Helper()
	atts := make([]chatevents.TurnAttachment, len(copies))
	for i, p := range copies {
		atts[i] = chatevents.TurnAttachment{Name: filepath.Base(p), Path: p}
		if fi, err := os.Lstat(p); err == nil {
			atts[i].Dev, atts[i].Ino, _ = fileIdentity(fi)
		}
	}
	recordStartedWith(t, r, convID, turnID, atts)
}

func recordStartedWith(t *testing.T, r *accessRig, convID, turnID string, atts []chatevents.TurnAttachment) {
	t.Helper()
	if _, _, err := r.store.CreateTurn(convID, "default", turnID, "i", "go"); err != nil {
		t.Fatal(err)
	}
	started := chatevents.TurnStartedPayload{Backend: "agent", Text: "hi", Attachments: atts}
	if _, err := r.store.AppendEvent("default", convID, turnID, chatevents.EventTurnStarted, started); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.store.FinalizeTurn("default", convID, turnID, chatevents.StatusCompleted, "", nil, false); err != nil {
		t.Fatal(err)
	}
}

func needIdentity(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("no file identity on Windows: no copy is removed there")
	}
}

func cleanupRows(t *testing.T, r *accessRig) int {
	t.Helper()
	db, err := storage.NewDatabase(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM configs WHERE name LIKE ?`, cleanupPrefix+"%").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Deleting a conversation removes what its turns left outside the database: the host's log and prompt
// file of each turn, and the attachment copies the host recorded, each only while it still is the file
// that was made. A file the user had in the folder, a file named from elsewhere and another
// conversation's files stay.
func TestDeleteRemovesTheFilesItsTurnsLeft(t *testing.T) {
	needIdentity(t)
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	other := r.conv(t, ai.AccessFull)
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})
	logs := filepath.Join(hostDir(cfg), "turns")

	copyIn := filepath.Join(c.Cwd, ".monoagent", "attachments", "t1", "report.txt")
	userFile := filepath.Join(c.Cwd, "notes.txt")
	outside := filepath.Join(t.TempDir(), "elsewhere.txt")
	for _, p := range []string{copyIn, userFile, outside, filepath.Join(logs, "t1.log"), filepath.Join(logs, "t1.prompt"), filepath.Join(logs, "o1.log")} {
		touchFile(t, p)
	}
	recordStarted(t, r, c.ID, "t1", copyIn, userFile, outside)
	if _, _, err := r.store.CreateTurn(other.ID, "default", "o1", "i", "go"); err != nil {
		t.Fatal(err)
	}

	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	for _, p := range []string{copyIn, filepath.Dir(copyIn), filepath.Join(logs, "t1.log"), filepath.Join(logs, "t1.prompt")} {
		if exists(p) {
			t.Errorf("%s survived the deletion", p)
		}
	}
	for _, p := range []string{userFile, outside, filepath.Join(logs, "o1.log")} {
		if !exists(p) {
			t.Errorf("%s was removed", p)
		}
	}
	if n := cleanupRows(t, r); n != 0 {
		t.Errorf("%d cleanup row(s) left after the cleanup ran", n)
	}
}

// A copy is removed only while it is the file that was made: the same name holding another file
// (replaced since) is not the host's copy and stays.
func TestDeleteKeepsACopyThatIsNotTheRecordedFile(t *testing.T) {
	needIdentity(t)
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	copyIn := filepath.Join(c.Cwd, ".monoagent", "attachments", "t1", "report.txt")
	touchFile(t, copyIn)
	recordStarted(t, r, c.ID, "t1", copyIn)
	// A new file is made while the old one still exists (so it cannot get the same inode) and takes its name.
	repl := copyIn + ".new"
	touchFile(t, repl)
	if err := os.Rename(repl, copyIn); err != nil {
		t.Fatal(err)
	}

	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if !exists(copyIn) {
		t.Error("a file that is not the recorded copy was removed")
	}
}

// A copy recorded before its identity was (no device and inode) is never removed.
func TestDeleteLeavesACopyWithoutARecordedIdentity(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	copyIn := filepath.Join(c.Cwd, ".monoagent", "attachments", "t1", "report.txt")
	touchFile(t, copyIn)
	recordStartedWith(t, r, c.ID, "t1", []chatevents.TurnAttachment{{Name: "report.txt", Path: copyIn}})

	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if !exists(copyIn) {
		t.Error("a copy with no recorded identity was removed")
	}
}

// An id given to a new turn after the delete keeps its log: a log written after the delete is not the
// deleted turn's.
func TestDeleteKeepsALogWrittenAfterTheDelete(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})
	log := filepath.Join(hostDir(cfg), "turns", "t1.log")
	touchFile(t, log)
	recordStarted(t, r, c.ID, "t1")
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(log, future, future); err != nil {
		t.Fatal(err)
	}

	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if !exists(log) {
		t.Error("a log written after the delete was removed")
	}
}

// The conversation's folder swapped for a link to somewhere else is not trusted for what it holds:
// a same-named file there is not the recorded copy (another inode) and stays.
func TestDeleteDoesNotRemoveFromAWorkspaceSwappedForALink(t *testing.T) {
	needIdentity(t)
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	copyIn := filepath.Join(c.Cwd, ".monoagent", "attachments", "t", "victim")
	touchFile(t, copyIn)
	recordStarted(t, r, c.ID, "t1", copyIn)

	outsideDir := t.TempDir()
	victim := filepath.Join(outsideDir, ".monoagent", "attachments", "t", "victim")
	touchFile(t, victim)
	if err := os.Rename(c.Cwd, c.Cwd+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, c.Cwd); err != nil {
		t.Skip("no symlinks: " + err.Error())
	}

	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if !exists(victim) {
		t.Error("a file outside the recorded workspace, reached through a link in its place, was removed")
	}
}

// A folder swapped for a link, or for another real folder, between being inspected and being opened
// is refused: the one opened must be the one inspected.
func TestDeleteSurvivesAFolderBeingSwapped(t *testing.T) {
	needIdentity(t)
	for _, swap := range []string{"link", "real folder"} {
		t.Run(swap, func(t *testing.T) {
			r := newAccessRig(t, true)
			c := r.conv(t, ai.AccessFull)
			attach := filepath.Join(c.Cwd, ".monoagent", "attachments")
			copyIn := filepath.Join(attach, "t1", "report.txt")
			touchFile(t, copyIn)
			recordStarted(t, r, c.ID, "t1", copyIn)
			victimDir := t.TempDir()
			victim := filepath.Join(victimDir, "t1", "report.txt")
			touchFile(t, victim)

			swapped := false
			afterLstat = func(path string) {
				if path != attach || swapped {
					return
				}
				swapped = true
				if err := os.Rename(attach, attach+".real"); err != nil {
					t.Error(err)
					return
				}
				if swap == "link" {
					if err := os.Symlink(victimDir, attach); err != nil {
						t.Error(err)
					}
				} else {
					touchFile(t, copyIn)
				}
			}
			t.Cleanup(func() { afterLstat = func(string) {} })

			if out, err := r.history(t, nil, "delete", c.ID); err != nil {
				t.Fatalf("delete: %v\n%s", err, out)
			}
			if !swapped {
				t.Fatal("the seam never ran")
			}
			if !exists(victim) {
				t.Error("a file outside the folder that was inspected was removed")
			}
			if swap == "real folder" && !exists(copyIn) {
				t.Error("a file in a folder that was not the one inspected was removed")
			}
		})
	}
}

// The turns folder swapped for a link between being inspected and being opened is refused too.
func TestDeleteSurvivesTheTurnsFolderBeingSwappedForALink(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})
	logs := filepath.Join(hostDir(cfg), "turns")
	touchFile(t, filepath.Join(logs, "t1.log"))
	victimDir := t.TempDir()
	victim := filepath.Join(victimDir, "t1.log")
	touchFile(t, victim)
	recordStarted(t, r, c.ID, "t1")

	swapped := false
	afterLstat = func(path string) {
		if path != logs || swapped {
			return
		}
		swapped = true
		if err := os.Rename(logs, logs+".real"); err != nil {
			t.Error(err)
			return
		}
		if err := os.Symlink(victimDir, logs); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterLstat = func(string) {} })

	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if !swapped {
		t.Fatal("the seam never ran")
	}
	if !exists(victim) {
		t.Error("a log outside the turns folder, reached through a swapped folder, was removed")
	}
}

// A delete that does not commit leaves no cleanup row and touches no file: here a turn is still active.
func TestARefusedDeleteListsAndRemovesNothing(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})
	log := filepath.Join(hostDir(cfg), "turns", "t1.log")
	touchFile(t, log)
	recordStarted(t, r, c.ID, "t1")
	if _, _, err := r.store.CreateTurn(c.ID, "default", "live-1", "i", "go"); err != nil {
		t.Fatal(err)
	}

	if _, err := r.history(t, nil, "delete", c.ID); exitCodeFor(err) != 3 {
		t.Fatalf("delete with an active turn: exit %d (%v), want 3", exitCodeFor(err), err)
	}
	if !exists(log) {
		t.Error("a refused delete removed a log")
	}
	if n := cleanupRows(t, r); n != 0 {
		t.Errorf("a refused delete left %d cleanup row(s)", n)
	}
}

// If the process dies after the delete committed and before the cleanup ran, the list is still there
// and `chat history reconcile` (run at app startup) finishes the job.
func TestReconcileFinishesACleanupACrashLeft(t *testing.T) {
	needIdentity(t)
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})
	log := filepath.Join(hostDir(cfg), "turns", "t1.log")
	copyIn := filepath.Join(c.Cwd, ".monoagent", "attachments", "t1", "a.txt")
	touchFile(t, log)
	touchFile(t, copyIn)
	recordStarted(t, r, c.ID, "t1", copyIn)

	skipFileCleanup = true
	t.Cleanup(func() { skipFileCleanup = false })
	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	skipFileCleanup = false
	if !exists(log) || !exists(copyIn) || cleanupRows(t, r) != 1 {
		t.Fatalf("after the simulated crash: log %v, copy %v, rows %d; want the files and one row", exists(log), exists(copyIn), cleanupRows(t, r))
	}

	if out, err := r.history(t, nil, "reconcile"); err != nil {
		t.Fatalf("reconcile: %v\n%s", err, out)
	}
	if exists(log) || exists(copyIn) {
		t.Errorf("reconcile left a file: log %v, copy %v", exists(log), exists(copyIn))
	}
	if n := cleanupRows(t, r); n != 0 {
		t.Errorf("reconcile left %d cleanup row(s)", n)
	}
}

// The host records the identity of a copy when it makes it.
func TestCopyAttachmentsRecordsTheCopysIdentity(t *testing.T) {
	needIdentity(t)
	src := filepath.Join(t.TempDir(), "in.txt")
	touchFile(t, src)
	cwd := t.TempDir()
	atts, err := copyAttachments(cwd, "t1", []string{src})
	if err != nil || len(atts) != 1 {
		t.Fatalf("%v %v", atts, err)
	}
	fi, err := os.Lstat(atts[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	dev, ino, _ := fileIdentity(fi)
	if atts[0].Dev != dev || atts[0].Ino != ino || ino == 0 {
		t.Errorf("recorded %d/%d, the copy is %d/%d", atts[0].Dev, atts[0].Ino, dev, ino)
	}
}

// A turn id belongs to one conversation: it is the primary key of the turns table, and asking for it
// under another conversation returns the existing turn instead of making a second. A turn's log file
// (named by the id) is therefore never another conversation's.
func TestATurnIDBelongsToOneConversation(t *testing.T) {
	r := newAccessRig(t, true)
	a := r.conv(t, ai.AccessFull)
	b := r.conv(t, ai.AccessFull)
	if _, _, err := r.store.CreateTurn(a.ID, "default", "same", "i", "go"); err != nil {
		t.Fatal(err)
	}
	got, existed, err := r.store.CreateTurn(b.ID, "default", "same", "i", "go")
	if err != nil || !existed || got.ConversationID != a.ID {
		t.Fatalf("the second request for the id: %+v existed=%v err=%v; want the first conversation's turn", got, existed, err)
	}
	if turns, _, _ := r.store.ListTurns(b.ID, "default", "", 10); len(turns) != 0 {
		t.Errorf("the second conversation has %d turn(s) with that id", len(turns))
	}
}

// A swapped-in folder that holds a HARD LINK to the recorded file passes the identity check (it is the
// recorded inode); only the check that the folder opened is the folder inspected refuses it.
func TestDeleteRefusesAFolderSwappedForOneHoldingAHardLink(t *testing.T) {
	needIdentity(t)
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	attach := filepath.Join(c.Cwd, ".monoagent", "attachments")
	copyIn := filepath.Join(attach, "t1", "report.txt")
	touchFile(t, copyIn)
	recordStarted(t, r, c.ID, "t1", copyIn)

	swapped := false
	afterLstat = func(path string) {
		if path != attach || swapped {
			return
		}
		swapped = true
		if err := os.Rename(attach, attach+".real"); err != nil {
			t.Error(err)
			return
		}
		if err := os.MkdirAll(filepath.Join(attach, "t1"), 0o700); err != nil {
			t.Error(err)
			return
		}
		if err := os.Link(filepath.Join(attach+".real", "t1", "report.txt"), copyIn); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { afterLstat = func(string) {} })

	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if !swapped {
		t.Fatal("the seam never ran")
	}
	if !exists(copyIn) {
		t.Error("a file in a folder that was not the one inspected was removed (it passes the identity check, not the folder check)")
	}
}

// The row is not trusted: a log folder, a log name or a time written into it by anyone else removes
// nothing outside the host's own turns folder, and nothing at all when the time cannot be read.
func TestACleanupRowIsNotTrusted(t *testing.T) {
	r := newAccessRig(t, true)
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})
	logDir := filepath.Join(hostDir(cfg), "turns")
	hostLog := filepath.Join(logDir, "t9.log")
	touchFile(t, hostLog)
	foreign := filepath.Join(t.TempDir(), "keep.log")
	touchFile(t, foreign)
	warn := func(string, ...any) {}

	// Names that are not the host's are not removed, whatever folder the row would name.
	in := cleanupIntent{DeletedAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), Logs: []string{"../" + filepath.Base(foreign), "/etc/passwd", "t9.log"}}
	if err := in.run(logDir, warn); err != nil {
		t.Fatal(err)
	}
	if !exists(foreign) {
		t.Error("a name leading out of the turns folder was removed")
	}
	if exists(hostLog) {
		t.Error("the host's own log was not removed")
	}

	// A time that cannot be read removes no log.
	touchFile(t, hostLog)
	bad := cleanupIntent{DeletedAt: "not a time", Logs: []string{"t9.log"}}
	_ = bad.run(logDir, warn)
	if !exists(hostLog) {
		t.Error("a log was removed on a row whose time cannot be read")
	}
	// A folder that is not absolute is not opened.
	rel := cleanupIntent{DeletedAt: in.DeletedAt, Cwd: "relative/dir", Copies: []copyRef{{Rel: "a", Dev: 1, Ino: 1}}}
	if err := rel.run(logDir, warn); err != nil {
		t.Errorf("a relative folder was opened: %v", err)
	}
}

// A row whose files could not all be dealt with is kept for another try; a good one is removed.
func TestACleanupRowThatFailedIsKept(t *testing.T) {
	r := newAccessRig(t, true)
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})
	logDir := filepath.Join(hostDir(cfg), "turns")
	db, err := storage.NewDatabase(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// A conversation folder that exists but cannot be entered is a failure worth retrying.
	cwd := t.TempDir()
	if err := os.Chmod(cwd, 0); err != nil {
		t.Skip("cannot remove permissions: " + err.Error())
	}
	t.Cleanup(func() { os.Chmod(cwd, 0o700) })
	if os.Geteuid() == 0 {
		t.Skip("root enters any folder")
	}
	in := cleanupIntent{DeletedAt: time.Now().UTC().Format(time.RFC3339Nano), Cwd: cwd, Copies: []copyRef{{Rel: "a", Dev: 1, Ino: 1}}}
	raw, _ := json.Marshal(in)
	name := cleanupPrefix + "default/c-kept"
	if _, err := db.DB.Exec(`INSERT INTO configs (name, config_data) VALUES (?, ?)`, name, string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := runCleanups(context.Background(), db.DB, logDir, name, false, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM configs WHERE name = ?`, name).Scan(&n); err != nil || n != 1 {
		t.Errorf("the row of a cleanup that failed: %d, %v; want it kept", n, err)
	}
}

// Running the cleanup of one conversation does not run another whose id only starts the same.
func TestACleanupRunsOnlyItsOwnRow(t *testing.T) {
	needIdentity(t)
	r := newAccessRig(t, true)
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})
	logDir := filepath.Join(hostDir(cfg), "turns")
	touchFile(t, filepath.Join(logDir, "t1.log"))
	db, err := storage.NewDatabase(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	in := cleanupIntent{DeletedAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), Logs: []string{"t1.log"}}
	raw, _ := json.Marshal(in)
	if _, err := db.DB.Exec(`INSERT INTO configs (name, config_data) VALUES (?, ?)`, cleanupName("default", "c10"), string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := runCleanups(context.Background(), db.DB, logDir, cleanupName("default", "c1"), false, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(logDir, "t1.log")) {
		t.Error("another conversation's cleanup row was run")
	}
}
