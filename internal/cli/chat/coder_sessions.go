package chat

import (
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// `coder sessions`: the persistent view of coder conversations and the way to
// drive them from a terminal, the app or (later) another session. send, stop,
// pause and resume are the operator's: an agent can't start a full-access turn.

func newCoderSessionsCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "Coder sessions that keep running after the app closes",
		Long: "A session is a coder conversation the coder host carries on: `send` queues a message (the host starts " +
			"the turn, or waits until the running one ends), `stop` ends the running turn, `pause` keeps new turns " +
			"from starting and `resume` lifts that. Turns run unattended: the access level of the conversation is the " +
			"only limit, so `list` shows it.",
	}
	cmd.AddCommand(newCoderSessionsListCmd(cfg), newCoderSessionsSendCmd(cfg), newCoderSessionsCommandCmd(cfg),
		newCoderSessionsControlCmd(cfg, coderhost.KindStop, "Stop the session's running turn"),
		newCoderSessionsControlCmd(cfg, coderhost.KindPause, "Start no new turn until resumed (a running turn finishes)"),
		newCoderSessionsControlCmd(cfg, coderhost.KindResume, "Lift a pause"))
	return cmd
}

type sessionRow struct {
	coderhost.Session
	Access  string `json:"access"`
	Cwd     string `json:"cwd"`
	Waiting int    `json:"waitingCommands"`
}

func newCoderSessionsListCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the sessions the host knows, most recently changed first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, profileID, closeDB, err := openChatHistory(cfg)
			if err != nil {
				return err
			}
			defer closeDB()
			db, err := cfg.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			cs := coderhost.NewStore(db.DB)
			sessions, err := cs.ListSessions(profileID)
			if err != nil {
				return err
			}
			waiting := map[string]int{}
			pending, err := cs.Pending()
			if err != nil {
				return err
			}
			for _, c := range pending {
				waiting[c.ProfileID+"\x00"+c.ConversationID]++
			}
			rows := []sessionRow{}
			for _, s := range sessions {
				r := sessionRow{Session: s, Waiting: waiting[s.ProfileID+"\x00"+s.ConversationID]}
				if conv, err := store.GetConversation(s.ConversationID, profileID); err == nil {
					r.Access, r.Cwd = string(conv.Access), conv.Cwd
				}
				rows = append(rows, r)
			}
			if cfg.JSONOutput() {
				return printQueueJSON(cmd, map[string]any{"host": hostStatus(cfg), "sessions": rows})
			}
			for _, r := range rows {
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %-12s access=%s unattended=%v waiting=%d  %s\n", r.ConversationID, r.State, r.Access, r.Unattended, r.Waiting, r.Cwd)
			}
			return nil
		},
	}
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}

// coderConversation checks that id is a coder conversation of the profile.
func coderConversation(cfg *Deps, id string) (string, error) {
	store, profileID, closeDB, err := openChatHistory(cfg)
	if err != nil {
		return "", err
	}
	defer closeDB()
	conv, err := store.GetConversation(id, profileID)
	if err != nil {
		return "", chatStoreErr(err)
	}
	if conv.Scoped() {
		return "", clicore.InvalidInput("conversation %s is not a coder conversation", id)
	}
	return profileID, nil
}

// enqueue queues one command for the host, starting the host first when asked.
func enqueue(cfg *Deps, c coderhost.Command, startHost bool) (coderhost.Command, error) {
	db, err := cfg.OpenDB()
	if err != nil {
		return c, err
	}
	defer db.Close()
	out, err := coderhost.NewStore(db.DB).Enqueue(c)
	if err != nil {
		return c, clicore.InvalidInput("%v", err)
	}
	if startHost {
		if err := ensureHost(cfg); err != nil {
			return out, err
		}
	}
	return out, nil
}

func newCoderSessionsSendCmd(cfg *Deps) *cobra.Command {
	var turn string
	var attach []string
	var noHost bool
	cmd := &cobra.Command{
		Use:   "send CONVERSATION MESSAGE",
		Short: "Queue a message; the host starts the turn when the session is free",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := clicore.RequireOperator(cfg.RequireOperator, "send to a coder session, which starts a full-access turn"); err != nil {
				return err
			}
			profileID, err := coderConversation(cfg, args[0])
			if err != nil {
				return err
			}
			atts, err := attachmentSources(attach)
			if err != nil {
				return err
			}
			if turn == "" {
				turn = "t-" + uuid.NewString()
			}
			c, err := enqueue(cfg, coderhost.Command{ProfileID: profileID, ConversationID: args[0], Kind: coderhost.KindSend, TurnID: turn, Message: args[1], Attachments: atts}, !noHost)
			if err != nil {
				return err
			}
			return printQueueJSON(cmd, map[string]any{"commandId": c.ID, "conversationId": c.ConversationID, "turnId": c.TurnID, "hostStarted": !noHost})
		},
	}
	cmd.Flags().StringVar(&turn, "turn", "", "Turn id (default: a new one)")
	cmd.Flags().StringArrayVar(&attach, "attach", nil, "A file to attach to the turn (repeatable)")
	cmd.Flags().BoolVar(&noHost, "no-host", false, "Queue only; do not start the host")
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}

func newCoderSessionsControlCmd(cfg *Deps, kind coderhost.Kind, short string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   string(kind) + " CONVERSATION",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := clicore.RequireOperator(cfg.RequireOperator, string(kind)+" a coder session"); err != nil {
				return err
			}
			profileID, err := coderConversation(cfg, args[0])
			if err != nil {
				return err
			}
			c, err := enqueue(cfg, coderhost.Command{ProfileID: profileID, ConversationID: args[0], Kind: kind}, true)
			if err != nil {
				return err
			}
			return printQueueJSON(cmd, map[string]any{"commandId": c.ID, "conversationId": c.ConversationID, "kind": kind})
		},
	}
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}

// newCoderSessionsCommandCmd reports how one queued command ended: the app asks
// it for a send whose turn never appeared, to show the reason.
func newCoderSessionsCommandCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "command ID",
		Short: "Show a queued command: pending, done, or failed with the reason",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || id <= 0 {
				return clicore.InvalidInput("%q is not a command id", args[0])
			}
			db, err := cfg.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			c, ok, err := coderhost.NewStore(db.DB).GetCommand(id)
			if err != nil {
				return err
			}
			if !ok || c.ProfileID != cfg.ProfileID() {
				return clicore.NotFound("no command %d", id)
			}
			c.Message, c.Attachments = "", nil // the message is the caller's own; the status is what is asked for
			return printQueueJSON(cmd, c)
		},
	}
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}
