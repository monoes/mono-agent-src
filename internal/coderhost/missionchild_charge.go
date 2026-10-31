package coderhost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// Charging an ended child's spend to the mission (phase P-B2). An in-turn worker's cost is added to the task
// session's spent_usd by the lead's conductor, so the 90 percent warning and the 100 percent halt see it. A child
// session runs in its own turns, which the conductor does not watch: when the child ends, the host adds what
// the child cost (Store.Spent over its turns) to the same meter with the same function the conductor uses
// (tasks.Store.AddSessionSpend, as the operator: only the operator's process records spend).
//
// INVARIANTS (the table-driven walk in missionchild_charge_walk_test.go holds each of them over every state a
// child's cost can be in). A child's cost is in a state of: how the row knows the conversation (its child id, its
// pending id only, neither) x where the cost is (live usage events, a "spend" snapshot, lost) x the row (live,
// ended, failed, gone, marked again after a release) x the charge (none, written) x when the conversation was deleted
// (never, before the link, after it) x how the spawn went (normal end, failed spawn, undo, release).
//
//	(i)   The mission's meter never ends below a child's real cost when that cost is known or recoverable: from the
//	      live events, or from the snapshot a conversation delete writes for a child AND for a pending conversation. The
//	      snapshot is chargeable whatever the row became (a released row keeps no link, the snapshot still finds it).
//	(ii)  A child is charged once: the meter update and the "charge" record are ONE transaction
//	      (tasks.AddSessionSpendWith), so a failed record write leaves the meter as it was and a later pass adds the
//	      cost exactly once; a charge found inside the transaction ends it without adding.
//	(iii) An unknown cost (the conversation is gone with neither events nor snapshot) is never settled silently and
//	      never guessed: the row gets an "unresolved" record that holds a conservative amount against the mission's
//	      budget (missionBudgetRefusal), and only the person's `resolve-charge` ends the state, with a number or a skip.
//	(iv)  Mark cannot overwrite a row whose spend is not on the meter, nor one that is unresolved: the row's identity
//	      (child id, else pending id) stays until a "charge" record exists.
//
// Exactly once: the child's end is the single moment its total is known, and the host is the only writer (it is a
// single process, and its pass is sequential). A "charge" audit row, written in the transaction that adds the
// spend, is the record that it was done; a conversation with that row is never charged again, so a second settle,
// a restarted host and a sweep over old rows add nothing.
//
// A charge never needs the mission to be running: a stopped, halted or archived mission still pays for its
// children, and the lead's next look at the budget (missionBudget) reads the new total.

// chargeKind is the audit row that says a child's spend was added to its mission.
const chargeKind = "charge"

// unresolvedKind is the audit row of a child whose cost nobody knows. Its note is "<conversation> $<left> ...": while
// it has no charge row, missionBudgetRefusal holds the whole remainder of the mission's budget (the view v_mission_child_held).
const unresolvedKind = "unresolved"

// chargeBatch bounds the rows one pass of the sweep settles: a backlog is worked off over several passes.
const chargeBatch = 50

// rowIdent is the identity a row's spend is charged under: its child conversation, else the pending one (a
// conversation deleted before the spawn linked it is known to the row as pending only).
const rowIdent = `(CASE WHEN coder_mission_children.child_conversation_id <> '' THEN coder_mission_children.child_conversation_id ELSE coder_mission_children.pending_conversation_id END)`

// notCharged is the ended rows of any age with no charge row for their conversation (an uncharged row has no
// age limit: its spend is not on the meter yet, however long ago the child ended). Mark refuses to reuse such a
// row (it would forget the conversation the sweep finds the child through). The way out of the state is a charge
// row, and every charge ends in one: the spend, or a "skipped" note with the reason, or the person's resolution.
const notCharged = `coder_mission_children.state IN ('ended', 'failed', 'gone') AND ` + rowIdent + ` <> ''
	AND NOT EXISTS (SELECT 1 FROM coder_mission_events e WHERE e.profile_id = coder_mission_children.profile_id
		AND e.session_id = coder_mission_children.session_id AND e.kind = 'charge'
		AND substr(e.note, 1, length(` + rowIdent + `) + 1) = ` + rowIdent + ` || ' ')`

// unresolvedRow is a row the host has found no cost for (see unresolvedKind): the sweep leaves it to the person.
const unresolvedRow = `EXISTS (SELECT 1 FROM coder_mission_events u WHERE u.profile_id = coder_mission_children.profile_id
	AND u.session_id = coder_mission_children.session_id AND u.kind = '` + unresolvedKind + `'
	AND substr(u.note, 1, length(` + rowIdent + `) + 1) = ` + rowIdent + ` || ' ')`

func (r ChildRow) ident() string {
	if r.ChildConversationID != "" {
		return r.ChildConversationID
	}
	return r.PendingConversationID
}

// chargeTarget is what a charge needs to know about a child: whose mission and task it was, and the row if there is one.
type chargeTarget struct {
	row     ChildRow
	hasRow  bool
	session int64
	task    int64
}

// targetOf finds the ended row that holds the conversation as its child or its pending conversation or, when
// the row has since been released and marked again, the snapshot a delete left for it.
func (s *Store) targetOf(profileID, conv string) (chargeTarget, bool, error) {
	rows, err := s.childrenWhere(`profile_id = ? AND (child_conversation_id = ? OR pending_conversation_id = ?) AND state IN ('ended', 'failed', 'gone')`, profileID, conv, conv)
	if err != nil {
		return chargeTarget{}, false, err
	}
	if len(rows) > 0 {
		return chargeTarget{row: rows[0], hasRow: true, session: rows[0].SessionID, task: rows[0].TaskID}, true, nil
	}
	var t chargeTarget
	err = s.db.QueryRow(`SELECT session_id, task_id FROM coder_mission_events WHERE profile_id = ? AND kind = 'spend' AND substr(note, 1, ?) = ? ORDER BY id DESC LIMIT 1`,
		profileID, len(conv)+1, conv+" ").Scan(&t.session, &t.task)
	if errors.Is(err, sql.ErrNoRows) {
		return chargeTarget{}, false, nil
	}
	return t, err == nil, err
}

// ChargeChild adds the spend of an ended child to its mission's meter once and says how much it added (0 when
// there was nothing to add, it was charged before, or its cost is unknown and waits for the person). A child whose
// row is still live is not charged: its total is not known.
func (s *Store) ChargeChild(profileID, childConversationID string) (float64, error) {
	t, ok, err := s.targetOf(profileID, childConversationID)
	if err != nil || !ok {
		return 0, err
	}
	done, err := s.charged(profileID, t.session, childConversationID)
	if err != nil {
		return 0, err // not knowing is not "not charged": the next pass asks again
	}
	if done {
		return 0, nil
	}
	spent, err := s.Spent(profileID, childConversationID) // a deleted conversation has no events left: 0
	if err != nil {
		return 0, err
	}
	rec, err := s.recoveredSpend(profileID, t, childConversationID, spent)
	if err != nil {
		return 0, err
	}
	if rec.unknown {
		return 0, s.markUnresolved(profileID, t, childConversationID, rec.note)
	}
	return s.writeCharge(profileID, t.session, t.task, childConversationID, rec.usd, rec.note, "host")
}

// errAlreadyCharged ends a charge transaction that found its charge row: nothing is added.
var errAlreadyCharged = errors.New("coder host: the child was charged already")

// writeCharge is the one place a child's spend reaches the meter: the spend and the charge row stand or fall together.
func (s *Store) writeCharge(profileID string, sid, tid int64, conv string, spent float64, note, who string) (float64, error) {
	usd, _, err := s.writeChargeOnce(profileID, sid, tid, conv, spent, note, who)
	return usd, err
}

// insertChargeOnce writes the child's charge row unless one exists, in ONE statement (so two writers, in this
// process or another, cannot both find none and both insert). It says whether this call wrote it.
func insertChargeOnce(x execer, profileID string, sid, tid int64, conv, who, note, at string) (bool, error) {
	res, err := x.Exec(`INSERT INTO coder_mission_events (profile_id, session_id, task_id, kind, who, note, at)
		SELECT ?, ?, ?, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM coder_mission_events WHERE profile_id = ? AND session_id = ? AND kind = ? AND substr(note, 1, ?) = ?)`,
		profileID, sid, tid, chargeKind, who, clipNote(note), at, profileID, sid, chargeKind, len(conv)+1, conv+" ")
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// writeChargeOnce is writeCharge that also says whether the child was charged before (so nothing was written).
func (s *Store) writeChargeOnce(profileID string, sid, tid int64, conv string, spent float64, note, who string) (float64, bool, error) {
	_, found, err := s.loadMissionSession(profileID, sid)
	if err != nil {
		return 0, false, err
	}
	record := func(x execer) (bool, error) {
		return insertChargeOnce(x, profileID, sid, tid, conv, who, note, s.stamp())
	}
	skip := func(why string) (float64, bool, error) {
		note += " skipped: " + why
		wrote, err := record(s.db)
		if err != nil {
			return 0, false, fmt.Errorf("the charge of %s was skipped but its record was not written: %w", conv, err)
		}
		return 0, !wrote, nil
	}
	switch {
	case !found:
		return skip("the mission session is gone")
	case spent <= 0:
		wrote, err := record(s.db)
		if err != nil {
			return 0, false, fmt.Errorf("the charge row of %s was not written: %w", conv, err)
		}
		return 0, !wrote, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	why := fmt.Sprintf("child session of task #%d", tid)
	_, err = tasks.NewStore(s.db).AddSessionSpendWith(ctx, profileID, sid, spent, why, tasks.Actor{Kind: tasks.Human}, func(ctx context.Context, x tasks.Tx) error {
		wrote, err := record(ctxExec{ctx, x})
		if err != nil {
			return err
		}
		if !wrote {
			return errAlreadyCharged // rolls the meter update back with it
		}
		return nil
	})
	switch {
	case errors.Is(err, errAlreadyCharged):
		return 0, true, nil
	case errors.Is(err, tasks.ErrNotFound):
		return skip("the mission session is gone") // vanished since it was read: the charge can never succeed
	case err != nil:
		return 0, false, fmt.Errorf("charging the child %s to mission #%d: %w", conv, sid, err)
	}
	return spent, false, nil
}

// ctxExec lets the charge row be written through the spend's transaction.
type ctxExec struct {
	ctx context.Context
	x   tasks.Tx
}

func (c ctxExec) Exec(q string, a ...any) (sql.Result, error) { return c.x.ExecContext(c.ctx, q, a...) }

// markUnresolved records, once, that nobody knows what the child cost, and how much of the mission's budget stays held until the person resolves it.
func (s *Store) markUnresolved(profileID string, t chargeTarget, conv, why string) error {
	hold, err := s.unresolvedHold(s.db, profileID, t.session)
	if err != nil {
		return err
	}
	_, err = insertUnresolvedOnce(s.db, profileID, t.session, t.task, conv, unresolvedNote(conv, hold, why), s.stamp())
	return err
}

// unresolvedNote is the note of an unresolved record: "<conversation> $<held> ...".
func unresolvedNote(conv string, hold float64, why string) string {
	return fmt.Sprintf("%s $%s unresolved: %s; the person resolves it with `coder sessions mission-children resolve-charge`", conv, formatUSD(hold), why)
}

// charged says whether a charge row exists for the child conversation. A query that fails is an error, never "no".
func (s *Store) charged(profileID string, sessionID int64, conv string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM coder_mission_events WHERE profile_id = ? AND session_id = ? AND kind = 'charge'
		AND substr(note, 1, ?) = ?`, profileID, sessionID, len(conv)+1, conv+" ").Scan(&n)
	return n > 0, err
}

// settleChild is what happens once a child's row has ended: its task tokens are revoked (the run is over), and its
// spend is charged to the mission. Both are idempotent, and a failure of either is retried by the next pass.
func (h *Host) settleChild(profileID, conv string) {
	if conv == "" {
		return
	}
	if err := h.revokeChildTokens(profileID, conv); err != nil {
		return // charged on the pass after the token is gone: nothing runs under a token while its cost is counted
	}
	if usd, err := h.Store.ChargeChild(profileID, conv); err != nil {
		h.logf("coder host: %v", err)
	} else if usd > 0 {
		h.logf("coder host: charged $%.4f of %s to its mission", usd, conv)
	}
}

// settleEnded settles the rows that ended without being settled: a host that was down or a failed write. A pass
// takes the next chargeBatch rows after the cursor, oldest row first; when it reaches the end the cursor starts
// over, so rows whose charge keeps failing cannot keep the later ones from being tried. Rows whose cost is unresolved are the person's
// and are skipped. Then the snapshots no charge row answers (a deleted child whose row was released and marked again).
func (h *Host) settleEnded() {
	rows, err := h.Store.childrenWhereLimit(strings.TrimSpace(notCharged)+` AND NOT `+unresolvedRow+` AND coder_mission_children.id > ?`, chargeBatch, h.chargeCursor)
	if err != nil {
		h.logf("coder host: ended mission children: %v", err)
		return
	}
	h.chargeCursor = 0
	if len(rows) == chargeBatch {
		h.chargeCursor = rows[len(rows)-1].ID
	}
	for _, r := range rows {
		h.settleChild(r.ProfileID, r.ident())
	}
	h.settleSnapshots()
}

// settleSnapshots charges the "spend" snapshots that have no charge row, whatever became of the row they were taken for.
func (h *Host) settleSnapshots() {
	rows, err := h.Store.db.Query(`SELECT e.id, e.profile_id, substr(e.note, 1, instr(e.note, ' ') - 1) FROM coder_mission_events e
		WHERE e.kind = 'spend' AND e.id > ? AND NOT EXISTS (SELECT 1 FROM coder_mission_events c WHERE c.profile_id = e.profile_id
			AND c.session_id = e.session_id AND c.kind = 'charge' AND substr(c.note, 1, instr(e.note, ' ')) = substr(e.note, 1, instr(e.note, ' ')))
		ORDER BY e.id LIMIT ?`, h.snapshotCursor, chargeBatch)
	if err != nil {
		h.logf("coder host: unrecorded spend snapshots: %v", err)
		return
	}
	type snap struct {
		id            int64
		profile, conv string
	}
	var list []snap
	for rows.Next() {
		var x snap
		if err := rows.Scan(&x.id, &x.profile, &x.conv); err != nil {
			rows.Close()
			h.logf("coder host: unrecorded spend snapshots: %v", err)
			return
		}
		list = append(list, x)
	}
	rows.Close()
	h.snapshotCursor = 0
	if len(list) == chargeBatch {
		h.snapshotCursor = list[len(list)-1].id
	}
	for _, x := range list {
		h.settleChild(x.profile, x.conv)
	}
}
