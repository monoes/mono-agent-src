package ai

import (
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/dbconn/dbconntest"
	"github.com/monoes/mono-agent/internal/storage"
)

// AppendEvent reads MAX(seq) and then writes. A writer that commits between the two must not make it fail with
// SQLITE_BUSY_SNAPSHOT (517, which busy_timeout does not wait for): the write lock is taken first (#1460).
func TestAppendEventSurvivesAWriterCommittingBetweenItsReadAndItsWrite(t *testing.T) {
	keyring.MockInit()
	path := filepath.Join(t.TempDir(), "ai-busy.db")
	d, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	setup, _ := NewAIStore(d.DB)
	conv, err := setup.CreateConversation("p1", "agent", "general", "claude", "", "")
	if err != nil {
		t.Fatal(err)
	}
	turn, _, err := setup.CreateTurn(conv.ID, "p1", "turn-1", "instance-a", "hello")
	if err != nil {
		t.Fatal(err)
	}
	d.Close()

	db, trip := dbconntest.OpenPath(t, path)
	s, _ := NewAIStore(db)
	competing := dbconntest.CompetingWriter(t, path)
	fired := false
	trip.BeforeExec = func(string) {
		if !fired {
			fired = true
			competing()
		}
	}
	ev, err := s.AppendEvent("p1", conv.ID, turn.ID, chatevents.EventAssistantDelta, chatevents.AssistantDeltaPayload{PartID: "part-1", Text: "x"})
	if err != nil {
		t.Fatalf("AppendEvent failed when a writer committed between its read and its write: %v", err)
	}
	if !fired || ev.Seq < 1 {
		t.Fatalf("hook fired=%v seq=%d", fired, ev.Seq)
	}
}
