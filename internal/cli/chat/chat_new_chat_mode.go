package chat

import (
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai"
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

// `coder sessions chat-modes --access LEVEL`: the three choices of the mode selector at an access level, for a chat
// that does not exist yet (the selector is shown before the first message). Reading; it changes nothing.
func newCoderSessionsChatModesCmd(cfg *Deps) *cobra.Command {
	var access string
	cmd := &cobra.Command{
		Use:   "chat-modes",
		Short: "Show which chat modes (off, mission, open) can be chosen at an access level",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !ai.ValidAccess(access) {
				return clicore.InvalidInput("--access is scoped, coding, workspace or full")
			}
			return printQueueJSON(cmd, map[string]any{"access": access, "options": chatModeOptions(access)})
		},
	}
	cmd.Flags().StringVar(&access, "access", ai.AccessFull, "scoped, coding, workspace or full")
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}
