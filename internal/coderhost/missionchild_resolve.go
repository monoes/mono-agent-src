package coderhost

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/monoes/mono-agent/internal/tasks"
)

// ResolveCharge is the person's way out of an unresolved child (missionchild_charge.go, invariant iii): the host
// found a deleted child whose cost nobody recorded and holds a conservative amount of the mission's budget for it.
// The operator gives the figure (usd, added to the mission's meter) or skips it (skip, with the reason, nothing
// added); either writes the child's charge row, with the operator as its author, in the same transaction as the
// spend, and ends the hold. It returns what was added. Only the operator's CLI verb calls it.
func (s *Store) ResolveCharge(profileID, conv string, usd float64, skip bool, reason string) (float64, error) {
	reason = strings.TrimSpace(reason)
	switch {
	case skip && (usd != 0 || reason == ""):
		return 0, fmt.Errorf("%w: a skip is given with a reason and without an amount", ErrInvalid)
	case !skip && (usd <= 0 || math.IsNaN(usd) || math.IsInf(usd, 0)):
		return 0, fmt.Errorf("%w: give the child's cost in dollars above 0 (or --skip with a reason)", ErrInvalid)
	case !skip && usd > tasks.MaxBudgetUSD:
		return 0, fmt.Errorf("%w: $%g is beyond the supported amount ($%g): check the figure", ErrInvalid, usd, tasks.MaxBudgetUSD)
	}
	if len(reason) > 150 {
		reason = reason[:150]
	}
	var sid, tid int64
	err := s.db.QueryRow(`SELECT session_id, task_id FROM coder_mission_events WHERE profile_id = ? AND kind = ? AND substr(note, 1, ?) = ? ORDER BY id DESC LIMIT 1`,
		profileID, unresolvedKind, len(conv)+1, conv+" ").Scan(&sid, &tid)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("%w: %s has no unresolved spend (the host marks one on its next pass over a deleted child with no record)", ErrInvalid, conv)
		}
		return 0, err
	}
	done, err := s.charged(profileID, sid, conv)
	if err != nil {
		return 0, err
	}
	if done {
		return 0, fmt.Errorf("%w: %s was charged already", ErrInvalid, conv)
	}
	note := fmt.Sprintf("%s $%s resolved by the operator", conv, formatUSD(usd))
	if skip {
		note = fmt.Sprintf("%s $0.000000 skipped by the operator", conv)
	}
	if reason != "" {
		note += ": " + reason
	}
	added, already, err := s.writeChargeOnce(profileID, sid, tid, conv, usd, note, "operator")
	if err == nil && already { // another resolution (or the host) wrote the charge between the check and the write
		return 0, fmt.Errorf("%w: %s was charged already", ErrInvalid, conv)
	}
	return added, err
}
