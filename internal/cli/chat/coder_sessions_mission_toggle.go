package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/tasks"
)

// The Mission toggle: `coder sessions settings set CONVERSATION --mission on|off`.
// On, every turn of the conversation is a confirmed mission from its first message
// (the operator's switch replaces the mission_propose click). Only the operator sets
// it, and while the conversation has an open mission session it stays on.

// missionToggleView is what `settings get` says about the toggle. Locked is the
// reason On cannot change; Available is false below the access a mission needs.
type missionToggleView struct {
	On        bool   `json:"on"`
	Locked    bool   `json:"locked"`
	SessionID int64  `json:"sessionId,omitempty"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// missionOpenSessionID is the id of the conversation's open mission session
// (draft, running or paused), 0 when it has none.
func missionOpenSessionID(ctx context.Context, store *tasks.Store, profileID, conversationID string) (int64, error) {
	m := &dynorg.MissionConfig{Store: store, ProfileID: profileID, ConversationID: conversationID, Actor: missionActor}
	return m.OpenSessionID(ctx)
}

func missionNeedsAccess(access string) string {
	return fmt.Sprintf("a mission needs coding, workspace or full access; this chat has %s access", access)
}

// missionToggleOf reads the toggle: the stored flag, held on by an open mission session.
func missionToggleOf(ctx context.Context, store *tasks.Store, profileID string, conv ai.Conversation, stored bool) missionToggleView {
	access := conv.EffectiveAccess()
	v := missionToggleView{On: stored, Available: dynorg.MissionAccess(access)}
	if id, err := missionOpenSessionID(ctx, store, profileID, conv.ID); err != nil {
		v.On, v.Locked, v.Reason = true, true, missionUnreadableMessage(err)
	} else if id != 0 {
		v.On, v.Locked, v.SessionID = true, true, id
		v.Reason = missionLockedMessage(id)
	} else if !v.Available {
		v.Reason = missionNeedsAccess(access)
	}
	return v
}

func missionLockedMessage(session int64) string {
	return fmt.Sprintf("this chat is running a mission (session %d): stop or close that session first (`task session stop %d` or `task session close %d`; pausing keeps it open)", session, session, session)
}

// missionUnreadableMessage is why the toggle cannot be turned off when the store cannot say whether a
// mission is open: off would otherwise be allowed while a mission may be running.
func missionUnreadableMessage(err error) string {
	return fmt.Sprintf("the mission state of this chat could not be read, so the toggle stays as it is: %v", err)
}

// guardMissionToggle refuses a change of the flag the rules do not allow: on needs mission access, off needs no
// open mission session. q is the connection of the transaction that stores the change, so the answer holds until
// the change is committed: a mission session cannot be added in between (its creation recheck takes the same
// write lock, see missionCreateGuard).
func guardMissionToggle(ctx context.Context, q tasks.Queryer, profileID string, conv ai.Conversation, on bool) error {
	if on {
		if access := conv.EffectiveAccess(); !dynorg.MissionAccess(access) {
			return clicore.InvalidInput("%s", missionNeedsAccess(access))
		}
		return nil
	}
	id, err := tasks.OpenMissionSessionIn(ctx, q, profileID, conv.ID)
	if err != nil {
		return clicore.InvalidInput("%s", missionUnreadableMessage(err))
	}
	if id != 0 {
		return clicore.InvalidInput("%s", missionLockedMessage(id))
	}
	return nil
}

// missionToggleAfterCheck, when set by a test, runs after a change of the toggle passed its check and before it is stored.
var missionToggleAfterCheck func()

// setSettingsAsOperator applies the operator's change of a conversation's settings (a --patch and/or --mission)
// as one read-modify-write transaction (coderhost.UpdateSettings): the patch is applied to the settings as they
// are inside the transaction, and a change of the Mission toggle is checked there too, on the same connection,
// so no other writer's value is brought back and no mission session can appear between the check and the write.
// It returns the keys the change named.
func setSettingsAsOperator(cfg *Deps, cs *coderhost.Store, profileID, conversationID, patch, mission string) ([]string, error) {
	chat, _, closeDB, err := openChatHistory(cfg)
	if err != nil {
		return nil, err
	}
	conv, err := chat.GetConversation(conversationID, profileID)
	closeDB()
	if err != nil {
		return nil, chatStoreErr(err)
	}
	var keys []string
	_, err = cs.UpdateSettings(profileID, conversationID, func(ctx context.Context, tx *sql.Conn, cur coderhost.Settings) (coderhost.Settings, error) {
		next, k := cur, []string(nil)
		if patch != "" {
			var err error
			if next, k, err = cur.Apply([]byte(patch)); err != nil {
				return cur, clicore.InvalidInput("%v", err)
			}
		}
		if mission != "" {
			next.Mission = mission == "on"
			k = append(k, "mission")
		}
		if slices.Contains(k, "mission") && !(cur.Mission && next.Mission) { // on when it is on needs no check
			if err := guardMissionToggle(ctx, tx, profileID, conv, next.Mission); err != nil {
				return cur, err
			}
			if missionToggleAfterCheck != nil {
				missionToggleAfterCheck()
			}
		}
		keys = k
		return next, nil
	})
	return keys, err
}

// missionCreateGuard is the recheck made inside the transaction that adds a mission session (the planner's
// mission_plan, via tasks.SessionInput.BeforeInsert). A mission that only the toggle confirmed may not start
// once the operator has turned the toggle off: the turn read it as on when it began, and a turn that is still
// running when the operator turns it off must not start a mission after all. A session the operator confirmed
// by hand (send --mission) does not depend on the toggle and is not rechecked. Refusing, not switching the
// toggle on, because the toggle is the operator's: nothing the planner does may set it.
func missionCreateGuard(profileID, conversationID string) func(context.Context, tasks.Queryer) error {
	return func(ctx context.Context, x tasks.Queryer) error {
		var raw string
		err := x.QueryRowContext(ctx, `SELECT settings FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?`, profileID, conversationID).Scan(&raw)
		cur, err := coderhost.ParseSettings(conversationID, raw, err)
		if err != nil {
			return fmt.Errorf("the Mission toggle of this chat could not be read, so no mission starts: %w", err)
		}
		if !cur.Mission {
			return errors.New("the operator turned the Mission toggle off: no mission starts from this turn. Say so and carry on with the request without a mission")
		}
		return nil
	}
}
