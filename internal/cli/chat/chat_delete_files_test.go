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
