package chat

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/tasks"
)

// BossFirstTurn is what the boss is told in its first turn: the run it leads, the charter that bounds it and its goal.
type BossFirstTurn struct {
	RunID       string // the run's id, as `doomsday start` printed it
	CharterHash string // the full hash of the entered charter
	Goal        string // the charter's goal
}

// BossFirstTurnText is the first message the boss receives: its role, where the charter is, that there is nobody to ask, and
// the steering contract. The start flow queues it with QueueBossFirstTurn.
func BossFirstTurnText(in BossFirstTurn) string {
	hash := in.CharterHash
	if len(hash) > 8 {
		hash = hash[:8]
	}
	return "You are the boss of an open-ended run (run " + strings.TrimSpace(in.RunID) + ", charter " + hash + "). The charter bounds you: read it first " +
		"(`monoagentcli doomsday overview`) and act only inside the limits it lifted. Your goal: " + strings.TrimSpace(in.Goal) + "\n\n" +
		"There is no person to ask: questions that would go to a person go to you, and you answer, approve and grant only through your boss tools, inside the charter. " +
		"Steer your own work with the tools of your session; do not wait for a reply from the person.\n\n" +
		"The person may type in the chat that started this run. You receive that as a message headed [OPERATOR STEERING]. " + steerContract
}

// QueueBossFirstTurn queues the boss's first turn for the conversation of a stamped boss that has had no turn and has none
// waiting, and starts the host. It is the only way a person's turn is sent to a boss's conversation besides the steer.
func QueueBossFirstTurn(cfg *Deps, bossConversation string, in BossFirstTurn) (coderhost.Command, error) {
	if strings.TrimSpace(in.RunID) == "" || strings.TrimSpace(in.CharterHash) == "" || strings.TrimSpace(in.Goal) == "" {
		return coderhost.Command{}, clicore.InvalidInput("the first turn needs the run id, the charter hash and the goal")
	}
	profileID, err := coderConversation(cfg, bossConversation)
	if err != nil {
		return coderhost.Command{}, err
	}
	db, err := cfg.OpenDB()
	if err != nil {
		return coderhost.Command{}, err
	}
	_, stamped, ok, err := tasks.BossFrontDesk(context.Background(), db.DB, profileID, bossConversation)
	waiting := coderhost.NewStore(db.DB).PendingFor(profileID, bossConversation)
	db.Close()
	if err != nil {
		return coderhost.Command{}, err
	}
	if !ok || !stamped {
		return coderhost.Command{}, clicore.InvalidInput("conversation %s is not the conversation of a recorded boss", bossConversation)
	}
	if had, err := bossHadATurn(cfg, profileID, bossConversation); err != nil {
		return coderhost.Command{}, err
	} else if had || waiting > 0 {
		return coderhost.Command{}, clicore.InvalidInput("the boss already had its first turn, or one is waiting: nothing was queued")
	}
	c, err := enqueue(cfg, coderhost.Command{ProfileID: profileID, ConversationID: bossConversation, Kind: coderhost.KindSend,
		TurnID: "t-" + uuid.NewString(), Message: BossFirstTurnText(in)}, true)
	if err != nil {
		return c, fmt.Errorf("queueing the boss's first turn: %w", err)
	}
	return c, nil
}

// bossHadATurn says whether the conversation has any turn yet.
func bossHadATurn(cfg *Deps, profileID, conversation string) (bool, error) {
	store, _, closeDB, err := openChatHistory(cfg)
	if err != nil {
		return false, err
	}
	defer closeDB()
	turns, _, err := store.ListTurns(conversation, profileID, "", 1)
	if err != nil {
		return false, chatStoreErr(err)
	}
	return len(turns) > 0, nil
}
