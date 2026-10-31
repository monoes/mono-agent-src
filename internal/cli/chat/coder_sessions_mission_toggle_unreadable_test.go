package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
)

// Review finding 5: when the store cannot say whether a mission is open, turning the toggle off is
// refused and the view stays locked, instead of reading "no mission".
func TestMissionToggleOffIsRefusedWhenTheMissionStateIsUnreadable(t *testing.T) {
	f := newToggleFixture(t)
	id := f.openMission(t)
	db, err := storage.NewDatabase(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(`UPDATE task_sessions SET parent_session_id = id WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	ts := tasks.NewStore(db.DB)
	conv := ai.Conversation{ID: f.conv.ID, Access: ai.AccessCoding}
	err = guardMissionToggle(context.Background(), db.DB, "default", conv, false)
	// the write path reads the columns, so the row the checked list skips still counts as the open mission
	if err == nil || !strings.Contains(err.Error(), "running a mission") {
		t.Fatalf("off with an unreadable mission: %v", err)
	}
	if v := missionToggleOf(context.Background(), ts, "default", conv, false); !v.On || !v.Locked {
		t.Fatalf("the view: %+v", v)
	}
}
