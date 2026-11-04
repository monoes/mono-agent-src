//go:build doomsday

package doomsday

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
	"github.com/monoes/mono-agent/internal/doomsday/governor/machine"
	"github.com/monoes/mono-agent/internal/doomsday/governor/policy"
	"github.com/monoes/mono-agent/internal/doomsday/governor/queue"
	"github.com/monoes/mono-agent/internal/doomsday/governor/sched"
	"github.com/monoes/mono-agent/internal/doomsday/governor/slots"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// The resource governor of the open-ended mode, wired into the coder host (note B3). It is compiled only into a build with
// the mode and constructed only while the profile has a RUNNING run; with none, every hook here is a plain call and the host
// is what it was.
//
// What it does: before a turn's process starts it takes a process slot (admission), and a turn the machine has no room for
// is deferred, not failed: the host leaves the send pending, in order, and the governor's bounded durable queue holds one
// small reference row per waiting launch so that the line is bounded, fair per lead and visible.
//
// What it never does: raise a cap (the slots are taken against min(charter max_concurrent, effective)), pause or kill a
// running session, sit on the Stop path, mint a token, read the vault or touch a spend meter. Its admission runs BEFORE the
// dispatch gate's ticket, so a refusal leaks no ticket; a run that is not running (or whose stop marker is set) admits nothing.

// Bounds of the wiring.
const (
	govCallTimeout    = 5 * time.Second  // every database call of an admission; a wedged database refuses, it never hangs the host
	govClaimTTL       = 30 * time.Second // a slot the queue reserved for a send that never claimed it is given back after this
	govReconcileEvery = 30 * time.Second
	govPublishEvery   = 2 * time.Second
	govPumpEvery      = 2 * time.Second // waiting sends asked again with nothing changed pump at most this often
	govStateKey       = "snapshot"
	govMemberPrefix   = "turn|"
)

// GovernorWire builds the governor of the profile's running run and hooks it into the host.
type GovernorWire struct {
	DB      *sql.DB
	Profile string
	// Keys are the MAC keys of the queue rows (the same keyer as the store's).
	Keys governor.Keyer
	// Runs is the sequenced store: the run listing and the verified charter.
	Runs interface {
		OpenRuns(ctx context.Context, profile string) ([]store.Run, error)
		LoadCharter(ctx context.Context, profile string, generation int64) (store.Charter, error)
	}
	// RunDir is where the stop marker is read: a set marker admits nothing (Stop wins before the database says so).
	RunDir stop.RunDir
	// Dirs are the directories whose free space the sampler counts (the data and scratch directories).
	Dirs []string
	// Sampler reads the machine; nil is the platform's (machine.New). Tests pass a fake.
	Sampler governor.Sampler
	// Clock is monotonic time; nil is the system clock.
	Clock governor.Clock
	// ProviderOf names the provider of a conversation for the 429 backoff ("" when unknown: then only the global limit applies).
	ProviderOf func(profile, conversation string) string
	// Alive says whether the turn of a conversation is still a live process (Launcher.Adopt); nil counts only turns this
	// process started. A restarted host uses it to keep counting turns that outlived it.
	Alive func(conversation, turn string) bool
	Logf  func(format string, args ...any)

	mu  sync.Mutex
	cur *runGov
}

// errGovStopping is the refusal of a run that is not running: Stop wins, nothing is admitted.
var errGovStopping = fmt.Errorf("%w: the run is stopping; nothing new is admitted", errDispatchRefused)

func (w *GovernorWire) logf(f string, a ...any) {
	if w.Logf != nil {
		w.Logf(f, a...)
	}
}

// runGov is the governor of one generation.
type runGov struct {
	w     *GovernorWire
	gen   int64
	id    string // charter id
	lim   governor.Limits
	s     *sched.Scheduler
	slots *slots.Store
	q     *queue.Queue
	pol   *policy.Policy
	clock governor.Clock

	mu       sync.Mutex           // serialises admission, pump and reconcile (never held by Release or Stop)
	queued   map[string]bool      // guarded by liveMu: members with a queue row
	admitted map[string]time.Time // reserved by the queue, not yet claimed by the send
	lastRec  time.Time
	lastPump time.Time
	dirty    atomic.Bool // something changed (a release, a new waiting launch): the next ask pumps at once
	lastPub  time.Time

	liveMu   sync.Mutex
	started  map[string]bool // turns this process runs
	starting map[string]bool // members between the decision to reserve and the reservation
}

// For returns the governor of the profile's running run. (nil, nil) is the mode off for the profile: no open run, so the
// host acts as it always did. A run that is stop-requested or later, a set stop marker, or a state that cannot be read
// returns an error that refuses the launch: Stop wins and a governor never admits against a run it cannot see.
func (w *GovernorWire) For(ctx context.Context) (*runGov, error) {
	runs, err := w.Runs.OpenRuns(ctx, w.Profile)
	if err != nil {
		return nil, fmt.Errorf("%w: the run state cannot be read: %v", errDispatchRefused, err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(runs) == 0 {
		if w.cur != nil {
			w.cur.s.NotifyStop()
			w.cur = nil
		}
		return nil, nil
	}
	r := runs[0]
	if w.cur != nil && w.cur.gen != r.Generation {
		w.cur.s.NotifyStop()
		w.cur = nil
	}
	if r.State != store.StateRunning || w.RunDir.ReadMarker(uint64(r.Generation), r.CharterID).Stops() {
		if w.cur != nil {
			w.cur.s.NotifyStop() // one atomic store: no admission after it, whatever the database says
		}
		return nil, errGovStopping
	}
	if w.cur == nil {
		rg, err := w.build(ctx, r)
		if err != nil {
			return nil, fmt.Errorf("%w: the governor cannot be built: %v", errDispatchRefused, err)
		}
		w.cur = rg
	}
	return w.cur, nil
}

func (w *GovernorWire) build(ctx context.Context, r store.Run) (*runGov, error) {
	c, err := w.Runs.LoadCharter(ctx, w.Profile, r.Generation)
	if err != nil {
		return nil, err
	}
	lim := governor.Limits{Profile: w.Profile, Generation: r.Generation, MaxConcurrent: c.Limits.MaxConcurrent, LivePerTree: c.Limits.LivePerTree,
		SafeMin: c.Limits.SafeMinConcurrent, QueueMaxRows: c.Limits.QueueMaxRows, QueueMaxBytes: c.Limits.QueueMaxBytes}.WithDefaults()
	clock := w.Clock
	if clock == nil {
		clock = policy.NewSystemClock()
	}
	sampler := w.Sampler
	if sampler == nil {
		sampler = machine.New(w.Dirs...)
	}
	rg := &runGov{w: w, gen: r.Generation, id: r.CharterID, lim: lim, clock: clock,
		queued: map[string]bool{}, admitted: map[string]time.Time{}, started: map[string]bool{}, starting: map[string]bool{}}
	rg.slots = slots.New(w.DB, rg)
	rg.q = queue.New(w.DB, w.Keys)
	rg.pol = policy.New(clock, policy.WithJitter(rand.Float64))
	rg.s, err = sched.New(sched.Config{Limits: lim, Sampler: sampler, Policy: rg.pol, Slots: rg.slots, Queue: rg.q, View: rg, Clock: clock, Decode: decodeItem, Stale: rg.stale})
	if err != nil {
		return nil, err
	}
	return rg, nil
}

// Running implements governor.RunView: the run is the generation's running run.
func (rg *runGov) Running(ctx context.Context, profile string, generation int64) (bool, error) {
	runs, err := rg.w.Runs.OpenRuns(ctx, profile)
	if err != nil {
		return false, err
	}
	for _, r := range runs {
		if r.Generation == generation {
			return r.State == store.StateRunning && !rg.w.RunDir.ReadMarker(uint64(r.Generation), r.CharterID).Stops(), nil
		}
	}
	return false, nil
}

// LiveMemberIDs implements governor.RunView for the orphan reconcile: the turns this process runs or is starting, the
// reservations not yet claimed, and every turn the launcher still finds alive (a host that restarted keeps counting turns
// that outlived it).
func (rg *runGov) LiveMemberIDs(ctx context.Context, profile string, generation int64) (map[string]bool, error) {
	live := map[string]bool{}
	rg.liveMu.Lock()
	for m := range rg.started {
		live[m] = true
	}
	for m := range rg.starting {
		live[m] = true
	}
	for m, at := range rg.admitted {
		if time.Since(at) < govClaimTTL {
			live[m] = true
		}
	}
	rg.liveMu.Unlock()
	rows, err := rg.w.DB.QueryContext(ctx, `SELECT DISTINCT member_id FROM doomsday_slots WHERE profile = ? AND generation = ? AND released_at IS NULL`, profile, generation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		if live[m] {
			continue
		}
		if conv, turn, ok := splitMember(m); ok && rg.w.Alive != nil && rg.w.Alive(conv, turn) {
			live[m] = true
		}
	}
	return live, rows.Err()
}

func memberID(conv, turn string) string { return govMemberPrefix + conv + "|" + turn }

func splitMember(m string) (conv, turn string, ok bool) {
	rest, found := strings.CutPrefix(m, govMemberPrefix)
	if !found {
		return "", "", false
	}
	conv, turn, ok = strings.Cut(rest, "|")
	return conv, turn, ok && conv != "" && turn != ""
}

// payload is what a queue row holds: references only. No message, token, key or credential ever enters it.
type govPayload struct {
	M string `json:"m"` // the member id
	P string `json:"p,omitempty"`
}

func decodeItem(it governor.Item) (string, governor.Request, error) {
	var p govPayload
	if err := json.Unmarshal(it.Payload, &p); err != nil || p.M == "" {
		return "", governor.Request{}, errors.New("governor: a queue row that is not a launch reference")
	}
	return p.P, governor.Request{Kind: governor.KindProcess, MemberID: p.M}, nil
}

// claim moves a reservation the queue made into the started set.
func (rg *runGov) claim(m string) bool {
	rg.liveMu.Lock()
	defer rg.liveMu.Unlock()
	at, ok := rg.admitted[m]
	if !ok {
		return false
	}
	delete(rg.admitted, m)
	if time.Since(at) >= govClaimTTL { // too late: Reconcile may have given the slot away already
		return false
	}
	rg.started[m] = true
	return true
}

func (rg *runGov) setStarting(m string, on bool) {
	rg.liveMu.Lock()
	defer rg.liveMu.Unlock()
	if on {
		rg.starting[m] = true
		return
	}
	delete(rg.starting, m)
}

func (rg *runGov) isQueued(m string) bool {
	rg.liveMu.Lock()
	defer rg.liveMu.Unlock()
	return rg.queued[m]
}

func (rg *runGov) setQueued(m string) {
	rg.liveMu.Lock()
	rg.queued[m] = true
	rg.liveMu.Unlock()
}

func (rg *runGov) markStarted(m string) {
	rg.liveMu.Lock()
	rg.started[m] = true
	delete(rg.starting, m)
	rg.liveMu.Unlock()
}

// release frees a member's slots, at the end of its turn or when its start failed. It works after Stop and never blocks on
// the governor's decision; a failure leaves an orphan the next Reconcile frees.
func (rg *runGov) release(m string) {
	rg.liveMu.Lock()
	delete(rg.started, m)
	delete(rg.starting, m)
	delete(rg.admitted, m)
	delete(rg.queued, m)
	rg.liveMu.Unlock()
	rg.dirty.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), govCallTimeout)
	defer cancel()
	if err := rg.s.Release(ctx, m); err != nil {
		rg.w.logf("doomsday governor: releasing a slot failed (the next reconcile frees it): %v", err)
	}
}

// pumpLocked admits queued launches into free slots and records the reservations. rg.mu is held.
func (rg *runGov) pumpLocked(ctx context.Context) {
	rg.lastPump = time.Now()
	rg.dirty.Store(false)
	res, err := rg.s.Pump(ctx)
	if err != nil && !errors.Is(err, governor.ErrNotRunning) {
		rg.w.logf("doomsday governor: pump: %v", err)
	}
	rg.liveMu.Lock()
	for _, a := range res.Admitted {
		rg.admitted[a.Request.MemberID] = time.Now()
		delete(rg.queued, a.Request.MemberID)
	}
	// reservations nobody claimed in time are no longer live: the reconcile below gives their slots back
	for m, at := range rg.admitted {
		if time.Since(at) >= govClaimTTL {
			delete(rg.admitted, m)
		}
	}
	rg.liveMu.Unlock()
	if time.Since(rg.lastRec) >= govReconcileEvery {
		rg.lastRec = time.Now()
		if n, err := rg.slots.Reconcile(ctx, rg.lim.Profile, rg.lim.Generation); err != nil {
			rg.w.logf("doomsday governor: reconcile: %v", err)
		} else if n > 0 {
			rg.w.logf("doomsday governor: gave back %d slots of members that are gone", n)
		}
	}
}

// Tick is one step of the host loop: pump the queue, reconcile orphans and publish the snapshot. A panic is recovered. It
// does nothing for a profile with no running run.
func (w *GovernorWire) Tick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			w.logf("doomsday governor: a tick panicked (%T) and was recovered", r)
		}
	}()
	tctx, cancel := context.WithTimeout(ctx, govCallTimeout)
	defer cancel()
	rg, err := w.For(tctx)
	if rg == nil || err != nil {
		return
	}
	rg.tickLocked(tctx) // the lock is released by a defer: a panic recovered above must not leave admission blocked
}

func (rg *runGov) tickLocked(ctx context.Context) {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	rg.pumpLocked(ctx)
	rg.publishLocked(ctx, true)
}

// published is the host's last report: derived display state, never read as authority.
type published struct {
	At        int64     `json:"at"`
	Cap       int       `json:"cap"`
	Effective int       `json:"effective"`
	Admitting bool      `json:"admitting"`
	Reasons   []string  `json:"reasons,omitempty"`
	Providers []pubProv `json:"providers,omitempty"`
}

type pubProv struct {
	Provider string `json:"provider"`
	Value    int    `json:"value"`
	LeftSecs int64  `json:"left_secs"`
}

// publishLocked writes the host's decision into the derived-state table so another process (the status verb) can show it.
func (rg *runGov) publishLocked(ctx context.Context, force bool) {
	if !force && time.Since(rg.lastPub) < govPublishEvery {
		return
	}
	rg.lastPub = time.Now()
	snap, err := rg.s.Snapshot(ctx)
	if err != nil {
		return
	}
	p := published{At: time.Now().Unix(), Cap: snap.Cap, Effective: snap.Effective, Admitting: snap.Admitting}
	for _, r := range snap.Reasons {
		p.Reasons = append(p.Reasons, string(r))
	}
	now := rg.clock.Now()
	for _, pr := range snap.Providers {
		left := int64((pr.Until - now) / time.Second)
		p.Providers = append(p.Providers, pubProv{Provider: pr.Provider, Value: pr.Value, LeftSecs: max(left, 0)})
	}
	b, err := json.Marshal(p)
	if err != nil {
		return
	}
	if _, err := rg.w.DB.ExecContext(ctx, `INSERT INTO doomsday_governor_state(profile, generation, key, value) VALUES (?,?,?,?)
		ON CONFLICT(profile, generation, key) DO UPDATE SET value = excluded.value`, rg.lim.Profile, rg.lim.Generation, govStateKey, string(b)); err != nil {
		rg.w.logf("doomsday governor: publishing the snapshot: %v", err)
	}
}

// RateLimited is the 429 observer: only the provider name and a number cross the seam.
func (w *GovernorWire) RateLimited(provider string, retryAfter time.Duration) {
	if provider == "" {
		return
	}
	w.mu.Lock()
	rg := w.cur
	w.mu.Unlock()
	if rg != nil {
		rg.s.ReportRateLimit(provider, retryAfter)
	}
}

// parsePublished reads the host's report; a row that is not one is no report.
func parsePublished(raw string) (published, bool) {
	var p published
	if len(raw) > 4096 || json.Unmarshal([]byte(raw), &p) != nil || p.At <= 0 {
		return published{}, false
	}
	return p, true
}

// stale says whether the send behind a queued launch is gone: its command exists and is no longer pending (cancelled by a
// stop or the person, failed, done). A launch with no command row at all is not judged.
func (rg *runGov) stale(ctx context.Context, it governor.Item) bool {
	_, req, err := decodeItem(it)
	if err != nil {
		return false
	}
	conv, turn, ok := splitMember(req.MemberID)
	if !ok {
		return false
	}
	var status string
	err = rg.w.DB.QueryRowContext(ctx, `SELECT status FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND turn_id = ? ORDER BY id DESC LIMIT 1`,
		rg.lim.Profile, conv, turn).Scan(&status)
	return err == nil && status != "pending"
}

// holdsSlot says whether the member already holds a live slot (reserved for it by the queue, or kept from before a restart).
func (rg *runGov) holdsSlot(ctx context.Context, m string) bool {
	var n int
	err := rg.w.DB.QueryRowContext(ctx, `SELECT count(*) FROM doomsday_slots WHERE profile = ? AND generation = ? AND member_id = ? AND released_at IS NULL`,
		rg.lim.Profile, rg.lim.Generation, m).Scan(&n)
	return err == nil && n > 0
}

// queuedInDB says whether a queued row already names this launch, so a restarted host does not queue it twice.
func (rg *runGov) queuedInDB(ctx context.Context, payload []byte) bool {
	var n int
	err := rg.w.DB.QueryRowContext(ctx, `SELECT count(*) FROM doomsday_queue WHERE profile = ? AND generation = ? AND state = 'queued' AND payload = ?`,
		rg.lim.Profile, rg.lim.Generation, string(payload)).Scan(&n)
	return err == nil && n > 0
}
