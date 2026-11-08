package coderhost

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/epoch"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/judge"
	"github.com/monoes/mono-agent/internal/missionpursuit/judgecost"
)

// The meter of the pursuit's own spend (S5a). The 20% rule used to see only the mission session's metered spend; a judge call
// and the lead's wake-up turns were on no meter. Now:
//   - every judge call is a row in mission_pursuit_spend (migration 168), written BEFORE the call at the per-call cap and
//     settled once afterwards with the cost the runtime reported; a call that reports nothing, or whose process dies, stays at
//     the cap: unknown is never free. The rows after the person's last action are part of the away spend, and all rows of the
//     last hour, of every mission, are the host-wide cap on judge calls;
//   - the lead's own turns and its helpers' turns are read from the host's turn journals (Store.Spent) and measured from the
//     person's last action. A helper that already ended is on the mission meter as well, so it can be counted twice: the stop
//     then comes earlier, never later.

// meteredModel charges every call of the inner judge model. It is the only model the pursuit's judge ever sees.
type meteredModel struct {
	inner   judge.Model
	store   *Store
	session int64
}

func (m meteredModel) Pin() string { return m.inner.Pin() }

func (m meteredModel) Complete(ctx context.Context, req judge.Request) (out []byte, err error) {
	id, rerr := m.store.reserveJudgeCall(m.session)
	if rerr != nil {
		return nil, fmt.Errorf("%w: the call could not be recorded, so it was not made: %v", judge.ErrModelFailed, rerr)
	}
	micro, known := usdMicro(judgecost.PerCallCapUSD), false
	defer func() {
		// Settled whatever happened (an error, a panic): the call is charged what the runtime reported, else the cap.
		_ = m.store.settleJudgeCall(id, micro, known)
	}()
	ctx = judgecost.With(ctx, func(usd float64, ok bool) {
		if ok {
			micro, known = usdMicro(usd), true
		}
	})
	return m.inner.Complete(ctx, req)
}

// reserveJudgeCall records a judge call at the per-call cap before it is made and returns its ledger id.
func (s *Store) reserveJudgeCall(session int64) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO mission_pursuit_spend (session_id, kind, at, micro, known, settled) VALUES (?, 'judge', ?, ?, 0, 0)`,
		session, s.now().UTC().Format(time.RFC3339Nano), usdMicro(judgecost.PerCallCapUSD))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// settleJudgeCall writes the cost a call turned out to have. A row settles once.
func (s *Store) settleJudgeCall(id, micro int64, known bool) error {
	k := 0
	if known {
		k = 1
	}
	_, err := s.db.Exec(`UPDATE mission_pursuit_spend SET micro = ?, known = ?, settled = 1 WHERE id = ? AND settled = 0`, micro, k, id)
	return err
}

// judgeLedgerHead is the highest ledger id of the mission: the person's action is measured from here.
func (s *Store) judgeLedgerHead(session int64) (int64, error) {
	var id int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM mission_pursuit_spend WHERE session_id = ?`, session).Scan(&id); err != nil {
		return 0, fmt.Errorf("reading the judge ledger: %w", err)
	}
	return id, nil
}

// judgeSpendAfter is the judge spend (micro-USD) and the number of calls after a ledger id.
func (s *Store) judgeSpendAfter(session, after int64) (micro int64, calls int, err error) {
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(micro), 0), COUNT(*) FROM mission_pursuit_spend WHERE session_id = ? AND id > ?`, session, after).Scan(&micro, &calls); err != nil {
		return 0, 0, fmt.Errorf("reading the judge spend: %w", err)
	}
	return micro, calls, nil
}

// pursuitJudgeCallsLastHour counts the judge calls of every mission in the last hour (settled or not: a call that failed was
// paid for). An unreadable time counts.
func (s *Store) pursuitJudgeCallsLastHour() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM mission_pursuit_spend WHERE kind = 'judge' AND ` + tsInWindow("at", s.now().Add(-time.Hour))).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting the judge calls of the hour: %w", err)
	}
	return n, nil
}

// leadTurnSpendMicro is what the lead's chat and the chats below it have cost, from the host's turn journals.
func (s *Store) leadTurnSpendMicro(m pursuitMission, tree []string) (int64, error) {
	var sum int64
	for _, conv := range tree {
		usd, err := s.Spent(m.Profile, conv)
		if err != nil {
			return 0, fmt.Errorf("reading the turn spend of a chat of the mission: %w", err)
		}
		sum += usdMicro(usd)
		if sum < 0 || sum > 1e12*1e6 {
			sum = 1e12 * 1e6
		}
	}
	return sum, nil
}

// pursuitStopReason is why the pursuit opens no epoch beyond the away limits, or "": the judge failed too often in a row, or
// the lead has no spend cap of its own (a wake-up turn of an uncapped lead is bounded by nothing but the runtime).
func (h *Host) pursuitStopReason(m pursuitMission, st pursuitState) string {
	if st.BadRounds >= MaxBadJudgeRounds {
		return pursuitWaitingJudge
	}
	if m.Conv != "" {
		if budget, err := h.Store.Budget(m.Profile, m.Conv); err != nil || !(budget > 0) {
			return pursuitWaitingLead
		}
	}
	return ""
}

// noteJudgeRound keeps how the latest judge round ended and counts the failed ones in a row. A round that was held (the
// floor, the hourly cap, the pin) or never started changes nothing.
func (h *Host) noteJudgeRound(m pursuitMission, res epoch.TickResult, _ error) {
	var round string
	switch res.Outcome {
	case epoch.OutcomeApplied:
		round = "applied"
	case epoch.OutcomeBadJudgment:
		round = res.BadReason
		if round == "" {
			round = "input"
		}
	default:
		return
	}
	st, found, err := h.Store.pursuitStateOf(m.ID)
	if err != nil || !found {
		return
	}
	if round == "applied" {
		st.BadRounds = 0
	} else {
		st.BadRounds++
	}
	st.LastRound = round
	if err := h.Store.savePursuitState(st, h.Pursuit.clock().Now()); err != nil && !errors.Is(err, context.Canceled) {
		h.logf("coder host: pursuit of session %d: %v", m.ID, err)
	}
}
