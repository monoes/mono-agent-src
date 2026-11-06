package chat

import (
	"context"

	"github.com/monoes/mono-agent/internal/cli/clicore"
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
