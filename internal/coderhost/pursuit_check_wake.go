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

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/missionpursuit/inputs"
)

// What the host tells the lead about its goal checks (S7c2), and when. The host never accepts or rejects a goal: only the person's
// answers do (goal answer, an operator-only verb). The host only WAKES the lead, with fixed sentences made of counts, host-written
// ids (c1..c16) and closed enums: no title, note, program output, checker reason or word of the person's.
//
//   - A check that ended without the goal being met (failed, error) or that the person rejected is a finish park: it feeds the epoch
//     engine's FinishParks (at most types.MaxParkedFinishRounds of them), earns ONE epoch and so ONE ordinary wake-up of the lead,
//     counted and limited like any other, and counts as a parked question in the away limits.
//   - When the person has answered yes to every criterion that is theirs the check is accepted (by them, in the store), and the lead is
//     woken ONCE to call mission_finish (the accept wake). It reuses the close wake's rules: the same gate, away limits, caps and cap X,
//     and a turn id that starts with PursuitTurnPrefix.
//
// Both lift the "no open work" refusal of pursuitDeliverable only for themselves, as the close wake does.

// acceptTurnHead starts the turn id of an accept wake: "pursuit-accept-<check>-<random>".
const acceptTurnHead = PursuitTurnPrefix + "accept-"

func acceptWakeTurn(check int64, rnd string) string {
	return acceptTurnHead + strconv.FormatInt(check, 10) + "-" + rnd
}

// acceptWakeCheck is the check an accept wake's turn id names, or 0 when the id is not one.
func acceptWakeCheck(turnID string) int64 {
	rest, ok := strings.CutPrefix(turnID, acceptTurnHead)
	if !ok {
		return 0
	}
	id, _, _ := strings.Cut(rest, "-")
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n < 1 || strconv.FormatInt(n, 10) != id {
		return 0
	}
	return n
}

// pursuitCheckWakeFacts fills the check facts of the gate: what the goal's checks say (for the wake text), the finish parks and
// whether an epoch wake for them, or an accept wake, is owed (the caller says how many of the wake-ups of its own it is deciding on).
// It does nothing, and reads nothing, on a host without goal checks. ok is false when a row cannot be read.
func (h *Host) pursuitCheckWakeFacts(ctx context.Context, m pursuitMission, selfWakes int, g *pursuitGateResult) bool {
	if !h.verifierReady() {
		return true
	}
	gc, err := missionpursuit.NewStore(h.Store.db).GoalChecks(ctx, m.Profile, m.ID)
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		return false
	}
	g.goalChecks = gc
	parks, err := missionpursuit.FinishParks(ctx, h.Store.db, m.ID)
	if err != nil {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
		return false
	}
	// A park that a later check has overtaken (the lead fixed it and the next check passed) earns nothing: only while the LATEST check is a
	// park does it count beyond the epochs already opened for parks.
	ep := g.snap.Epochs
	g.finishParks = ep.ParkedFinishRounds
	if latestIsPark(gc) {
		g.finishParks = min(parks, types.MaxParkedFinishRounds)
	}
	switch {
	case !latestIsPark(gc):
	case g.finishParks > ep.ParkedFinishRounds && ep.ParkedFinishRounds < types.MaxParkedFinishRounds:
		g.parkWakeDue = true // the engine opens the epoch for it
	case ep.HasLatest && ep.Latest.State == types.EpochApplied && ep.Latest.Trigger == types.TriggerFinishParked:
		// the epoch for a park is applied: its wake-up is owed until one is queued or ran (the one being decided on counts for nothing)
		live, lost, err := h.Store.wakeCounts(h.Store.db, m, ep.Latest.Seq)
		g.parkWakeDue = err == nil && live-selfWakes <= 0 && lost < maxWakeTries
	}
	if gc.Satisfied && gc.Latest != nil && checkSettled(g.snap.Tasks) {
		live, lost, err := h.Store.acceptWakeCounts(h.Store.db, m, gc.Latest.ID)
		g.acceptWakeDue = err == nil && live-selfWakes <= 0 && lost < maxWakeTries
	}
	return true
}

// finishParksOf is the epoch engine's FinishParks of a mission: the checks that ended without the goal being met, clamped to the
// engine's own limit; nil on a host that runs no goal checks. A count that cannot be read is none: nothing is spent on a guess.
func (h *Host) finishParksOf(m pursuitMission) func(context.Context) int {
	if !h.verifierReady() {
		return nil
	}
	return func(ctx context.Context) int {
		st := missionpursuit.NewStore(h.Store.db)
		gc, err := st.GoalChecks(ctx, m.Profile, m.ID)
		if err != nil || !latestIsPark(gc) {
			return 0
		}
		n, err := missionpursuit.FinishParks(ctx, h.Store.db, m.ID)
		if err != nil {
			return 0
		}
		return min(n, types.MaxParkedFinishRounds)
	}
}

// latestIsPark says whether the latest check ended without the goal being met (failed, error, or the person's no).
func latestIsPark(gc missionpursuit.GoalChecks) bool {
	if gc.Latest == nil {
		return false
	}
	switch gc.Latest.Outcome {
	case "failed", "error", missionpursuit.OutcomeRejected:
		return true
	}
	return false
}

// checkWakeFacts is what the gate knows of the goal checks, filled only on a host that runs them: how the latest one stands, the
// finish parks it left (clamped), whether the wake-up of an epoch for a park is owed and whether the accept wake is owed.
type checkWakeFacts struct {
	goalChecks    missionpursuit.GoalChecks
	finishParks   int
	parkWakeDue   bool
	acceptWakeDue bool
}

// acceptWakeCounts says how many accept wakes of the check wait or ran (live) and how many ended without running (lost).
func (s *Store) acceptWakeCounts(q querier, m pursuitMission, check int64) (live, lost int, err error) {
	return s.wakeCountsLike(q, m, acceptTurnHead+strconv.FormatInt(check, 10)+"-%")
}

// EnqueueWithAccept queues the accept wake of a check and counts it as a wake-up of the mission (wakes_since), in one transaction. It
// fails, queueing nothing, when one of this check waits or ran, or its tries are used up.
func (s *Store) EnqueueWithAccept(c Command, m pursuitMission, check int64) (Command, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return c, err
	}
	defer tx.Rollback() //nolint:errcheck // after commit it is a no-op
	res, err := tx.Exec(`UPDATE mission_pursuit_state SET wakes_since = wakes_since + 1, updated_at = ? WHERE session_id = ?`, s.now().UTC().Format(time.RFC3339Nano), m.ID)
	if err != nil {
		return c, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return c, errEpochWoken
	}
	live, lost, err := s.acceptWakeCounts(tx, m, check)
	if err != nil {
		return c, err
	}
	if live > 0 || lost >= maxWakeTries {
		return c, errEpochWoken
	}
	if at, err := s.wakeRetryAt(tx, m, acceptTurnHead+strconv.FormatInt(check, 10)+"-%"); err != nil || s.now().Before(at) {
		return c, errEpochWoken
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

// pursuitAcceptWake queues the accept wake when it is owed: the person accepted the check, the mission is settled and the lead can be
// woken. The gate is read again right before (a limit may have been reached, the person may have paused), and so is the run state.
func (h *Host) pursuitAcceptWake(ctx context.Context, rd *inputs.Reader, m pursuitMission, g pursuitGateResult) {
	if !g.acceptWakeDue || g.st.Waiting != "" || !h.verifierReady() {
		return
	}
	g2, ok := h.pursuitGate(ctx, rd, m, 0)
	if !ok || g2.st.Waiting != "" || !g2.acceptWakeDue || g2.goalChecks.Latest == nil {
		return
	}
	if why := h.pursuitDeliverableFor(m, g2, true); why != "" || !h.Store.checkMergesDone(m.ID) {
		return
	}
	if !h.pursuitRunning(ctx, rd, m.ID) {
		return
	}
	turn := make([]byte, 8)
	_, _ = rand.Read(turn)
	l := g2.goalChecks.Latest
	_, err := h.Store.EnqueueWithAccept(Command{ProfileID: m.Profile, ConversationID: m.Conv, Kind: KindSend,
		TurnID: acceptWakeTurn(l.ID, hex.EncodeToString(turn)), Message: acceptWakeText(*l)}, m, l.ID)
	if err != nil && !errors.Is(err, errEpochWoken) {
		h.logf("coder host: pursuit of session %d: waking the lead for the accepted goal: %v", m.ID, err)
	}
}

// pursuitAcceptStillDue is the run-time half of an accept wake: the check it names must still be the latest one and still accepted,
// and the goal checks must still be set up on this host (a wake queued before a restart with the checks off does not run).
func (h *Host) pursuitAcceptStillDue(x Session, check int64) string {
	if !h.verifierReady() {
		return "the goal checks are not set up on this host, so the accept wake does not run"
	}
	var session int64
	err := h.Store.db.QueryRow(`SELECT s.id FROM task_sessions s JOIN mission_pursuit p ON p.session_id = s.id
		WHERE s.profile_id = ? AND s.conversation_id = ? AND s.archived_at = '' AND p.enabled = 1 ORDER BY s.id DESC LIMIT 1`, x.ProfileID, x.ConversationID).Scan(&session)
	if err != nil {
		return "the session is no longer the lead of a started pursuit"
	}
	gc, err := missionpursuit.NewStore(h.Store.db).GoalChecks(context.Background(), x.ProfileID, session)
	if err != nil || !gc.Satisfied || gc.Latest == nil || gc.Latest.ID != check {
		return "the goal is no longer accepted by the check this wake-up named"
	}
	return ""
}

// The sentences. Fixed words, numbers, host-written ids and closed enums only.

// ids lists criterion ids as "c1, c2".
func ids(cs []missionpursuit.CriterionSummary, want func(missionpursuit.CriterionSummary) bool) string {
	var out []string
	for _, c := range cs {
		if want(c) {
			out = append(out, c.ID)
		}
	}
	return strings.Join(out, ", ")
}

// checkParagraph is what the latest goal check says, for the end of an epoch wake-up. "" when there is none.
func checkParagraph(gc missionpursuit.GoalChecks) string {
	l := gc.Latest
	if l == nil {
		return ""
	}
	notPass := ids(l.Machine, func(c missionpursuit.CriterionSummary) bool { return c.Result != "pass" })
	switch l.Outcome {
	case "passed-machine":
		return fmt.Sprintf("Goal check %d (round %d): the machine criteria pass (%d of %d) and the checker found nothing against it. The %d criteria that are the person's wait for their answers; there is nothing for you to do for the goal yet, and the mission is proposed for close only after they accept it.",
			l.ID, l.Round, l.MachinePass, l.MachineTotal, l.RubricTotal)
	case "accepted":
		return fmt.Sprintf("Goal check %d (round %d) was accepted by the person.", l.ID, l.Round)
	case "failed":
		return fmt.Sprintf("Goal check %d (round %d) failed: %d of %d machine criteria pass; not passing: %s. Read them with mission_goal_status and fix what they ask; the host checks again when the work has settled.",
			l.ID, l.Round, l.MachinePass, l.MachineTotal, orNone(notPass))
	case "error":
		return fmt.Sprintf("Goal check %d (round %d) could not measure every machine criterion (%d of %d pass); not passing: %s. Read them with mission_goal_status; the host checks again when the work has changed and settled.",
			l.ID, l.Round, l.MachinePass, l.MachineTotal, orNone(notPass))
	case "rejected":
		no := ids(l.Rubric, func(c missionpursuit.CriterionSummary) bool { return c.Result == "no" })
		return fmt.Sprintf("The person answered no for %s on goal check %d (round %d): the goal is not met yet, and more work is needed on what those criteria say. The host checks again when the project has changed and the work has settled.", orNone(no), l.ID, l.Round)
	case "blocked", "stale", "cannot-check", "abandoned":
		return fmt.Sprintf("Goal check %d (round %d) did not finish (%s): the host could not complete it. The host tries again within its limits, and the person can see why in the pursuit status. There is nothing for you to change because of it.", l.ID, l.Round, l.Outcome)
	}
	return ""
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// acceptWakeText is the whole text of the accept wake.
func acceptWakeText(l missionpursuit.CheckSummary) string {
	return fmt.Sprintf("[PURSUIT] The person accepted the goal: goal check %d (round %d), %d of %d machine criteria pass and every one of the %d criteria that are theirs was answered yes. Every task is settled. Call mission_finish to integrate the mission and propose its close; the operator closes it. Do not redo finished work. This message was written by the host, not by the person.",
		l.ID, l.Round, l.MachinePass, l.MachineTotal, l.RubricTotal)
}
