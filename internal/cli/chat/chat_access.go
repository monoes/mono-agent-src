package chat

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// A conversation's access level (ai.AccessScoped … ai.AccessFull) is what
// chooses its turn: scoped is the sandboxed assistant (mono-agent's own
// tools, canvas, page read), every other level is a coder turn. The coder
// gate (`coder enable`, per runtime) applies to every level above scoped, at
// creation, at an upgrade and on every turn. Raising a level is the
// operator's; lowering is anyone's.

// forAccess is the settings a turn at access runs under. Coding is told
// nothing about the profile (no summary in prompts, no MonoAgent surfaces for
// a dynamic org's workers); workspace and full follow coder.appAccess. An
// empty access is full.
func (s coderSettings) forAccess(access string) coderSettings {
	if access == ai.AccessCoding {
		off := false
		s.AppAccess = &off
	}
	return s
}

// browserKindFor is the browser-permission kind a conversation's turns run
// as: the chat kind for scoped, the coder kind for every level above it. The
// kind follows the level, never the other way, so a scoped chat does not get
// the coder's shell-side level and a coder chat does not get the chat's
// wider browser one.
func browserKindFor(access string) string {
	if access == ai.AccessScoped {
		return browserctl.KindChat
	}
	return browserctl.KindCoder
}

// resolveCreateAccess is the level `chat history create` makes: --access, or
// what an older --mode stands for (assistant is scoped, coder is full), or
// scoped. Naming both must agree.
func resolveCreateAccess(mode, access string) (string, error) {
	if mode != "" && mode != ai.ModeAssistant && mode != ai.ModeCoder {
		return "", clicore.InvalidInput("unknown --mode %q (assistant or coder; --access says more)", mode)
	}
	if access == "" {
		if mode == "" {
			return ai.AccessScoped, nil
		}
		return ai.AccessForMode(mode), nil
	}
	if !ai.ValidAccess(access) {
		return "", clicore.InvalidInput("unknown --access %q (scoped, coding, workspace or full)", access)
	}
	if mode != "" && ai.ModeForAccess(access) != mode {
		return "", clicore.InvalidInput("--mode %s and --access %s disagree", mode, access)
	}
	return access, nil
}

// exitSurvivors is the exit code of a lowering that left a privileged process
// running (error code survivors_remain). The level is lowered all the same, so
// no new privileged turn can start.
const exitSurvivors = 5

// exitAccessChanged is the exit code of a set-access --expect whose
// conversation no longer has the expected level (error code access_changed,
// with the real level under "current"). Nothing was changed.
const exitAccessChanged = 6

type accessChangedError struct {
	current string
	err     *clicore.Error
}

func (e *accessChangedError) Error() string { return e.err.Error() }
func (e *accessChangedError) Unwrap() error { return e.err }
func (e *accessChangedError) JSONErrorFields() map[string]any {
	return map[string]any{"code": "access_changed", "current": e.current}
}

func errAccessChanged(current, expect string) error {
	return &accessChangedError{current: current, err: &clicore.Error{Code: exitAccessChanged,
		Msg: fmt.Sprintf("the conversation's access is %s, not the expected %s: nothing was changed", current, expect)}}
}

// loweredEffects is what lowering a conversation's access did.
type loweredEffects struct {
	Stopped        []int      `json:"stopped"`         // background processes of its turns that were stopped
	Survivors      []survivor `json:"survivors"`       // recorded processes that were not stopped, with why
	WorkersDropped bool       `json:"workers_dropped"` // its kept org workers were forgotten
	TaskRevoked    bool       `json:"task_revoked"`    // its open AI task's token was replaced
}

// lowerAccessEffects takes the privileges of a conversation that now has less
// access: the processes its turns left running are stopped, its kept
// dynamic-org workers forgotten and its open AI task's token replaced by one
// nobody holds. All of it runs before the next turn is admitted.
func lowerAccessEffects(cfg *Deps, store *ai.AIStore, profileID, conversationID string) (loweredEffects, error) {
	eff := loweredEffects{Stopped: []int{}, Survivors: []survivor{}}
	stop, _, err := stopConversationBackground(store, profileID, conversationID, true)
	if err != nil {
		return eff, err
	}
	eff.Stopped = append(eff.Stopped, stop.Stopped...)
	eff.Survivors = append(eff.Survivors, stop.Survivors...)
	if err := store.ForgetOrgWorkers(profileID, conversationID); err != nil {
		return eff, err
	}
	eff.WorkersDropped = true
	db, err := cfg.OpenDB()
	if err != nil {
		return eff, fmt.Errorf("initializing database: %w", err)
	}
	defer db.Close()
	ts := &aitask.Store{DB: db.DB, Profile: profileID}
	t, ok, err := ts.OpenForConversation(conversationID)
	if err != nil {
		return eff, fmt.Errorf("looking up the conversation's task: %w", err)
	}
	if ok {
		if err := ts.SetToken(t.ID, aitask.NewToken()); err != nil {
			return eff, fmt.Errorf("revoking the task's token: %w", err)
		}
		eff.TaskRevoked = true
	}
	return eff, nil
}

// enforceLoweredAccess runs when a coder turn ends: a conversation whose
// level went down while the turn ran (the turn itself, an agent or the
// operator lowered it) loses what the turn left behind before anything else
// can use it.
func enforceLoweredAccess(cfg *Deps, store *ai.AIStore, profileID string, started ai.Conversation) {
	now, err := store.GetConversation(started.ID, profileID)
	if err != nil || ai.AccessRank(now.EffectiveAccess()) >= ai.AccessRank(started.EffectiveAccess()) {
		return
	}
	_, _ = lowerAccessEffects(cfg, store, profileID, started.ID)
}

// accessChange is `chat history set-access --json`.
type accessChange struct {
	ai.ConversationRecord
	Previous string          `json:"previous_access"`
	Lowered  *loweredEffects `json:"lowered,omitempty"`
}

func newChatHistorySetAccessCmd(cfg *Deps) *cobra.Command {
	var cwd, expect string
	var newWorkspace, coderRoot, profileFolder bool
	cmd := &cobra.Command{
		Use:   "set-access <conversation-id> scoped|coding|workspace|full",
		Short: "Change a conversation's access level (raising is the operator's; lowering is anyone's)",
		Long: "scoped is the sandboxed assistant; coding, workspace and full are coder turns in the conversation's folder " +
			"(coding tells the agent nothing about the profile; workspace and full follow `coder set --app-access`). " +
			"It takes effect from the next turn. " + clicore.OperatorOnlyHelp + " Raising a conversation's level is the operator's " +
			"(exit 3, operator_only for an agent, the chat or a workflow) and needs coder mode enabled; lowering it is anyone's, " +
			"and stops the processes its turns left running, forgets its kept org workers and revokes its open AI task's token " +
			"before the next turn; one it cannot stop (no recorded identity, or it outlives SIGKILL) is listed under survivors and the " +
			"command exits 5 (survivors_remain), the level lowered all the same. A scoped conversation has no folder: raising it needs --cwd, --profile-folder, --coder-root " +
			"or --new-workspace. --expect <level> makes the change conditional on the conversation still having that level, in one update: " +
			"if another process changed it first the command exits 6 (access_changed, current is the real level) and changes nothing. The conversation starts a fresh session when it leaves or enters scoped.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, level := args[0], args[1]
			if !ai.ValidAccess(level) {
				return clicore.InvalidInput("unknown access %q (scoped, coding, workspace or full)", level)
			}
			store, profileID, closeDB, err := openChatHistory(cfg)
			if err != nil {
				return err
			}
			defer closeDB()
			conv, err := store.GetConversation(id, profileID)
			if err != nil {
				return chatStoreErr(err)
			}
			prev := conv.EffectiveAccess()
			if expect != "" {
				if !ai.ValidAccess(expect) {
					return clicore.InvalidInput("unknown --expect %q (scoped, coding, workspace or full)", expect)
				}
				if prev != expect {
					return errAccessChanged(prev, expect)
				}
			}
			raise := ai.AccessRank(level) > ai.AccessRank(prev)
			folder := coderFolderChoice{cwd: cwd, root: coderRoot, newWorkspace: newWorkspace, profile: profileFolder}
			newCwd := ""
			if raise {
				if err := clicore.RequireOperator(cfg.RequireOperator, "raise a conversation's access"); err != nil {
					return err
				}
				if newCwd, err = raisedFolder(cmd, cfg, conv, folder); err != nil {
					return err
				}
			} else if folder.set() > 0 {
				return clicore.InvalidInput("--cwd, --profile-folder, --coder-root and --new-workspace only apply when raising a scoped conversation")
			}
			out := accessChange{Previous: prev}
			if level != prev {
				if expect != "" {
					// The level the caller saw must still be the row's at the update itself.
					conv, err = store.SetConversationAccessIf(id, profileID, level, newCwd, expect)
					var changed *ai.AccessChangedError
					if errors.As(err, &changed) {
						return errAccessChanged(changed.Current, expect)
					}
				} else {
					conv, err = store.SetConversationAccess(id, profileID, level, newCwd)
				}
				if err != nil {
					return chatStoreErr(err)
				}
				if !raise {
					eff, err := lowerAccessEffects(cfg, store, profileID, id)
					if err != nil {
						return err
					}
					out.Lowered = &eff
				}
			}
			out.ConversationRecord = conv.Record()
			if cfg.JSONOutput() {
				if err := clicore.PrintJSON(out); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "conversation %s: access %s (was %s)\n", id, conv.EffectiveAccess(), prev)
				if out.Lowered != nil {
					for _, sv := range out.Lowered.Survivors {
						fmt.Fprintf(cmd.OutOrStdout(), "  still running: %d %s: %s\n", sv.Pid, sv.Command, sv.Reason)
					}
				}
			}
			if out.Lowered != nil && len(out.Lowered.Survivors) > 0 {
				return &coderError{code: "survivors_remain", err: &clicore.Error{Code: exitSurvivors, Msg: fmt.Sprintf(
					"the level is lowered, but %d process(es) the conversation's turns started could not be stopped and may still hold the old access (see survivors); stop them yourself", len(out.Lowered.Survivors))}}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&expect, "expect", "", "Only change the level while the conversation still has this one (scoped, coding, workspace or full); otherwise exit 6, access_changed, with the real level under current and nothing changed")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Raising a scoped conversation: the folder the agent works in (any existing folder)")
	cmd.Flags().BoolVar(&profileFolder, "profile-folder", false, "Raising a scoped conversation: work in the folder of the profile named by --profile")
	cmd.Flags().BoolVar(&coderRoot, "coder-root", false, "Raising a scoped conversation: work in the coder root folder itself")
	cmd.Flags().BoolVar(&newWorkspace, "new-workspace", false, "Raising a scoped conversation: work in a fresh, randomly named test folder")
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}

// raisedFolder checks the coder gate for raising conv above scoped and returns
// the folder a scoped conversation gets ("" when it keeps its own). A
// conversation that has a folder keeps it: the CLIs key their sessions by it.
func raisedFolder(cmd *cobra.Command, cfg *Deps, conv ai.Conversation, folder coderFolderChoice) (string, error) {
	if conv.Cwd != "" {
		if folder.set() > 0 {
			return "", clicore.InvalidInput("conversation %s already works in %s; its folder is fixed", conv.ID, conv.Cwd)
		}
		db, err := cfg.OpenDB()
		if err != nil {
			return "", fmt.Errorf("initializing database: %w", err)
		}
		defer db.Close()
		_, _, _, err = requireCoderReady(cmd, db.DB, conv.RuntimeID)
		return "", err
	}
	return coderFolder(cmd, cfg, folder, conv.RuntimeID)
}
