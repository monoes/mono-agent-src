package chat

import (
	"context"
	"fmt"

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

// guardMissionToggle refuses a change of the flag the rules do not allow: on needs
// mission access, off needs no open mission session.
func guardMissionToggle(ctx context.Context, store *tasks.Store, profileID string, conv ai.Conversation, on bool) error {
	if on {
		if access := conv.EffectiveAccess(); !dynorg.MissionAccess(access) {
			return clicore.InvalidInput("%s", missionNeedsAccess(access))
		}
		return nil
	}
	id, err := missionOpenSessionID(ctx, store, profileID, conv.ID)
	if err != nil {
		return clicore.InvalidInput("%s", missionUnreadableMessage(err))
	}
	if id != 0 {
		return clicore.InvalidInput("%s", missionLockedMessage(id))
	}
	return nil
}

// applyMissionToggle checks a request to set the flag: turning it on when it is on
// needs no check; anything else must pass guardMissionToggle. Turning it off is
// checked even when the stored flag is already off, because an open mission session
// (a mission started by the card) reads as on.
func applyMissionToggle(cfg *Deps, profileID string, cur, next coderhost.Settings, conversationID string) error {
	if cur.Mission && next.Mission {
		return nil
	}
	chat, _, closeDB, err := openChatHistory(cfg)
	if err != nil {
		return err
	}
	conv, err := chat.GetConversation(conversationID, profileID)
	closeDB()
	if err != nil {
		return chatStoreErr(err)
	}
	db, err := cfg.OpenDB()
	if err != nil {
		return err
	}
	defer db.Close()
	return guardMissionToggle(context.Background(), tasks.NewStore(db.DB), profileID, conv, next.Mission)
}
