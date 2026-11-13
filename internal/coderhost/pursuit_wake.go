package coderhost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/policy"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
	"github.com/monoes/mono-agent/internal/missionpursuit/inputs"
	"github.com/monoes/mono-agent/internal/missionpursuit/scorer"
	"github.com/monoes/mono-agent/internal/missionpursuit/strategy"
)

// The wake-up: when an epoch was applied and the lead has not been woken for it, the lead gets ONE host-made send, if it is
// free. The gate is read again first (run state, away limits), the queue write and the "woken" mark happen in one
// transaction (a restart or a second host cannot wake twice for one epoch), and the text is numbers, enums and host-written
// ids only.

var errEpochWoken = errors.New("coder host: the lead was already woken for this epoch")

// maxWakeTries bounds how often the wake-up of one epoch is queued: the first, and again each time a queued one ended
// without running (the lead was busy for its whole life, a short pause, an expiry).
const maxWakeTries = 3

// pursuitWakeTurn is the turn id of a wake-up for an epoch: "pursuit-<epoch>-<random>".
func pursuitWakeTurn(epoch int64, rnd string) string {
	return PursuitTurnPrefix + strconv.FormatInt(epoch, 10) + "-" + rnd
}

// wakeCounts says how many wake-ups of the epoch are waiting or ran (live) and how many ended without running (lost).
func (s *Store) wakeCounts(q querier, m pursuitMission, epoch int64) (live, lost int, err error) {
	return s.wakeCountsLike(q, m, PursuitTurnPrefix+strconv.FormatInt(epoch, 10)+"-%")
}

// wakeCountsLike is wakeCounts for the wake-ups whose turn id matches the pattern.
func (s *Store) wakeCountsLike(q querier, m pursuitMission, pattern string) (live, lost int, err error) {
	rows, err := q.Query(`SELECT status, COUNT(*) FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND kind = 'send' AND turn_id LIKE ? GROUP BY status`,
		m.Profile, m.Conv, pattern)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return 0, 0, err
		}
		if st == CmdFailed {
			lost += n
		} else {
			live += n
		}
	}
	return live, lost, rows.Err()
}

// wakeDelivered says the lead owes nothing for the latest epoch: it is not an applied one, it was woken for (a wake-up
// waits or ran), or the tries are used up. A read that fails says "delivered": nothing is queued or spent on a guess.
func (s *Store) wakeDelivered(m pursuitMission, ep inputs.EpochFacts, woken int64) bool {
	if !ep.HasLatest || ep.Latest.State != types.EpochApplied || ep.Latest.Seq < woken {
		return true
	}
	if ep.Latest.Seq > woken {
		return false
	}
	live, lost, err := s.wakeCounts(s.db, m, ep.Latest.Seq)
	return err != nil || live > 0 || lost >= maxWakeTries
}

// EnqueueWithPursuit queues the wake-up and marks the epoch as woken in one transaction. It fails, queueing nothing, when
// the epoch is older than the one already woken, when the same epoch's wake-up waits or ran, or when its tries are used up.
func (s *Store) EnqueueWithPursuit(c Command, m pursuitMission, epoch int64) (Command, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return c, err
	}
	defer tx.Rollback() //nolint:errcheck // after commit it is a no-op
	res, err := tx.Exec(`UPDATE mission_pursuit_state SET woken_epoch = ?, wakes_since = wakes_since + 1, updated_at = ? WHERE session_id = ? AND woken_epoch <= ?`,
		epoch, s.now().UTC().Format(time.RFC3339Nano), m.ID, epoch)
	if err != nil {
		return c, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return c, errEpochWoken
	}
	live, lost, err := s.wakeCounts(tx, m, epoch)
	if err != nil {
		return c, err
	}
	if live > 0 || lost >= maxWakeTries {
		return c, errEpochWoken
	}
	if at, err := s.wakeRetryAt(tx, m, PursuitTurnPrefix+strconv.FormatInt(epoch, 10)+"-%"); err != nil || s.now().Before(at) {
		return c, errEpochWoken // a lost wake-up of this epoch is retried after its wait (or not at all when that cannot be read)
	}
	c.pursuit = true
	got, err := s.enqueueOn(tx, c)
	if err != nil {
		return c, err
	}
	if err := tx.Commit(); err != nil {
		return c, err
	}
	return got, nil
}

// pursuitWake queues the wake-up the latest applied epoch earned, from the gate the pass just read (spend and away counters
// do not change by a judge call, so the gate is not read twice); the run state is read again right before.
func (h *Host) pursuitWake(ctx context.Context, rd *inputs.Reader, m pursuitMission, g pursuitGateResult) {
	if g.st.Waiting != "" {
		return
	}
	// the epoch may have been applied by this very pass: read the latest one again
	ep, found, err := strategy.NewStore(h.Store.db).LatestEpoch(ctx, strconv.FormatInt(m.ID, 10), 1)
	if err != nil || !found || ep.State != types.EpochApplied {
		return
	}
	g.snap.Epochs = inputs.EpochFacts{HasLatest: true, Latest: ep, Count: ep.Seq}
	st, _, err := h.Store.pursuitStateOf(m.ID)
	if err != nil || h.Store.wakeDelivered(m, g.snap.Epochs, st.WokenEpoch) {
		return
	}
	// The wake-up is a spend of the lead's attention: the whole gate is read again now (a judge call can take minutes: the
	// work may have ended, a limit may have been reached, the person may have paused), not taken from the start of the pass.
	g2, ok := h.pursuitGate(ctx, rd, m, 0)
	if !ok || g2.st.Waiting != "" {
		return
	}
	g2.snap.Epochs = g.snap.Epochs
	g = g2
	if why := h.pursuitDeliverable(m, g); why != "" {
		return
	}
	focus, picked := h.pursuitFocus(ctx, m, g)
	if !h.pursuitRunning(ctx, rd, m.ID) {
		return // paused or stopped while the focus was read
	}
	turn := make([]byte, 8)
	_, _ = rand.Read(turn)
	_, err = h.Store.EnqueueWithPursuit(Command{ProfileID: m.Profile, ConversationID: m.Conv, Kind: KindSend,
		TurnID: pursuitWakeTurn(ep.Seq, hex.EncodeToString(turn)), Message: pursuitDigest(ep, g, focus, picked)}, m, ep.Seq)
	if err != nil && !errors.Is(err, errEpochWoken) {
		h.logf("coder host: pursuit of session %d: waking the lead: %v", m.ID, err)
	}
}

// pursuitFocus picks the direction to give new work: the best SCORED one, through scorer.Pick, never the first of a
// ranking (an all-unscored set has no first).
func (h *Host) pursuitFocus(ctx context.Context, m pursuitMission, g pursuitGateResult) (types.Direction, bool) {
	facts := newPursuitFacts(h.Store.db, m.Profile, m.ID, len(g.goal.Criteria), g.snap.Directions)
	ranked, err := policy.Rank(ctx, g.snap.Directions, scorer.New(facts))
	if err != nil {
		return types.Direction{}, false
	}
	conv := make([]scorer.Ranked, len(ranked))
	for i, r := range ranked {
		conv[i] = scorer.Ranked(r)
	}
	return scorer.Pick(conv)
}

const maxDigestDirections = 10

// pursuitDigest is the wake-up text: counts, enums and host-written ids. No title, note or goal text of any agent.
func pursuitDigest(ep types.Epoch, g pursuitGateResult, focus types.Direction, picked bool) string {
	var b strings.Builder
	t := g.snap.Tasks
	fmt.Fprintf(&b, "[PURSUIT] Epoch %d (%s) of your mission's pursuit is done. Mission, as the host sees it: %d of %d task(s) done, %d in review, %d open, %d blocked; ",
		ep.Seq, ep.Trigger, t.Done, t.Total-t.Archived, t.Review, t.Open(), t.Blocked)
	if g.snap.Budget.Known {
		fmt.Fprintf(&b, "%d%% of the budget spent.\n", int(min(g.snap.Budget.Share*100, 999)))
	} else {
		b.WriteString("the budget is not known.\n")
	}
	n := 0
	for _, d := range g.snap.Directions {
		if !hostID.MatchString(d.ID) || n >= maxDigestDirections {
			continue
		}
		fmt.Fprintf(&b, "- direction %s: %s\n", d.ID, d.State)
		n++
	}
	unlinked := 0
	for _, d := range g.snap.Directions {
		if d.State == types.DirProposed && d.SessionID == "" {
			unlinked++
		}
	}
	if unlinked > 0 {
		fmt.Fprintf(&b, "%d direction(s) are proposed and have no session yet: to work on one, start a direction session; the host links it to the oldest one.\n", unlinked)
	}
	if picked && hostID.MatchString(focus.ID) {
		fmt.Fprintf(&b, "Focus: %s (the best measured).\n", focus.ID)
	} else {
		b.WriteString("No direction has measured results yet, so no direction is chosen for you.\n")
	}
	if p := checkParagraph(g.goalChecks); p != "" {
		b.WriteString(p + "\n")
	}
	if g.closeWakeDue {
		// the one close wake of a settled state: fixed words, nothing for it to move forward
		b.WriteString("Every task is settled (done, or verified and waiting in review). Call mission_finish to integrate the mission and propose its close; the operator closes it. Do not redo finished work. This message was written by the host, not by the person.")
		return b.String()
	}
	b.WriteString("Read mission_status and move the open tasks forward. Do not redo finished work. This message was written by the host, not by the person.")
	return b.String()
}
