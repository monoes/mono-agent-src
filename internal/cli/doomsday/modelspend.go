package doomsday

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"math"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/coderspend"
	"github.com/monoes/mono-agent/internal/tasks"
)

// ModelSpend is what the open run's sessions' model turns cost: the boss's, the planners' (direction and research sessions) and
// the doers' (every session below those). It is a DISPLAY of costs that already exist in the chat turns' usage and the host's turn
// caps (internal/coderspend): reading it changes no budget and enforces nothing. It is kept apart from the chain-verified run
// view and from the charter's counters (Spend of the overview: asks and grants), and says so: Enforced is false and
// Authenticated is false (the usage events are rows no MAC covers). What the host does enforce is each session's own budget
// (SessionBudgetsEnforced), turn by turn, and the charter's caps on asks and grants.
type ModelSpend struct {
	BossMicro    int64 `json:"boss_micro"`
	PlannerMicro int64 `json:"planner_micro"`
	DoerMicro    int64 `json:"doer_micro"`
	TotalMicro   int64 `json:"total_micro"`
	Sessions     int   `json:"sessions"`
	// Enforced is false: these figures are informational; nothing is capped by them.
	Enforced bool `json:"enforced"`
	// Authenticated is false: the numbers come from usage rows that carry no MAC.
	Authenticated bool `json:"authenticated"`
	// SessionBudgetsEnforced is true: each session's own budget caps its next turn, separately from this display.
	SessionBudgetsEnforced bool `json:"session_budgets_enforced"`
}

const maxSpendSessions = 2000

// ModelSpendOf reads the model spend of the profile's open run, or nil when it has none. Read-only.
func (w Wiring) ModelSpendOf(ctx context.Context) (*ModelSpend, error) {
	if w.DB == nil {
		return nil, nil
	}
	rows, err := w.DB.QueryContext(ctx, `WITH RECURSIVE tree(id, parent, kind, conv) AS (
		SELECT s.id, COALESCE(s.parent_session_id, 0), s.kind, s.conversation_id FROM task_sessions s
		JOIN doomsday_runs r ON r.profile = s.profile_id AND r.charter_id = s.doomsday_charter_id AND r.generation = s.doomsday_generation AND r.state <> 'quiescent'
		WHERE s.profile_id = ? AND COALESCE(s.parent_session_id, 0) = 0 AND s.archived_at = '' AND `+coderhost.LeadSessionSQL+`
		UNION ALL
		SELECT c.id, c.parent_session_id, c.kind, c.conversation_id FROM task_sessions c JOIN tree t ON c.parent_session_id = t.id WHERE c.profile_id = ?)
		SELECT parent, kind, COALESCE(conv, '') FROM tree LIMIT ?`, w.Profile, w.Profile, maxSpendSessions)
	if err != nil {
		return nil, err
	}
	type member struct {
		parent int64
		kind   string
		conv   string
	}
	var members []member
	for rows.Next() {
		var m member
		if err := rows.Scan(&m.parent, &m.kind, &m.conv); err != nil {
			rows.Close()
			return nil, err
		}
		members = append(members, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(members) == 0 {
		return nil, err
	}
	ms := &ModelSpend{SessionBudgetsEnforced: true}
	for _, m := range members {
		micro, err := sessionMicro(w.DB, w.Profile, m.conv)
		if err != nil {
			return nil, err
		}
		switch {
		case m.parent == 0:
			ms.BossMicro += micro
		case m.kind == tasks.SessionKindDirection || m.kind == tasks.SessionKindResearch:
			ms.PlannerMicro += micro
		default:
			ms.DoerMicro += micro
		}
		ms.Sessions++
	}
	ms.TotalMicro = ms.BossMicro + ms.PlannerMicro + ms.DoerMicro
	return ms, nil
}

func sessionMicro(db *sql.DB, profile, conv string) (int64, error) {
	if conv == "" {
		return 0, nil
	}
	usd, err := coderspend.Spent(db, profile, conv)
	if err != nil {
		return 0, err
	}
	return int64(math.Round(usd * 1e6)), nil
}

// writeModelSpend prints the model spend with its labels: it is the line `status` and `overview` add to their text.
func writeModelSpend(w io.Writer, ms *ModelSpend) {
	if ms == nil {
		return
	}
	fmt.Fprintf(w, "model turns (informational, not enforced, not authenticated): %d micro in %d sessions: boss %d, planners %d, doers %d\n",
		ms.TotalMicro, ms.Sessions, ms.BossMicro, ms.PlannerMicro, ms.DoerMicro)
	fmt.Fprintln(w, "  enforced separately: each session's own budget caps its turns; the charter's caps count asks and grants only")
}
