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
	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/missionpursuit/inputs"
	"github.com/monoes/mono-agent/internal/missionpursuit/strategy"
)

// The host's pursuit loop (T6, S4): the one thing that wakes the lead of a mission whose pursuit the person started.
//
// It is OFF unless the host is given PursuitDeps (a judge model), and per mission unless the person started the pursuit
// (mission_pursuit.enabled, set only with a confirmed goal) and has not paused it. Everything it spends (a judge call, a
// wake-up of the lead) is re-checked against the run state in the same breath: inputs.Check is read again immediately
// before each, never taken from an earlier Snapshot. An unreadable answer is "stop". It adds no verb, route or tool: it
// reads host rows, writes its own state table (mission_pursuit_state) and queues one host-made send per applied epoch.
// What the lead then does, it does through its own tools and the usual approval classes; plan_approval stays hard.

// PursuitTurnPrefix starts the turn id of a wake-up the pursuit loop queues. Like a check-in it is host-made: it can never
// carry a mission confirmation, and the person's own messages are told apart from it by this prefix.
const PursuitTurnPrefix = "pursuit-"

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
	if d.Clock == nil {
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
	h.pursuitMu.Unlock()
	if !until.IsZero() && h.Store.now().Before(until) {
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

// pursue is one mission's turn in a pass: the gate, at most one epoch step, and the wake-up of the lead the epoch earned.
func (h *Host) pursue(ctx context.Context, m pursuitMission) {
	rd := inputs.New(h.Store.db)
	gate, ok := h.pursuitGate(ctx, rd, m)
	if !ok || gate.st.Waiting != "" {
		return
	}
	run, err := h.pursuitRunOf(m, gate.goal)
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		return
	}
	// The gate was read a moment ago: read the run state again right before the judge is paid for.
	if !h.pursuitRunning(ctx, rd, m.ID) {
		return
	}
	tctx, cancel := context.WithTimeout(ctx, h.Pursuit.timeout()+pursuitPassSlack)
	_, err = run.engine.Tick(tctx)
	cancel()
	if err != nil && ctx.Err() == nil {
		h.logf("coder host: pursuit of session %d: epoch: %v", m.ID, err)
	}
	h.pursuitWake(ctx, rd, m, run)
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
	st   pursuitState
	snap inputs.Snapshot
	goal missionpursuit.Goal
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
	pa, err := h.Store.personActionAt(m)
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
	changed := !found
	switch {
	case !found:
		st = pursuitState{SessionID: m.ID, PersonAt: pa, BaseSpentMicro: spent}
	case pa.After(st.PersonAt):
		st.PersonAt, st.BaseSpentMicro = pa, spent // the person acted: the counters start again
		changed = true
	}
	parked, err := h.Store.parkedSince(m, st.PersonAt)
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		return g, false
	}
	away := AwayState{ParkedMax: pur.ParkedMax, Pct: pur.Pct, Parked: parked}
	if snap.Budget.Known {
		away.BudgetMicro = usdMicro(snap.Budget.BudgetUSD)
		away.Spend(spent - st.BaseSpentMicro)
	}
	if waiting := awayReason(away, snap.Budget.Known); waiting != st.Waiting {
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
	return g, true
}

// awayReason is why the pursuit may not open new work, or "". An unknown budget is unknown, never unlimited.
func awayReason(a AwayState, budgetKnown bool) string {
	switch {
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
	err := s.db.QueryRow(`SELECT person_at, base_spent_micro, waiting, woken_epoch FROM mission_pursuit_state WHERE session_id = ?`, session).
		Scan(&at, &st.BaseSpentMicro, &st.Waiting, &st.WokenEpoch)
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

// savePursuitState writes the state; the woken epoch only ever moves forward.
func (s *Store) savePursuitState(st pursuitState, now time.Time) error {
	at := ""
	if !st.PersonAt.IsZero() {
		at = st.PersonAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.Exec(`INSERT INTO mission_pursuit_state (session_id, person_at, base_spent_micro, waiting, woken_epoch, updated_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET person_at = excluded.person_at, base_spent_micro = excluded.base_spent_micro, waiting = excluded.waiting,
		updated_at = excluded.updated_at`, st.SessionID, at, st.BaseSpentMicro, st.Waiting, st.WokenEpoch, now.UTC().Format(time.RFC3339Nano))
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
	jd, err := judge.New(judge.Config{Model: d.Model, Refuse: d.Refuse, Timeout: d.Timeout})
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

type pursuitRun struct{ engine *epoch.Engine }

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
	var n int
	err := h.Store.db.QueryRow(`SELECT COUNT(*) FROM mission_pursuit WHERE session_id = ? AND enabled = 1`, session).Scan(&n)
	return n > 0, err
}

// conversationPursuing says whether the conversation is the lead of a mission whose pursuit is started. A read that
// fails says no: the pursuit loop reads the same rows and cannot wake the lead either, so nothing is woken twice, and a
// check-in's own failed-read handling stays as it was.
func (h *Host) conversationPursuing(profileID, conversationID string) bool {
	var n int
	err := h.Store.db.QueryRow(`SELECT COUNT(*) FROM task_sessions s JOIN mission_pursuit p ON p.session_id = s.id
		WHERE s.profile_id = ? AND s.conversation_id = ? AND s.archived_at = '' AND p.enabled = 1`, profileID, conversationID).Scan(&n)
	return err == nil && n > 0
}

// pursuitWakeStillAllowed re-checks, when a wake-up is about to run, that the pursuit still runs. It returns why not, or "".
func (h *Host) pursuitWakeStillAllowed(x Session) string {
	var id int64
	err := h.Store.db.QueryRow(`SELECT s.id FROM task_sessions s JOIN mission_pursuit p ON p.session_id = s.id
		WHERE s.profile_id = ? AND s.conversation_id = ? AND s.archived_at = '' AND p.enabled = 1 ORDER BY s.id DESC LIMIT 1`, x.ProfileID, x.ConversationID).Scan(&id)
	if err != nil {
		return "the session is no longer the lead of a started pursuit"
	}
	if !h.pursuitRunning(context.Background(), inputs.New(h.Store.db), id) {
		return "the mission's pursuit is paused or stopped"
	}
	return ""
}
