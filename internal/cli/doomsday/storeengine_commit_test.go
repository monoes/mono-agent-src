package doomsday

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// commitStore makes the store's Enter report that its commit's outcome is unknown, after (or instead of) really committing, and
// answers GetRun as the test says.
type commitStore struct {
	store.Store
	really    bool  // the entry really commits before it reports the unknown outcome
	getRunErr error // GetRun fails with this (nil: it reads the real row)
}

func (c commitStore) Enter(ctx context.Context, ch store.Charter, h [32]byte, by string) (store.Run, error) {
	if c.really {
		if _, err := c.Store.Enter(ctx, ch, h, by); err != nil {
			return store.Run{}, err
		}
	}
	return store.Run{}, errors.Join(store.ErrCommitUnknown, errors.New("disk I/O error"))
}

func (c commitStore) GetRun(ctx context.Context, p string, g int64) (store.Run, error) {
	if c.getRunErr != nil {
		return store.Run{}, c.getRunErr
	}
	return c.Store.GetRun(ctx, p, g)
}

func enterWith(t *testing.T, c commitStore) (Entry, error, *wireRig) {
	t.Helper()
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.w.DB = db.DB
	e := w.w.Engine()
	c.Store = e.Store
	e.Store = c
	w.charter = writeCharter(t, goodCharter)
	h := fullHash(t, w.rig, w.charter)
	canon, err := e.Canonical(context.Background(), mustCharter(t, w.charter))
	if err != nil {
		t.Fatal(err)
	}
	_ = h
	ent, err := e.Enter(context.Background(), canon, canon.Hash, Ack{Kind: "script"})
	return ent, err, w
}

// A commit that reported an error but really happened is the entry's own run: it goes on and succeeds.
func TestEnterWithACommitErrorThatHappenedGoesOn(t *testing.T) {
	ent, err, _ := enterWith(t, commitStore{really: true})
	if err != nil || ent.RunID == "" {
		t.Fatalf("a committed entry was refused: %+v %v", ent, err)
	}
}

// A commit that reported an error and did not happen is a plain refusal, and the unit made for it is removed.
func TestEnterWithACommitErrorThatDidNotHappenIsARefusal(t *testing.T) {
	_, err, w := enterWith(t, commitStore{getRunErr: sql.ErrNoRows})
	if err == nil || errors.Is(err, ErrRunRecorded) || errors.Is(err, ErrEntryUnknown) {
		t.Fatalf("err = %v", err)
	}
	if len(w.world.stopped) != 1 {
		t.Fatalf("the unit made for a refused entry stays: stopped=%v", w.world.stopped)
	}
}

// A commit whose outcome cannot be read is UNKNOWN: marked so, the unit stays, and a caller that made a boss keeps it.
func TestEnterWithAnUnreadableCommitOutcomeIsUnknownAndTearsNothingDown(t *testing.T) {
	_, err, w := enterWith(t, commitStore{really: true, getRunErr: errors.New("database is locked")})
	if err == nil || !errors.Is(err, ErrEntryUnknown) || !strings.Contains(err.Error(), " started but ") {
		t.Fatalf("err = %v", err)
	}
	if len(w.world.stopped) != 0 {
		t.Fatalf("a unit of a run that may exist was torn down: %v", w.world.stopped)
	}
}

// ... and through the chat start: the boss is kept.
func TestChatStartKeepsTheBossWhenTheEntryOutcomeIsUnknown(t *testing.T) {
	c := newChatRig(t, guiCharter)
	c.eng.enterErr = unknownEntry(errors.New("the run may have started but the commit could not be read"))
	_, err := c.start(t)
	if err == nil || len(c.chat.ended) != 0 {
		t.Fatalf("err=%v ended=%v", err, c.chat.ended)
	}
}
