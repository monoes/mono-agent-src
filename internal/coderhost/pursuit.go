package coderhost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/epoch"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/judge"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
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
)

// PursuitDeps turns the loop on. Model is the judge (resolved by the caller for the judging purpose) and Refuse its
// independence rule (both required: a loop without them does nothing).
type PursuitDeps struct {
	Model   judge.Model
	Refuse  func(pin string) bool
	Timeout time.Duration // one judge call; 0 = two minutes
	Clock   epoch.Clock   // nil = the system clock
	Every   time.Duration // spacing of passes; 0 = 5 s

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

func (d *PursuitDeps) ready() bool { return d != nil && d.Model != nil && d.Refuse != nil }

// pursuitMission is one mission whose pursuit is started.
type pursuitMission struct {
	ID      int64
	Profile string
	Conv    string // the lead's conversation; "" when the session is bound to none
}

// pursuitState is what the loop remembers between ticks (migration 167).
type pursuitState struct {
	SessionID      int64
	PersonAt       time.Time
	BaseSpentMicro int64
	Waiting        string
	WokenEpoch     int64
	WakesSince     int // wake-ups queued since the person last acted
	// shown by the status (pursuit progress), written with the rest when any of them changes
	Parked      int
	SpentAway   int64 // micro-USD of metered spend since the person last acted
	BudgetMicro int64
	HourWakes   int
	HourFreesAt string
	// the judge and the lead's turns (migration 168)
	BadRounds     int    // judge rounds in a row that failed
	BaseJudgeID   int64  // the judge ledger's highest id when the person last acted
	BaseLeadMicro int64  // the lead's and its helpers' turn-journal spend then
	JudgeMicro    int64  // judge spend since the person last acted (shown)
	LeadMicro     int64  // lead and helper turn spend since then (shown)
	JudgeCalls    int    // judge calls since then (shown)
	HourJudges    int    // judge calls of all missions in the last hour (shown)
	LastRound     string // how the latest judge round ended
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
		tctx, cancel := context.WithTimeout(ctx, h.Pursuit.timeout()+pursuitPassSlack)
		res, err := run.engine.Tick(tctx)
		cancel()
		if err != nil && ctx.Err() == nil {
			h.logf("coder host: pursuit of session %d: epoch: %v", m.ID, err)
		}
		h.noteEngineResult(m, run, res, err)
		h.pursuitPropose(ctx, rd, m, g.goal, res)
	}
	h.pursuitWake(ctx, rd, m, g)
}

// pursuitMayTick says whether an epoch step may spend now: the wake-up it earns must be deliverable, the lead must not owe
// an undelivered one already, and the engine's floor between two epochs must have passed (the engine would hold it anyway,
// after reading a whole snapshot). An epoch the engine left unfinished is resumed under the same conditions.
func (h *Host) pursuitMayTick(ctx context.Context, m pursuitMission, g pursuitGateResult) bool {
	if h.pursuitUndelivered(g) {
		return false
	}
	if g.hourJudges >= MaxPursuitJudgeCallsPerHour {
		return false // the host-wide cap on judge calls in an hour is used; it frees as the oldest call leaves the hour
	}
	if why := h.pursuitDeliverable(m, g); why != "" {
		return false
	}
	if ep := g.snap.Epochs; ep.HasLatest && (ep.Latest.State != types.EpochOpen && ep.Latest.State != types.EpochJudged) && h.Pursuit.clock().Now().Sub(time.Unix(ep.Latest.StartedAt, 0)) < types.MinEpochInterval {
		return false
	}
	return true
}

// pursuitUndelivered: an applied epoch whose wake-up is still owed (not queued yet, or queued and lost).
func (h *Host) pursuitUndelivered(g pursuitGateResult) bool {
	return !g.delivered
}

// pursuitDeliverable says why the lead cannot be woken now, or "": no chat, outside its active hours, not unattended,
// in a turn or with a message waiting, or no open work for it (the check-in this replaces had each of these conditions).
func (h *Host) pursuitDeliverable(m pursuitMission, g pursuitGateResult) string {
	if m.Conv == "" {
		return "the mission is bound to no chat"
	}
	lead, found, err := h.Store.GetSession(m.Profile, m.Conv)
	switch {
	case err != nil || !found:
		return "the lead's session does not read"
	case !lead.Unattended:
		return "the lead is not unattended"
	case lead.State != StateIdle || lead.TurnID != "" || h.Store.PendingFor(m.Profile, m.Conv) > 0:
		return "the lead is not free"
	}
	settings, err := h.Store.GetSettings(m.Profile, m.Conv)
	if err != nil || !withinActiveHours(settings.ActiveHours, h.Store.now()) {
		return "outside the lead's active hours"
	}
	if t := g.snap.Tasks; t.Open()-t.Blocked <= 0 {
		return "the mission has no open work"
	}
	if g.hourWakes >= MaxPursuitWakesPerHour {
		return "the host-wide cap on pursuit wake-ups in an hour is used"
	}
	return h.pursuitStreakStop(m.Profile, m.Conv, settings)
}

// pursuitStreakStop is the check-in limit applied to the pursuit's wake-ups, which are host-made turns like a check-in: no
// wake-up once the lead has had CheckinLimit host-made turns in a row with no message from the person between.
func (h *Host) pursuitStreakStop(profileID, conv string, settings Settings) string {
	streak, _, _, err := h.Store.CheckinStreak(profileID, conv)
	switch {
	case err != nil:
		return readFailed + "the lead's turns do not read"
	case streak >= settings.CheckinLimit:
		return "the lead has had its host-made turns in a row; it waits for a person"
	}
	return ""
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
		return g, false
	}
	leadNow, err := h.Store.leadTurnSpendMicro(m, tree)
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		return g, false
	}
	changed := !found
	switch {
	case !found:
		st = pursuitState{SessionID: m.ID, PersonAt: pa, BaseSpentMicro: spent, BaseJudgeID: judgeID, BaseLeadMicro: leadNow}
	case pa.After(st.PersonAt):
		// the person acted: the counters start again, the judge-failing stop is lifted
		st.PersonAt, st.BaseSpentMicro, st.WakesSince = pa, spent, 0
		st.BaseJudgeID, st.BaseLeadMicro, st.BadRounds = judgeID, leadNow, 0
		changed = true
	}
	judgeMicro, judgeCalls, err := h.Store.judgeSpendAfter(m.ID, st.BaseJudgeID)
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		return g, false
	}
	leadMicro := max(leadNow-st.BaseLeadMicro, 0)
	parked, err := h.Store.parkedSince(m, tree, st.PersonAt)
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		return g, false
	}
	away := AwayState{ParkedMax: pur.ParkedMax, Pct: pur.Pct, Parked: parked}
	if snap.Budget.Known {
		away.BudgetMicro = usdMicro(snap.Budget.BudgetUSD)
		away.Spend(spent - st.BaseSpentMicro)
		away.Spend(judgeMicro)
		away.Spend(leadMicro)
	}
	hourN, freesAt, err := h.Store.pursuitWakesLastHour()
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		return g, false
	}
	hourJ, err := h.Store.pursuitJudgeCallsLastHour()
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
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
	if changed {
		if err := h.Store.savePursuitState(st, h.Pursuit.clock().Now()); err != nil {
			h.logf("coder host: pursuit of session %d: %v", m.ID, err)
			return g, false
		}
	}
	g.st = st
	g.snap, g.goal = snap, goal
	g.delivered = h.Store.wakeDelivered(m, snap.Epochs, st.WokenEpoch)
	return g, true
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

// usdMicro is an amount in micro-USD, clamped so it can neither overflow nor go negative.
func usdMicro(usd float64) int64 {
	if math.IsNaN(usd) || usd <= 0 {
		return 0
	}
	if usd > 1e12 {
		return int64(1e12 * 1e6)
	}
	return int64(math.Round(usd * 1e6))
}

// pursuitStateOf is the loop's remembered state of a mission.
func (s *Store) pursuitStateOf(session int64) (pursuitState, bool, error) {
	st := pursuitState{SessionID: session}
	var at string
	err := s.db.QueryRow(`SELECT person_at, base_spent_micro, waiting, woken_epoch, wakes_since, parked, spent_away_micro, budget_micro, hour_wakes, hour_frees_at,
		bad_rounds, base_judge_id, base_lead_micro, judge_micro, lead_micro, judge_calls, hour_judges, last_round FROM mission_pursuit_state WHERE session_id = ?`, session).
		Scan(&at, &st.BaseSpentMicro, &st.Waiting, &st.WokenEpoch, &st.WakesSince, &st.Parked, &st.SpentAway, &st.BudgetMicro, &st.HourWakes, &st.HourFreesAt,
			&st.BadRounds, &st.BaseJudgeID, &st.BaseLeadMicro, &st.JudgeMicro, &st.LeadMicro, &st.JudgeCalls, &st.HourJudges, &st.LastRound)
	if errors.Is(err, sql.ErrNoRows) {
		return pursuitState{SessionID: session}, false, nil
	}
	if err != nil {
		return pursuitState{}, false, fmt.Errorf("reading the pursuit state: %w", err)
	}
	if at != "" {
		if st.PersonAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return pursuitState{}, false, fmt.Errorf("the pursuit state's time does not read: %w", err)
		}
	}
	return st, true, nil
}

// savePursuitState writes the state. It never writes woken_epoch on an existing row: that moves only in EnqueueWithPursuit,
// in the transaction that queues the wake-up.
func (s *Store) savePursuitState(st pursuitState, now time.Time) error {
	at := ""
	if !st.PersonAt.IsZero() {
		at = st.PersonAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.Exec(`INSERT INTO mission_pursuit_state (session_id, person_at, base_spent_micro, waiting, woken_epoch, wakes_since, parked, spent_away_micro, budget_micro, hour_wakes, hour_frees_at,
		bad_rounds, base_judge_id, base_lead_micro, judge_micro, lead_micro, judge_calls, hour_judges, last_round, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET person_at = excluded.person_at, base_spent_micro = excluded.base_spent_micro, waiting = excluded.waiting,
		wakes_since = excluded.wakes_since, parked = excluded.parked, spent_away_micro = excluded.spent_away_micro, budget_micro = excluded.budget_micro,
		hour_wakes = excluded.hour_wakes, hour_frees_at = excluded.hour_frees_at, bad_rounds = excluded.bad_rounds, base_judge_id = excluded.base_judge_id,
		base_lead_micro = excluded.base_lead_micro, judge_micro = excluded.judge_micro, lead_micro = excluded.lead_micro, judge_calls = excluded.judge_calls,
		hour_judges = excluded.hour_judges, last_round = excluded.last_round, updated_at = excluded.updated_at`,
		st.SessionID, at, st.BaseSpentMicro, st.Waiting, st.WokenEpoch, st.WakesSince, st.Parked, st.SpentAway, st.BudgetMicro, st.HourWakes, st.HourFreesAt,
		st.BadRounds, st.BaseJudgeID, st.BaseLeadMicro, st.JudgeMicro, st.LeadMicro, st.JudgeCalls, st.HourJudges, st.LastRound, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("saving the pursuit state: %w", err)
	}
	return nil
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
	jd, err := judge.New(judge.Config{Model: meteredModel{inner: d.Model, store: h.Store, session: m.ID}, Refuse: d.Refuse, Timeout: d.Timeout})
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
