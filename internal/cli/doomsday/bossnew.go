package doomsday

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/tasks"
)

// The boss of an open-ended run started from a chat (`boss new`): a sibling of the chat, not the chat. The chat the person
// types in is the front desk and holds no lifted line; the boss is a top-level mission session in a conversation of its
// own (so the chat's own mission session is never shadowed by it) that copies the chat's runtime, folder and access. The
// verb only makes it: the stamp that makes it the run's boss is still `start --boss-session`, the operator's, once.

// BossDeps is what the boss verbs need from the CLI.
type BossDeps struct {
	// Open opens the profile's database; the returned func closes it.
	Open func() (*sql.DB, func(), error)
	// Profile is the profile the verbs act in.
	Profile func() string
	// OperatorGate is the turn-aware refusal every verb that acts asks first (nil error: the user).
	OperatorGate func(what string) error
	// JSON is --json.
	JSON func() bool
}

// BossRef names a boss session and the chat it stands behind.
type BossRef struct {
	SessionID      int64  `json:"session_id"`
	ConversationID string `json:"conversation_id"`
	FrontDesk      string `json:"front_desk"`
	Reused         bool   `json:"reused,omitempty"`
}

// bossActor is the operator, as the task store knows her.
var bossActor = tasks.Actor{Kind: tasks.Human, Name: "operator"}

// BossCommand is the `doomsday boss` group.
func BossCommand(d BossDeps) *cobra.Command {
	cmd := &cobra.Command{Use: "boss", Short: "The boss session of an open-ended run started from a chat (the user's own)"}
	cmd.AddCommand(d.newBossNew())
	return cmd
}

func (d BossDeps) newBossNew() *cobra.Command {
	var front, goal string
	c := &cobra.Command{
		Use:   "new --conversation CHAT",
		Short: "Make the boss session that stands behind a chat (a sibling, in a conversation of its own)",
		Long: "Makes the top-level mission session and the conversation of its own that become the run's boss once " +
			"`doomsday start --boss-session ID` records it. The chat keeps its own mission; the boss copies the chat's runtime, " +
			"folder and access. Repeating the call for the same chat answers with the session that exists. " + clicore.OperatorOnlyHelp,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := d.OperatorGate("make the boss of an open-ended run"); err != nil {
				return err
			}
			db, closeDB, err := d.Open()
			if err != nil {
				return err
			}
			defer closeDB()
			ref, err := makeBoss(cmd.Context(), db, d.Profile(), strings.TrimSpace(front), goal)
			if err != nil {
				return err
			}
			if d.JSON != nil && d.JSON() {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(ref)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "boss session %d in conversation %s stands behind chat %s (record it with `doomsday start --boss-session %d`)\n", ref.SessionID, ref.ConversationID, ref.FrontDesk, ref.SessionID)
			return nil
		},
	}
	c.Flags().StringVar(&front, "conversation", "", "The chat the person types in (a coder chat)")
	c.Flags().StringVar(&goal, "goal", "", "The goal the boss session carries (default: a line naming the chat)")
	_ = c.MarkFlagRequired("conversation")
	clicore.WithJSONErrors(d.JSON, c)
	return c
}

// makeBoss creates the boss conversation and session for the front-desk chat, or returns the ones that exist.
func makeBoss(ctx context.Context, db *sql.DB, profile, front, goal string) (BossRef, error) {
	if front == "" {
		return BossRef{}, clicore.InvalidInput("--conversation names the chat the boss stands behind")
	}
	chats, err := ai.NewAIStore(db)
	if err != nil {
		return BossRef{}, err
	}
	fc, err := chats.GetConversation(front, profile)
	if errors.Is(err, ai.ErrConversationNotFound) {
		return BossRef{}, clicore.NotFound("no chat %s in this profile", front)
	}
	if err != nil {
		return BossRef{}, err
	}
	if fc.Scoped() {
		return BossRef{}, clicore.InvalidInput("chat %s is a scoped chat; an open-ended run needs a coder chat", front)
	}
	key := tasks.BossClientID(front)
	if ref, ok, err := existingBoss(ctx, db, profile, front, key); err != nil || ok {
		return ref, err
	}
	if other, err := otherOpenBoss(ctx, db, profile, key); err != nil {
		return BossRef{}, err
	} else if other != 0 {
		return BossRef{}, clicore.InvalidInput("session %d is already the boss behind another chat: one open-ended run at a time per profile", other)
	}
	bc, err := chats.CreateConversationAccess(profile, "agent", "general", fc.RuntimeID, "", fc.Model, fc.EffectiveAccess(), fc.Cwd, fc.Effort)
	if err != nil {
		return BossRef{}, err
	}
	if goal = strings.TrimSpace(goal); goal == "" {
		goal = "Boss of the open-ended run that chat " + front + " started: pursue the charter's goal inside its limits."
	}
	se, err := tasks.NewStore(db).CreateSession(ctx, profile, tasks.SessionInput{
		Title: "Boss", Goal: goal, ConversationID: bc.ID, Kind: tasks.SessionKindMission, ClientID: key,
	}, bossActor)
	if err != nil {
		return BossRef{}, err
	}
	return BossRef{SessionID: se.ID, ConversationID: bc.ID, FrontDesk: front}, nil
}

// existingBoss answers with the operator's boss session for the chat, when the key already names one. A session under that
// key that is not the operator's mission in a conversation of its own (an agent can link a session to its own chat) is
// refused, never adopted.
func existingBoss(ctx context.Context, db *sql.DB, profile, front, key string) (BossRef, bool, error) {
	var id int64
	var conv sql.NullString
	var kind, by string
	var parent sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT id, conversation_id, kind, created_by, parent_session_id FROM task_sessions WHERE profile_id = ? AND client_id = ?`, profile, key).
		Scan(&id, &conv, &kind, &by, &parent)
	if errors.Is(err, sql.ErrNoRows) {
		return BossRef{}, false, nil
	}
	if err != nil {
		return BossRef{}, false, err
	}
	if kind != tasks.SessionKindMission || by != bossActor.Label() || parent.Int64 != 0 || conv.String == "" || conv.String == front {
		return BossRef{}, false, clicore.InvalidInput("client id %s is used by session %d, which is not a boss: nothing was made", key, id)
	}
	return BossRef{SessionID: id, ConversationID: conv.String, FrontDesk: front, Reused: true}, true, nil
}

// otherOpenBoss is an open boss session the operator made behind another chat (0: none). A session that merely carries a
// boss key in its own chat is not one.
func otherOpenBoss(ctx context.Context, db *sql.DB, profile, mine string) (int64, error) {
	var id int64
	err := db.QueryRowContext(ctx, `SELECT id FROM task_sessions WHERE profile_id = ? AND client_id LIKE ? ESCAPE '\' AND client_id <> ? AND created_by = ?
		AND kind = ? AND archived_at = '' AND state IN (?, ?, ?) AND conversation_id <> substr(client_id, ?) ORDER BY id LIMIT 1`,
		profile, tasks.BossClientIDPrefix+"%", mine, bossActor.Label(), tasks.SessionKindMission,
		string(tasks.SessionDraft), string(tasks.SessionRunning), string(tasks.SessionPaused), len(tasks.BossClientIDPrefix)+1).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// OperatorGate is the gate of every verb of the group that acts (operatorGate): the shared turn-aware refusal, then the
// CLI's own operator check.
func (d Deps) OperatorGate(what string) error { return d.operatorGate(what) }
