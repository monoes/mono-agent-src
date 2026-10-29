package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/secrets"
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

// An agent's delete of a conversation (one with no mission and nothing left running) still lists and
// removes its files while the storage write guard refuses every non-operator write of a reserved row:
// the delete writes its row on its own transaction's connection, not through the guarded store path,
// and nothing else can write that row (file-cleanup/ is a reserved config prefix).
func TestAnAgentsDeleteStillCleansUpFilesUnderTheWriteGuard(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})
	log := filepath.Join(hostDir(cfg), "turns", "t1.log")
	touchFile(t, log)
	recordStarted(t, r, c.ID, "t1")

	old := storage.ConfigWriteGuard
	storage.ConfigWriteGuard = func(name string) error { return errors.New("a non-operator may not write " + name) }
	t.Cleanup(func() { storage.ConfigWriteGuard = old })

	// The guarded store path refuses the reserved row...
	db, err := storage.NewDatabase(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveConfig(cleanupName("default", c.ID, "x"), "{}"); err == nil {
		t.Error("the guarded store path wrote a cleanup row for a non-operator")
	}
	db.Close()
	// ...and the delete does its work all the same.
	if out, err := r.history(t, errNotOperator, "delete", c.ID); err != nil {
		t.Fatalf("an agent's delete: %v\n%s", err, out)
	}
	if exists(log) {
		t.Error("the log of the deleted conversation survived")
	}
	if n := cleanupRows(t, r); n != 0 {
		t.Errorf("%d cleanup row(s) left", n)
	}
}

// signedRow is a cleanup row as the delete writes it: the intent and its MAC under the profile's key.
func signedRow(t *testing.T, r *accessRig, profile, conv string, in cleanupIntent) (name, data string) {
	t.Helper()
	db, err := storage.NewDatabase(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	key, err := secrets.DeriveKey(context.Background(), db.DB, profile, cleanupKeyPurpose)
	if err != nil {
		t.Fatal(err)
	}
	if in.Nonce == "" {
		in.Nonce = "n-" + conv
	}
	name = cleanupName(profile, conv, in.Nonce)
	intent, _ := json.Marshal(in)
	raw, _ := json.Marshal(envelope{Intent: intent, Mac: cleanupMac(key, name, intent)})
	return name, string(raw)
}

func putRow(t *testing.T, r *accessRig, name, data string) {
	t.Helper()
	db, err := storage.NewDatabase(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(`INSERT OR REPLACE INTO configs (name, config_data) VALUES (?, ?)`, name, data); err != nil {
		t.Fatal(err)
	}
}

func rowData(t *testing.T, r *accessRig, name string) (string, bool) {
	t.Helper()
	db, err := storage.NewDatabase(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var data string
	err = db.DB.QueryRow(`SELECT config_data FROM configs WHERE name = ?`, name).Scan(&data)
	return data, err == nil
}

func sweep(t *testing.T, r *accessRig, name string, prefix bool) {
	t.Helper()
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})
	db, err := storage.NewDatabase(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := runCleanups(context.Background(), db.DB, filepath.Join(hostDir(cfg), "turns"), name, prefix, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
}

func fileRefOf(t *testing.T, path string) fileRef {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	dev, ino, _ := fileIdentity(fi)
	return fileRef{Rel: filepath.Base(path), Dev: dev, Ino: ino}
}

// A log is removed only while it is the recorded file: one given a new inode after the inventory (a
// new turn that was given the id) stays.
func TestDeleteKeepsALogThatIsNotTheRecordedFile(t *testing.T) {
	needIdentity(t)
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})
	log := filepath.Join(hostDir(cfg), "turns", "t1.log")
	touchFile(t, log)
	recordStarted(t, r, c.ID, "t1")
	beforeGuardedDelete = func() { // after the identity was read, before the delete commits
		repl := log + ".new"
		touchFile(t, repl)
		if err := os.Rename(repl, log); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { beforeGuardedDelete = func() {} })

	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if !exists(log) {
		t.Error("a log that is not the recorded file was removed")
	}
	if entries, _ := os.ReadDir(filepath.Dir(log)); len(entries) != 1 {
		t.Errorf("the turns folder holds %d entries, want just the log", len(entries))
	}
}

// What the row names is checked again at run time: only the host's own log names are removed (a name
// that passes the folder handle but is not a log, or lies in a sub-folder, stays), a row with a bad
// signature, an unsigned one, or one whose conversation still exists is dropped and removes nothing.
func TestACleanupRowIsNotTrusted(t *testing.T) {
	needIdentity(t)
	r := newAccessRig(t, true)
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})
	logDir := filepath.Join(hostDir(cfg), "turns")
	warn := func(string, ...any) {}
	notes := filepath.Join(logDir, "notes.txt")
	nested := filepath.Join(logDir, "sub", "x.log")
	hostLog := filepath.Join(logDir, "t9.log")
	for _, p := range []string{notes, nested, hostLog} {
		touchFile(t, p)
	}
	// Real identities, so only the name check can keep these two.
	in := cleanupIntent{Logs: []fileRef{fileRefOf(t, notes), {Rel: "sub/x.log", Dev: fileRefOf(t, nested).Dev, Ino: fileRefOf(t, nested).Ino}, fileRefOf(t, hostLog)}}
	if err := in.run(logDir, warn); err != nil {
		t.Fatal(err)
	}
	if !exists(notes) || !exists(nested) {
		t.Errorf("a name that is not a host log was removed: notes %v, nested %v", exists(notes), exists(nested))
	}
	if exists(hostLog) {
		t.Error("the host's own log was not removed")
	}

	// A row that is not what the delete wrote, or whose conversation still exists, is dropped.
	c := r.conv(t, ai.AccessFull)
	touchFile(t, hostLog)
	ref := fileRefOf(t, hostLog)
	good := cleanupIntent{Logs: []fileRef{ref}}
	name, data := signedRow(t, r, "default", "gone-conv", good)
	var env envelope
	_ = json.Unmarshal([]byte(data), &env)
	env.Mac = "00" + env.Mac[2:]
	badMac, _ := json.Marshal(env)
	forged, _ := json.Marshal(good)
	liveName, liveData := signedRow(t, r, "default", c.ID, good)
	for label, row := range map[string][2]string{
		"bad signature":     {name, string(badMac)},
		"unsigned JSON":     {name, string(forged)},
		"live conversation": {liveName, liveData},
	} {
		putRow(t, r, row[0], row[1])
		sweep(t, r, row[0], false)
		if !exists(hostLog) {
			t.Errorf("%s: a log was removed", label)
		}
		if _, still := rowData(t, r, row[0]); still {
			t.Errorf("%s: the row was kept", label)
		}
	}
	// And a valid row for a conversation that is gone does remove it.
	putRow(t, r, name, data)
	sweep(t, r, name, false)
	if exists(hostLog) {
		t.Error("a valid row did not remove the log")
	}
}

// A row whose files could not all be dealt with is kept for another try; a good one is removed.
func TestACleanupRowThatFailedIsKept(t *testing.T) {
	r := newAccessRig(t, true)
	if os.Geteuid() == 0 {
		t.Skip("root enters any folder")
	}
	cwd := t.TempDir()
	if err := os.Chmod(cwd, 0); err != nil {
		t.Skip("cannot remove permissions: " + err.Error())
	}
	t.Cleanup(func() { os.Chmod(cwd, 0o700) })
	in := cleanupIntent{DeletedAt: time.Now().UTC().Format(time.RFC3339Nano), Cwd: cwd, Copies: []fileRef{{Rel: "a", Dev: 1, Ino: 1}}}
	name, data := signedRow(t, r, "default", "c-kept", in)
	putRow(t, r, name, data)
	sweep(t, r, name, false)
	if _, still := rowData(t, r, name); !still {
		t.Error("the row of a cleanup that failed was dropped")
	}
}

// Running the cleanup of one conversation does not run another whose id only starts the same.
func TestACleanupRunsOnlyItsOwnRow(t *testing.T) {
	needIdentity(t)
	r := newAccessRig(t, true)
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})
	log := filepath.Join(hostDir(cfg), "turns", "t1.log")
	touchFile(t, log)
	name, data := signedRow(t, r, "default", "c10", cleanupIntent{Logs: []fileRef{fileRefOf(t, log)}})
	putRow(t, r, name, data)
	sweep(t, r, cleanupConvPrefix("default", "c1"), true)
	if !exists(log) {
		t.Error("another conversation's cleanup row was run")
	}
}

// A newer row written for the same conversation while a sweep is running survives that sweep: the row
// is removed by its name AND the data that was read.
func TestASweepDoesNotRemoveANewerRow(t *testing.T) {
	needIdentity(t)
	r := newAccessRig(t, true)
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})
	log := filepath.Join(hostDir(cfg), "turns", "t1.log")
	touchFile(t, log)
	name, data := signedRow(t, r, "default", "c-new", cleanupIntent{Nonce: "old", Logs: []fileRef{fileRefOf(t, log)}})
	putRow(t, r, name, data)
	_, newer := signedRow(t, r, "default", "c-new", cleanupIntent{Nonce: "old", DeletedAt: "later"}) // the same row name, other data
	leafHook = func(stage string, _ *os.Root, _ string) {
		if stage == "before" {
			putRow(t, r, name, newer)
		}
	}
	t.Cleanup(func() { leafHook = func(string, *os.Root, string) {} })
	sweep(t, r, name, false)
	if got, ok := rowData(t, r, name); !ok || got != newer {
		t.Errorf("the newer row was removed by the sweep that read the older one (present=%v)", ok)
	}
}

// The file is moved to a name nobody else knows and checked THERE: a replacement between looking and
// removing is moved instead of the recorded file, found not to be it, and put back; nothing unrecorded
// is unlinked.
func TestARecordedFileSwappedJustBeforeItIsRemovedStays(t *testing.T) {
	needIdentity(t)
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	copyIn := filepath.Join(c.Cwd, ".monoagent", "attachments", "t1", "report.txt")
	touchFile(t, copyIn)
	recordStarted(t, r, c.ID, "t1", copyIn)
	leafHook = func(stage string, root *os.Root, rel string) {
		if stage != "before" {
			return
		}
		// The recorded file is replaced by another one (made while it exists, so another inode).
		if err := os.WriteFile(copyIn+".x", []byte("stranger"), 0o600); err != nil {
			t.Error(err)
			return
		}
		if err := os.Rename(copyIn+".x", copyIn); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { leafHook = func(string, *os.Root, string) {} })

	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	b, err := os.ReadFile(copyIn)
	if err != nil || string(b) != "stranger" {
		t.Errorf("the replacement was removed or changed: %q %v", b, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(copyIn))
	if len(entries) != 1 {
		t.Errorf("the folder holds %d entries, want just the replacement (a tombstone was left)", len(entries))
	}
}

// A replacement made after the recorded file was moved aside takes the original name; the recorded
// file is still the one unlinked, and the newcomer is left alone.
func TestANameTakenAfterTheMoveKeepsItsNewFile(t *testing.T) {
	needIdentity(t)
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	copyIn := filepath.Join(c.Cwd, ".monoagent", "attachments", "t1", "report.txt")
	touchFile(t, copyIn)
	recordStarted(t, r, c.ID, "t1", copyIn)
	leafHook = func(stage string, root *os.Root, rel string) {
		if stage == "moved" {
			if err := os.WriteFile(copyIn, []byte("newcomer"), 0o600); err != nil {
				t.Error(err)
			}
		}
	}
	t.Cleanup(func() { leafHook = func(string, *os.Root, string) {} })

	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	b, err := os.ReadFile(copyIn)
	if err != nil || string(b) != "newcomer" {
		t.Errorf("the file that took the name afterwards was removed: %q %v", b, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(copyIn)); len(entries) != 1 {
		t.Errorf("the folder holds %d entries, want just the newcomer", len(entries))
	}
}

// F1: a failure to get the vault's key is the machine's, not the row's: the row is kept (and tried again at
// the next reconcile) instead of being dropped with its files left for good; a bad signature still drops it.
func TestACleanupRowIsKeptWhenTheKeyCannotBeHad(t *testing.T) {
	needIdentity(t)
	r := newAccessRig(t, true)
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})
	log := filepath.Join(hostDir(cfg), "turns", "t1.log")
	touchFile(t, log)
	name, data := signedRow(t, r, "default", "c-key", cleanupIntent{Logs: []fileRef{fileRefOf(t, log)}})
	putRow(t, r, name, data)

	old := deriveKey
	deriveKey = func(context.Context, *sql.DB, string, string) ([]byte, error) {
		return nil, errors.New("the keychain is locked")
	}
	sweep(t, r, name, false)
	deriveKey = old
	if _, still := rowData(t, r, name); !still {
		t.Fatal("the row was dropped because the key could not be had")
	}
	if !exists(log) {
		t.Fatal("a log was removed without the row being verified")
	}
	// The key is back: the same row runs and goes.
	sweep(t, r, name, false)
	if exists(log) {
		t.Error("the row did not run once the key was back")
	}
	if _, still := rowData(t, r, name); still {
		t.Error("the row was kept after it ran")
	}
}

// F2: asking for the key does not hang the delete: it gives up after the timeout, and the files stay.
func TestAKeyThatNeverComesDoesNotHangTheCleanup(t *testing.T) {
	old, oldTimeout := deriveKey, keyTimeout
	block, finished := make(chan struct{}), make(chan struct{})
	deriveKey = func(ctx context.Context, _ *sql.DB, _ string, _ string) ([]byte, error) {
		defer close(finished)
		<-block
		return nil, nil
	}
	keyTimeout = 100 * time.Millisecond
	// The fetch that timed out is still running: let it go and wait for it to end before the shared
	// settings are put back, so nothing it does can meet the restore.
	t.Cleanup(func() { close(block); <-finished; deriveKey, keyTimeout = old, oldTimeout })
	start := time.Now()
	r := newAccessRig(t, true)
	db, err := storage.NewDatabase(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := deriveKeyWithin(context.Background(), db.DB, "default"); err == nil {
		t.Fatal("an answer came from a key that never comes")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("it took %s", d)
	}
}

// F3: a crash between the move and the unlink leaves a tombstone: the next run removes it when it still is a
// recorded file, and leaves one that is not.
func TestALeftoverTombstoneIsRemovedByTheNextRun(t *testing.T) {
	needIdentity(t)
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	dir := filepath.Join(c.Cwd, ".monoagent", "attachments", "t1")
	copyIn := filepath.Join(dir, "report.txt")
	touchFile(t, copyIn)
	ref := fileRefOf(t, copyIn)
	// The crash: the recorded file was moved to its tombstone name and nothing more happened. Another
	// file with a tombstone-like name is not the recorded one.
	left := filepath.Join(dir, tombstonePrefix+"deadbeefdeadbeef")
	if err := os.Rename(copyIn, left); err != nil {
		t.Fatal(err)
	}
	stranger := filepath.Join(dir, tombstonePrefix+"0123456789abcdef")
	touchFile(t, stranger)
	in := cleanupIntent{Cwd: c.Cwd, Copies: []fileRef{{Rel: filepath.Join("t1", "report.txt"), Dev: ref.Dev, Ino: ref.Ino}}}
	if err := in.run(filepath.Join(hostDir(testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})), "turns"), func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if exists(left) {
		t.Error("the leftover of the recorded file survived the run")
	}
	if !exists(stranger) {
		t.Error("a file that is not the recorded one was removed for its name")
	}
}

// F4: the tombstone name does not depend on the file's name: a name at the limit still works.
func TestAFileNamedAtTheLimitIsRemoved(t *testing.T) {
	needIdentity(t)
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	long := strings.Repeat("a", 250) + ".txt"
	copyIn := filepath.Join(c.Cwd, ".monoagent", "attachments", "t1", long)
	if err := os.MkdirAll(filepath.Dir(copyIn), 0o700); err != nil {
		t.Skip(err)
	}
	if err := os.WriteFile(copyIn, []byte("x"), 0o600); err != nil {
		t.Skip("the file system takes no name this long: " + err.Error())
	}
	recordStarted(t, r, c.ID, "t1", copyIn)
	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if exists(copyIn) {
		t.Error("a file with a name at the limit was not removed")
	}
}

// F5: every delete has a row of its own: a second delete (an id used again, or a crash and a retry) does not
// overwrite the first one's unrun row, and a run for the conversation runs all of them.
func TestTwoDeletesOfOneIDKeepTheirRows(t *testing.T) {
	needIdentity(t)
	r := newAccessRig(t, true)
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})
	logDir := filepath.Join(hostDir(cfg), "turns")
	a, b := filepath.Join(logDir, "a.log"), filepath.Join(logDir, "b.log")
	touchFile(t, a)
	touchFile(t, b)
	n1, d1 := signedRow(t, r, "default", "same-id", cleanupIntent{Nonce: "one", Logs: []fileRef{fileRefOf(t, a)}})
	n2, d2 := signedRow(t, r, "default", "same-id", cleanupIntent{Nonce: "two", Logs: []fileRef{fileRefOf(t, b)}})
	if n1 == n2 {
		t.Fatal("two deletes got the same row name")
	}
	putRow(t, r, n1, d1)
	putRow(t, r, n2, d2)
	if _, ok := rowData(t, r, n1); !ok {
		t.Fatal("the first row was overwritten")
	}
	sweep(t, r, cleanupConvPrefix("default", "same-id"), true)
	if exists(a) || exists(b) {
		t.Errorf("a run for the conversation left a file of one of its deletes: a %v, b %v", exists(a), exists(b))
	}
}

// F6: a file whose device is not the recorded one is not removed, even with the right inode (and the other
// way round): both halves of the identity count.
func TestBothHalvesOfTheIdentityCount(t *testing.T) {
	needIdentity(t)
	r := newAccessRig(t, true)
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})
	logDir := filepath.Join(hostDir(cfg), "turns")
	warn := func(string, ...any) {}
	for _, which := range []string{"device", "inode"} {
		log := filepath.Join(logDir, which+".log")
		touchFile(t, log)
		ref := fileRefOf(t, log)
		ref.Rel = which + ".log"
		old := statIdentity
		statIdentity = func(fi os.FileInfo) (uint64, uint64, bool) {
			dev, ino, ok := old(fi)
			if which == "device" {
				dev++ // the same inode on another device
			} else {
				ino++ // the same device, another inode
			}
			return dev, ino, ok
		}
		in := cleanupIntent{DeletedAt: time.Now().UTC().Format(time.RFC3339Nano), Logs: []fileRef{ref}}
		if err := in.run(logDir, warn); err != nil {
			t.Fatal(err)
		}
		statIdentity = old
		if !exists(log) {
			t.Errorf("a file with the recorded %s changed was removed", which)
		}
		if entries, _ := os.ReadDir(logDir); len(entries) != 1 {
			t.Errorf("%s: %d entries in the folder, want just the log (a tombstone was left)", which, len(entries))
		}
		os.Remove(log)
	}
}

// F7: only a regular file is removed: a link with the recorded identity (its own inode) is not.
func TestOnlyARegularFileIsRemoved(t *testing.T) {
	needIdentity(t)
	r := newAccessRig(t, true)
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})
	logDir := filepath.Join(hostDir(cfg), "turns")
	target := filepath.Join(t.TempDir(), "target.log")
	touchFile(t, target)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(logDir, "t1.log")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("no symlinks: " + err.Error())
	}
	ref := fileRefOf(t, link) // the link's own identity
	in := cleanupIntent{DeletedAt: time.Now().UTC().Format(time.RFC3339Nano), Logs: []fileRef{ref}}
	if err := in.run(logDir, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Error("a link with the recorded identity was removed")
	}
	if !exists(target) {
		t.Error("the link's target was removed")
	}
}

// A leftover tombstone that cannot be removed this time (its folder is not writable) is not forgotten: the
// run reports it, and the row is kept for the next try instead of being dropped as done.
func TestAFailedTombstoneCleanupKeepsTheRow(t *testing.T) {
	needIdentity(t)
	if os.Geteuid() == 0 {
		t.Skip("root writes any folder")
	}
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	dir := filepath.Join(c.Cwd, ".monoagent", "attachments", "t1")
	copyIn := filepath.Join(dir, "report.txt")
	touchFile(t, copyIn)
	ref := fileRefOf(t, copyIn)
	left := filepath.Join(dir, tombstonePrefix+"deadbeefdeadbeef")
	if err := os.Rename(copyIn, left); err != nil { // the crash: moved, not unlinked
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	in := cleanupIntent{DeletedAt: time.Now().UTC().Format(time.RFC3339Nano), Cwd: c.Cwd, Copies: []fileRef{{Rel: filepath.Join("t1", "report.txt"), Dev: ref.Dev, Ino: ref.Ino}}}
	name, data := signedRow(t, r, "default", "c-left", in)
	putRow(t, r, name, data)

	sweep(t, r, name, false)
	if _, still := rowData(t, r, name); !still {
		t.Error("the row was dropped although the leftover could not be removed")
	}
	if !exists(left) {
		t.Fatal("the leftover is gone")
	}
	// The folder is writable again: the next run removes it and the row goes.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sweep(t, r, name, false)
	if exists(left) {
		t.Error("the leftover survived the next run")
	}
	if _, still := rowData(t, r, name); still {
		t.Error("the row was kept after it was done")
	}
}

// A turn that starts and finishes after the log files were looked at, but before the delete commits, has its
// log removed too: the transaction sees a turn the scan did not, the pass is repeated, and the scan with it.
func TestALogOfATurnRecordedAfterTheScanIsRemoved(t *testing.T) {
	needIdentity(t)
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})
	logs := filepath.Join(hostDir(cfg), "turns")
	early, late := filepath.Join(logs, "t1.log"), filepath.Join(logs, "t2.log")
	touchFile(t, early)
	recordStarted(t, r, c.ID, "t1")
	calls := 0
	beforeGuardedDelete = func() { // after the scan, before the transaction
		calls++
		if calls == 1 {
			touchFile(t, late)
			recordStarted(t, r, c.ID, "t2")
		}
	}
	t.Cleanup(func() { beforeGuardedDelete = func() {} })

	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if exists(early) || exists(late) {
		t.Errorf("a log survived: t1 %v, t2 %v", exists(early), exists(late))
	}
	if n := cleanupRows(t, r); n != 0 {
		t.Errorf("%d cleanup row(s) left", n)
	}
}

// A key that cannot be had is asked for once per profile in a sweep, not once per row: N rows of a locked
// profile do not wait N times; the rows are kept.
func TestAFailedKeyIsAskedForOncePerSweep(t *testing.T) {
	r := newAccessRig(t, true)
	var names []string
	for i := 0; i < 4; i++ {
		name, data := signedRow(t, r, "default", fmt.Sprintf("c-%d", i), cleanupIntent{Nonce: fmt.Sprintf("n%d", i)})
		putRow(t, r, name, data)
		names = append(names, name)
	}
	old := deriveKey
	calls := 0
	deriveKey = func(context.Context, *sql.DB, string, string) ([]byte, error) {
		calls++
		return nil, errors.New("locked")
	}
	t.Cleanup(func() { deriveKey = old })

	sweep(t, r, cleanupPrefix, true)
	if calls != 1 {
		t.Errorf("the key was asked for %d times for rows of one profile, want 1", calls)
	}
	for _, n := range names {
		if _, still := rowData(t, r, n); !still {
			t.Errorf("row %s was dropped", n)
		}
	}
}
