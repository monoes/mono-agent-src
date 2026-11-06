package chat

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/tasks"
)

// `coder list`: the coder conversations that ran a turn, for the browser
// extension's side panel (bridge request coder.list). startedInDesktop
// says whether the side panel may send follow-ups (coder.send): only to a
// conversation whose first turn the person started in the desktop app.
//
// Who started a turn is its owner instance id, with no extra column or
// migration: the app's chat supervisor passes `--instance <uuid>`
// (wails-app/app_chat.go), the extension bridge `--instance ext-…`, and a
// terminal none. So a finished turn owned by a UUID is a desktop turn. This
// is a courtesy check, not a barrier: anything that can run `chat` can pass
// a UUID of its own, and a full-access agent can do that. It keeps the side
// panel from starting what the person never started in the app.

type coderChatRow struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	Cwd              string `json:"cwd"`
	Runtime          string `json:"runtime"`
	Model            string `json:"model"`
	StartedInDesktop bool   `json:"startedInDesktop"`
	LastTurnAt       string `json:"lastTurnAt"`
	LastTurnID       string `json:"lastTurnId"`
	Running          bool   `json:"running"`
	Boss             bool   `json:"boss,omitempty"` // the conversation of a run's recorded boss
}

const coderTitleMax = 80

// isDesktopInstance reports whether owner is an app instance id (a UUID).
func isDesktopInstance(owner string) bool {
	if len(owner) != 36 {
		return false
	}
	_, err := uuid.Parse(owner)
	return err == nil
}

// coderChat summarizes conv from all of its turns; ok is false when it has
// none.
func coderChat(store *ai.AIStore, profileID string, conv ai.Conversation) (coderChatRow, bool, error) {
	row := coderChatRow{ID: conv.ID, Cwd: conv.Cwd, Runtime: conv.RuntimeID, Model: conv.Model}
	cursor, first := "", ""
	for {
		turns, next, err := store.ListTurns(conv.ID, profileID, cursor, 200)
		if err != nil {
			return row, false, err
		}
		for _, t := range turns {
			if row.LastTurnID == "" {
				row.LastTurnID, row.LastTurnAt = t.ID, t.UpdatedAt
			}
			if t.Status == "active" {
				row.Running = true
			} else if isDesktopInstance(t.OwnerInstanceID) {
				row.StartedInDesktop = true
			}
			first = t.Prompt
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if row.LastTurnID == "" {
		return row, false, nil
	}
	row.Title = strings.TrimSpace(firstLineOf(strings.TrimSpace(first)))
	if utf8.RuneCountInString(row.Title) > coderTitleMax {
		row.Title = string([]rune(row.Title)[:coderTitleMax]) + "…"
	}
	return row, true, nil
}

func newCoderListCmd(cfg *Deps) *cobra.Command {
	var conversation string
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List coder conversations that ran a turn, most recent first",
		Long: "Each row says whether the conversation's turn is running and whether one of its turns was started " +
			"in the desktop app (startedInDesktop): the browser extension may send follow-ups only to those.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, profileID, closeDB, err := openChatHistory(cfg)
			if err != nil {
				return err
			}
			defer closeDB()
			bdb, err := cfg.OpenDB()
			if err != nil {
				return err
			}
			defer bdb.Close()
			isBoss := func(id string) bool { b, _ := tasks.IsBossConversation(cmd.Context(), bdb.DB, profileID, id); return b }
			rows := []coderChatRow{}
			if conversation != "" {
				conv, err := store.GetConversation(conversation, profileID)
				if err != nil {
					return chatStoreErr(err)
				}
				if conv.Scoped() {
					return clicore.InvalidInput("conversation %s is not a coder conversation", conversation)
				}
				row, _, err := coderChat(store, profileID, conv)
				if err != nil {
					return err
				}
				row.Boss = isBoss(conv.ID)
				rows = append(rows, row)
			} else if rows, err = coderChats(store, profileID, limit, isBoss); err != nil {
				return err
			}
			if cfg.JSONOutput() {
				return printQueueJSON(cmd, map[string]any{"conversations": rows})
			}
			for _, r := range rows {
				fmt.Fprintf(cmd.OutOrStdout(), "%s  desktop=%v running=%v  %s  %s\n", r.ID, r.StartedInDesktop, r.Running, r.Cwd, r.Title)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&conversation, "conversation", "", "Only this conversation (it may have no turns yet)")
	cmd.Flags().IntVar(&limit, "limit", 20, "Maximum conversations (1-100)")
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}

// coderChats walks the profile's conversations, newest first, for the
// first limit coder ones that ran a turn.
func coderChats(store *ai.AIStore, profileID string, limit int, isBoss func(conversationID string) bool) ([]coderChatRow, error) {
	if limit <= 0 || limit > 100 {
		return nil, clicore.InvalidInput("--limit must be between 1 and 100")
	}
	rows := []coderChatRow{}
	cursor := ""
	for {
		convs, next, err := store.ListConversations(profileID, cursor, 200)
		if err != nil {
			return nil, err
		}
		for _, c := range convs {
			if c.Scoped() {
				continue
			}
			row, ok, err := coderChat(store, profileID, c)
			if err != nil {
				return nil, err
			}
			row.Boss = isBoss(c.ID)
			if ok {
				rows = append(rows, row)
				if len(rows) == limit {
					return rows, nil
				}
			}
		}
		if next == "" {
			return rows, nil
		}
		cursor = next
	}
}
