package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
)

// coderTurn is one full-access turn: the agent runs as a full session of
// its runtime's own CLI in cwd, with the user's normal setup loaded and no
// tool restrictions.
type coderTurn struct {
	runtime string // "" = claude
	prompt  string
	model   string
	effort  string
	resume  string
	cwd     string
	orgMode string // ai.OrgModeDynamic lets the agent spawn workers (#226)
}

// coderSystemPrompt is appended to the runtime's own system prompt (not a
// replacement: monomind keeps the CLI's preset with --settings). cli is the
// monoagentInvocation its mono-agent section tells the agent to run.
func coderSystemPrompt(cwd, cli string) string {
	return "You are running as mono-agent's coder chat: a full coding-agent session with unrestricted " +
		"access to this computer, working in " + cwd + ". The user sees every tool call you make " +
		"(commands, edits, file writes) live in the chat. Work inside that folder unless the user " +
		"asks for something elsewhere, and say plainly what you changed.\n\n" + monoagentPrompt(cli)
}

// runCoderTurn runs t through monomind with full access. journal is nil for
// a plain `chat --mode coder` turn, which prints raw events instead.
func runCoderTurn(cmd *cobra.Command, cfg *globalConfig, journal *turnJournal, t coderTurn) error {
	ctx := cmd.Context()
	db, err := initDB(cfg)
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	settings, st, rt, err := requireCoderReady(cmd, db.DB, t.runtime)
	db.Close()
	if err != nil {
		return err
	}
	if fi, err := os.Stat(t.cwd); err != nil || !fi.IsDir() {
		return errInvalidInput("this coder conversation's folder %s no longer exists; start a new conversation", t.cwd)
	}
	bin, err := monomind.EnsureIn(ctx, t.cwd)
	if err != nil {
		return err
	}
	self, _ := os.Executable() // "" when unknown; see monoagentInvocation
	opts := monomind.ExecOptions{
		Bin:          bin,
		Runtime:      rt.ID,
		Prompt:       t.prompt,
		Model:        t.model,
		Effort:       t.effort,
		EffortFlag:   st.caps.Has(monomind.CapAgentExecEffort),
		Resume:       t.resume,
		Cwd:          t.cwd,
		Access:       monomind.AccessFull,
		Settings:     monomind.CoderSettings,
		MaxTurns:     settings.MaxTurns,
		Timeout:      settings.timeout(),
		BudgetUSD:    settings.BudgetUSD,
		SystemPrompt: coderSystemPrompt(t.cwd, monoagentInvocation(self, cfg)),
	}
	onEvent := func(ev monomind.Event) {
		b, _ := json.Marshal(ev)
		fmt.Fprintln(cmd.OutOrStdout(), string(b))
	}
	if journal != nil {
		journal.cwd = t.cwd
		journal.coderRuntime = rt
		journal.notice(noticeCoderWorkspace, "Working in "+t.cwd, chatevents.SeverityInfo)
		onEvent = journal.handle
	}
	var closeOrg func()
	if journal != nil && t.orgMode == ai.OrgModeDynamic {
		var leadEvent func(monomind.Event)
		closeOrg, leadEvent = startDynamicOrg(ctx, cfg, journal, settings, st, rt, t, &opts)
		if leadEvent != nil {
			journalEvent := onEvent
			onEvent = func(ev monomind.Event) {
				leadEvent(ev)
				journalEvent(ev)
			}
		}
	}
	res, err := monomind.Exec(ctx, opts, onEvent)
	if closeOrg != nil {
		// The lead is done: workers still running are stopped and their
		// ends journaled before the turn finishes.
		closeOrg()
	}
	if journal != nil {
		if err != nil && res == nil {
			res = &monomind.TurnResult{Err: &monomind.ProtocolError{Code: monomind.ErrRunnerError, Message: err.Error()}}
		}
		journal.finish(ctx.Err() != nil, res)
	}
	if err != nil {
		return err
	}
	if res.Err != nil {
		return res.Err
	}
	return nil
}

// coderFolderChoice is where a new coder conversation or plain coder turn
// works: a folder the user picked (--cwd), the coder root itself
// (--coder-root), or a fresh test folder inside it (--new-workspace).
type coderFolderChoice struct {
	cwd          string
	root         bool
	newWorkspace bool
}

func (c coderFolderChoice) set() int {
	n := 0
	for _, on := range []bool{c.cwd != "", c.root, c.newWorkspace} {
		if on {
			n++
		}
	}
	return n
}

// coderFolder resolves c to a ready folder for runtime ("" = claude),
// initialized with that runtime's setup files.
func coderFolder(cmd *cobra.Command, cfg *globalConfig, c coderFolderChoice, runtime string) (string, error) {
	switch c.set() {
	case 0:
		return "", errInvalidInput("coder mode needs a folder: pass --cwd <dir>, --coder-root or --new-workspace")
	case 1:
	default:
		return "", errInvalidInput("--cwd, --coder-root and --new-workspace are alternatives; pass one")
	}
	db, err := initDB(cfg)
	if err != nil {
		return "", fmt.Errorf("initializing database: %w", err)
	}
	defer db.Close()
	settings, _, rt, err := requireCoderReady(cmd, db.DB, runtime)
	if err != nil {
		return "", err
	}
	if c.newWorkspace || c.root {
		setUp := newCoderWorkspace
		if c.root {
			setUp = rootCoderWorkspace
		}
		ws, err := setUp(cmd.Context(), expandHome(settings.WorkspaceRoot), rt.InitTarget)
		if err != nil {
			return "", err
		}
		return ws.Path, nil
	}
	dir, err := resolveCoderCwd(c.cwd)
	if err != nil {
		return "", err
	}
	// A folder the user picked may be their own repo: add only what is
	// missing, never touch what is there.
	if _, err := initWorkspace(cmd.Context(), "", dir, rt.InitTarget); err != nil {
		return "", err
	}
	return dir, nil
}

// createCoderConversation is `chat history create --mode coder`; runtimeID
// "" means claude.
func createCoderConversation(cmd *cobra.Command, cfg *globalConfig, runtimeID, model, effort, workflowID string, folder coderFolderChoice) (ai.Conversation, error) {
	if runtimeID == "" {
		runtimeID = monomind.DefaultCoderRuntime
	}
	dir, err := coderFolder(cmd, cfg, folder, runtimeID)
	if err != nil {
		return ai.Conversation{}, err
	}
	store, profileID, closeDB, err := openChatHistory(cfg)
	if err != nil {
		return ai.Conversation{}, err
	}
	defer closeDB()
	return store.CreateConversationModeEffort(profileID, "agent", workflowID, runtimeID, "", model, ai.ModeCoder, dir, effort)
}
