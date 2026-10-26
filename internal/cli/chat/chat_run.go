package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai"
	aichat "github.com/monoes/mono-agent/internal/ai/chat"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/workflowio"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// runChat is the chat command's RunE: one agent turn (see newChatCmd). The
// flag values are copied into locals, so a turn that adopts a stored
// conversation's runtime, model or session changes only its own run.
func runChat(cfg *Deps, cmd *cobra.Command, args []string, f *chatFlags) (retErr error) {
	runtime, model, effort, resume, canvasID, historyID, timeoutS, budget, tools, noHistory, promptStdin, conversationID, turnID, instanceID, mode, coderCwd, newWorkspace, coderRoot, profileFold, attach := f.runtime, f.model, f.effort, f.resume, f.canvasID, f.historyID, f.timeoutS, f.budget, f.tools, f.noHistory, f.promptStdin, f.conversationID, f.turnID, f.instanceID, f.mode, f.coderCwd, f.newWorkspace, f.coderRoot, f.profileFold, f.attach
	// The coder host hands a turn the token of its mission confirmation in the
	// environment, not in the arguments; the binary moves it out of the environment before
	// it can start any process (coderhost.StashMissionToken, called first in main's run), and
	// it is taken here (and cleared from Go's environment, should a caller not have stashed it)
	// before anything this turn starts can inherit it. The process's original environment block
	// still shows it until exit; the token is dead once the confirmation is consumed. There is no flag that confirms a mission.
	missionToken := coderhost.TakeMissionToken()
	prompt := strings.Join(args, " ")
	if promptStdin {
		p, perr := readPromptStdin(cmd.InOrStdin())
		if perr != nil {
			return perr
		}
		prompt = p
	}
	timeout, err := parseDurationFlag(timeoutS)
	if err != nil {
		return err
	}
	pageRead := isPageReadTools(tools)
	wantMonoagentTools, wantRuns, err := parseToolsMode(tools, pageRead)
	if err != nil {
		return err
	}
	if pageRead && canvasID != "" {
		return clicore.InvalidInput("--tools %s does not combine with --canvas", toolsPageRead)
	}
	ctx := cmd.Context()
	// A coder turn's own spend cap, when --budget-usd was given (0 is no spend).
	var turnCap *float64
	if cmd.Flags().Changed("budget-usd") {
		turnCap = &budget
	}

	// A page-read turn's --attach files are images for the model, handed
	// straight to the runner (chat_attach.go); every other turn's go on to
	// the coder-only handling below.
	var pageImages []string
	if pageRead && len(attach) > 0 {
		if pageImages, err = pageReadImages(cmd, attach); err != nil {
			return err
		}
		attach = nil
	}

	// A journaled turn takes its runtime, model and session from
	// the conversation, and writes the event journal instead of the
	// legacy transcript.
	var journal *turnJournal
	if conversationID != "" || turnID != "" {
		if conversationID == "" || turnID == "" {
			return clicore.InvalidInput("--conversation and --turn go together")
		}
		if runtime != "" || model != "" || effort != "" || resume != "" || canvasID != "" || historyID != "" || mode != "" || coderCwd != "" || newWorkspace || coderRoot || profileFold {
			return clicore.InvalidInput("--runtime, --model, --effort, --resume, --canvas, --history-id, --mode and --cwd come from the conversation; drop them with --conversation")
		}
		jstore, jprofile, closeJournal, err := openChatHistory(cfg)
		if err != nil {
			return err
		}
		defer closeJournal()
		attachSrc, err := journaledAttachments(jstore, jprofile, conversationID, turnID, attach)
		if err != nil {
			return err
		}
		conv, turn, existed, err := admitJournaledTurn(jstore, jprofile, conversationID, turnID, instanceID, prompt)
		if err != nil {
			return err
		}
		journal = newTurnJournal(jstore, jprofile, conversationID, turnID, conv.RuntimeID, os.Stdout)
		journal.print(chatAdmission{Admitted: !existed, Existed: existed, Turn: turn.Record()})
		if existed {
			return nil // a retried start of a turn that already ran
		}
		atts, err := copyAttachments(conv.Cwd, turnID, attachSrc)
		if err != nil {
			journal.fail(err)
			return err
		}
		if err := journal.start(conv.Model, prompt, atts); err != nil {
			journal.fail(err)
			return err
		}
		// Anything that stops the turn before monomind reports a
		// result still finishes it.
		defer func() {
			if retErr != nil {
				journal.fail(retErr)
			}
		}()
		runtime, model, resume, effort = conv.RuntimeID, conv.Model, conv.SessionID, conv.Effort
		noHistory = true
		if access := conv.EffectiveAccess(); access != ai.AccessScoped {
			if tools != "" {
				return clicore.InvalidInput("--tools does not apply to a %s conversation: it runs as a coder; only a scoped conversation takes it", access)
			}
			task := taskTurn(cfg, conv.ID, turnCap)
			if task.budget != nil {
				turnCap = task.budget
			}
			if task.err != nil {
				return task.err
			}
			err := runCoderTurn(cmd, cfg, journal, coderTurn{runtime: conv.RuntimeID, prompt: prompt, model: model, effort: conv.Effort, resume: resume, cwd: conv.Cwd, orgMode: conv.OrgMode, access: access, attachments: atts, budget: turnCap, env: task.env, agentKind: task.kind, confine: task.confine, missionConfirmed: hostMissionConfirmed(cfg, jprofile, conv.ID, turnID, missionToken, access)})
			task.end(err)
			enforceLoweredAccess(cfg, jstore, jprofile, conv)
			return err
		}
	}
	if mode == ai.ModeCoder {
		if tools != "" || canvasID != "" || historyID != "" {
			return clicore.InvalidInput("--tools, --canvas and --history-id don't apply to --mode coder")
		}
		return runPlainCoderTurn(cmd, cfg, coderFolderChoice{cwd: coderCwd, root: coderRoot, newWorkspace: newWorkspace, profile: profileFold}, coderTurn{runtime: runtime, prompt: prompt, model: model, effort: effort, resume: resume, budget: turnCap}, attach)
	}
	if mode != "" && mode != ai.ModeAssistant {
		return clicore.InvalidInput("unknown --mode %q (assistant or coder)", mode)
	}
	if coderCwd != "" || newWorkspace || coderRoot || profileFold || len(attach) > 0 {
		return clicore.InvalidInput("--cwd, --profile-folder, --coder-root, --new-workspace and --attach only apply to --mode coder")
	}
	if runtime == "" {
		// With exactly one runtime that can run the turn, use it.
		rt, err := defaultChatRuntime(ctx, cfg)
		if err != nil {
			return err
		}
		runtime = rt
	}
	bin, _, err := monomind.Ensure(ctx)
	if err != nil {
		return err
	}
	// effectiveHistoryID is the persistence/session bucket key —
	// decoupled from --canvas, which only controls whether
	// workflow-builder tools are wired in. Falls back to canvasID
	// so canvas-only manual CLI usage still persists as before.
	effectiveHistoryID := historyID
	if effectiveHistoryID == "" {
		effectiveHistoryID = canvasID
	}
	// canvasExplicit distinguishes the real --canvas mode (Workflows
	// editor, building/editing one specific workflow) from
	// --tools monoagent auto-wiring canvas tools for the general
	// assistant below — the two want different system-prompt framing
	// (see the toolSpecs/systemPromptParts block further down).
	canvasExplicit := canvasID != ""
	// effectiveCanvasID: without this, --tools monoagent alone (the
	// global/general assistant, no --canvas) had zero tools capable
	// of creating a workflow or nodes — MonoagentTools only
	// lists/reads/deletes existing ones. Asked to build something,
	// the model would discover no create_workflow tool exists, try
	// Bash as a fallback (denied — see agent-runner.ts's
	// allowedTools restriction), and burn the whole turn on dead
	// ends before giving up. "draft" is the same placeholder
	// CanvasTools already treats as ownership-check-exempt for a
	// not-yet-created workflow (see checkWorkflowOwnership) — the
	// model's own create_workflow call replaces it with a real id
	// for every subsequent tool call in the turn.
	effectiveCanvasID := canvasID
	if effectiveCanvasID == "" && wantMonoagentTools && !pageRead {
		effectiveCanvasID = "draft"
	}

	var (
		store     *ai.AIStore
		closeDB   func()
		canvas    *aichat.CanvasTools
		monoTools *aichat.MonoagentTools
		nodeTypes []aichat.NodeTypeInfo
		profileID string
		db        *storage.Database
	)
	if effectiveCanvasID != "" || wantMonoagentTools || effectiveHistoryID != "" {
		store, closeDB, err = openAIStore(cfg)
		if err != nil {
			return err
		}
		defer closeDB()
		db, err = cfg.OpenDB()
		if err != nil {
			return fmt.Errorf("initializing database: %w", err)
		}
		defer db.Close()
		profileID, err = cfg.ResolveProfileID(db.DB, cfg.ProfileID())
		if err != nil {
			return fmt.Errorf("resolving profile: %w", err)
		}

		if effectiveCanvasID != "" {
			canvas = aichat.NewCanvasTools(db.DB, profileID)
			nodeTypes = registryNodeTypes(db.DB)
			canvas.SetNodeTypes(nodeTypes)
			if err := canvas.CheckWorkflowOwnership(effectiveCanvasID); err != nil {
				return err
			}
		}

		if wantMonoagentTools {
			// selfBin lets run_workflow shell back into this
			// same binary's already-wired execution path (engine/
			// scheduler/DI) rather than duplicating it — best-effort:
			// that tool just reports itself unavailable if
			// os.Executable() fails, everything else still works.
			selfBin, _ := os.Executable()
			monoTools = aichat.NewMonoagentTools(db.DB, selfBin, profileID)
			// Workflows the editor or `workflow save` wrote live as files:
			// activate and delete must reach those, not only the rows.
			if home, err := os.UserHomeDir(); err == nil {
				if store := chatWorkflowStore(db, home); store != nil {
					monoTools.SetWorkflowStore(store)
				}
			}
			// Mechanical run gate: run_workflow executes
			// only when the session was started with runs explicitly
			// enabled (--tools monoagent,runs). A model-supplied
			// confirm:true can never flip this.
			monoTools.SetAllowRuns(wantRuns)
			if pageRead {
				monoTools.SetPageReadOnly()
			}
		}
	}

	// No --cwd override for canvas-only turns: an earlier version of
	// this pointed EVERY turn at a dedicated working directory so
	// agent CLIs' own CLAUDE.md/AGENTS.md conventions would
	// auto-load monoagent guidance — measured to add anywhere from
	// ~20s to ~40s of pure overhead (and tens of thousands of
	// tokens) to every single turn, even a bare "hi": the file's
	// presence pushed several agent CLIs into an unprompted "let me
	// check the workspace guidance first" exploration step before
	// answering, on every call, not just the first. --canvas mode
	// never had this problem because it only ever used
	// opts.SystemPrompt (injected directly, not read from a file) —
	// monoagentSystemPrompt() below does the same for tool
	// guidance, so a canvas-only turn has nothing that needs a
	// stable cwd, and chatWorkingDir keeps it at "" for exactly
	// that case.
	//
	// MonoagentTools turns are different: they get a real --cwd
	// (chatWorkingDir returns projectRoot below) despite paying
	// that same cost, because real per-file-write enforcement needs
	// a real cwd to be relative to — see workspaceConventionPrompt.
	// Honest caveat: reading the external agent-exec bridge's own
	// canUseTool implementation suggests native Write/Edit are
	// already denied outright for this exec, cwd or no cwd — so
	// this isn't closing an open write hole so much as (a)
	// defense-in-depth if that ever changes, and (b) the
	// precondition for the taxonomy guidance text below to mean
	// anything (a relative path needs something to be relative to).
	opts := monomind.ExecOptions{
		Bin:       bin,
		Runtime:   runtime,
		Prompt:    prompt,
		Model:     model,
		Effort:    effort,
		Resume:    resume,
		Timeout:   timeout,
		BudgetUSD: budget,
		// A canvas-only or plain turn keeps its empty --cwd unless it
		// is sandboxed; then it runs in an empty chat workspace (no
		// guidance files, so none of the overhead described above).
		Sandbox:          monomind.TurnSandboxMode,
		WorkspacePurpose: monomind.WorkspaceChat,
		Attach:           pageImages,
	}
	if effort != "" {
		set, _ := capabilityProbe(cmd)
		opts.EffortFlag = set.Has(monomind.CapAgentExecEffort)
	}

	var toolSpecs []monomind.ToolSpec
	var systemPromptParts []string
	if canvas != nil {
		// canvasSystemPrompt's framing ("You are a workflow builder
		// AI... You ONLY communicate by calling tool functions") is
		// correct for the real Workflows-editor session (the user
		// opened it specifically to build/edit canvasID) but wrong
		// for the general assistant's auto-wired case — injecting
		// "only respond via tool calls" into every plain "hi"
		// message would force tool-only behavior onto ordinary
		// conversation. The auto-wired case only needs the tools
		// themselves plus the mention already in
		// monoagentSystemPrompt() below.
		if canvasExplicit {
			systemPromptParts = append(systemPromptParts, canvasSystemPrompt(effectiveCanvasID, nodeTypes))
		}
		toolSpecs = append(toolSpecs, canvasToolSpecs(canvas)...)
	}
	if monoTools != nil && pageRead {
		// Page text is in this turn: only the read tools exist, there
		// is no shell, no --cwd, no knowledge-graph search (see
		// toolsPageRead).
		toolSpecs = append(toolSpecs, monoagentToolSpecs(monoTools)...)
		systemPromptParts = append(systemPromptParts, pageReadSystemPrompt)
	} else if monoTools != nil {
		// MONOMIND_CWD scopes monograph_search/memory_kg_search
		// below to this profile's own knowledge-graph databases,
		// independent of --cwd (set right below) — kept as its own
		// env var rather than folded into --cwd since it points at
		// .monomind/ specifically, not the project root.
		monomindDir := profiledir.MonomindDir(db.DB, profileID)
		projectRoot := profiledir.Root(db.DB, profileID)
		opts.Env = map[string]string{"MONOMIND_CWD": monomindDir}
		// EnsureLayout failing (e.g. root_dir points at an unmounted
		// external drive or otherwise-inaccessible folder — see
		// profiledir.Root's own doc comment on root_dir overrides)
		// must degrade, not kill the whole turn: --cwd/the taxonomy
		// guidance below are explicitly defense-in-depth (see the
		// no-cwd-override comment above), never required for a turn
		// to work — wails-app/app_orgs.go's orgProjectRoot already
		// establishes "warn and fall back" as this codebase's
		// precedent for this exact failure mode.
		if err := profiledir.EnsureLayout(db.DB, profileID); err != nil {
			fmt.Fprintf(os.Stderr, "warning: preparing profile folder for chat: %v (continuing without --cwd scoping)\n", err)
		} else {
			opts.Cwd = chatWorkingDir(true, projectRoot)
			systemPromptParts = append(systemPromptParts, workspaceConventionPrompt(projectRoot))
		}
		systemPromptParts = append(systemPromptParts, monoagentSystemPrompt(canvas != nil, wantRuns))
		// MonoagentTools and CanvasTools both define a "create_workflow"
		// tool (CanvasTools' is the one actually wired into the
		// canvas != nil branch's ownership/"draft"-placeholder
		// mechanism above — see effectiveCanvasID's comment — and
		// OnToolCall below already tries canvas.Execute before
		// monoTools.ExecuteContext, so CanvasTools' implementation is
		// the only one ever invoked regardless). Sending both
		// definitions to the runtime under the identical name isn't
		// just redundant, it's fatal: the runtime rejects the second
		// registration outright ("Tool create_workflow is already
		// registered"), which happens on every single turn once
		// canvas is auto-wired (see effectiveCanvasID above) — i.e.
		// every plain "hi" to the general assistant with monoagent
		// tools enabled, not just real canvas sessions. appendNewToolSpecs
		// drops any entry whose name a prior source already supplied.
		toolSpecs = appendNewToolSpecs(toolSpecs, monoagentToolSpecs(monoTools))
		toolSpecs = append(toolSpecs, monographSearchToolSpec(), memoryKGSearchToolSpec())
		// Real Bash access to `monomind`/`monoagentcli` themselves, on
		// top of the mcp__org__* tool bridge above — measured directly
		// to be far more reliable for the model to actually use (see
		// canUseTool's allowBashPrefixes doc comment in agent-exec.ts
		// for the A/B evidence). Still fully scoped: canUseTool only
		// allows Bash commands starting with these two names, nothing
		// else. --cwd is deliberately NOT set for this exec (see the
		// load-bearing comment above about CLAUDE.md auto-discovery
		// overhead), so every command must carry --project itself —
		// spell out the exact path here rather than let the model
		// guess it or rely on a relative cwd.
		// Scoped to the org/workflow subcommand families specifically
		// — not a blanket grant of the whole binary. Both CLIs expose
		// far more than org/workflow management (monoagentcli has
		// secret/connect/login/export; monomind has security/config/
		// providers), none of which this feature exists to reach.
		// Message bodies and other synced content are explicitly
		// untrusted input (see monoagentSystemPrompt above) — a
		// prompt-injected instruction hidden in one could otherwise
		// try to walk the model into running something well outside
		// what "help me create an org" ever needs.
		opts.AllowBashPrefixes = chatBashPrefixes
		systemPromptParts = append(systemPromptParts, chatBashPrompt(projectRoot, profileID))
	}
	// Only the EMPTY case gets extra framing. An earlier version also
	// added a "you might have no tools, be careful" caution AND a
	// "here are your N/exact tool names" reinforcement to the
	// non-empty case too — measured (via direct A/B testing: same
	// prompt, same tools-file, only opts.SystemPrompt varied) to be a
	// net regression. With real tools verified present end to end
	// (Go toolSpecs count, agent-runner.ts's sdkTools, all matching),
	// system prompt "" or the pre-existing monoagentSystemPrompt
	// alone reliably called the real tool and returned real data;
	// stacking the extra meta-commentary on top made the model
	// reliably refuse and fabricate a plausible-sounding excuse
	// instead (a fake skill lookup, a fake tool error) — zero actual
	// tool_use events in the output stream either time. More warning
	// text about not hallucinating tool access, counterintuitively,
	// made the hallucination worse, not better. So: leave the
	// non-empty case exactly as it already was (monoagentSystemPrompt
	// + canvasSystemPrompt when applicable, added above) — only the
	// genuinely-empty case, which this same A/B method confirmed
	// DOES need and reliably benefits from explicit framing, gets it.
	if len(toolSpecs) == 0 {
		systemPromptParts = append(systemPromptParts, `Your tool list this turn is EMPTY. Not restricted — empty. No Bash, no Write, no Read, no Task/Agent/subagent-spawning, nothing. Any tool call you attempt will fail. If the user's request needs a tool, your entire reply is: say plainly that you have no tools available right now, then tell them to enable it in Settings → "Assistant tool access" and start a NEW chat. Do not describe a plan, a setup, or a capability "via" some tool — you have none to invoke, so any such claim is false regardless of how it's phrased.`)
	}
	opts.SystemPrompt = strings.Join(systemPromptParts, "\n\n---\n\n")
	if len(toolSpecs) > 0 {
		opts.Tools = toolSpecs
		opts.ToolTimeout = 2 * time.Minute
		opts.OnToolCall = func(ctx context.Context, name string, args json.RawMessage) (string, error) {
			if !pageRead && (name == "monograph_search" || name == "memory_kg_search") {
				return runKGTool(ctx, bin, name, args, profiledir.MonomindDir(db.DB, profileID))
			}
			if canvas != nil {
				if text, err := canvas.Execute(name, string(args)); err == nil || !strings.HasPrefix(err.Error(), "unknown tool:") {
					return text, err
				}
			}
			if monoTools != nil {
				// ExecuteContext threads this turn's ctx into
				// run-tool subprocesses so they are cancelled with
				// the turn (no Background-derived orphans).
				return monoTools.ExecuteContext(ctx, name, string(args))
			}
			return "", fmt.Errorf("unknown tool: %s", name)
		}
	}

	emit := func(v interface{}) {
		b, _ := json.Marshal(v)
		fmt.Println(string(b))
	}
	// res.ResultText holds the reply even when the result event has
	// no text (monomind.ApplyEventToResult assembles it from the
	// assistant events), including a failed turn's partial reply.
	onEvent := func(ev monomind.Event) { emit(ev) }
	if journal != nil {
		onEvent = journal.handle
	}
	res, err := monomind.Exec(ctx, opts, onEvent)
	if err == nil {
		monomind.ReclassifyMissingRuntime(ctx, runtime, res)
	}
	if journal != nil {
		// Finished here, with the real result, rather than by the
		// deferred fail above (a no-op once finished).
		if err != nil && res == nil {
			res = &monomind.TurnResult{Err: &monomind.ProtocolError{Code: monomind.ErrRunnerError, Message: err.Error()}}
		}
		journal.finish(ctx.Err() != nil, res)
	}
	if err != nil {
		return err
	}
	// Both messages are saved together, after the turn completes,
	// so the (possibly newly-assigned) res.SessionID is known for
	// both — simpler than saving the user turn before running and
	// risking it never getting tagged with the right session. This
	// runs even when res.Err != nil: a failed/cancelled turn's
	// partial conversation (whatever reply text arrived before the
	// failure) is still worth preserving, not silently
	// dropped the way the old os.Exit(res.Err.ExitCode) below used
	// to (it returned before this block ever ran).
	//
	// noHistory (--no-history) suppresses only this legacy
	// transcript write, never store/profile/tool initialization
	// above. A --conversation turn sets it, since its event journal
	// (internal/ai/chat_events.go) is that caller's history source
	// instead, and a dual writer would double-persist the same
	// turn. Checked here, not by clearing effectiveHistoryID: that
	// variable also gates whether `store` itself gets opened above
	// (effectiveCanvasID != "" || wantMonoagentTools ||
	// effectiveHistoryID != ""), which must stay unaffected by this
	// flag.
	if store != nil && effectiveHistoryID != "" && !noHistory {
		if err := store.SaveChatMessage(ai.ChatMessage{
			ID:         uuid.NewString(),
			WorkflowID: effectiveHistoryID,
			ProfileID:  profileID,
			Role:       "user",
			Content:    prompt,
			ProviderID: runtime,
			Model:      model,
			SessionID:  res.SessionID,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "warning: saving chat message: %v\n", err)
		}
		if final := res.ResultText; final != "" {
			if err := store.SaveChatMessage(ai.ChatMessage{
				ID:         uuid.NewString(),
				WorkflowID: effectiveHistoryID,
				ProfileID:  profileID,
				Role:       "assistant",
				Content:    final,
				ProviderID: runtime,
				Model:      model,
				SessionID:  res.SessionID,
			}); err != nil {
				fmt.Fprintf(os.Stderr, "warning: saving chat message: %v\n", err)
			}
		}
	}
	if res.Err != nil {
		// Returned (not os.Exit'd) so every deferred cleanup above
		// (closeDB, db.Close, temp prompt/system/tools file
		// removal in monomind.Exec) still runs. main.go prints this
		// to stderr and exitCodeFor maps *monomind.ProtocolError to
		// its own ExitCode (see cmd/monoagentcli/exitcodes.go),
		// preserving the same process exit code this used to set
		// directly.
		return res.Err
	}
	return nil
}

// chatWorkflowStore is the file-plus-SQLite workflow store the chat tools
// activate and delete through. Ownerless workflow files are stamped first, so
// a legacy file is owned by a profile before the tools look at it. It is nil
// when the file directory cannot be opened.
func chatWorkflowStore(db *storage.Database, home string) *workflow.HybridWorkflowStore {
	dir := filepath.Join(home, ".monoagent", "workflows")
	fs, err := workflow.NewWorkflowFileStore(dir)
	if err != nil {
		return nil
	}
	_ = workflowio.StampFileWorkflows(context.Background(), db.DB, dir)
	return workflow.NewHybridWorkflowStore(fs, workflow.NewSQLiteWorkflowStore(db.DB))
}
