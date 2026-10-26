package chat

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/monoes/mono-agent/internal/agentroster"
	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/profilectx"
)

// noticeOrgUnavailable is journaled when a dynamic-org conversation's turn
// has to run solo, with the reason.
const noticeOrgUnavailable = "org.unavailable"

// orgUnavailableReason says why this turn's lead can't get org tools, or
// "" when it can: monomind needs caller tools alongside full access
// (agent-exec-full-access-tools), on this runtime.
func orgUnavailableReason(st coderStatus, runtime string) string {
	if !st.caps.Has(monomind.CapAgentExecFullAccessTools) {
		return "this monomind can't give a full-access agent caller tools (needs monomind 2.19 or newer), so the agent works alone this turn"
	}
	if st.scan == nil {
		return "the runtime scan failed, so the agent works alone this turn"
	}
	e := st.scan.Find(runtime)
	if e == nil || !e.CallerToolsWithFullAccess {
		return runtime + " can't take caller tools with full access, so the agent works alone this turn"
	}
	return ""
}

// orgRoster is the models a dynamic org may staff: the ready (and stale)
// entries of the validated roster whose runtime is installed and runs with
// full or read access. With no roster it is the lead's own model alone.
func orgRoster(ctx context.Context, db *sql.DB, st coderStatus, lead dynorg.Model) []dynorg.Model {
	results, err := agentroster.List(ctx, db)
	if err != nil || len(results) == 0 {
		return []dynorg.Model{lead}
	}
	readCap := st.caps.Has(monomind.CapAgentExecAccessRead)
	var models []dynorg.Model
	for _, rr := range agentroster.Build(results, st.scan, time.Now(), agentroster.DefaultMaxAge) {
		var e *monomind.ScanEntry
		if st.scan != nil {
			e = st.scan.Find(rr.Runtime)
		}
		if e == nil || !e.Installed {
			continue
		}
		read := readCap && slices.Contains(e.AccessModes, monomind.AccessRead)
		if !e.FullAccess && !read {
			continue
		}
		for _, m := range rr.Models {
			if m.State != agentroster.StateReady && m.State != agentroster.StateStale {
				continue
			}
			model := m.Model
			if model == agentroster.DefaultModel {
				model = ""
			}
			models = append(models, dynorg.Model{
				Runtime: rr.Runtime, Model: model, Label: m.Label, Efforts: m.EffortLevels,
				FullAccess: e.FullAccess, Read: read, Resume: e.Resume, ReadOnlySandbox: readOnlySandbox(st, e), Fidelity: e.ToolActivityFidelity,
				CallerTools: e.CallerTools, CallerToolsFull: e.CallerToolsWithFullAccess, ReportsCost: e.ReportsCost,
				CostUSD: m.CostUSD, LatencyMs: m.LatencyMs, Stale: m.State == agentroster.StateStale,
			})
		}
	}
	if len(models) == 0 {
		return []dynorg.Model{lead}
	}
	// The lead's own model first, then ready before stale.
	slices.SortStableFunc(models, func(a, b dynorg.Model) int {
		switch {
		case a.Key() == lead.Key() && b.Key() != lead.Key():
			return -1
		case b.Key() == lead.Key() && a.Key() != lead.Key():
			return 1
		case a.Stale != b.Stale && !a.Stale:
			return -1
		case a.Stale != b.Stale:
			return 1
		}
		return 0
	})
	return models
}

// readOnlySandbox reports whether agent exec can run this runtime in a
// read-only sandbox, as SandboxArgs decides it from the scan's modes.
func readOnlySandbox(st coderStatus, e *monomind.ScanEntry) bool {
	if e == nil || !st.caps.Has(monomind.CapAgentExecSandbox) {
		return false
	}
	_, eff := monomind.SandboxArgs(st.caps, e.SandboxModes, e.ID, monomind.SandboxReadOnly)
	return eff == monomind.SandboxStatusSandboxed
}

// startDynamicOrg wires a conductor into a dynamic-org coder turn: the lead
// gets the org tools and the org part of its system prompt. It returns a
// close func that ends every worker (call it before the turn finishes) and
// the func the lead's own events go through (its file edits take the write
// lease), or nils with the journal told why the turn runs solo.
func startDynamicOrg(ctx context.Context, cfg *Deps, journal *turnJournal, settings coderSettings, st coderStatus, rt monomind.CoderRuntime, t coderTurn, opts *monomind.ExecOptions) (func(), func(monomind.Event)) {
	if reason := orgUnavailableReason(st, rt.ID); reason != "" {
		journal.notice(noticeOrgUnavailable, reason, chatevents.SeverityWarning)
		return nil, nil
	}
	db, err := cfg.OpenDB()
	if err != nil {
		journal.notice(noticeOrgUnavailable, "the agent works alone this turn: "+err.Error(), chatevents.SeverityWarning)
		return nil, nil
	}
	// Workers see the active profile (name, folder, counts, names only); the
	// lead has it in its own prompt already.
	dynorg.ProfileContext = func(ctx context.Context) string { return profilectx.Block(ctx, db.DB, cfg.ProfileID()) }
	dynorg.ProfileID = func(context.Context) string { return cfg.ProfileID() } // their --profile on every monoagentcli command
	lead := dynorg.Model{Runtime: rt.ID, Model: t.model, FullAccess: true, Resume: rt.Resume}
	if e := st.scan.Find(rt.ID); e != nil {
		lead.Read = st.caps.Has(monomind.CapAgentExecAccessRead) && slices.Contains(e.AccessModes, monomind.AccessRead)
		lead.ReadOnlySandbox = readOnlySandbox(st, e)
		lead.CallerTools, lead.CallerToolsFull = e.CallerTools, e.CallerToolsWithFullAccess
		lead.Fidelity = e.ToolActivityFidelity
		lead.ReportsCost = e.ReportsCost
	}
	lib := &dynorg.MonomindLibrary{Bin: opts.Bin, Cwd: t.cwd}
	staffer := &dynorg.Staffer{
		Roster: orgRoster(ctx, db.DB, st, lead), Lead: lead, ModelPicker: settings.OrgModelPicker, Router: settings.OrgRouter,
		Picker: lib, Library: lib, NoAppAccess: !settings.appAccess(),
	}
	if q, err := agentroster.LoadQuality(ctx, db.DB, time.Now()); err == nil {
		staffer.Quality = q
	}
	if client, err := jevconf.NewClient(ctx, db.DB, cfg.ProfileID(), "", "", jevconf.DynamicOrg); err == nil {
		staffer.Chooser = dynorg.JevChooser{Client: client}
	}
	limits := dynorg.Limits{MaxAgents: settings.OrgMaxAgents, MaxConcurrent: settings.OrgMaxConcurrent, BudgetUSD: settings.OrgBudgetUSD}
	// A task's workers draw from a share of its remaining cap, never unlimited.
	pool, taskExec, isTask, refuse := taskWorkers(db.DB, cfg, journal.conversationID, journal.turnID, settings, opts, monomind.Exec)
	if isTask && taskExec == nil {
		db.Close()
		journal.notice(noticeOrgUnavailable, refuse, chatevents.SeverityWarning)
		return nil, nil
	}
	if isTask {
		limits.BudgetUSD = pool
	}
	base := monomind.ExecOptions{
		Bin: opts.Bin, Settings: monomind.CoderSettings, MaxTurns: settings.MaxTurns, Timeout: settings.timeout(),
		EffortFlag: opts.EffortFlag, Env: agentSendEnv(journal, "", ""),
	}
	mission := missionConfig(ctx, db.DB, cfg.ProfileID(), journal.conversationID, t)
	limits.MaxAgents = missionMaxAgents(settings, mission)
	writers := missionWriters(ctx, settings, mission, t.cwd)
	if writers == dynorg.WritersIsolated {
		reconcileTurnWorktrees(ctx, journal, t.cwd)
	}
	cond := dynorg.New(ctx, dynorg.Config{
		Cwd: t.cwd, Limits: limits, Staffer: staffer, Base: base, Exec: taskExec,
		Writers: writers, TurnID: journal.turnID,
		ReadAccess: st.caps.Has(monomind.CapAgentExecAccessRead),
		Emit:       journal,
		Answers:    journalAnswers{journal},
		Outcome: func(runtime, model, status, detail string, at time.Time) {
			_ = agentroster.RecordOutcome(context.WithoutCancel(ctx), db.DB, runtime, model, status, detail, at)
		},
		Quality: &dynorg.Quality{Record: func(e agentroster.QualityEvent) {
			_ = agentroster.RecordQuality(context.WithoutCancel(ctx), db.DB, e)
		}},
		Remember: journal.rememberWorker,
		Mission:  mission,
		Missions: missionRun(mission),
	})
	cond.AddVeterans(journal.veterans())
	opts.Tools = dynorg.ToolSpecs(settings.appAccess())
	opts.OnToolCall = cond.Handle
	opts.ToolTimeout = dynorg.ToolTimeout
	// The profile context is already in the lead's prompt (coderLeadSystemPrompt).
	opts.SystemPrompt += dynorg.LeadPrompt(limits, settings.appAccess()) + dynorg.LeadExtras(settings.OrgRouter)
	opts.Tools = append(opts.Tools, dynorg.MissionPlanToolSpecs(cond.Mission())...)
	opts.Tools = append(opts.Tools, dynorg.MissionRunToolSpecs(ctx, cond.Mission())...)
	opts.SystemPrompt += dynorg.MissionPrompt(cond.Mission())
	if cond.Isolated() {
		opts.Tools = append(opts.Tools, dynorg.MergeToolSpec())
		opts.SystemPrompt += dynorg.IsolatedLeadPrompt
	}
	// `chat turn stop --agent` reaches this turn's workers through its
	// mailbox folder (#255). An unknown agent id is a no-op.
	stopWatch := func() {}
	if controlID.MatchString(journal.turnID) {
		stopWatch = watchAgentStops(agentControlDir(cfg, journal.turnID), func(id string) { _, _ = cond.Stop(id) })
	}
	return func() {
		stopWatch()
		cond.Close()
		db.Close()
	}, cond.LeadEvent
}

// journalAnswers hands a turn's conductor the answers `chat turn answer`
// stored for its workers' questions (#256).
type journalAnswers struct{ j *turnJournal }

func (a journalAnswers) Answer(_ context.Context, agentID, questionID string) (string, bool, error) {
	return a.j.store.TakeAnswer(a.j.profileID, a.j.turnID, agentID, questionID)
}

// Emit implements dynorg.Emitter: a worker's event, journaled in the
// lead's turn. Events after the turn finished are dropped.
func (j *turnJournal) Emit(typ chatevents.EventType, payload any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.finished {
		return
	}
	_ = j.appendLocked(typ, payload)
}

// reconcileTurnWorktrees removes the worktrees earlier turns in this folder
// left behind (a crash, a killed process) before an isolated turn starts,
// and journals the branches it kept because their work was never merged.
func reconcileTurnWorktrees(ctx context.Context, journal *turnJournal, cwd string) {
	active, err := activeTurnIDs(journal.store)
	if err != nil {
		return
	}
	for _, r := range dynorg.ReconcileWorktrees(context.WithoutCancel(ctx), cwd, func(id string) bool { return active[id] }) {
		switch {
		case r.Error != "":
			journal.notice(dynorg.NoticeBranchKept, fmt.Sprintf("A worktree an earlier turn left (%s) was kept: %s", r.Path, r.Error), chatevents.SeverityWarning)
		case r.BranchKept:
			journal.notice(dynorg.NoticeBranchKept, fmt.Sprintf("An earlier turn's worker %s left unmerged work; it is kept on branch %s.", r.Agent, r.Branch), chatevents.SeverityInfo)
		}
	}
}

// activeTurnIDs is the set of turns still running, in every profile.
func activeTurnIDs(store *ai.AIStore) (map[string]bool, error) {
	turns, err := store.QueryActiveTurns()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(turns))
	for _, t := range turns {
		out[t.ID] = true
	}
	return out, nil
}
