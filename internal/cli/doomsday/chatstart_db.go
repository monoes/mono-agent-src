package doomsday

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/monoes/mono-agent/internal/tasks"
)

// DBChatStart is the ChatStarter over the profile's database. The first turn is not this package's to queue (it belongs to the
// chat commands): Queue is handed in by the CLI wiring.
type DBChatStart struct {
	Open    func() (*sql.DB, func(), error)
	Profile func() string
	Queue   func(ctx context.Context, bossConversation string, in FirstTurn) error
}

// MakeBoss is makeBoss: it reuses the chat's unstamped boss, so a start repeated after a dead one makes no second.
func (s DBChatStart) MakeBoss(ctx context.Context, chat, goal string) (BossRef, error) {
	db, closeDB, err := s.Open()
	if err != nil {
		return BossRef{}, err
	}
	defer closeDB()
	return makeBoss(ctx, db, s.Profile(), strings.TrimSpace(chat), goal)
}

// SetBossBudget sets the exact budget, 0 included: a reused boss must not keep the ceiling of an earlier attempt.
func (s DBChatStart) SetBossBudget(ctx context.Context, session int64, usd float64) error {
	db, closeDB, err := s.Open()
	if err != nil {
		return err
	}
	defer closeDB()
	_, err = tasks.NewStore(db).UpdateSession(ctx, s.Profile(), session, tasks.SessionUpdate{BudgetUSD: &usd}, bossActor)
	return err
}

// EndBoss stops the boss session and archives it, by the store's own guarded paths (no raw write). A session that is already
// stopped or archived is not an error, so a repeated cleanup is safe.
func (s DBChatStart) EndBoss(ctx context.Context, session int64) error {
	db, closeDB, err := s.Open()
	if err != nil {
		return err
	}
	defer closeDB()
	ts := tasks.NewStore(db)
	if _, err := ts.StopSession(ctx, s.Profile(), session, bossActor); err != nil && !errors.Is(err, tasks.ErrNotReady) {
		return err
	}
	se, err := ts.GetSession(ctx, s.Profile(), session, bossActor)
	if err != nil {
		return err
	}
	if se.Archived {
		return nil
	}
	_, err = ts.ArchiveSession(ctx, s.Profile(), session, bossActor)
	return err
}

// QueueFirstTurn hands the first turn to the chat commands' queue.
func (s DBChatStart) QueueFirstTurn(ctx context.Context, bossConversation string, in FirstTurn) error {
	if s.Queue == nil {
		return errors.New("this install cannot queue the boss's first turn")
	}
	return s.Queue(ctx, bossConversation, in)
}
