package coderhost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/policy"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
	"github.com/monoes/mono-agent/internal/missionpursuit/inputs"
	"github.com/monoes/mono-agent/internal/missionpursuit/scorer"
)

// The wake-up: when an epoch was applied and the lead has not been woken for it, the lead gets ONE host-made send, if it is
// free. The gate is read again first (run state, away limits), the queue write and the "woken" mark happen in one
// transaction (a restart or a second host cannot wake twice for one epoch), and the text is numbers, enums and host-written
// ids only.

var errEpochWoken = errors.New("coder host: the lead was already woken for this epoch")

// EnqueueWithPursuit queues the wake-up and marks the epoch as woken in one transaction. It fails when the epoch is not
// newer than the one already woken, and then queues nothing.
func (s *Store) EnqueueWithPursuit(c Command, session, epoch int64) (Command, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return c, err
	}
	defer tx.Rollback() //nolint:errcheck // after commit it is a no-op
	res, err := tx.Exec(`UPDATE mission_pursuit_state SET woken_epoch = ?, updated_at = ? WHERE session_id = ? AND woken_epoch < ?`,
		epoch, s.now().UTC().Format(time.RFC3339Nano), session, epoch)
	if err != nil {
		return c, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return c, errEpochWoken
	}
	got, err := s.enqueueOn(tx, c)
	if err != nil {
		return c, err
	}
	if err := tx.Commit(); err != nil {
		return c, err
	}
	return got, nil
}

func (h *Host) pursuitWake(ctx context.Context, rd *inputs.Reader, m pursuitMission, run *pursuitRun) {
	if m.Conv == "" {
		return
	}
	g, ok := h.pursuitGate(ctx, rd, m) // run state, goal and away limits, read now
	if !ok || g.st.Waiting != "" {
		return
	}
	ep := g.snap.Epochs
	if !ep.HasLatest || ep.Latest.State != types.EpochApplied || ep.Latest.Seq <= g.st.WokenEpoch {
		return
	}
	lead, found, err := h.Store.GetSession(m.Profile, m.Conv)
	if err != nil || !found || lead.State != StateIdle || lead.TurnID != "" || h.Store.PendingFor(m.Profile, m.Conv) > 0 {
		return
	}
	settings, err := h.Store.GetSettings(m.Profile, m.Conv)
	if err != nil || !withinActiveHours(settings.ActiveHours, h.Store.now()) {
		return
	}
	focus, picked := h.pursuitFocus(ctx, m, g)
	if !h.pursuitRunning(ctx, rd, m.ID) {
		return // paused or stopped while the focus was read
	}
	turn := make([]byte, 8)
	_, _ = rand.Read(turn)
	_, err = h.Store.EnqueueWithPursuit(Command{ProfileID: m.Profile, ConversationID: m.Conv, Kind: KindSend,
		TurnID: PursuitTurnPrefix + hex.EncodeToString(turn), Message: pursuitDigest(ep.Latest, g, focus, picked)}, m.ID, ep.Latest.Seq)
	if err != nil && !errors.Is(err, errEpochWoken) {
		h.logf("coder host: pursuit of session %d: waking the lead: %v", m.ID, err)
	}
	_ = run
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
	if picked && hostID.MatchString(focus.ID) {
		fmt.Fprintf(&b, "Focus: %s (the best measured).\n", focus.ID)
	} else {
		b.WriteString("No direction has measured results yet, so no direction is chosen for you.\n")
	}
	b.WriteString("Read mission_status and move the open tasks forward. Do not redo finished work. This message was written by the host, not by the person.")
	return b.String()
}
