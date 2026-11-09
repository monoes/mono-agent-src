package coderhost

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/epoch"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/judge"
	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/missionpursuit/inputs"
	"github.com/monoes/mono-agent/internal/missionpursuit/strategy"
)

// The host's pursuit loop (T6, S4): the one thing that wakes the lead of a mission whose pursuit the person started.
//
// It is OFF unless the host is given PursuitDeps (a judge model): with none, nothing here runs AND the check-ins of a
// mission whose pursuit is started go on as before (the exclusion below is gated on the same switch, so a started
// pursuit never leaves a lead without a waker). Per mission it runs only when the person started the pursuit
// (mission_pursuit.enabled, set only with a confirmed goal) and has not paused it. A paused pursuit wakes nothing, check-ins
// included: only the person's resume does (owner decision Q4). Everything it spends (a judge call, a
// wake-up of the lead) is re-checked against the run state in the same breath: inputs.Check is read again immediately
// before each, never taken from an earlier Snapshot. An unreadable answer is "stop". It adds no verb, route or tool: it
// reads host rows, writes its own state table (mission_pursuit_state) and queues one host-made send per applied epoch.
// What the lead then does, it does through its own tools and the usual approval classes; plan_approval stays hard.

// PursuitTurnPrefix starts the turn id of a wake-up the pursuit loop queues. Like a check-in it is host-made: it can never
// carry a mission confirmation, and the person's own messages are told apart from it by this prefix.
const PursuitTurnPrefix = "pursuit-"

// Hard stops that do not depend on the meter. The 20% rule counts the mission session's metered spend, the judge ledger and
// the lead's and helpers' turn journals (pursuit_meter.go); unknown cost is never taken as free. These bound the pursuit's
// activity by count as well.
const (
	// The wake-ups of one lead since the person last acted are limited per mission (mission_pursuit.away_wakes_max, default
	// 6, set when the person starts the pursuit); reaching it is "waiting for you".
	//
	// MaxPursuitWakesPerHour: wake-ups across ALL missions of this host in any hour (queued, ran or lost alike).
	MaxPursuitWakesPerHour = missionpursuit.MaxHostWakesPerHour

	// MaxPursuitJudgeCallsPerHour: judge calls across ALL missions of this host in any hour (a call that failed or was cut
	// off counts: it was paid for). Reaching it opens no epoch until the oldest call leaves the hour.
	MaxPursuitJudgeCallsPerHour = missionpursuit.MaxHostJudgeCallsPerHour

	// MaxBadJudgeRounds: this many judge rounds in a row that failed (invalid or oversized answer, model failure) stop the
	// pursuit and make it wait for the person ("judge-failing"); the person acting starts the count again.
	MaxBadJudgeRounds = missionpursuit.MaxBadJudgeRounds
)

const (
	maxPursuitsPerPass   = 4                // missions served by one pass; the rest wait for the next
	defaultPursuitEvery  = 5 * time.Second  // spacing of passes
	defaultEvidenceGap   = 30 * time.Minute // the most often a growth of the evidence can start an epoch
	pursuitPanicBackoff  = time.Minute
	pursuitPassSlack     = 30 * time.Second // time allowed on top of the judge's own timeout for one mission
	defaultPursuitJudge  = 2 * time.Minute
	pursuitWaitingBudget = "budget-unknown"
	pursuitWaitingParked = "parked"
	pursuitWaitingSpend  = "spend"
	pursuitWaitingLimits = "limits"
	pursuitWaitingEngine = "engine-parked"
	pursuitWaitingWakes  = "wakes"
	pursuitWaitingJudge  = "judge-failing"
	pursuitWaitingLead   = "lead-uncapped"
	pursuitWaitingMeter  = "meter-unreadable"
	pursuitWaitingPin    = "judge-pin"
	pursuitWaitingNoChat = "no-chat"
)

// PursuitDeps turns the loop on. Model is the judge (resolved by the caller for the judging purpose) and Refuse its
// independence rule (both required: a loop without them does nothing).
type PursuitDeps struct {
	Model   judge.Model
	Refuse  func(pin string) bool
	Timeout time.Duration // one judge call; 0 = two minutes
	Clock   epoch.Clock   // nil = the system clock
	Every   time.Duration // spacing of passes; 0 = 5 s

	// WakeCapUSD is the most one wake-up turn of the lead may spend (host-wide, X): the host starts a pursuit- turn under
	// min(what the lead has left, X) and never without a cap. It must be above 0 and at most MaxWakeCapUSD.
	WakeCapUSD float64
	// LeadPin names the runtime/model a conversation runs ("" = not known). It is read at the moment of each judge call and
	// each gate, so the independence rule follows a lead whose model changed: a judge that equals it, or a lead that is not
	// pinned, makes the pursuit wait.
	LeadPin func(profileID, conversationID string) string

	Interval         time.Duration // the epoch timer; the engine clamps it to its code bounds
	StallWindow      time.Duration
	MaxEpochsPerHour int
	EvidenceGap      time.Duration // 0 = 30 minutes
}

func (d *PursuitDeps) clock() epoch.Clock {
	if d == nil || d.Clock == nil {
		return epoch.RealClock{}
	}
	return d.Clock
}

func (d *PursuitDeps) ready() bool {
	return d != nil && d.Model != nil && d.Refuse != nil && d.LeadPin != nil && validWakeCap(d.WakeCapUSD)
}

// DefaultWakeCapUSD is the per-wake-up cap the product proposes; MaxWakeCapUSD is the most it may be set to (at 12 wake-ups
// an hour host-wide that is $120 an hour of wake-ups alone).
const (
	DefaultWakeCapUSD = 2.0
	MaxWakeCapUSD     = 10.0
	MinWakeCapUSD     = 0.01
)

// pursuitMission is one mission whose pursuit is started.
type pursuitMission struct {
	ID      int64
	Profile string
	Conv    string // the lead's conversation; "" when the session is bound to none
}

// pursuitIfDue starts a pass in its own goroutine when one is due and none is running: a judge call can take minutes and
// must not hold up the host's tick. Run waits for it.
func (h *Host) pursuitIfDue(ctx context.Context) {
	if !h.Pursuit.ready() {
		return
	}
	every := h.Pursuit.Every
	if every <= 0 {
		every = defaultPursuitEvery
	}
	now := h.Store.now()
	if now.Sub(h.lastPursuit) < every || !h.pursuitBusy.CompareAndSwap(false, true) {
		return
	}
	h.lastPursuit = now
	h.pursuitWG.Add(1)
	go func() {
		defer h.pursuitWG.Done()
		defer h.pursuitBusy.Store(false)
		h.pursuitPass(ctx)
	}()
}

// pursuitPass serves up to maxPursuitsPerPass started pursuits, resuming after the last one served. A panic in one
// mission is logged, backs that mission off for a minute and never stops the others or the host.
func (h *Host) pursuitPass(ctx context.Context) {
	if !h.Pursuit.ready() || ctx.Err() != nil {
		return
	}
	missions, err := h.pursuingMissions(ctx)
	if err != nil {
		h.logf("coder host: pursuit: listing the missions: %v", err)
		return
	}
	for _, m := range missions {
		if ctx.Err() != nil {
			return
		}
		h.pursueGuarded(ctx, m)
	}
}

func (h *Host) pursueGuarded(ctx context.Context, m pursuitMission) {
	h.pursuitMu.Lock()
	until := h.pursuitBackoff[m.ID]
	if !until.IsZero() && !h.Store.now().Before(until) {
		delete(h.pursuitBackoff, m.ID) // expired: not kept
		until = time.Time{}
	}
	h.pursuitMu.Unlock()
	if !until.IsZero() {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			h.logf("coder host: pursuit of session %d stopped by a panic: %v", m.ID, r)
			h.pursuitMu.Lock()
			if h.pursuitBackoff == nil {
				h.pursuitBackoff = map[int64]time.Time{}
			}
			h.pursuitBackoff[m.ID] = h.Store.now().Add(pursuitPanicBackoff)
			delete(h.pursuitRuns, m.ID)
			h.pursuitMu.Unlock()
		}
	}()
	h.pursue(ctx, m)
}

// pursuingMissions lists the next missions whose pursuit is started and whose session runs, after the cursor.
func (h *Host) pursuingMissions(ctx context.Context) ([]pursuitMission, error) {
	h.pursuitMu.Lock()
	after := h.pursuitCursor
	h.pursuitMu.Unlock()
	rows, err := h.Store.db.QueryContext(ctx, `SELECT s.id, s.profile_id, COALESCE(s.conversation_id, '') FROM mission_pursuit p JOIN task_sessions s ON s.id = p.session_id
		WHERE p.enabled = 1 AND s.kind = 'mission' AND s.state = 'running' AND s.archived_at = '' AND s.id > ? ORDER BY s.id LIMIT ?`, after, maxPursuitsPerPass)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pursuitMission
	for rows.Next() {
		var m pursuitMission
		if err := rows.Scan(&m.ID, &m.Profile, &m.Conv); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	h.pursuitMu.Lock()
	if len(out) < maxPursuitsPerPass {
		h.pursuitCursor = 0
	} else {
		h.pursuitCursor = out[len(out)-1].ID
	}
	h.pursuitMu.Unlock()
	return out, nil
}

// pursue is one mission's turn in a pass: the gate (read once), at most one epoch step, and the wake-up of the lead the
// epoch earned. The judge is paid for only when the wake-up it earns could be delivered (pursuitDeliverable), and not
// inside the engine's own floor between two epochs (no snapshot is read then either).
func (h *Host) pursue(ctx context.Context, m pursuitMission) {
	rd := inputs.New(h.Store.db)
	g, ok := h.pursuitGate(ctx, rd, m)
	if !ok {
		return
	}
	h.pursuitBind(ctx, m) // bookkeeping on rows the host owns: no spend, so it also runs while the pursuit waits
	if g.st.Waiting != "" {
		return
	}
	run, err := h.pursuitRunOf(m, g.goal)
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		return
	}
	if h.pursuitMayTick(ctx, m, g) {
		// The gate was read a moment ago: read the run state again right before the judge is paid for.
		if !h.pursuitRunning(ctx, rd, m.ID) {
			return
		}
		before, _ := h.Store.judgeLedgerHead(m.ID)
		tctx, cancel := context.WithTimeout(ctx, h.Pursuit.timeout()+pursuitPassSlack)
		res, err := run.engine.Tick(tctx)
		cancel()
		after, _ := h.Store.judgeLedgerHead(m.ID)
		if err != nil && ctx.Err() == nil {
			h.logf("coder host: pursuit of session %d: epoch: %v", m.ID, err)
		}
		h.noteEngineResult(m, run, res, err)
		if err != nil && ctx.Err() == nil && res.Outcome != epoch.OutcomeApplied && res.Outcome != epoch.OutcomeBadJudgment && after > before {
			// A judge call was paid for and the tick still failed (the judgment could not be recorded, so the epoch stays open and
			// the next pass would resume it with another paid call): that is a failed round, so the stop counts it.
			h.noteFailedRound(m, "store-failed")
		}
		h.pursuitPropose(ctx, rd, m, g.goal, res)
	}
	h.pursuitWake(ctx, rd, m, g)
}

// noteEngineResult keeps what a tick said that the person should be able to see: a parked engine is recorded (the next
// gate stores it as "engine-parked") and a held reason is logged when it changes.
func (h *Host) noteEngineResult(m pursuitMission, run *pursuitRun, res epoch.TickResult, err error) {
	held := string(res.Held)
	if res.Held == epoch.HeldParked {
		run.parked = true
	}
	if held != run.lastHeld {
		run.lastHeld = held
		if held != "" {
			h.logf("coder host: pursuit of session %d: the epoch engine holds (%s)", m.ID, held)
		}
	}
	h.noteJudgeRound(m, res, err)
}

func (d *PursuitDeps) timeout() time.Duration {
	if d.Timeout > 0 {
		return d.Timeout
	}
	return defaultPursuitJudge
}

// pursuitRunning is inputs.Check read now: true only for a clean "running". An error is "stop".
func (h *Host) pursuitRunning(ctx context.Context, rd *inputs.Reader, session int64) bool {
	run, err := rd.Check(ctx, session)
	return err == nil && run.Running
}

// pursuitGateResult is a fresh read of a started pursuit that may go on.
type pursuitGateResult struct {
	st         pursuitState
	snap       inputs.Snapshot
	goal       missionpursuit.Goal
	delivered  bool // the latest applied epoch's wake-up is queued or ran (filled by the gate)
	hourWakes  int  // pursuit wake-ups of ALL missions in the last hour
	hourJudges int  // judge calls of ALL missions in the last hour
}

// pursuitGate reads the run state, the confirmed goal, the facts and the away counters in one go and says whether the
// pursuit may spend now. ok is false when anything is unreadable or the pursuit does not run. Waiting says the person
// must act first; the reason is stored where the person can see it.
func (h *Host) pursuitGate(ctx context.Context, rd *inputs.Reader, m pursuitMission) (pursuitGateResult, bool) {
	var g pursuitGateResult
	snap, err := rd.Snapshot(ctx, m.ID)
	if err != nil || !snap.Run.Running {
		if err != nil && ctx.Err() == nil {
			h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		}
		return g, false
	}
	goal, err := missionpursuit.NewStore(h.Store.db).Get(ctx, m.Profile, m.ID)
	if err != nil || (goal.State != missionpursuit.StateConfirmed && goal.State != missionpursuit.StateLocked) || len(goal.Criteria) == 0 {
		h.logf("coder host: pursuit of session %d: no confirmed goal with a criterion (%v)", m.ID, err)
		return g, false // no confirmed goal with a criterion: nothing to pursue
	}
	pur, err := missionpursuit.NewStore(h.Store.db).GetPursuit(ctx, m.Profile, m.ID)
	if err != nil || !pur.Enabled {
		h.logf("coder host: pursuit of session %d: the pursuit record does not read (%v)", m.ID, err)
		return g, false
	}
	tree, err := h.Store.missionTree(m)
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		return g, false
	}
	pa, err := h.Store.personActionAt(m, tree)
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		return g, false
	}
	spent := usdMicro(snap.Budget.SpentUSD)
	st, found, err := h.Store.pursuitStateOf(m.ID)
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		return g, false
	}
	// What the 20% rule counts beyond the mission session's own meter, all measured by the host: the judge ledger, and the
	// turn journals of the lead and its helpers.
	judgeID, err := h.Store.judgeLedgerHead(m.ID)
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		h.stallUnreadable(m)
		return g, false
	}
	leadNow, err := h.Store.leadTurnSpendMicro(m, tree)
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		h.stallUnreadable(m)
		return g, false
	}
	changed, reset := !found, false
	switch {
	case !found:
		st = pursuitState{SessionID: m.ID, PersonAt: pa, BaseSpentMicro: spent, BaseJudgeID: judgeID, BaseLeadMicro: leadNow}
	case pa.After(st.PersonAt):
		// the person acted: the counters start again, the judge-failing stop is lifted
		st.PersonAt, st.BaseSpentMicro, st.WakesSince = pa, spent, 0
		st.BaseJudgeID, st.BaseLeadMicro, st.BadRounds, st.LastRound = judgeID, leadNow, 0, ""
		changed, reset = true, true
	}
	judgeMicro, judgeCalls, err := h.Store.judgeSpendAfter(m.ID, st.BaseJudgeID)
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		h.stallUnreadable(m)
		return g, false
	}
	leadMicro := max(leadNow-st.BaseLeadMicro, 0)
	parked, err := h.Store.parkedSince(m, tree, st.PersonAt)
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		return g, false
	}
	// Money the lead has handed to helpers and they have not spent is committed too: a helper may spend it all after this gate.
	carveMicro, err := h.Store.helperCarveUnspentMicro(m, tree)
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		h.stallUnreadable(m)
		return g, false
	}
	away := AwayState{ParkedMax: pur.ParkedMax, Pct: pur.Pct, Parked: parked}
	if snap.Budget.Known {
		away.BudgetMicro = usdMicro(snap.Budget.BudgetUSD)
		away.Spend(spent - st.BaseSpentMicro)
		away.Spend(judgeMicro)
		away.Spend(leadMicro)
		away.Spend(carveMicro)
	}
	hourN, freesAt, err := h.Store.pursuitWakesLastHour()
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		return g, false
	}
	hourJ, err := h.Store.pursuitJudgeCallsLastHour()
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		h.stallUnreadable(m)
		return g, false
	}
	g.hourWakes, g.hourJudges = hourN, hourJ
	shown := pursuitState{Parked: parked, SpentAway: away.SpentMicro, BudgetMicro: away.BudgetMicro, HourWakes: hourN, HourFreesAt: freesAt,
		JudgeMicro: judgeMicro, LeadMicro: leadMicro, JudgeCalls: judgeCalls, HourJudges: hourJ}
	if shown.Parked != st.Parked || shown.SpentAway != st.SpentAway || shown.BudgetMicro != st.BudgetMicro || shown.HourWakes != st.HourWakes || shown.HourFreesAt != st.HourFreesAt ||
		shown.JudgeMicro != st.JudgeMicro || shown.LeadMicro != st.LeadMicro || shown.JudgeCalls != st.JudgeCalls || shown.HourJudges != st.HourJudges {
		st.Parked, st.SpentAway, st.BudgetMicro, st.HourWakes, st.HourFreesAt = shown.Parked, shown.SpentAway, shown.BudgetMicro, shown.HourWakes, shown.HourFreesAt
		st.JudgeMicro, st.LeadMicro, st.JudgeCalls, st.HourJudges = shown.JudgeMicro, shown.LeadMicro, shown.JudgeCalls, shown.HourJudges
		changed = true
	}
	waiting := awayReason(away, snap.Budget.Known, st.WakesSince, pur.WakesMax)
	if waiting == "" {
		waiting = h.pursuitStopReason(m, st)
	}
	if h.pursuitEngineParked(m.ID) {
		waiting = pursuitWaitingEngine
	}
	if waiting != st.Waiting {
		st.Waiting, changed = waiting, true
	}
	if !found {
		err = h.Store.insertPursuitState(st, h.Pursuit.clock().Now())
	} else if changed {
		err = h.Store.updateGateState(st, reset, h.Pursuit.clock().Now())
	}
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		return g, false
	}
	g.st = st
	g.snap, g.goal = snap, goal
	g.delivered = h.Store.wakeDelivered(m, snap.Epochs, st.WokenEpoch)
	return g, true
}

// stallUnreadable makes a spend that cannot be measured visible: the pursuit waits ("meter-unreadable") instead of stalling with
// no reason. The next pass that can read again clears it.
func (h *Host) stallUnreadable(m pursuitMission) {
	_ = h.Store.setPursuitWaiting(m.ID, pursuitWaitingMeter, false, h.Pursuit.clock().Now())
}

// awayReason is why the pursuit may not open new work, or "". An unknown budget is unknown, never unlimited.
func awayReason(a AwayState, budgetKnown bool, wakes, wakesMax int) string {
	switch {
	case wakesMax < 1 || wakesMax > missionpursuit.MaxWakesMax:
		return pursuitWaitingLimits
	case wakes >= wakesMax:
		return pursuitWaitingWakes
	case a.ParkedMax < 1 || a.ParkedMax > 20 || a.Pct < 1 || a.Pct > 100:
		return pursuitWaitingLimits
	case !budgetKnown:
		return pursuitWaitingBudget
	case a.Parked >= a.ParkedMax:
		return pursuitWaitingParked
	case a.WaitingForYou():
		return pursuitWaitingSpend
	}
	return ""
}

// pursuitRunOf is the epoch engine of a mission, built once and kept; a panic or a restart builds it again over the
// same stored epochs, so it resumes where the store says it was.
func (h *Host) pursuitRunOf(m pursuitMission, goal missionpursuit.Goal) (*pursuitRun, error) {
	h.pursuitMu.Lock()
	defer h.pursuitMu.Unlock()
	if r := h.pursuitRuns[m.ID]; r != nil {
		return r, nil
	}
	d := h.Pursuit
	jd, err := judge.New(judge.Config{Model: meteredModel{inner: d.Model, store: h.Store, session: m.ID}, Refuse: h.pursuitRefuse(m), Timeout: d.Timeout})
	if err != nil {
		return nil, err
	}
	gap := d.EvidenceGap
	if gap <= 0 {
		gap = defaultEvidenceGap
	}
	in := newPursuitInputs(h.Store.db, m.Profile, m.ID, len(goal.Criteria), goalCriteria(goal), d.clock(), gap)
	rd := inputs.New(h.Store.db)
	eng, err := epoch.New(epoch.Config{Profile: strconv.FormatInt(m.ID, 10), Generation: 1, Interval: d.Interval, StallWindow: d.StallWindow, MaxEpochsPerHour: d.MaxEpochsPerHour},
		epoch.Deps{Store: strategy.NewStore(h.Store.db), Judge: jd, Inputs: in, Run: rd.RunOf(m.ID), Clock: d.clock()})
	if err != nil {
		return nil, err
	}
	if h.pursuitRuns == nil {
		h.pursuitRuns = map[int64]*pursuitRun{}
	}
	r := &pursuitRun{engine: eng}
	h.pursuitRuns[m.ID] = r
	return r, nil
}

type pursuitRun struct {
	engine   *epoch.Engine
	parked   bool   // the engine reported itself parked (a stored row failed verification): it stays so until a restart
	lastHeld string // the last reason the engine held, so that a reason is logged when it changes and not at every pass
}

func (h *Host) pursuitEngineParked(session int64) bool {
	h.pursuitMu.Lock()
	defer h.pursuitMu.Unlock()
	r := h.pursuitRuns[session]
	return r != nil && r.parked
}

// The pursuit fields of the Host, kept together here.
type pursuitHostState struct {
	pursuitMu      sync.Mutex
	pursuitWG      sync.WaitGroup
	lastPursuit    time.Time
	pursuitBusy    atomic.Bool
	pursuitCursor  int64
	pursuitBackoff map[int64]time.Time
	pursuitRuns    map[int64]*pursuitRun
}

// sessionPursuing says whether the person started the pursuit of the mission session (paused or not: a paused pursuit
// still wakes nothing but the person's resume).
func (h *Host) sessionPursuing(session int64) (bool, error) {
	if !h.Pursuit.ready() {
		return false, nil // no loop is wired: the check-ins go on
	}
	var n int
	err := h.Store.db.QueryRow(`SELECT COUNT(*) FROM mission_pursuit WHERE session_id = ? AND enabled = 1`, session).Scan(&n)
	return n > 0, err
}

// conversationPursuing says whether the conversation is the lead of a mission whose pursuit is started. A read that
// fails says no: the pursuit loop reads the same rows and cannot wake the lead either, so nothing is woken twice, and a
// check-in's own failed-read handling stays as it was.
func (h *Host) conversationPursuing(profileID, conversationID string) bool {
	if !h.Pursuit.ready() {
		return false
	}
	var n int
	err := h.Store.db.QueryRow(`SELECT COUNT(*) FROM task_sessions s JOIN mission_pursuit p ON p.session_id = s.id
		WHERE s.profile_id = ? AND s.conversation_id = ? AND s.archived_at = '' AND p.enabled = 1`, profileID, conversationID).Scan(&n)
	return err == nil && n > 0
}

// pursuitWakeStillAllowed re-checks, when a wake-up is about to run, that the pursuit still runs. It returns why not, or "".
func (h *Host) pursuitWakeStillAllowed(x Session) string {
	if !h.Pursuit.ready() {
		return errNoPursuitLoop.Error() // a wake-up queued before this host restarted with the loop off
	}
	m := pursuitMission{Profile: x.ProfileID, Conv: x.ConversationID}
	err := h.Store.db.QueryRow(`SELECT s.id FROM task_sessions s JOIN mission_pursuit p ON p.session_id = s.id
		WHERE s.profile_id = ? AND s.conversation_id = ? AND s.archived_at = '' AND p.enabled = 1 ORDER BY s.id DESC LIMIT 1`, x.ProfileID, x.ConversationID).Scan(&m.ID)
	if err != nil {
		return "the session is no longer the lead of a started pursuit"
	}
	ctx := context.Background()
	// the run state AND the away limits, read now: a wake-up queued before a limit was reached does not run after it
	g, ok := h.pursuitGate(ctx, inputs.New(h.Store.db), m)
	if !ok {
		return "the mission's pursuit is paused or stopped"
	}
	if g.st.Waiting != "" {
		return missionpursuit.WaitingWhy(g.st.Waiting)
	}
	return h.pursuitStillDeliverable(x, g)
}

// pursuitStillDeliverable is the run-time half of the wake-up's conditions, the ones that can change while it waits: the
// lead unattended, inside its active hours, the mission with open work, and the host-made turns in a row below the limit.
// (The lead being free is the host's own queue order; the hourly cap was decided when the wake-up was queued.)
func (h *Host) pursuitStillDeliverable(x Session, g pursuitGateResult) string {
	lead, found, err := h.Store.GetSession(x.ProfileID, x.ConversationID)
	switch {
	case err != nil || !found:
		return readFailed + "the lead's session does not read"
	case !lead.Unattended:
		return "the lead is not unattended"
	}
	settings, err := h.Store.GetSettings(x.ProfileID, x.ConversationID)
	switch {
	case err != nil:
		return readFailed + "the session's settings do not read: " + err.Error()
	case !withinActiveHours(settings.ActiveHours, h.Store.now()):
		return "outside the lead's active hours"
	}
	if t := g.snap.Tasks; t.Open()-t.Blocked <= 0 {
		return "the mission has no open work"
	}
	return h.pursuitStreakStop(x.ProfileID, x.ConversationID, settings)
}

// pursuitWakesLastHour counts the pursuit wake-ups of every mission queued in the last hour (any status: a lost one was
// still paid for in the lead's attention) and says when the oldest leaves the hour. An unreadable time counts.
func (s *Store) pursuitWakesLastHour() (int, string, error) {
	since := s.now().Add(-time.Hour)
	rows, err := s.db.Query(`SELECT created_at FROM coder_session_commands WHERE kind = 'send' AND substr(turn_id, 1, 8) = 'pursuit-' AND ` + tsInWindow("created_at", since) + ` LIMIT 1000`)
	if err != nil {
		return 0, "", fmt.Errorf("counting the pursuit wake-ups of the hour: %w", err)
	}
	defer rows.Close()
	n := 0
	var oldest time.Time
	for rows.Next() {
		var at string
		if err := rows.Scan(&at); err != nil {
			return 0, "", err
		}
		n++
		if t, perr := time.Parse(time.RFC3339Nano, at); perr == nil && (oldest.IsZero() || t.Before(oldest)) {
			oldest = t
		}
	}
	if oldest.IsZero() {
		return n, "", rows.Err()
	}
	return n, oldest.Add(time.Hour).UTC().Format(time.RFC3339), rows.Err()
}

// PursuitHoldsCheckins says whether the check-ins of the conversation are held back for the pursuit loop. They are only
// when the loop is wired into this host (Host.Pursuit set) AND the conversation leads a mission whose pursuit is started:
// a host without the loop behaves as if no pursuit were started. Production wiring of Host.Pursuit is deferred to the slice
// that brings the real judge (S5); the production-wiring test in internal/cli/chat pins that until then it is nil.
func (h *Host) PursuitHoldsCheckins(profileID, conversationID string) bool {
	return h.conversationPursuing(profileID, conversationID)
}
