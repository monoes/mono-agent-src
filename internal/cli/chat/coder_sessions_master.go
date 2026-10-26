package chat

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// The master side of `coder sessions`: a coder turn the host started carries a
// session token (coderhost.SessionTokenEnv) naming its session. With a token set,
// send, stop, pause, resume and `settings set` act only on descendants of that
// session, `spawn` creates a child, and `status` reads the session's own tree. A token
// that does not verify is a refusal, never a fall-back to the operator path (which an
// agent cannot pass anyway). Without a token these verbs stay the operator's.

// masterCall is an open store and the verified caller.
type masterCall struct {
	store  *coderhost.Store
	caller coderhost.Caller
	close  func()
}

// sessionTokenFromEnv is the token this process holds, "" when none.
func sessionTokenFromEnv() string { return os.Getenv(coderhost.SessionTokenEnv) }

// openMasterCall verifies the token. The profile of the call is the one the CLI
// runs under (--profile); a token for another profile does not verify.
func openMasterCall(cfg *Deps) (*masterCall, error) {
	db, err := cfg.OpenDB()
	if err != nil {
		return nil, err
	}
	store := coderhost.NewStore(db.DB)
	caller, err := store.CallerFor(sessionTokenFromEnv(), cfg.ProfileID())
	if err != nil {
		db.Close()
		return nil, masterErr(err)
	}
	return &masterCall{store: store, caller: caller, close: func() { db.Close() }}, nil
}

// masterErr maps a master-tool error to a CLI error: the sentinel decides the code.
func masterErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, coderhost.ErrSessionToken):
		return clicore.AuthConnection("%v", err)
	case errors.Is(err, coderhost.ErrNotYours):
		return clicore.NotFound("%v", err)
	case errors.Is(err, coderhost.ErrOperatorOnly):
		return clicore.OperatorOnly("%v", err)
	case errors.Is(err, coderhost.ErrLimit), errors.Is(err, coderhost.ErrAccess), errors.Is(err, coderhost.ErrInvalid), errors.Is(err, coderhost.ErrQueueFull):
		return clicore.InvalidInput("%v", err)
	}
	return err
}

// queuedJSON prints what a master verb queued. The host is not started from here:
// the caller's own turn runs under a host, and starting one from an agent's shell
// would hand it the agent's environment.
func queuedJSON(cmd *cobra.Command, c coderhost.Command) error {
	return printQueueJSON(cmd, map[string]any{"commandId": c.ID, "conversationId": c.ConversationID, "turnId": c.TurnID, "kind": c.Kind})
}

func newCoderSessionsSpawnCmd(cfg *Deps) *cobra.Command {
	var message, access, cwd string
	var budget float64
	cmd := &cobra.Command{
		Use:   "spawn",
		Short: "Create a child session of this master session (needs the session token of a master's turn)",
		Long: "Creates a coder session below the calling master: same runtime, model and folder (or a folder inside it), " +
			"access at most the master's and never full, a budget carved out of the master's remaining one. It is limited by " +
			"the master's max_children and max_depth and by a spawn rate. --message queues the child's first message.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if sessionTokenFromEnv() == "" {
				return clicore.OperatorOnly("spawn needs the session token of a master's turn (%s); sessions are linked by the operator with `sessions settings set --role`", coderhost.SessionTokenEnv)
			}
			mc, err := openMasterCall(cfg)
			if err != nil {
				return err
			}
			defer mc.close()
			out, err := mc.store.Spawn(mc.caller, coderhost.SpawnRequest{Message: message, Access: access, Cwd: cwd, BudgetUSD: budget})
			if err != nil {
				return masterErr(err)
			}
			return printQueueJSON(cmd, out)
		},
	}
	cmd.Flags().StringVar(&message, "message", "", "The child's first message")
	cmd.Flags().StringVar(&access, "access", "", "coding or workspace, at most the master's (default: the master's, capped at workspace)")
	cmd.Flags().StringVar(&cwd, "cwd", "", "A folder inside the master's (default: the master's)")
	cmd.Flags().Float64Var(&budget, "budget-usd", 0, "Spend cap for the child, carved out of the master's remaining budget")
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}

func newCoderSessionsStatusCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status [CONVERSATION]",
		Short: "A master's view of its own session or a descendant; without a conversation, the whole subtree",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if sessionTokenFromEnv() == "" {
				return clicore.OperatorOnly("status needs the session token of a coder turn (%s); the operator uses `sessions list`", coderhost.SessionTokenEnv)
			}
			mc, err := openMasterCall(cfg)
			if err != nil {
				return err
			}
			defer mc.close()
			if len(args) == 0 {
				kids, err := mc.store.ListChildren(mc.caller)
				if err != nil {
					return masterErr(err)
				}
				return printQueueJSON(cmd, map[string]any{"sessions": kids})
			}
			st, err := mc.store.StatusOf(mc.caller, args[0])
			if err != nil {
				return masterErr(err)
			}
			return printQueueJSON(cmd, st)
		},
	}
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}

// masterSend, masterControl and masterSet are the token paths of the verbs.
func masterSend(cfg *Deps, cmd *cobra.Command, target, message string, attach []string, mission bool) error {
	if mission || len(attach) > 0 {
		return clicore.OperatorOnly("a master's send carries no mission confirmation and no attachment")
	}
	mc, err := openMasterCall(cfg)
	if err != nil {
		return err
	}
	defer mc.close()
	c, err := mc.store.SendToChild(mc.caller, target, message)
	if err != nil {
		return masterErr(err)
	}
	return queuedJSON(cmd, c)
}

func masterControl(cfg *Deps, cmd *cobra.Command, target string, kind coderhost.Kind) error {
	mc, err := openMasterCall(cfg)
	if err != nil {
		return err
	}
	defer mc.close()
	c, err := mc.store.ControlChild(mc.caller, target, kind)
	if err != nil {
		return masterErr(err)
	}
	return queuedJSON(cmd, c)
}

func masterSet(cfg *Deps, cmd *cobra.Command, target, patch string, others bool) error {
	if others || patch == "" {
		return clicore.OperatorOnly("a master changes only agent-adjustable settings, with --patch; role, parent, unattended and budget are the operator's")
	}
	mc, err := openMasterCall(cfg)
	if err != nil {
		return err
	}
	defer mc.close()
	next, keys, err := mc.store.SetChild(mc.caller, target, []byte(patch))
	if err != nil {
		return masterErr(err)
	}
	return printQueueJSON(cmd, map[string]any{"conversationId": target, "changed": keys, "settings": next, "agentAdjustable": coderhost.AgentAdjustable})
}

// visibleToToken is the filter a session token puts on the read verbs: with a token, a function that
// says whether a conversation is the caller's own session or one of its descendants (a bad token is a
// refusal). Without a token the caller must be the operator (the same check the operator verbs use):
// an agent that has no token, or has lost it, reads nothing, so dropping the variable is no way
// around the filter. The operator's terminal sees everything (nil filter).
func visibleToToken(cfg *Deps) (func(conv string) bool, error) {
	if sessionTokenFromEnv() == "" {
		if err := clicore.RequireOperator(cfg.RequireOperator, "read coder sessions without a session token"); err != nil {
			return nil, err
		}
		return nil, nil
	}
	mc, err := openMasterCall(cfg)
	if err != nil {
		return nil, err
	}
	defer mc.close()
	own, store := mc.caller, mc.store
	// the answers are read now, from the tree as it is, with the database open
	kids, err := store.ListChildren(own)
	if err != nil {
		return nil, masterErr(err)
	}
	seen := map[string]bool{own.ConversationID: true}
	for _, k := range kids {
		seen[k.ConversationID] = true
	}
	return func(conv string) bool { return seen[conv] }, nil
}
