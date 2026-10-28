package chat

import (
	"os"
	"path/filepath"
	"testing"

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
