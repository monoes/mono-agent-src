package chat

import (
	"context"

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

// enqueueBounded queues a person's send under the bounds of coderhost.EnqueueSendBounded and starts the host.
func enqueueBounded(cfg *Deps, c coderhost.Command, b coderhost.SendBounds) (coderhost.Command, bool, error) {
	db, err := cfg.OpenDB()
	if err != nil {
		return c, false, err
	}
	out, deduped, err := coderhost.NewStore(db.DB).EnqueueSendBounded(c, b)
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

// refuseBossTurnWithoutHostIdentity is the guard of a turn run straight through `chat --conversation C --turn T`: in the
// conversation of a boss only the HOST's own launch of the turn is run. The proof is the session token the host minted for
// exactly this conversation and turn when it launched it (hashed in the database, bound to the turn's process, live only while
// the turn is the session's current one): the caller must present it and it must name C and T. It is not the text of the prompt
// (the host rewrites that), nor a row that exists (a done, failed or ask row would do), and an agent's own `chat` call has no such
// token. Another conversation is not asked.
func refuseBossTurnWithoutHostIdentity(ctx context.Context, cfg *Deps, profileID, conversationID, turnID string) error {
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
	caller, err := coderhost.NewStore(db.DB).CallerFor(sessionTokenFromEnv(), profileID)
	if err == nil && caller.ConversationID == conversationID && caller.TurnID == turnID {
		return nil
	}
	return clicore.InvalidInput("conversation %s is a boss's: a turn there runs only as the host launches it from a queued message (`coder sessions steer %s`)", conversationID, front)
}

// refuseBossResume refuses `chat --resume <runtime session id>` (a turn outside any conversation) when the id is the runtime
// session of a boss's conversation: resuming it would talk to the boss unwrapped, outside the host.
func refuseBossResume(ctx context.Context, cfg *Deps, profileID, resume string) error {
	if resume == "" {
		return nil
	}
	db, err := cfg.OpenDB()
	if err != nil {
		return err
	}
	defer db.Close()
	var n int
	if err := db.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_chat_conversations c WHERE c.profile_id = ? AND c.session_id = ? AND `+tasks.BossAnyConversationSQL("c"), profileID, resume).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return clicore.InvalidInput("that runtime session is a boss's: it cannot be resumed from outside the host")
	}
	return nil
}
