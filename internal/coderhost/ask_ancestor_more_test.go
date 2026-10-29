package coderhost

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

func TestOneAncestorTakesAtMostSixOpenAsksFromBelow(t *testing.T) {
	f := newAskTree(t)
	for i := 0; i < MaxOpenAsksPerTarget+1; i++ {
		conv := chat(t, f.st)
		if _, err := f.ts.CreateSession(context.Background(), "p", tasks.SessionInput{Title: "c", ConversationID: conv, ParentSessionID: f.P.ID, BudgetUSD: 1, MaxTasks: 5}, opActor); err != nil {
			t.Fatal(err)
		}
		_, err := f.st.AskAncestor(f.lead(conv), "p", AskRequest{Question: "q"})
		if i < MaxOpenAsksPerTarget && err != nil {
			t.Fatalf("ask %d: %v", i, err)
		}
		if i == MaxOpenAsksPerTarget && (!errors.Is(err, ErrAskRate) || !strings.Contains(err.Error(), "already has")) {
			t.Fatalf("the seventh ask into one ancestor: %v", err)
		}
	}
	// A critical ask is not held by that cap: it reaches the person, not the ancestor's queue.
	if _, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q", Importance: AskCritical}); err != nil {
		t.Fatalf("critical: %v", err)
	}
}

func TestOnlyOnePersonAnswerWinsARace(t *testing.T) {
	f := newAskTree(t)
	got, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q", Importance: AskCritical})
	if err != nil {
		t.Fatal(err)
	}
	f.st.db.SetMaxOpenConns(8)
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := f.st.AnswerAncestorQuestion("p", got.AskID, "yes"); err == nil {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if won != 1 || len(f.commandsTo(f.cc)) != 1 {
		t.Fatalf("%d answers won, %d notes queued", won, len(f.commandsTo(f.cc)))
	}
}

func TestAReplyIsRefusedWhenTheAncestorsLeadChatChanged(t *testing.T) {
	f := newAskTree(t)
	got, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET conversation_id = ? WHERE id = ?`, f.s, f.R.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ReplyAsk(f.lead(f.rc), "p", got.AskID, "late"); err == nil {
		t.Fatal("the old lead of the ancestor replied")
	}
	// The new lead of that session was not the one asked: the ask names the chat, not the session.
	if _, err := f.st.ReplyAsk(f.lead(f.s), "p", got.AskID, "late"); !errors.Is(err, ErrNotYours) {
		t.Fatalf("the new lead replied: %v", err)
	}
	if n := len(f.commandsTo(f.cc)); n != 0 {
		t.Fatalf("%d notes queued", n)
	}
}

func TestAnArchivedOrFinishedAncestorTakesNoAsk(t *testing.T) {
	f := newAskTree(t)
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET archived_at = ? WHERE id = ?`, "2026-01-01T00:00:00Z", f.R.ID); err != nil {
		t.Fatal(err)
	}
	for _, imp := range []string{AskNormal, AskCritical} {
		f.tick()
		if _, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q", Importance: imp}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", imp, err)
		}
	}
}

func TestClosingAnAskIsOneWayEvenWhenTheCheckBeforeItIsStale(t *testing.T) {
	f := newAskTree(t)
	got, err := f.st.AskAncestor(f.lead(f.cc), "p", AskRequest{Question: "q", Importance: AskCritical})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.closeAndDeliver("p", got.AskID, f.cc, "q", "the person", "one", "person", askFromPerson); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.closeAndDeliver("p", got.AskID, f.cc, "q", "the person", "two", "person", askFromPerson); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("a second close won: %v", err)
	}
	if n := len(f.commandsTo(f.cc)); n != 1 {
		t.Fatalf("%d notes queued", n)
	}
}
