package chat

import (
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/dynorg"
)

// checkNewChatMode refuses, before the conversation exists, a mode `chat history create --chat-mode` cannot give it:
// not one of the three, or mission/open below the access a mission needs, or open while it is not available.
func checkNewChatMode(mode, access string) error {
	if mode == "" || mode == coderhost.ChatModeOff {
		return nil
	}
	if !coderhost.ValidChatMode(mode) {
		return clicore.InvalidInput("--chat-mode is off, mission or open")
	}
	if !dynorg.MissionAccess(access) {
		return clicore.InvalidInput("%s", missionNeedsAccess(access))
	}
	if mode == coderhost.ChatModeOpen {
		if ok, reason := coderhost.OpenModeAvailability(); !ok {
			return clicore.InvalidInput("the open-ended chat mode is %s", reason)
		}
	}
	return nil
}

// setNewChatMode stores the mode of a conversation that was just created, by the same write every other change of
// the mode goes through. If it cannot be stored the empty conversation is removed again, so a create that asked for a
// mode never leaves behind a chat that does not have it.
func setNewChatMode(cfg *Deps, conversationID, mode string) error {
	if mode == "" || mode == coderhost.ChatModeOff {
		return nil
	}
	store, profileID, closeDB, err := openChatHistory(cfg)
	if err != nil {
		return err
	}
	defer closeDB()
	db, err := cfg.OpenDB()
	if err != nil {
		_ = store.DeleteConversation(conversationID, profileID)
		return err
	}
	defer db.Close()
	if err := coderhost.NewStore(db.DB).SetChatMode(profileID, conversationID, mode); err != nil {
		_ = store.DeleteConversation(conversationID, profileID)
		return clicore.InvalidInput("%v", err)
	}
	return nil
}
