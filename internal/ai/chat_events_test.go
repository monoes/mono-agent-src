package ai

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/storage"
)

func newChatEventStore(t *testing.T) (*AIStore, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	s, err := NewAIStore(db)
	if err != nil {
		t.Fatalf("NewAIStore: %v", err)
	}
	return s, db
}

func TestChatEvents_TwoProfilesDoNotSeeEachOthersGeneralConversation(t *testing.T) {
	s, _ := newChatEventStore(t)
	c1, err := s.CreateConversation("profile-a", "agent", "general", "claude", "", "")
	if err != nil {
		t.Fatalf("CreateConversation(a): %v", err)
	}
	c2, err := s.CreateConversation("profile-b", "agent", "general", "claude", "", "")
	if err != nil {
		t.Fatalf("CreateConversation(b): %v", err)
	}
	if c1.ID == c2.ID {
		t.Fatal("two profiles' general conversations collided on ID")
	}
	if _, err := s.GetConversation(c1.ID, "profile-b"); err != ErrConversationNotFound {
		t.Errorf("profile-b reading profile-a's conversation: err = %v, want ErrConversationNotFound", err)
	}
	listA, _, err := s.ListConversations("profile-a", "", 10)
	if err != nil {
		t.Fatalf("ListConversations(a): %v", err)
	}
	if len(listA) != 1 || listA[0].ID != c1.ID {
		t.Errorf("profile-a's list = %+v, want exactly [c1]", listA)
	}
}

func TestChatEvents_ConversationRecordsEffort(t *testing.T) {
	s, _ := newChatEventStore(t)
	conv, err := s.CreateConversationEffort("p1", "agent", "general", "claude", "", "opus", "high")
	if err != nil {
		t.Fatalf("CreateConversationEffort: %v", err)
	}
	if conv.Effort != "high" {
		t.Errorf("conv.Effort = %q, want %q", conv.Effort, "high")
	}

	got, err := s.GetConversation(conv.ID, "p1")
	if err != nil {
		t.Fatalf("GetConversation: %v", err)
	}
	if got.Effort != "high" {
		t.Errorf("got.Effort = %q, want %q", got.Effort, "high")
	}

	rec := got.Record()
	if rec.Effort != "high" {
		t.Errorf("rec.Effort = %q, want %q", rec.Effort, "high")
	}
	if rec.Conversation().Effort != "high" {
		t.Errorf("rec.Conversation().Effort = %q, want %q", rec.Conversation().Effort, "high")
	}

	list, _, err := s.ListConversations("p1", "", 10)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(list) != 1 || list[0].Effort != "high" {
		t.Errorf("list[0].Effort = %q, want %q", list[0].Effort, "high")
	}
}

func TestChatEvents_CreateTurnIsIdempotentByID(t *testing.T) {
	s, _ := newChatEventStore(t)
	conv, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")

	t1, existed1, err := s.CreateTurn(conv.ID, "p1", "turn-1", "instance-a", "hello")
	if err != nil || existed1 {
		t.Fatalf("first CreateTurn: turn=%+v existed=%v err=%v", t1, existed1, err)
	}
	t2, existed2, err := s.CreateTurn(conv.ID, "p1", "turn-1", "instance-a", "hello again — ignored")
	if err != nil {
		t.Fatalf("duplicate CreateTurn: %v", err)
	}
	if !existed2 {
		t.Error("duplicate Start with the same turn ID must report it already existed")
	}
	if t2.Prompt != "hello" {
		t.Errorf("duplicate Start must never overwrite the original admission: Prompt = %q, want %q", t2.Prompt, "hello")
	}

	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ai_chat_turns WHERE id = ?`, "turn-1").Scan(&count); err != nil {
		t.Fatalf("count turns: %v", err)
	}
	if count != 1 {
		t.Errorf("duplicate Start created %d rows, want exactly 1 (never a second process/row)", count)
	}
}

// TestChatEvents_CreateTurnRefusedWhenAnotherInstanceOwnsActiveTurn is the
// core enforcement for docs/mastermind/plans/2026-09-12-interactive-agent-chat-followups.md's
// "OwnerInstanceID is written and read back but never compared to
// anything": two live app instances sharing one database must not both be
// able to run a turn against the same conversation.
func TestChatEvents_CreateTurnRefusedWhenAnotherInstanceOwnsActiveTurn(t *testing.T) {
	s, _ := newChatEventStore(t)
	conv, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")

	if _, _, err := s.CreateTurn(conv.ID, "p1", "turn-a", "instance-a", "hello from a"); err != nil {
		t.Fatalf("CreateTurn(instance-a): %v", err)
	}

	// Instance b tries to start its OWN turn (a different turn ID) against
	// the same conversation while instance-a's turn is still active.
	if _, _, err := s.CreateTurn(conv.ID, "p1", "turn-b", "instance-b", "hello from b"); !errors.Is(err, ErrTurnOwnedByOtherInstance) {
		t.Fatalf("CreateTurn(instance-b) while instance-a's turn is active on the same conversation: err = %v, want ErrTurnOwnedByOtherInstance", err)
	}
	// Refused admission must not have inserted a row.
	if _, err := s.GetTurn("turn-b", "p1"); !errors.Is(err, ErrTurnNotFound) {
		t.Errorf("GetTurn(turn-b) after refused CreateTurn: err = %v, want ErrTurnNotFound (nothing should have been written)", err)
	}

	// The SAME instance may still retry/re-admit its OWN turn ID — this must
	// remain the existing idempotent-by-ID behavior, not newly refused by
	// comparing an owner against itself.
	again, existed, err := s.CreateTurn(conv.ID, "p1", "turn-a", "instance-a", "hello from a, retried")
	if err != nil {
		t.Fatalf("CreateTurn(instance-a) retrying its own turn ID: %v", err)
	}
	if !existed || again.Prompt != "hello from a" {
		t.Errorf("CreateTurn(instance-a) retry = %+v existed=%v, want the original admission returned unchanged", again, existed)
	}

	// Once instance-a's turn reaches a terminal state, the conversation is
	// no longer "owned" by it — a different instance may now create its own
	// active turn there. The refusal is a live-turn lock, not a permanent
	// per-conversation assignment to whichever instance touched it first.
	if _, _, err := s.FinalizeTurn("p1", conv.ID, "turn-a", chatevents.StatusCompleted, "", nil, true); err != nil {
		t.Fatalf("FinalizeTurn(turn-a): %v", err)
	}
	if _, _, err := s.CreateTurn(conv.ID, "p1", "turn-b", "instance-b", "hello from b, retried"); err != nil {
		t.Fatalf("CreateTurn(instance-b) after instance-a's turn finished: %v", err)
	}
}

func TestChatEvents_FinalizeTurnCompareAndSetIsExactlyOnce(t *testing.T) {
	s, _ := newChatEventStore(t)
	conv, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	turn, _, _ := s.CreateTurn(conv.ID, "p1", "turn-1", "instance-a", "hello")

	ev1, already1, err := s.FinalizeTurn("p1", conv.ID, turn.ID, chatevents.StatusCompleted, "", nil, true)
	if err != nil || already1 {
		t.Fatalf("first FinalizeTurn: already=%v err=%v", already1, err)
	}
	if ev1.Type != chatevents.EventTurnFinished {
		t.Errorf("first FinalizeTurn returned event type %q, want %q", ev1.Type, chatevents.EventTurnFinished)
	}
	// A second, "duplicate terminal" finalize (e.g. a stale Stop racing EOF
	// handling) must be a harmless no-op, never a second turn.finished event
	// and never overwriting the first outcome.
	ev2, already2, err := s.FinalizeTurn("p1", conv.ID, turn.ID, chatevents.StatusCancelled, "stale stop", nil, true)
	if err != nil {
		t.Fatalf("second FinalizeTurn: %v", err)
	}
	if !already2 {
		t.Error("second FinalizeTurn on an already-terminal turn must report alreadyFinalized")
	}
	if ev2.Type != "" {
		t.Errorf("second (already-finalized) FinalizeTurn returned a non-zero event %+v, want the zero value (nothing written)", ev2)
	}

	got, err := s.GetTurn(turn.ID, "p1")
	if err != nil {
		t.Fatalf("GetTurn: %v", err)
	}
	if got.Status != string(chatevents.StatusCompleted) {
		t.Errorf("Status = %q, want the FIRST finalize's %q to stick", got.Status, chatevents.StatusCompleted)
	}

	events, err := s.GetEvents(conv.ID, turn.ID, "p1", 0, 100)
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	finishedCount := 0
	for _, ev := range events {
		if ev.Type == chatevents.EventTurnFinished {
			finishedCount++
		}
	}
	if finishedCount != 1 {
		t.Errorf("turn.finished appeared %d times, want exactly once", finishedCount)
	}
}

func TestChatEvents_AppendEventAllocatesStrictlyIncreasingSequence(t *testing.T) {
	s, _ := newChatEventStore(t)
	conv, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	turn, _, _ := s.CreateTurn(conv.ID, "p1", "turn-1", "instance-a", "hello")

	var lastSeq int64
	for i := 0; i < 5; i++ {
		ev, err := s.AppendEvent("p1", conv.ID, turn.ID, chatevents.EventAssistantDelta, chatevents.AssistantDeltaPayload{PartID: "part-1", Text: "x"})
		if err != nil {
			t.Fatalf("AppendEvent #%d: %v", i, err)
		}
		if ev.Seq <= lastSeq {
			t.Fatalf("Seq #%d = %d, want strictly greater than previous %d", i, ev.Seq, lastSeq)
		}
		lastSeq = ev.Seq
	}
	got, err := s.GetTurn(turn.ID, "p1")
	if err != nil {
		t.Fatalf("GetTurn: %v", err)
	}
	if got.LastCommittedSeq != lastSeq {
		t.Errorf("LastCommittedSeq = %d, want %d (the last appended seq)", got.LastCommittedSeq, lastSeq)
	}
}

// Regression guard: sequence ordering must not depend on wall-clock
// timestamps distinguishing events — several appends can share the same
// RFC3339-second timestamp and must still come back in insertion order.
func TestChatEvents_EqualTimestampsPreserveSequenceOrder(t *testing.T) {
	s, _ := newChatEventStore(t)
	conv, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	turn, _, _ := s.CreateTurn(conv.ID, "p1", "turn-1", "instance-a", "hello")

	for i := 0; i < 3; i++ {
		if _, err := s.AppendEvent("p1", conv.ID, turn.ID, chatevents.EventAssistantDelta, chatevents.AssistantDeltaPayload{PartID: "p", Text: "x"}); err != nil {
			t.Fatalf("AppendEvent #%d: %v", i, err)
		}
	}
	events, err := s.GetEvents(conv.ID, turn.ID, "p1", 0, 100)
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	for i := 1; i < len(events); i++ {
		if events[i].Seq <= events[i-1].Seq {
			t.Errorf("event %d Seq %d not greater than event %d Seq %d", i, events[i].Seq, i-1, events[i-1].Seq)
		}
	}
}

func TestChatEvents_ConversationWithNoSessionBoundIsStillUsable(t *testing.T) {
	s, _ := newChatEventStore(t)
	conv, err := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	if conv.SessionID != "" {
		t.Errorf("SessionID = %q, want empty until session.bound", conv.SessionID)
	}
	list, _, err := s.ListConversations("p1", "", 10)
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("a sessionless conversation must still appear in history: got %d entries", len(list))
	}
}

func TestChatEvents_DeleteConversationBlockedWhileTurnActive(t *testing.T) {
	s, _ := newChatEventStore(t)
	conv, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	s.CreateTurn(conv.ID, "p1", "turn-1", "instance-a", "hello")

	if err := s.DeleteConversation(conv.ID, "p1"); err != ErrTurnActive {
		t.Fatalf("DeleteConversation while active: err = %v, want ErrTurnActive", err)
	}
	if _, err := s.GetConversation(conv.ID, "p1"); err != nil {
		t.Errorf("conversation must survive a refused delete: %v", err)
	}
}

func TestChatEvents_DeleteConversationRemovesTurnsAndEventsTransactionally(t *testing.T) {
	s, _ := newChatEventStore(t)
	conv, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	turn, _, _ := s.CreateTurn(conv.ID, "p1", "turn-1", "instance-a", "hello")
	s.AppendEvent("p1", conv.ID, turn.ID, chatevents.EventAssistantDelta, chatevents.AssistantDeltaPayload{PartID: "p", Text: "x"})
	if _, _, err := s.FinalizeTurn("p1", conv.ID, turn.ID, chatevents.StatusCompleted, "", nil, true); err != nil {
		t.Fatalf("FinalizeTurn: %v", err)
	}

	if err := s.DeleteConversation(conv.ID, "p1"); err != nil {
		t.Fatalf("DeleteConversation: %v", err)
	}
	if _, err := s.GetConversation(conv.ID, "p1"); err != ErrConversationNotFound {
		t.Errorf("conversation row survived delete: err = %v", err)
	}
	if _, err := s.GetTurn(turn.ID, "p1"); err != ErrTurnNotFound {
		t.Errorf("turn row survived conversation delete: err = %v", err)
	}
	events, err := s.GetEvents(conv.ID, turn.ID, "p1", 0, 100)
	if err != nil {
		t.Fatalf("GetEvents after delete: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events after conversation delete, want 0", len(events))
	}
}

// "a reopened temporary database returns exactly the committed event
// sequence" — the store's Gate criterion, verified directly: close the DB,
// reopen the same file, confirm nothing committed was lost or duplicated.
func TestChatEvents_ReopenedDatabaseReturnsExactCommittedSequence(t *testing.T) {
	keyring.MockInit()
	path := t.TempDir() + "/reopen-test.db"

	db1, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db1.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	s1, err := NewAIStore(db1.DB)
	if err != nil {
		t.Fatalf("NewAIStore: %v", err)
	}
	conv, err := s1.CreateConversation("p1", "agent", "general", "claude", "", "")
	if err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}
	turn, _, err := s1.CreateTurn(conv.ID, "p1", "turn-1", "instance-a", "hello")
	if err != nil {
		t.Fatalf("CreateTurn: %v", err)
	}
	var wantSeqs []int64
	for i := 0; i < 4; i++ {
		ev, err := s1.AppendEvent("p1", conv.ID, turn.ID, chatevents.EventAssistantDelta, chatevents.AssistantDeltaPayload{PartID: "p", Text: "x"})
		if err != nil {
			t.Fatalf("AppendEvent #%d: %v", i, err)
		}
		wantSeqs = append(wantSeqs, ev.Seq)
	}
	if _, _, err := s1.FinalizeTurn("p1", conv.ID, turn.ID, chatevents.StatusCompleted, "", nil, true); err != nil {
		t.Fatalf("FinalizeTurn: %v", err)
	}
	if err := db1.DB.Close(); err != nil {
		t.Fatalf("close db1: %v", err)
	}

	db2, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatalf("reopen NewDatabase: %v", err)
	}
	t.Cleanup(func() { db2.DB.Close() })
	if err := db2.ApplyMigrations(); err != nil {
		t.Fatalf("reopen ApplyMigrations: %v", err)
	}
	s2, err := NewAIStore(db2.DB)
	if err != nil {
		t.Fatalf("reopen NewAIStore: %v", err)
	}
	events, err := s2.GetEvents(conv.ID, turn.ID, "p1", 0, 100)
	if err != nil {
		t.Fatalf("reopen GetEvents: %v", err)
	}
	if len(events) != len(wantSeqs)+1 { // +1 for turn.finished
		t.Fatalf("reopened DB has %d events, want %d (deltas) + 1 (finished)", len(events), len(wantSeqs))
	}
	for i, want := range wantSeqs {
		if events[i].Seq != want {
			t.Errorf("event %d Seq = %d, want %d", i, events[i].Seq, want)
		}
	}
	if events[len(events)-1].Type != chatevents.EventTurnFinished {
		t.Errorf("last event type = %q, want turn.finished", events[len(events)-1].Type)
	}
}

// The schema comes from the migrations alone: opening an AIStore (twice,
// as every CLI command and the GUI do) creates and alters nothing.
func TestChatEvents_NewAIStoreLeavesTheSchemaAlone(t *testing.T) {
	db := openTestDB(t)
	schema := func() string {
		t.Helper()
		var out string
		rows, err := db.Query(`SELECT type || ' ' || name || ' ' || COALESCE(sql, '') FROM sqlite_master ORDER BY type, name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			out += line + "\n"
		}
		return out
	}
	before := schema()
	for i := 0; i < 2; i++ {
		if _, err := NewAIStore(db); err != nil {
			t.Fatalf("NewAIStore #%d: %v", i+1, err)
		}
	}
	if after := schema(); after != before {
		t.Errorf("NewAIStore changed the schema:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestChatEvents_DeleteConversationRaceAgainstConcurrentCreateTurn is the
// regression test for
// docs/mastermind/plans/2026-09-12-interactive-agent-chat-followups.md's
// "DeleteConversation isn't atomic against a concurrent StartChatTurn":
// DeleteConversation's active-turn check used to run outside any
// transaction, before its own delete transaction even opened, so a
// CreateTurn admitted in between could leave an orphaned, invisible turn —
// and, once app_chat.go went on to AppendEvent for it, orphaned events —
// under a conversation_id nothing could query anymore.
//
// Every iteration races a fresh DeleteConversation against a fresh
// CreateTurn (+ a best-effort AppendEvent, mirroring app_chat.go's real
// StartChatTurn -> CreateTurn -> AppendEvent sequence) for the SAME
// conversation, released from a shared start gate so the two collide as
// tightly as two goroutines reasonably can.
//
// The assertion is deliberately keyed off the one fact that actually
// matters -- did the conversation row survive -- rather than which
// specific error each call returned: CreateTurn's failure mode when it
// loses this race can be a wrapped generic error (an INSERT the foreign
// key rejects) depending purely on scheduling, and AppendEvent can
// independently return a transient error when it collides with
// DeleteConversation's write lock (it is a read-then-write transaction —
// SELECT MAX(seq) then INSERT — so a losing collision there is a snapshot
// conflict, not evidence of a correctness bug); neither is what this test
// is about, so appendErr is recorded but never asserted on. What must
// ALWAYS hold, on every single iteration, is: if the conversation is gone,
// its turns and events are ALL gone with it (no orphan); if the
// conversation survived, delete was actually refused, not silently
// skipped.
func TestChatEvents_DeleteConversationRaceAgainstConcurrentCreateTurn(t *testing.T) {
	s, _ := newChatEventStore(t)
	const iterations = 100

	survived, deleted := 0, 0
	for i := 0; i < iterations; i++ {
		conv, err := s.CreateConversation("p1", "agent", "general", "claude", "", "")
		if err != nil {
			t.Fatalf("iter %d: CreateConversation: %v", i, err)
		}
		turnID := fmt.Sprintf("turn-%d", i)

		// Start gate: both goroutines signal "arrived" then block on the same
		// channel, so closing it releases them as close to simultaneously as
		// two goroutines reasonably can be.
		var arrived sync.WaitGroup
		arrived.Add(2)
		ready := make(chan struct{})
		var done sync.WaitGroup
		done.Add(2)

		var deleteErr, createErr, appendErr error

		go func() {
			defer done.Done()
			arrived.Done()
			<-ready
			deleteErr = s.DeleteConversation(conv.ID, "p1")
		}()
		go func() {
			defer done.Done()
			arrived.Done()
			<-ready
			_, _, cErr := s.CreateTurn(conv.ID, "p1", turnID, "instance-a", "hello")
			createErr = cErr
			if cErr == nil {
				// Mirror app_chat.go's real sequence: only a successfully
				// admitted turn goes on to append an event.
				_, appendErr = s.AppendEvent("p1", conv.ID, turnID, chatevents.EventAssistantDelta, chatevents.AssistantDeltaPayload{PartID: "p", Text: "x"})
			}
		}()

		arrived.Wait()
		close(ready)
		done.Wait()

		_, getErr := s.GetConversation(conv.ID, "p1")
		conversationGone := errors.Is(getErr, ErrConversationNotFound)

		var turnCount, eventCount int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ai_chat_turns WHERE conversation_id = ?`, conv.ID).Scan(&turnCount); err != nil {
			t.Fatalf("iter %d: count turns: %v", i, err)
		}
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ai_chat_events WHERE conversation_id = ?`, conv.ID).Scan(&eventCount); err != nil {
			t.Fatalf("iter %d: count events: %v", i, err)
		}

		if conversationGone {
			deleted++
			if deleteErr != nil {
				t.Errorf("iter %d: conversation was deleted but DeleteConversation returned an error: %v", i, deleteErr)
			}
			if turnCount != 0 {
				t.Errorf("iter %d: conversation %s deleted but %d orphaned ai_chat_turns row(s) remain (createErr=%v)", i, conv.ID, turnCount, createErr)
			}
			if eventCount != 0 {
				t.Errorf("iter %d: conversation %s deleted but %d orphaned ai_chat_events row(s) remain (createErr=%v appendErr=%v)", i, conv.ID, eventCount, createErr, appendErr)
			}
		} else {
			survived++
			if deleteErr == nil {
				t.Errorf("iter %d: conversation %s still exists but DeleteConversation reported success (nil error) -- it must refuse, never silently no-op", i, conv.ID)
			}
			if createErr != nil {
				t.Errorf("iter %d: conversation survived (nothing removed its parent) but CreateTurn still failed: %v", i, createErr)
			}
			if got, gErr := s.GetTurn(turnID, "p1"); gErr != nil || got.Status != turnStatusActive {
				t.Errorf("iter %d: conversation survived but its turn is missing or not active: turn=%+v err=%v", i, got, gErr)
			}
		}
	}
	t.Logf("conversation survived %d/%d iterations, deleted on %d/%d iterations", survived, iterations, deleted, iterations)
}
