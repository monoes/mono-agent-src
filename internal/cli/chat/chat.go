package chat

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// openAIStore opens the DB and builds an AIStore over it, creating the chat
// history tables if they are missing.
func openAIStore(cfg *Deps) (*ai.AIStore, func(), error) {
	db, err := cfg.OpenDB()
	if err != nil {
		return nil, nil, fmt.Errorf("initializing database: %w", err)
	}
	store, err := ai.NewAIStore(db.DB)
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("initializing AI store: %w", err)
	}
	return store, func() { db.Close() }, nil
}

// parseDurationFlag accepts plain seconds ("90") or suffixed ("90s", "10m", "2h").
func parseDurationFlag(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	var secs float64
	if _, err := fmt.Sscanf(s, "%f", &secs); err == nil {
		return time.Duration(secs * float64(time.Second)), nil
	}
	return 0, fmt.Errorf("invalid duration %q (use e.g. 90s, 10m, 2h)", s)
}

func newChatCmd(cfg *Deps) *cobra.Command {
	var f chatFlags
	cmd := &cobra.Command{
		Use:   "chat [prompt]",
		Short: "Chat with a locally-installed AI agent (via the monomind engine)",
		Long: "One agent turn delegated through monomind's Agent Exec Protocol. Streams NDJSON " +
			"events to stdout (start, session, assistant, tool_call, tool_result, usage, result, " +
			"done) — this stream is the interface the desktop app consumes.\n\n" +
			"With --canvas <workflowID> the turn runs as a workflow-builder assistant: the eight " +
			"canvas tools (create workflow/nodes/connections, …) execute against this machine's " +
			"database over the stdio tool bridge, and the conversation is persisted to the chat " +
			"history table.\n\n" +
			"With --conversation <id> --turn <id> the turn belongs to a stored conversation (see " +
			"`chat history`): its runtime, model and session come from the conversation, and the turn " +
			"and its events are journaled as they happen. Stdout is then an admission line followed by " +
			"the committed events. Put the prompt after `--` so it is never read as a flag or as the " +
			"`history` or `turn` subcommand.",
		Args: func(cmd *cobra.Command, args []string) error {
			if f.promptStdin {
				if len(args) > 0 {
					return clicore.InvalidInput("--prompt-stdin takes the prompt from stdin; drop the prompt argument")
				}
				return nil
			}
			return cobra.MinimumNArgs(1)(cmd, args)
		},
		Example: `  monoagentcli chat --runtime claude "summarize the output folder"
  monoagentcli chat --runtime codex --canvas general "build a gmail digest workflow"
  monoagentcli chat --runtime claude --resume th_9f2a "continue"
  monoagentcli chat --conversation <id> --turn <uuid> --tools monoagent -- "what changed today?"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runChat(cfg, cmd, args, &f)
		},
	}
	cmd.Flags().StringVar(&f.runtime, "runtime", "", "Agent runtime id (claude, codex, kimicode, … — see 'agent scan')")
	cmd.Flags().StringVar(&f.model, "model", "", "Model override for the runtime")
	cmd.Flags().StringVar(&f.effort, "effort", "", "Reasoning effort level for the model")
	cmd.Flags().StringVar(&f.resume, "resume", "", "Session/thread id to resume (from the session event)")
	cmd.Flags().StringVar(&f.canvasID, "canvas", "", "Workflow-builder mode for this workflow id")
	cmd.Flags().StringVar(&f.historyID, "history-id", "", "Persistence/session bucket key (defaults to --canvas's id when unset)")
	cmd.Flags().StringVar(&f.tools, "tools", "", `Comma-separated tool surface to enable: "monoagent" gives the agent read/write access (no run execution) to workflows, vault, people, communications; append ",runs" (i.e. "monoagent,runs") to also allow run_workflow execution; "monoagent:read" (alone) is the browser extension's mode for turns that carry web-page text: three read-only workflow tools, nothing else`)
	cmd.Flags().StringVar(&f.timeoutS, "timeout", "", "Overall timeout (e.g. 90s, 10m)")
	cmd.Flags().Float64Var(&f.budget, "budget-usd", 0, "Spend cap for this turn")
	cmd.Flags().BoolVar(&f.promptStdin, "prompt-stdin", false, "Read the prompt from stdin instead of an argument, so it is in no process listing (the browser extension's chat uses this for page text)")
	cmd.Flags().StringVar(&f.conversationID, "conversation", "", "Run the turn in this stored conversation (see chat history), journaling its events")
	cmd.Flags().StringVar(&f.turnID, "turn", "", "Client-chosen turn id for --conversation; a repeated id never runs twice")
	cmd.Flags().StringVar(&f.instanceID, "instance", "", "App instance id recorded as the turn's owner (used with --conversation)")
	cmd.Flags().StringVar(&f.mode, "mode", "", "Set to coder for a full-access turn (see the coder command); needs --cwd, --profile-folder, --coder-root or --new-workspace")
	cmd.Flags().StringVar(&f.coderCwd, "cwd", "", "Coder mode: the folder the agent works in")
	cmd.Flags().BoolVar(&f.profileFold, "profile-folder", false, "Coder mode: work in the folder of the profile named by --profile")
	cmd.Flags().BoolVar(&f.coderRoot, "coder-root", false, "Coder mode: work in the coder root folder itself")
	cmd.Flags().BoolVar(&f.newWorkspace, "new-workspace", false, "Coder mode: work in a fresh, randomly named test folder")
	cmd.Flags().StringArrayVar(&f.attach, "attach", nil, "Coder turns: attach a file (repeatable; at most 10, 20 MB each). It is copied into the folder's .monoagent/attachments/<turn>/ and the prompt lists the copy")
	cmd.AddCommand(newChatHistoryCmd(cfg))
	cmd.AddCommand(newChatTurnCmd(cfg))
	cmd.AddCommand(newChatUICmd(cfg))
	cmd.Flags().BoolVar(&f.noHistory, "no-history", false, "Suppress this legacy chat-history table write (profile/tool init and runtime session events are unaffected; a --conversation turn never writes it)")
	// With --json a failure also ends stdout with {"error","code"}; an agent
	// that is not installed or not logged in is code agent_not_setup.
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}
