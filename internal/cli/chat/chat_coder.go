package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/profilectx"
	"github.com/monoes/mono-agent/internal/workflow"
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
	// access is the conversation's level (ai.AccessCoding and up); "" is full,
	// what a plain `chat --mode coder` turn has.
	access string
	// budget is a spend cap for this turn (--budget-usd, what an AI task's
	// supervisor passes); nil asks for none. See turnBudget.
	budget *float64
	// env is added to the turn's environment (an AI task's id and token).
	env map[string]string
	// agentKind is the browser-permission kind the turn runs as (coder, or
	// apply for an application-apply task's turn); "" is coder.
	agentKind string
	// confine, when set, makes this a confined turn (chat_apply.go): scoped
	// tools, not full access, in the task's own folder.
	confine *confinement
	// attachments are the files attached to the turn, already copied into cwd.
	attachments []chatevents.TurnAttachment
	// missionConfirmed: the user confirmed a mission for this conversation
	// (mission.propose), so the lead plans one (dynorg.MissionPrompt).
	missionConfirmed bool
}

// coderSystemPrompt is appended to the runtime's own system prompt (not a
// replacement: monomind keeps the CLI's preset with --settings).
func coderSystemPrompt(cwd string) string {
	return "You are running as mono-agent's coder chat: a full coding-agent session with unrestricted " +
		"access to this computer, working in " + cwd + ". The user sees every tool call you make " +
		"(commands, edits, file writes) live in the chat. Work inside that folder unless the user " +
		"asks for something elsewhere, and say plainly what you changed."
}

// coderLeadSystemPrompt is the lead's whole system prompt for a turn in cwd:
// the coder preamble, how to use the user's browser, the active profile's
// context (who the chat works for, what that profile holds; secrets never)
// when appAccess (coder.appAccess) is on, and the publication rules.
func coderLeadSystemPrompt(ctx context.Context, db *sql.DB, cfg *Deps, cwd string, appAccess bool) string {
	parts := []string{coderSystemPrompt(cwd), coderBrowserPrompt(cfg.ProfileID())}
	// Off, the profile summary (task titles, document and people names,
	// workflows, accounts, vault item names) is not given to the model.
	if appAccess {
		if block := profilectx.Block(ctx, db, cfg.ProfileID()); block != "" {
			parts = append(parts, block)
		}
	}
	return strings.Join(append(parts, cfg.PublicationPrompt()), "\n\n")
}

// turnBudget is the spend cap a coder turn runs under. A turn's own cap
// (--budget-usd) can lower the operator's coder budget, never raise it: the
// lower of the two wins (the operator's 0 is no cap set, so the turn's cap
// stands alone). A turn cap of 0 or less is no spend and the turn is refused,
// never run unlimited; so is a cap on a runtime that can't enforce one (kilo
// takes no budget).
func turnBudget(operator float64, turn *float64, runtime string) (float64, error) {
	if turn == nil {
		return operator, nil
	}
	if !validTurnCap(*turn) {
		return 0, noBudgetError(*turn)
	}
	if runtime == "kilo" {
		return 0, &coderError{code: "budget_unenforceable", err: &clicore.Error{Code: 3,
			Msg: "kilo can't enforce a spend cap, so a turn with --budget-usd does not run on it"}}
	}
	if operator > 0 && operator < *turn {
		return operator, nil
	}
	return *turn, nil
}

// dropKiloOptions clears the options kilo's runner refuses outright
// (--effort and --budget-usd; it accepts only --access full --settings
// user,project,local) and names what it cleared, "" when nothing.
func dropKiloOptions(opts *monomind.ExecOptions) string {
	if opts.Runtime != "kilo" {
		return ""
	}
	var dropped []string
	if opts.Effort != "" {
		opts.Effort = ""
		dropped = append(dropped, "effort")
	}
	if opts.BudgetUSD != 0 {
		opts.BudgetUSD = 0
		dropped = append(dropped, "budget")
	}
	return strings.Join(dropped, " or ")
}

// runCoderTurn runs t through monomind with full access. journal is nil for
// a plain `chat --mode coder` turn, which prints raw events instead.
func runCoderTurn(cmd *cobra.Command, cfg *Deps, journal *turnJournal, t coderTurn) error {
	if err := refuseCoderFromRuns(); err != nil {
		return err
	}
	ctx := cmd.Context()
	db, err := cfg.OpenDB()
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	settings, st, rt, err := requireCoderReady(cmd, db.DB, t.runtime)
	settings = settings.forAccess(t.access)
	// The profile block is read while the database is open; it never fails.
	systemPrompt := coderLeadSystemPrompt(ctx, db.DB, cfg, t.cwd, settings.appAccess())
	db.Close()
	if err != nil {
		return err
	}
	if t.confine != nil {
		if err := t.confine.check(rt.ID, st.MonomindVersion); err != nil {
			return err
		}
		t.cwd, t.orgMode, systemPrompt = t.confine.workDir, "", t.confine.systemPrompt()
	}
	budget, err := turnBudget(settings.BudgetUSD, t.budget, rt.ID)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(t.cwd); err != nil || !fi.IsDir() {
		return clicore.InvalidInput("this coder conversation's folder %s no longer exists; start a new conversation", t.cwd)
	}
	box, err := coderBox(settings, t.access, t.cwd, rt.ID, t.orgMode, cfg.DBPath())
	if err != nil {
		return err
	}
	bin, err := monomind.EnsureIn(ctx, t.cwd)
	if err != nil {
		return err
	}
	opts := monomind.ExecOptions{
		Bin:          bin,
		Runtime:      rt.ID,
		Prompt:       t.prompt + attachmentPrompt(t.attachments),
		Model:        t.model,
		Effort:       t.effort,
		EffortFlag:   st.caps.Has(monomind.CapAgentExecEffort),
		Resume:       t.resume,
		Cwd:          t.cwd,
		Access:       monomind.AccessFull,
		Settings:     monomind.CoderSettings,
		MaxTurns:     settings.MaxTurns,
		Timeout:      settings.timeout(),
		BudgetUSD:    budget,
		SystemPrompt: systemPrompt,
		Env:          agentSendEnv(journal, "lead", ""),
		Attach:       nativeAttachments(st.caps, t.attachments),
	}
	// The coder kind of browser permissions, vouched for by a token that
	// counts while this turn runs (its workers get their own, dynorg).
	for k, v := range t.env {
		opts.Env[k] = v
	}
	if t.confine != nil {
		t.confine.apply(&opts)
	}
	kind := t.agentKind
	if kind == "" {
		kind = browserctl.KindCoder
	}
	var kindDone func()
	if t.confine != nil && kind == browserctl.KindApply {
		// The token names the task, so the browser verbs derive its folders from it.
		kindDone = addKindEnv(opts.Env, func() ([]string, func(int), func()) { return issueApplyKind(t.confine.taskID) })
	} else {
		kindDone = addAgentKind(opts.Env, kind)
	}
	defer kindDone()
	// A confined turn without its vouched kind (the record could not be written:
	// the declared kind comes without its token) would be read as kind "other" by
	// the browser verbs, which scope neither its tab nor its files. It does not run.
	if t.confine != nil && (opts.Env[browserctl.EnvAgentKind] == "" || opts.Env[browserctl.EnvAgentKindToken] == "") {
		return &coderError{code: "confinement_unenforceable", err: &clicore.Error{Code: 3,
			Msg: fmt.Sprintf("task %s was not run: the record that vouches for its browser permissions could not be written (is ~/.monoagent writable?), and without it the task would not be held to its tab and files", t.confine.taskID)}}
	}
	kiloDropped := dropKiloOptions(&opts)
	onEvent := func(ev monomind.Event) {
		b, _ := json.Marshal(ev)
		fmt.Fprintln(cmd.OutOrStdout(), string(b))
	}
	if journal != nil {
		journal.cwd = t.cwd
		journal.coderRuntime = rt
		journal.notice(noticeCoderWorkspace, "Working in "+t.cwd, chatevents.SeverityInfo)
		onEvent = journal.handle
		if kiloDropped != "" {
			journal.notice(noticeCoderStatus, "Kilo takes no "+kiloDropped+": this turn runs without it", chatevents.SeverityWarning)
		}
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
	// Set last, so nothing a dynamic org derives from opts inherits the box.
	opts.Box = box
	if journal != nil {
		prev := opts.OnStart
		opts.OnStart = func(pid int) {
			journal.setLauncher(pid)
			if prev != nil {
				prev(pid)
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
		compactContext(ctx, journal, opts, res, err)
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

// runPlainCoderTurn is `chat --mode coder`: one unjournaled turn in the
// folder c picks, with the --attach files copied into it.
func runPlainCoderTurn(cmd *cobra.Command, cfg *Deps, c coderFolderChoice, t coderTurn, attach []string) error {
	if err := refuseCoderFromRuns(); err != nil {
		return err // before the folder is set up and the files copied
	}
	src, err := attachmentSources(attach)
	if err != nil {
		return err
	}
	if t.cwd, err = coderFolder(cmd, cfg, c, t.runtime); err != nil {
		return err
	}
	if t.attachments, err = copyAttachments(t.cwd, uuid.NewString(), src); err != nil {
		return err
	}
	return runCoderTurn(cmd, cfg, nil, t)
}

// coderFolderChoice is where a new coder conversation or plain coder turn
// works: a folder the user picked (--cwd), the coder root itself
// (--coder-root), a fresh test folder inside it (--new-workspace), or the
// active profile's own folder (--profile-folder, the app's default).
type coderFolderChoice struct {
	cwd          string
	root         bool
	newWorkspace bool
	profile      bool
}

func (c coderFolderChoice) set() int {
	n := 0
	for _, on := range []bool{c.cwd != "", c.root, c.newWorkspace, c.profile} {
		if on {
			n++
		}
	}
	return n
}

// coderFolder resolves c to a ready folder for runtime ("" = claude),
// initialized with that runtime's setup files.
func coderFolder(cmd *cobra.Command, cfg *Deps, c coderFolderChoice, runtime string) (string, error) {
	switch c.set() {
	case 0:
		return "", clicore.InvalidInput("coder mode needs a folder: pass --cwd <dir>, --profile-folder, --coder-root or --new-workspace")
	case 1:
	default:
		return "", clicore.InvalidInput("--cwd, --profile-folder, --coder-root and --new-workspace are alternatives; pass one")
	}
	db, err := cfg.OpenDB()
	if err != nil {
		return "", fmt.Errorf("initializing database: %w", err)
	}
	defer db.Close()
	settings, _, rt, err := requireCoderReady(cmd, db.DB, runtime)
	if err != nil {
		return "", err
	}
	if c.profile {
		ws, err := profileCoderWorkspace(cmd.Context(), db.DB, cfg.ProfileID(), rt.InitTarget)
		if err != nil {
			return "", err
		}
		return ws.Path, nil
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

// createCoderConversation is `chat history create` above the scoped level
// (--mode coder is full); runtimeID "" means claude.
func createCoderConversation(cmd *cobra.Command, cfg *Deps, access, runtimeID, model, effort, workflowID string, folder coderFolderChoice) (ai.Conversation, error) {
	// A conversation above scoped is a raise from nothing: an agent, the chat or
	// a workflow must not make itself a full-access one to run next.
	if err := clicore.RequireOperator(cfg.RequireOperator, "create a conversation above the scoped level"); err != nil {
		return ai.Conversation{}, err
	}
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
	return store.CreateConversationAccess(profileID, "agent", workflowID, runtimeID, "", model, access, dir, effort)
}

// addAgentKind adds kind's vouched environment (browserctl.IssueAgentKind)
// to env and returns its done.
func addAgentKind(env map[string]string, kind string) (done func()) {
	return addKindEnv(env, func() ([]string, func(int), func()) { return issueAgentKind(kind) })
}

// issueApplyKind is browserctl.IssueApplyKind; a var so a test can make it fail.
var (
	issueApplyKind = browserctl.IssueApplyKind
	issueAgentKind = browserctl.IssueAgentKind
)

func addKindEnv(env map[string]string, issue func() ([]string, func(int), func())) (done func()) {
	kindEnv, _, done := issue()
	for _, kv := range kindEnv {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return done
}

// agentSendEnv marks the agent's process so an outbound send it starts
// waits for approval (internal/hilsend) and is tagged to this chat and
// role, which is what puts the approval card in the right bubble.
func agentSendEnv(journal *turnJournal, role, agentID string) map[string]string {
	env := map[string]string{hilsend.EnvAgentContext: "1", hilsend.EnvAgentRole: role}
	if agentID != "" {
		env[hilsend.EnvAgentID] = agentID
	}
	if journal != nil && journal.conversationID != "" {
		env[hilsend.EnvConversation] = journal.conversationID
	}
	return env
}

// refuseCoderFromRuns refuses a coder turn that a workflow's process or the
// chat's runs start (workflow.EnvWorkflowRun, browserctl.EnvAICaller): a
// coder turn has full access to this computer, so a workflow a chat or an
// org built must not get one. An agent's shell, which has full access
// already, and the app's own turns are not refused.
func refuseCoderFromRuns() error {
	for _, m := range [][2]string{{workflow.EnvWorkflowRun, "a workflow run"}, {browserctl.EnvAICaller, "the chat's run"}} {
		if strings.TrimSpace(os.Getenv(m[0])) != "" {
			return &coderError{code: "operator_only", err: &clicore.Error{Code: 3, Msg: fmt.Sprintf(
				"%s can't start a coder chat (%s is set): a coder chat has full access to this computer. Start it from the app or your own terminal, "+
					"or queue one for the app with `monoagentcli coder queue add --kind ask --note <what to do>` (it reads the browser's active tab) and press Start there. %s",
				m[1], m[0], operator.InheritedHint())}}
		}
	}
	return nil
}
