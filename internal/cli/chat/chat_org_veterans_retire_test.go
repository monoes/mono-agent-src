package chat

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
	"github.com/monoes/mono-agent/internal/testdb"
)

// Between turns: a mission worker the operator retired is not loaded as a veteran for the
// conversation's next turn, so the lead cannot message it; the conversation's other workers are.
func TestARetiredMissionWorkerIsNotLoadedAsAVeteran(t *testing.T) {
	db, err := storage.NewDatabase(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	aiStore, err := ai.NewAIStore(db.DB)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := aiStore.CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, "/w")
	if err != nil {
		t.Fatal(err)
	}
	ts := tasks.NewStore(db.DB)
	ctx := context.Background()
	human := tasks.Actor{Kind: tasks.Human}
	se, err := ts.CreateSession(ctx, "default", tasks.SessionInput{Title: "m", Goal: "g", ConversationID: conv.ID}, human)
	if err != nil {
		t.Fatal(err)
	}
	tk, err := ts.AddSessionTask(ctx, "default", se.ID, tasks.SessionTaskInput{Title: "t", Acceptance: "done"}, human)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ts.AddSessionTask(ctx, "default", se.ID, tasks.SessionTaskInput{Title: "u", Acceptance: "done"}, human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.ApproveSession(ctx, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{tk.ID, other.ID} {
		w := tasks.Actor{Kind: tasks.Agent, Name: "w", Session: se.ID, Task: id} // bound, as the conductor's worker is
		if _, err := ts.SessionClaim(ctx, "default", se.ID, id, w, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := ts.SessionFinish(ctx, "default", se.ID, id, tasks.Outcome{Result: "done"}, w); err != nil {
			t.Fatal(err)
		}
	}

	j := &turnJournal{store: aiStore, profileID: "default", conversationID: conv.ID, turnID: "t1"}
	j.rememberWorker(dynorg.Veteran{ID: "w1", Runtime: "claude", Session: "sess-1", Report: "r1", Outcome: "done", MissionSession: se.ID, MissionTask: tk.ID})
	j.rememberWorker(dynorg.Veteran{ID: "w2", Runtime: "claude", Session: "sess-2", Report: "r2", Outcome: "done", MissionSession: se.ID, MissionTask: other.ID})
	j.rememberWorker(dynorg.Veteran{ID: "w3", Runtime: "claude", Session: "sess-3", Report: "r3", Outcome: "done"})
	if got := len(j.veterans()); got != 3 {
		t.Fatalf("veterans before the retirement = %d, want 3", got)
	}
	if _, err := ts.RetireWorker(ctx, "default", se.ID, tk.ID, "", human); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, v := range j.veterans() {
		ids = append(ids, v.ID)
	}
	if len(ids) != 2 || ids[0] == "w1" || ids[1] == "w1" {
		t.Fatalf("veterans after the retirement = %v, want w2 and w3", ids)
	}
	// A late hand-off of the retired worker (its turn was still running) does not bring it back.
	j.rememberWorker(dynorg.Veteran{ID: "w1", Runtime: "claude", Session: "sess-9", Report: "late", Outcome: "done", MissionSession: se.ID, MissionTask: tk.ID})
	if got := len(j.veterans()); got != 2 {
		t.Fatalf("a late hand-off revived the retired worker: %d veterans", got)
	}
}
