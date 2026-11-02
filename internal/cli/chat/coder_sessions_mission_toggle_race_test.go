package chat

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
)

// Review round 1, P1-B. The check "no open mission session" and the write of the toggle are one transaction,
// and a mission session cannot be added while the toggle is off. Each test makes the other side of the race
// run exactly at the point the old code left open.

func (f toggleFixture) planner(t *testing.T) (*storage.Database, tasks.Actor) {
	t.Helper()
	db, err := storage.NewDatabase(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, tasks.Actor{Kind: tasks.Agent, Name: missionActor, Conversation: f.conv.ID, MissionPlanner: true}
}

// planMission is the planner's creation of the chat's mission session, as a toggle-confirmed turn does it.
func (f toggleFixture) planMission(t *testing.T, db *storage.Database, who tasks.Actor) error {
	t.Helper()
	m := missionConfig(context.Background(), db.DB, "default", f.conv.ID, coderTurn{access: "coding"})
	if m == nil {
		t.Fatal("no mission config")
	}
	_, err := m.Store.CreateSession(context.Background(), "default",
		tasks.SessionInput{Title: "M", Goal: "g", ConversationID: f.conv.ID, Kind: tasks.SessionKindMission, BeforeInsert: m.BeforeCreate}, who)
	return err
}

func (f toggleFixture) stored(t *testing.T) (on bool, open int64) {
	t.Helper()
	db, _ := f.planner(t)
	s, err := coderhost.NewStore(db.DB).GetSettings("default", f.conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := tasks.OpenMissionSessionIn(context.Background(), db.DB, "default", f.conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s.Mission, id
}

// A draft mission session created after the OFF check and before the OFF write must not leave OFF stored
// beside an open mission.
func TestAMissionCreatedBetweenTheOffCheckAndTheOffWriteCannotLeaveOffStored(t *testing.T) {
	f := newToggleFixture(t)
	f.run(t, f.cfg, "settings", "set", f.conv.ID, "--mission", "on")
	// the planner's turn read the toggle as on; the config is built then, the session is added later
	db, who := f.planner(t)
	var wg sync.WaitGroup
	var createErr error
	var once sync.Once
	missionToggleAfterCheck = func() {
		once.Do(func() {
			wg.Add(1)
			go func() { defer wg.Done(); createErr = f.planMission(t, db, who) }()
			time.Sleep(400 * time.Millisecond) // long enough for the creation to finish if nothing holds it back
		})
	}
	t.Cleanup(func() { missionToggleAfterCheck = nil })
	f.run(t, f.cfg, "settings", "set", f.conv.ID, "--mission", "off")
	wg.Wait()
	on, open := f.stored(t)
	if !on && open != 0 {
		t.Fatalf("OFF is stored while mission session %d is open (creation error: %v)", open, createErr)
	}
	if open == 0 && createErr == nil {
		t.Fatal("no session and no error")
	}
	if open == 0 && !strings.Contains(createErr.Error(), "Mission toggle off") && !strings.Contains(createErr.Error(), "turned the Mission toggle off") {
		t.Fatalf("creation refused for the wrong reason: %v", createErr)
	}
}

// The other order: the session is being added (its transaction holds the write lock) when the operator's OFF
// starts. The OFF may not pass its check on the committed state and then write after the session is there: it
// either waits for the lock and sees the session, or the old code's unlocked check lets it through.
func TestAnOffThatComesWhileAMissionSessionIsBeingAddedIsRefused(t *testing.T) {
	f := newToggleFixture(t)
	f.run(t, f.cfg, "settings", "set", f.conv.ID, "--mission", "on")
	db, who := f.planner(t)
	m := missionConfig(context.Background(), db.DB, "default", f.conv.ID, coderTurn{access: "coding"})
	reached := make(chan struct{}, 1) // the OFF passed its check
	missionToggleAfterCheck = func() { reached <- struct{}{} }
	t.Cleanup(func() { missionToggleAfterCheck = nil })
	var wg sync.WaitGroup
	var offErr error
	fired := false
	guard := m.BeforeCreate
	_, err := m.Store.CreateSession(context.Background(), "default", tasks.SessionInput{Title: "M", Goal: "g", ConversationID: f.conv.ID, Kind: tasks.SessionKindMission,
		BeforeInsert: func(ctx context.Context, x tasks.Queryer) error {
			if guard != nil {
				if err := guard(ctx, x); err != nil {
					return err
				}
			}
			fired = true
			wg.Add(1)
			go func() {
				defer wg.Done()
				cs := coderhost.NewStore(db.DB)
				_, offErr = setSettingsAsOperator(f.cfg, cs, "default", f.conv.ID, "", "off")
			}()
			select { // a check that is not held back by this transaction passes at once
			case <-reached:
			case <-time.After(1500 * time.Millisecond):
			}
			return nil
		}}, who)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("the creation hook did not run")
	}
	on, open := f.stored(t)
	if open == 0 || !on || offErr == nil || !strings.Contains(offErr.Error(), "running a mission") {
		t.Fatalf("on=%v open=%d off error: %v", on, open, offErr)
	}
}

// A turn already confirmed by the toggle, still running when the operator turns it off, may not start a mission.
func TestAToggleConfirmedTurnCannotStartAMissionAfterTheToggleIsOff(t *testing.T) {
	f := newToggleFixture(t)
	f.run(t, f.cfg, "settings", "set", f.conv.ID, "--mission", "on")
	db, who := f.planner(t)
	m := missionConfig(context.Background(), db.DB, "default", f.conv.ID, coderTurn{access: "coding"}) // the running turn's setup
	if !m.Planning() {
		t.Fatal("the turn is not confirmed")
	}
	if _, _, code := f.run(t, f.cfg, "settings", "set", f.conv.ID, "--mission", "off"); code != 0 {
		t.Fatal("off refused")
	}
	_, err := m.Store.CreateSession(context.Background(), "default",
		tasks.SessionInput{Title: "M", Goal: "g", ConversationID: f.conv.ID, Kind: tasks.SessionKindMission, BeforeInsert: m.BeforeCreate}, who)
	if err == nil || !strings.Contains(err.Error(), "turned the Mission toggle off") {
		t.Fatalf("the session was created: %v", err)
	}
	if _, open := f.stored(t); open != 0 {
		t.Fatalf("session %d is open", open)
	}
}

// A mission the operator confirmed by hand on this turn does not depend on the toggle.
func TestAMissionConfirmedByHandDoesNotNeedTheToggle(t *testing.T) {
	f := newToggleFixture(t)
	db, who := f.planner(t)
	m := missionConfig(context.Background(), db.DB, "default", f.conv.ID, coderTurn{access: "coding", missionConfirmed: true})
	if m.BeforeCreate != nil {
		t.Fatal("a hand-confirmed turn is rechecked against the toggle")
	}
	if _, err := m.Store.CreateSession(context.Background(), "default",
		tasks.SessionInput{Title: "M", Goal: "g", ConversationID: f.conv.ID, Kind: tasks.SessionKindMission, BeforeInsert: m.BeforeCreate}, who); err != nil {
		t.Fatal(err)
	}
}

// The in-transaction open-session query agrees with the planner's own view of "open".
func TestOpenMissionSessionInAgreesWithTheStoresView(t *testing.T) {
	f := newToggleFixture(t)
	db, _ := f.planner(t)
	ctx := context.Background()
	if id, err := tasks.OpenMissionSessionIn(ctx, db.DB, "default", f.conv.ID); err != nil || id != 0 {
		t.Fatalf("none: %d %v", id, err)
	}
	id := f.openMission(t)
	if got, _ := tasks.OpenMissionSessionIn(ctx, db.DB, "default", f.conv.ID); got != id {
		t.Fatalf("draft: %d, want %d", got, id)
	}
	f.endMission(t, id, true)
	if got, _ := tasks.OpenMissionSessionIn(ctx, db.DB, "default", f.conv.ID); got != 0 {
		t.Fatalf("stopped: %d", got)
	}
	other := tasks.SessionInput{Title: "B", Goal: "g", ConversationID: f.conv.ID, Kind: tasks.SessionKindBoard}
	if _, err := tasks.NewStore(db.DB).CreateSession(ctx, "default", other, tasks.Actor{Kind: tasks.Human}); err != nil {
		t.Fatal(err)
	}
	if got, _ := tasks.OpenMissionSessionIn(ctx, db.DB, "default", f.conv.ID); got != 0 {
		t.Fatalf("a board session is not the mission: %d", got)
	}
}
