package chat

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/tasks"
)

// refuseBossConversation is the ordinary send's refusal for the conversation of a boss: a person's turn typed there would
// reach the boss unwrapped, as if it were the charter's own words. The way in is `coder sessions steer` (marked, untrusted
// guidance) and the one first turn the start flow queues (QueueBossFirstTurn).
func refuseBossConversation(ctx context.Context, cfg *Deps, profileID, conversationID string) error {
	db, err := cfg.OpenDB()
	if err != nil {
		return err
	}
	defer db.Close()
	front, _, ok, err := tasks.BossFrontDesk(ctx, db.DB, profileID, conversationID)
	if err != nil {
		return err
	}
	if ok {
		return clicore.InvalidInput("conversation %s is a boss's: a message to it goes through `coder sessions steer %s`, as marked guidance", conversationID, front)
	}
	return nil
}

// enqueueBounded queues a send under the bounds of coderhost.EnqueueSendBounded and starts the host.
func enqueueBounded(cfg *Deps, c coderhost.Command, maxPending int, within time.Duration) (coderhost.Command, bool, error) {
	db, err := cfg.OpenDB()
	if err != nil {
		return c, false, err
	}
	out, deduped, err := coderhost.NewStore(db.DB).EnqueueSendBounded(c, maxPending, within)
	db.Close()
	if err != nil {
		return out, deduped, err
	}
	if !deduped {
		if err := ensureHost(cfg); err != nil {
			return out, false, err
		}
	}
	return out, deduped, nil
}

// refuseUnqueuedBossTurn is the guard of a turn run straight through `chat --conversation C --turn T`: in the conversation of a boss
// only a turn the host launches is run, that is, one whose id AND prompt are those of a send queued for the conversation
// (the steer's, the first turn's). Anything else (an agent's own `chat` call, the extension's) is refused before the turn exists.
// Another conversation is not asked. It does not rely on anything the caller says about itself.
func refuseUnqueuedBossTurn(ctx context.Context, cfg *Deps, profileID, conversationID, turnID, prompt string) error {
	db, err := cfg.OpenDB()
	if err != nil {
		return err
	}
	defer db.Close()
	front, _, ok, err := tasks.BossFrontDesk(ctx, db.DB, profileID, conversationID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	var one int
	err = db.DB.QueryRowContext(ctx, `SELECT 1 FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND kind = 'send' AND turn_id = ? AND message = ? LIMIT 1`,
		profileID, conversationID, turnID, prompt).Scan(&one)
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return clicore.InvalidInput("conversation %s is a boss's: a turn there runs only from a queued message (`coder sessions steer %s`)", conversationID, front)
	}
	return err
}
