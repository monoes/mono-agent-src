package chat

import (
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// CreateOpenChat makes the coder chat an open-ended run stands behind, in the open chat mode at full access, working in cwd.
// It is `chat history create --access full --chat-mode open --cwd cwd` for `doomsday run`: the same checks (operator, mode
// availability) apply, and it returns the new conversation's id.
func CreateOpenChat(cmd *cobra.Command, cfg *Deps, runtimeID, model, cwd string) (string, error) {
	if err := checkNewChatMode(coderhost.ChatModeOpen, ai.AccessFull); err != nil {
		return "", err
	}
	conv, err := createCoderConversation(cmd, cfg, ai.AccessFull, runtimeID, model, "", "general", coderhost.ChatModeOpen, coderFolderChoice{cwd: cwd})
	if err != nil {
		return "", err
	}
	// the open-ended boss needs the spawn tools, which only a Dynamic org turn has
	if _, err := setOrgMode(cfg, conv.ID, ai.OrgModeDynamic); err != nil {
		return "", err
	}
	return conv.ID, nil
}
