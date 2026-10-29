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

// The operator retires a worker that has finished but whose hand-off has not been stored yet: the
// row stored afterwards is born retired, so the next turn does not load it.
func TestAWorkerStoredAfterItsRetirementIsBornRetired(t *testing.T) {
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
	se, _ := ts.CreateSession(ctx, "default", tasks.SessionInput{Title: "m", Goal: "g", ConversationID: conv.ID}, human)
	tk, _ := ts.AddSessionTask(ctx, "default", se.ID, tasks.SessionTaskInput{Title: "t", Acceptance: "done"}, human)
	if _, err := ts.ApproveSession(ctx, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	w := tasks.Actor{Kind: tasks.Agent, Name: "w", Session: se.ID, Task: tk.ID}
	if _, err := ts.SessionClaim(ctx, "default", se.ID, tk.ID, w, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.SessionFinish(ctx, "default", se.ID, tk.ID, tasks.Outcome{Result: "done"}, w); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.RetireWorker(ctx, "default", se.ID, tk.ID, "", human); err != nil { // no stored row yet
		t.Fatal(err)
	}
	j := &turnJournal{store: aiStore, profileID: "default", conversationID: conv.ID, turnID: "t1"}
	j.rememberWorker(dynorg.Veteran{ID: "w1", Runtime: "claude", Session: "sess-1", Report: "late hand-off", Outcome: "done", MissionSession: se.ID, MissionTask: tk.ID})
	if got := j.veterans(); len(got) != 0 {
		t.Fatalf("the next turn loads a worker retired before it was stored: %+v", got)
	}
	all, _ := aiStore.ListOrgWorkers("default", conv.ID, 0)
	if len(all) != 1 || all[0].SessionID != "" || all[0].Outcome != ai.OrgWorkerRetired || all[0].Report != "late hand-off" {
		t.Fatalf("the stored row = %+v, want retired, no session, report kept", all)
	}
}

// A veteran loaded for the next turn names the mission task it worked, so the turn can still retire it.
func TestVeteransKeepTheirMissionLink(t *testing.T) {
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
	j := &turnJournal{store: aiStore, profileID: "default", conversationID: conv.ID, turnID: "t1"}
	j.rememberWorker(dynorg.Veteran{ID: "w1", Runtime: "claude", Session: "s", Report: "r", Outcome: "done", MissionSession: 7, MissionTask: 42})
	got := j.veterans()
	if len(got) != 1 || got[0].MissionSession != 7 || got[0].MissionTask != 42 {
		t.Fatalf("the loaded veteran = %+v, want its mission link", got)
	}
}

// A late hand-off of a retired worker is born retired even when a new worker has claimed the task since;
// the new worker's own hand-off is not.
func TestALateHandOffAfterANewClaimIsStillRetired(t *testing.T) {
	db, err := storage.NewDatabase(testdb.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	aiStore, _ := ai.NewAIStore(db.DB)
	conv, err := aiStore.CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, "/w")
	if err != nil {
		t.Fatal(err)
	}
	ts := tasks.NewStore(db.DB)
	ctx := context.Background()
	human := tasks.Actor{Kind: tasks.Human}
	se, _ := ts.CreateSession(ctx, "default", tasks.SessionInput{Title: "m", Goal: "g", ConversationID: conv.ID}, human)
	tk, _ := ts.AddSessionTask(ctx, "default", se.ID, tasks.SessionTaskInput{Title: "t", Acceptance: "done"}, human)
	if _, err := ts.ApproveSession(ctx, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	w := tasks.Actor{Kind: tasks.Agent, Name: "w", Session: se.ID, Task: tk.ID}
	if _, err := ts.SessionClaim(ctx, "default", se.ID, tk.ID, w, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.SessionFinish(ctx, "default", se.ID, tk.ID, tasks.Outcome{Result: "done"}, w); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.RetireWorker(ctx, "default", se.ID, tk.ID, "", human); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO task_events (task_id, at, actor, kind, note) VALUES (?, 'x', 'next', 'claimed', '')`, tk.ID); err != nil {
		t.Fatal(err)
	}
	j := &turnJournal{store: aiStore, profileID: "default", conversationID: conv.ID, turnID: "t1"}
	j.rememberWorker(dynorg.Veteran{ID: "old", Runtime: "claude", Session: "s1", Report: "r", Outcome: "done", MissionSession: se.ID, MissionTask: tk.ID, MissionClaims: 1})
	j.rememberWorker(dynorg.Veteran{ID: "new", Runtime: "claude", Session: "s2", Report: "r", Outcome: "done", MissionSession: se.ID, MissionTask: tk.ID, MissionClaims: 2})
	var ids []string
	for _, v := range j.veterans() {
		ids = append(ids, v.ID)
	}
	if len(ids) != 1 || ids[0] != "new" {
		t.Fatalf("veterans = %v, want only the worker that took the task after the retirement", ids)
	}
}
