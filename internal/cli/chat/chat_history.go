package chat

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/dynorg"
)

// `chat history …` reads and writes the desktop chat's conversation, turn
// and event journal (ai_chat_conversations/turns/events). It sits under one
// subcommand word so it takes as little as possible from `chat <prompt>`:
// only a prompt whose first unquoted word is exactly "history" routes here,
// and `chat -- history …` (what the app passes) never does.

// openChatHistory opens the DB once and returns the store and the resolved
// profile id (initDB resolves --profile or the active profile).
func openChatHistory(cfg *Deps) (*ai.AIStore, string, func(), error) {
	db, err := cfg.OpenDB()
	if err != nil {
		return nil, "", nil, fmt.Errorf("initializing database: %w", err)
	}
	store, err := ai.NewAIStore(db.DB)
	if err != nil {
		db.Close()
		return nil, "", nil, fmt.Errorf("initializing AI store: %w", err)
	}
	return store, cfg.ProfileID(), func() { db.Close() }, nil
}

// chatStoreErr maps the chat store's sentinels to CLI exit codes.
func chatStoreErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ai.ErrConversationNotFound), errors.Is(err, ai.ErrTurnNotFound):
		return clicore.NotFound("%v", err)
	case errors.Is(err, ai.ErrTurnActive):
		return clicore.InvalidInput("%v: stop the turn before deleting the conversation (a coder session: `coder sessions stop <conversation-id>`; a turn whose process is gone: `chat history reconcile`)", err)
	case errors.Is(err, ai.ErrTurnOwnedByOtherInstance):
		return clicore.InvalidInput("%v", err)
	}
	return err
}

// chatLimit validates a --limit value: 0 means def, above max is refused.
func chatLimit(limit, def, max int) (int, error) {
	if limit == 0 {
		return def, nil
	}
	if limit < 0 || limit > max {
		return 0, clicore.InvalidInput("--limit must be between 1 and %d", max)
	}
	return limit, nil
}

// turnInConversation returns turnID, or not-found when it belongs to a
// different conversation (or profile).
func turnInConversation(store *ai.AIStore, profileID, conversationID, turnID string) (ai.Turn, error) {
	t, err := store.GetTurn(turnID, profileID)
	if err != nil {
		return ai.Turn{}, chatStoreErr(err)
	}
	if t.ConversationID != conversationID {
		return ai.Turn{}, clicore.NotFound("%v", ai.ErrTurnNotFound)
	}
	return t, nil
}

type chatConversationPage struct {
	Items      []ai.ConversationRecord `json:"items"`
	NextCursor string                  `json:"next_cursor"`
}

type chatTurnPage struct {
	Items      []ai.TurnRecord `json:"items"`
	NextCursor string          `json:"next_cursor"`
}

type chatEventPage struct {
	Items            []chatevents.Record `json:"items"`
	Turn             ai.TurnRecord       `json:"turn"`
	LastCommittedSeq int64               `json:"last_committed_seq"`
	HasMore          bool                `json:"has_more"`
}

type chatFinishResult struct {
	Finalized bool               `json:"finalized"`
	Event     *chatevents.Record `json:"event"`
	Turn      ai.TurnRecord      `json:"turn"`
}

type chatReconcileResult struct {
	Reconciled []ai.TurnRecord `json:"reconciled"`
	Errors     []string        `json:"errors"`
	// Worktrees are the isolated org writers' worktrees of turns no longer
	// active that were cleaned up, with the branches kept because their
	// work was never merged (#230).
	Worktrees []dynorg.TreeCleanup `json:"worktrees"`
}

func newChatHistoryCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Read and manage the desktop chat's conversations, turns and events",
		Long: "The desktop chat's history: conversations (one runtime and model each), their turns, " +
			"and each turn's event journal. Everything except reconcile is scoped to the active " +
			"profile (or --profile). A turn runs with `chat --conversation <id> --turn <id> -- <prompt>`.",
		Example: `  monoagentcli --json chat history list
  monoagentcli --json chat history create --runtime claude --model sonnet
  monoagentcli --json chat history turns <conversation-id>
  monoagentcli --json chat history events <conversation-id> <turn-id> --after-seq 12`,
	}
	cmd.AddCommand(
		withListAll(cfg, newChatHistoryListCmd(cfg)),
		newChatHistoryShowCmd(cfg),
		withDefaultAccess(cfg, newChatHistoryCreateCmd(cfg)),
		newChatHistoryTurnsCmd(cfg),
		newChatHistoryTurnCmd(cfg),
		newChatHistoryEventsCmd(cfg),
		newChatHistoryDeleteCmd(cfg),
		newChatHistoryFinishCmd(cfg),
		newChatHistoryReconcileCmd(cfg),
		newChatHistoryTranscriptCmd(cfg),
		newChatHistorySetOrgCmd(cfg),
		newChatHistorySetAccessCmd(cfg),
	)
	return cmd
}

func newChatHistoryListCmd(cfg *Deps) *cobra.Command {
	var cursor string
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List conversations, most recently updated first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := chatLimit(limit, 50, 200)
			if err != nil {
				return err
			}
			store, profileID, closeDB, err := openChatHistory(cfg)
			if err != nil {
				return err
			}
			defer closeDB()
			convs, next, err := store.ListConversations(profileID, cursor, n)
			if err != nil {
				return err
			}
			page := chatConversationPage{Items: []ai.ConversationRecord{}, NextCursor: next}
			for _, c := range convs {
				page.Items = append(page.Items, c.Record())
			}
			if cfg.JSONOutput() {
				return clicore.PrintJSON(page)
			}
			for _, c := range page.Items {
				fmt.Printf("%s  %-8s %-10s %-12s %s\n", c.ID, c.Backend, c.RuntimeID, c.WorkflowContext, c.UpdatedAt)
			}
			if next != "" {
				fmt.Printf("more: --cursor %q\n", next)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cursor, "cursor", "", "next_cursor from the previous page")
	cmd.Flags().IntVar(&limit, "limit", 0, "Page size (default 50, max 200)")
	return cmd
}

func newChatHistoryShowCmd(cfg *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "show <conversation-id>",
		Short: "Show one conversation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, profileID, closeDB, err := openChatHistory(cfg)
			if err != nil {
				return err
			}
			defer closeDB()
			conv, err := store.GetConversation(args[0], profileID)
			if err != nil {
				return chatStoreErr(err)
			}
			return printConversation(cfg, conv.Record())
		},
	}
}

func printConversation(cfg *Deps, c ai.ConversationRecord) error {
	if cfg.JSONOutput() {
		return clicore.PrintJSON(c)
	}
	fmt.Printf("id:        %s\nbackend:   %s\nruntime:   %s\nmodel:     %s\neffort:    %s\nworkflow:  %s\nsession:   %s\ncreated:   %s\nupdated:   %s\n",
		c.ID, c.Backend, c.RuntimeID, c.Model, c.Effort, c.WorkflowContext, c.SessionID, c.CreatedAt, c.UpdatedAt)
	return nil
}

func newChatHistoryCreateCmd(cfg *Deps) *cobra.Command {
	var runtimeID, model, effort, workflowID, mode, access, cwd string
	var newWorkspace, coderRoot, profileFolder bool
	var orgMode string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an agent conversation",
		Long: "Create an agent conversation. --workflow is the tool and ownership context: " +
			`"general" (the default), "draft", or a workflow id.` + "\n\n" +
			"--access picks the level: scoped (the default; the sandboxed assistant), or coding, workspace or full (a coder conversation, see `coder`) " +
			"that works in --cwd, or in a --profile-folder (the folder of the profile named by --profile), a --coder-root (the coder root folder itself, shared), " +
			"or a fresh test folder with --new-workspace; those levels need coder mode enabled. The folder is fixed for the conversation's life. " +
			"--mode stays as an alias: assistant is scoped, coder is full. The JSON has both `access` and `mode`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			level, err := resolveCreateAccess(mode, access)
			if err != nil {
				return err
			}
			if level != ai.AccessScoped {
				if orgMode != ai.OrgModeSolo && orgMode != ai.OrgModeDynamic {
					return clicore.InvalidInput("--org must be solo or dynamic")
				}
				conv, err := createCoderConversation(cmd, cfg, level, runtimeID, model, effort, workflowID, coderFolderChoice{cwd: cwd, root: coderRoot, newWorkspace: newWorkspace, profile: profileFolder})
				if err != nil {
					return err
				}
				if orgMode == ai.OrgModeDynamic {
					if conv, err = setOrgMode(cfg, conv.ID, orgMode); err != nil {
						return err
					}
				}
				return printConversation(cfg, conv.Record())
			}
			if runtimeID == "" {
				rt, err := defaultChatRuntime(cmd.Context(), cfg)
				if err != nil {
					return err
				}
				runtimeID = rt
			}
			if cmd.Flags().Changed("org") {
				return clicore.InvalidInput("--org only applies above the scoped level (--access coding, workspace or full)")
			}
			if cwd != "" || newWorkspace || coderRoot || profileFolder {
				return clicore.InvalidInput("--cwd, --profile-folder, --coder-root and --new-workspace only apply above the scoped level (--access coding, workspace or full)")
			}
			store, profileID, closeDB, err := openChatHistory(cfg)
			if err != nil {
				return err
			}
			defer closeDB()
			conv, err := store.CreateConversationEffort(profileID, "agent", workflowID, runtimeID, "", model, effort)
			if err != nil {
				return err
			}
			return printConversation(cfg, conv.Record())
		},
	}
	cmd.Flags().StringVar(&runtimeID, "runtime", "", "Agent runtime id (claude, codex, …); --mode coder defaults to claude (see 'coder status' for the ready ones)")
	cmd.Flags().StringVar(&model, "model", "", "Model for the runtime")
	cmd.Flags().StringVar(&effort, "effort", "", "Reasoning effort level for the model (e.g. low, medium, high, max)")
	cmd.Flags().StringVar(&workflowID, "workflow", "general", "Workflow context")
	cmd.Flags().StringVar(&mode, "mode", "", "Alias of --access: assistant is scoped, coder is full")
	cmd.Flags().StringVar(&access, "access", "", "scoped (default), coding, workspace or full; every level above scoped needs coder mode enabled")
	cmd.Flags().StringVar(&cwd, "cwd", "", "Coder mode: the folder the agent works in (any existing folder)")
	cmd.Flags().BoolVar(&profileFolder, "profile-folder", false, "Coder mode: work in the folder of the profile named by --profile (see 'coder workspace default')")
	cmd.Flags().BoolVar(&coderRoot, "coder-root", false, "Coder mode: work in the coder root folder itself (see 'coder set --workspace-root')")
	cmd.Flags().BoolVar(&newWorkspace, "new-workspace", false, "Coder mode: work in a fresh, randomly named test folder")
	cmd.Flags().StringVar(&orgMode, "org", ai.OrgModeSolo, "Coder mode: solo, or dynamic to let the agent spawn worker agents")
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}

func newChatHistoryDeleteCmd(cfg *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <conversation-id>",
		Short: "Delete a conversation with its turns, events and coder session (refused while a turn is active)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, profileID, closeDB, err := openChatHistory(cfg)
			if err != nil {
				return err
			}
			defer closeDB()
			if err := store.DeleteConversation(args[0], profileID); err != nil {
				return chatStoreErr(err)
			}
			if cfg.JSONOutput() {
				return clicore.PrintJSON(map[string]any{"id": args[0], "deleted": true})
			}
			fmt.Printf("deleted conversation %s\n", args[0])
			return nil
		},
	}
}

func newChatHistoryTranscriptCmd(cfg *Deps) *cobra.Command {
	var byAgent bool
	cmd := &cobra.Command{
		Use:   "transcript <history-id> | --by-agent <conversation-id> <turn-id>",
		Short: "Show the legacy transcript `chat --history-id`/`--canvas` saved, or a dynamic-org turn by agent",
		Long: "Shows the messages a plain `chat --history-id <id>` (or `--canvas <id>`) turn saved to the " +
			"legacy ai_chat_messages transcript, oldest first. Conversations run through " +
			"`chat --conversation` are journaled as events instead and never appear here.\n\n" +
			"With --by-agent it shows a journaled dynamic-org turn split by agent instead: the lead, then " +
			"each worker with its role, model and status, the briefs and reports it exchanged, and the " +
			"text it wrote.",
		Args: func(cmd *cobra.Command, args []string) error {
			if byAgent {
				return cobra.ExactArgs(2)(cmd, args)
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			store, profileID, closeDB, err := openChatHistory(cfg)
			if err != nil {
				return err
			}
			defer closeDB()
			if byAgent {
				if _, err := turnInConversation(store, profileID, args[0], args[1]); err != nil {
					return err
				}
				recs, err := allTurnEvents(store, profileID, args[0], args[1])
				if err != nil {
					return err
				}
				items := agentTranscripts(recs)
				if cfg.JSONOutput() {
					return clicore.PrintJSON(map[string]any{"items": items})
				}
				printAgentTranscripts(items)
				return nil
			}
			msgs, err := store.GetChatHistory(args[0], profileID)
			if err != nil {
				return err
			}
			if msgs == nil {
				msgs = []ai.ChatMessage{}
			}
			if cfg.JSONOutput() {
				return clicore.PrintJSON(map[string]any{"items": msgs})
			}
			for _, m := range msgs {
				fmt.Printf("[%s] %s: %s\n", m.CreatedAt, m.Role, m.Content)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&byAgent, "by-agent", false, "Show a journaled dynamic-org turn (<conversation-id> <turn-id>) split by agent")
	return cmd
}

func firstLineOf(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}

// newChatHistorySetOrgCmd switches a coder conversation between working
// alone and a dynamic org (#226), from its next turn on.
func newChatHistorySetOrgCmd(cfg *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "set-org <conversation-id> solo|dynamic",
		Short: "Let a coder conversation's agent spawn worker agents (dynamic) or work alone (solo)",
		Long: "In a dynamic org the coder chat's agent gets org tools to spawn worker agents, each with its own " +
			"role, skills, model and access, within the limits in `coder set --org-*`. It takes effect from the " +
			"next turn. The runtime must take caller tools with full access (see `agent scan --json`).",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			conv, err := setOrgMode(cfg, args[0], args[1])
			if err != nil {
				return err
			}
			if cfg.JSONOutput() {
				return printConversation(cfg, conv.Record())
			}
			fmt.Printf("conversation %s: org %s\n", conv.ID, conv.OrgMode)
			return nil
		},
	}
}

// setOrgMode stores a coder conversation's org mode.
func setOrgMode(cfg *Deps, id, mode string) (ai.Conversation, error) {
	if mode != ai.OrgModeSolo && mode != ai.OrgModeDynamic {
		return ai.Conversation{}, clicore.InvalidInput("org mode must be solo or dynamic (got %q)", mode)
	}
	store, profileID, closeDB, err := openChatHistory(cfg)
	if err != nil {
		return ai.Conversation{}, err
	}
	defer closeDB()
	conv, err := store.GetConversation(id, profileID)
	if err != nil {
		return ai.Conversation{}, chatStoreErr(err)
	}
	if conv.Scoped() {
		return ai.Conversation{}, clicore.InvalidInput("conversation %s is a scoped conversation; only coder chats can run a dynamic org", id)
	}
	if err := store.SetConversationOrgMode(id, profileID, mode); err != nil {
		return ai.Conversation{}, chatStoreErr(err)
	}
	conv.OrgMode = mode
	return conv, nil
}
