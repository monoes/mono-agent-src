package chat

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
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

// Deleting a conversation removes what its turns left outside the database: the host's log and
// prompt file of each turn, and the copies of attached files made in the conversation's folder
// (only those the host recorded and that lie under <folder>/.monoagent/attachments). A file
// outside that folder, a file the user had there before, and another conversation's files stay.
func TestDeleteRemovesTheFilesItsTurnsLeft(t *testing.T) {
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
	if _, _, err := r.store.CreateTurn(c.ID, "default", "t1", "i", "go"); err != nil {
		t.Fatal(err)
	}
	started := chatevents.TurnStartedPayload{Backend: "agent", Text: "hi", Attachments: []chatevents.TurnAttachment{
		{Name: "report.txt", Path: copyIn}, {Name: "notes.txt", Path: userFile}, {Name: "elsewhere.txt", Path: outside},
	}}
	if _, err := r.store.AppendEvent("default", c.ID, "t1", chatevents.EventTurnStarted, started); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.store.FinalizeTurn("default", c.ID, "t1", chatevents.StatusCompleted, "", nil, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.store.CreateTurn(other.ID, "default", "o1", "i", "go"); err != nil {
		t.Fatal(err)
	}

	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	for _, p := range []string{copyIn, filepath.Join(logs, "t1.log"), filepath.Join(logs, "t1.prompt")} {
		if exists(p) {
			t.Errorf("%s survived the deletion", p)
		}
	}
	if exists(filepath.Dir(copyIn)) {
		t.Errorf("the empty attachments folder of the turn survived")
	}
	for _, p := range []string{userFile, outside, filepath.Join(logs, "o1.log")} {
		if !exists(p) {
			t.Errorf("%s was removed", p)
		}
	}
}

// A recorded path that leads out of the folder through a link is not followed.
func TestDeleteDoesNotFollowALinkOutOfTheAttachmentsFolder(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	victim := filepath.Join(t.TempDir(), "keep.txt")
	touchFile(t, victim)
	linkDir := filepath.Join(c.Cwd, ".monoagent", "attachments", "t1")
	if err := os.MkdirAll(filepath.Dir(linkDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(victim), linkDir); err != nil {
		t.Skip("no symlinks: " + err.Error())
	}
	if _, _, err := r.store.CreateTurn(c.ID, "default", "t1", "i", "go"); err != nil {
		t.Fatal(err)
	}
	started := chatevents.TurnStartedPayload{Backend: "agent", Text: "hi", Attachments: []chatevents.TurnAttachment{{Name: "keep.txt", Path: filepath.Join(linkDir, "keep.txt")}}}
	if _, err := r.store.AppendEvent("default", c.ID, "t1", chatevents.EventTurnStarted, started); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.store.FinalizeTurn("default", c.ID, "t1", chatevents.StatusCompleted, "", nil, false); err != nil {
		t.Fatal(err)
	}
	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if !exists(victim) {
		t.Error("a file outside the folder, reached through a link, was removed")
	}
}

// A turn id belongs to one conversation: it is the primary key of the turns table, and asking for it
// under another conversation returns the existing turn instead of making a second. A turn's log file
// (named by the id) is therefore never another conversation's, which the deletion relies on.
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

// When the attachments folder is itself a link into another folder, no copy is removed, a normal
// copy in another conversation's folder next to it still is (positive control), and nothing the link
// leads to is touched.
func TestDeleteSkipsCopiesWhenTheAttachmentsFolderIsALink(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	good := r.conv(t, ai.AccessFull)
	victimDir := t.TempDir()
	victim := filepath.Join(victimDir, "t1", "keep.txt")
	touchFile(t, victim)
	if err := os.MkdirAll(filepath.Join(c.Cwd, ".monoagent"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victimDir, filepath.Join(c.Cwd, ".monoagent", "attachments")); err != nil {
		t.Skip("no symlinks: " + err.Error())
	}
	recordCopy := func(convID, path string) {
		t.Helper()
		if _, _, err := r.store.CreateTurn(convID, "default", "turn-"+convID, "i", "go"); err != nil {
			t.Fatal(err)
		}
		started := chatevents.TurnStartedPayload{Backend: "agent", Text: "hi", Attachments: []chatevents.TurnAttachment{{Name: "f", Path: path}}}
		if _, err := r.store.AppendEvent("default", convID, "turn-"+convID, chatevents.EventTurnStarted, started); err != nil {
			t.Fatal(err)
		}
		if _, _, err := r.store.FinalizeTurn("default", convID, "turn-"+convID, chatevents.StatusCompleted, "", nil, false); err != nil {
			t.Fatal(err)
		}
	}
	recordCopy(c.ID, filepath.Join(c.Cwd, ".monoagent", "attachments", "t1", "keep.txt"))
	okCopy := filepath.Join(good.Cwd, ".monoagent", "attachments", "turn-"+good.ID, "f")
	touchFile(t, okCopy)
	recordCopy(good.ID, okCopy)

	for _, id := range []string{c.ID, good.ID} {
		if out, err := r.history(t, nil, "delete", id); err != nil {
			t.Fatalf("delete %s: %v\n%s", id, err, out)
		}
	}
	if !exists(victim) {
		t.Error("a file reached through a linked attachments folder was removed")
	}
	if exists(okCopy) {
		t.Error("a normal copy was not removed")
	}
}

// A folder swapped for a link between being inspected and being opened does not redirect the
// removal: the one opened must be the one inspected.
func TestDeleteSurvivesTheAttachmentsFolderBeingSwappedForALink(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	attach := filepath.Join(c.Cwd, ".monoagent", "attachments")
	copyIn := filepath.Join(attach, "t1", "report.txt")
	touchFile(t, copyIn)
	victimDir := t.TempDir()
	victim := filepath.Join(victimDir, "t1", "report.txt") // the same relative name, outside
	touchFile(t, victim)
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
		if err := os.Symlink(victimDir, attach); err != nil {
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
		t.Error("a file outside the conversation's folder, reached through a swapped folder, was removed")
	}
}

// The same for the host's turns folder, which holds the logs.
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

// Nothing a turn that starts after the list was made can leave is missed or mistaken: the list is
// made and the files are removed inside the delete's write transaction, so a turn cannot be admitted
// in between, and its id cannot be given to a new turn before the old files are gone.
func TestDeleteRemovesTheFilesInsideItsTransaction(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})
	log := filepath.Join(hostDir(cfg), "turns", "t1.log")
	touchFile(t, log)
	recordStarted(t, r, c.ID, "t1")
	// A turn that is admitted and finished after the delete began listing must find the conversation
	// gone or be refused: the seam runs before the transaction, so its files are in the list.
	late := filepath.Join(hostDir(cfg), "turns", "t2.log")
	touchFile(t, late)
	beforeGuardedDelete = func() { recordStarted(t, r, c.ID, "t2") }
	t.Cleanup(func() { beforeGuardedDelete = func() {} })

	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if exists(log) || exists(late) {
		t.Errorf("a turn's log survived: t1 %v, t2 %v", exists(log), exists(late))
	}
}

// recordStarted records a finished turn of the conversation with the given attached copies.
func recordStarted(t *testing.T, r *accessRig, convID, turnID string, copies ...string) {
	t.Helper()
	if _, _, err := r.store.CreateTurn(convID, "default", turnID, "i", "go"); err != nil {
		t.Fatal(err)
	}
	atts := make([]chatevents.TurnAttachment, len(copies))
	for i, p := range copies {
		atts[i] = chatevents.TurnAttachment{Name: filepath.Base(p), Path: p}
	}
	started := chatevents.TurnStartedPayload{Backend: "agent", Text: "hi", Attachments: atts}
	if _, err := r.store.AppendEvent("default", convID, turnID, chatevents.EventTurnStarted, started); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.store.FinalizeTurn("default", convID, turnID, chatevents.StatusCompleted, "", nil, false); err != nil {
		t.Fatal(err)
	}
}

// quarantine sets the files aside, finish(false) puts them back, finish(true) removes them: the
// delete can fail after the files were set aside, and then nothing is lost.
func TestQuarantinedFilesComeBackOrGoForGood(t *testing.T) {
	dir := t.TempDir()
	logs := filepath.Join(dir, "turns")
	touchFile(t, filepath.Join(logs, "t1.log"))
	cwd := t.TempDir()
	copyIn := filepath.Join(cwd, ".monoagent", "attachments", "t1", "a.txt")
	touchFile(t, copyIn)
	f := turnFiles{logDir: logs, logs: []string{"t1.log"}, cwd: cwd, attachRoot: filepath.Join(cwd, ".monoagent", "attachments"), copies: []string{copyIn}}
	warn := func(format string, a ...any) { t.Errorf("warning: "+format, a...) }

	q := f.quarantine(warn)
	if exists(filepath.Join(logs, "t1.log")) || exists(copyIn) {
		t.Fatal("the files were not set aside")
	}
	q.finish(false, warn)
	if !exists(filepath.Join(logs, "t1.log")) || !exists(copyIn) {
		t.Fatal("the files were not put back after a delete that did not commit")
	}
	q = f.quarantine(warn)
	q.finish(true, warn)
	if exists(filepath.Join(logs, "t1.log")) || exists(copyIn) || exists(filepath.Dir(copyIn)) {
		t.Error("the files, or the empty folder of the turn, survived a delete that committed")
	}
	if entries, _ := os.ReadDir(logs); len(entries) != 0 {
		t.Errorf("the turns folder still holds %d entries (a renamed file was left)", len(entries))
	}
}

// While the delete holds its write transaction no turn can be admitted: a turn started from another
// connection at that moment waits for the delete and then finds the conversation gone.
func TestNoTurnIsAdmittedWhileTheDeleteSetsFilesAside(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default"})
	touchFile(t, filepath.Join(hostDir(cfg), "turns", "t1.log"))
	recordStarted(t, r, c.ID, "t1")

	admitted := make(chan error, 1)
	var early bool
	inDeleteGuard = func() {
		go func() {
			_, _, err := r.store.CreateTurn(c.ID, "default", "late-1", "i", "go")
			admitted <- err
		}()
		select {
		case <-admitted:
			early = true // the turn was admitted inside the transaction
		case <-time.After(400 * time.Millisecond):
		}
	}
	t.Cleanup(func() { inDeleteGuard = func() {} })

	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if early {
		t.Fatal("a turn was admitted while the delete held its write transaction")
	}
	select {
	case err := <-admitted:
		if err == nil {
			t.Error("a turn was admitted for a conversation that was deleted")
		}
	case <-time.After(15 * time.Second):
		t.Error("the waiting turn never got an answer")
	}
}

// A folder swapped for ANOTHER REAL folder (not a link) between being inspected and being opened is
// refused too: only the identity check catches this, since the new folder is a legitimate path.
func TestDeleteRefusesAFolderSwappedForAnotherRealOne(t *testing.T) {
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
		touchFile(t, copyIn) // a different folder at the same path, holding the same name
	}
	t.Cleanup(func() { afterLstat = func(string) {} })

	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if !swapped {
		t.Fatal("the seam never ran")
	}
	if !exists(copyIn) {
		t.Error("a file in a folder that was not the one inspected was removed")
	}
}
