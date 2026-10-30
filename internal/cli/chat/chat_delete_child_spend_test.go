package chat

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/storage"
)

// `chat delete` of a mission child writes the child's spend down in the delete's own transaction: the usage it is
// computed from goes with the conversation, and the mission's meter is charged from the record afterwards.
func TestDeleteOfAMissionChildRecordsItsSpendBeforeTheUsageGoes(t *testing.T) {
	r := newAccessRig(t, true)
	c := r.conv(t, ai.AccessFull)
	db, err := storage.NewDatabase(r.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, _, err := r.store.CreateTurn(c.ID, "default", "u-1", "i", "go"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.AppendEvent("default", c.ID, "u-1", chatevents.EventUsageUpdated, map[string]any{"agentId": "", "costUsd": 1.25}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.store.FinalizeTurn("default", c.ID, "u-1", chatevents.StatusCompleted, "", nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO coder_mission_children (profile_id, session_id, task_id, lead_conversation_id, child_conversation_id, origin, state, created_at, updated_at)
		VALUES ('default', 7, 3, 'lead-conv', ?, 'lead', 'spawned', 'x', 'x')`, c.ID); err != nil {
		t.Fatal(err)
	}
	if out, err := r.history(t, nil, "delete", c.ID); err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	var note string
	if err := db.DB.QueryRow(`SELECT note FROM coder_mission_events WHERE profile_id = 'default' AND session_id = 7 AND kind = 'spend'`).Scan(&note); err != nil {
		t.Fatalf("no spend record after the delete: %v", err)
	}
	if !strings.HasPrefix(note, c.ID+" $1.250000") {
		t.Fatalf("spend record = %q", note)
	}
}
