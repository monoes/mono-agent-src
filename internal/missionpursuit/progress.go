package missionpursuit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/tasks"
)

// MaxHostWakesPerHour is the host-wide cap on pursuit wake-ups of the lead, across all missions, in any hour. It is a
// constant of this host (not a per-mission setting); the status shows how much of it is used and when it frees.
const MaxHostWakesPerHour = 12

// MaxHostJudgeCallsPerHour is the host-wide cap on judge calls, across all missions, in any hour (S5a). Like the wake-up cap
// it is a constant of the host.
const MaxHostJudgeCallsPerHour = 12

// MaxBadJudgeRounds: this many judge rounds in a row that failed stop the pursuit until the person acts.
const MaxBadJudgeRounds = 3

// Progress is what the pursuit loop last recorded for a mission, as the status shows it: how far each away limit has come
// and, in plain words, why the pursuit waits for the person (if it does). It is read-only data the host loop wrote from
// its own rows (never agent text), as of AsOf. The 20% rule sees only the METERED spend of the mission session: judge
// calls, wake-up turns and running children that the meter does not hold are not in it, which is why the counts of
// parked questions and wake-ups bound the pursuit too.
type Progress struct {
	Seen         bool    `json:"seen"` // the loop has looked at this pursuit
	Waiting      string  `json:"waiting,omitempty"`
	Why          string  `json:"why,omitempty"` // Waiting in plain words
	WakesUsed    int     `json:"wakes_used"`
	WakesMax     int     `json:"wakes_max"`
	Parked       int     `json:"parked"`
	ParkedMax    int     `json:"parked_max"`
	SpentAwayUSD float64 `json:"spent_away_usd"`
	BudgetUSD    float64 `json:"budget_usd"` // 0 = unknown
	PctMax       int     `json:"pct_max"`
	HostWakes    int     `json:"host_wakes_last_hour"`
	HostWakesMax int     `json:"host_wakes_max"`
	HostFreesAt  string  `json:"host_wakes_free_at,omitempty"` // when the oldest of those leaves the hour
	AsOf         string  `json:"as_of,omitempty"`

	// What the judge and the lead's turns cost since you last acted (S5a). Both are in SpentAwayUSD's rule as well: the 20%
	// rule counts the metered spend, the judge ledger and the lead's turns together. A judge call whose cost the runtime did not
	// report is counted at the per-call cap, never as free.
	JudgeSpendUSD float64 `json:"judge_spend_usd"`
	LeadSpendUSD  float64 `json:"lead_spend_usd"`
	JudgeCalls    int     `json:"judge_calls"`
	BadRounds     int     `json:"bad_rounds"` // judge rounds in a row that failed
	BadRoundsMax  int     `json:"bad_rounds_max"`
	HostJudges    int     `json:"host_judge_calls_last_hour"`
	HostJudgesMax int     `json:"host_judge_calls_max"`
	LastRound     string  `json:"last_round,omitempty"` // how the latest judge round ended: applied, invalid, model-failed, input
}

// WaitingWhy is a waiting reason in plain words.
func WaitingWhy(waiting string) string {
	switch waiting {
	case "":
		return ""
	case "parked":
		return "waiting for you: too many questions wait for your answer"
	case "spend":
		return "waiting for you: the metered spend since you last acted reached its limit"
	case "wakes":
		return "waiting for you: the pursuit woke the lead as often as it may since you last acted"
	case "budget-unknown":
		return "waiting for you: the mission has no known budget, so no share of it can be measured (set one)"
	case "limits":
		return "waiting for you: the away limits are out of range"
	case "judge-failing":
		return "waiting for you: the judge failed too many rounds in a row (its answer was invalid, too long or it did not answer); it is not called again until you act"
	case "lead-uncapped":
		return "waiting for you: the lead has no spend cap of its own, so a wake-up could cost without limit (set a budget for the lead's chat)"
	case "engine-parked":
		return "waiting for you: a stored strategy row failed verification and the strategy engine stopped"
	}
	return "waiting for you: " + waiting
}

// GetProgress reads the loop's last record for the mission session of the profile. A pursuit the loop has not looked at
// yet has Seen false and the limits only.
func (s *Store) GetProgress(ctx context.Context, profileID string, session int64) (Progress, error) {
	var out Progress
	err := s.read(ctx, func(x tasks.Queryer) error {
		var id int64
		if err := x.QueryRowContext(ctx, `SELECT id FROM task_sessions WHERE id = ? AND profile_id = ?`, session, profileID).Scan(&id); err != nil {
			return notFound(err, session)
		}
		p, err := readPursuit(ctx, x, session)
		if err != nil {
			return err
		}
		out = Progress{WakesMax: p.WakesMax, ParkedMax: p.ParkedMax, PctMax: p.Pct, HostWakesMax: MaxHostWakesPerHour, BadRoundsMax: MaxBadJudgeRounds, HostJudgesMax: MaxHostJudgeCallsPerHour}
		var spent, budget, judgeMicro, leadMicro int64
		err = x.QueryRowContext(ctx, `SELECT waiting, wakes_since, parked, spent_away_micro, budget_micro, hour_wakes, hour_frees_at, updated_at,
			judge_micro, lead_micro, judge_calls, bad_rounds, hour_judges, last_round FROM mission_pursuit_state WHERE session_id = ?`, session).
			Scan(&out.Waiting, &out.WakesUsed, &out.Parked, &spent, &budget, &out.HostWakes, &out.HostFreesAt, &out.AsOf,
				&judgeMicro, &leadMicro, &out.JudgeCalls, &out.BadRounds, &out.HostJudges, &out.LastRound)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("missionpursuit: reading the pursuit progress: %w", err)
		}
		out.Seen = true
		out.SpentAwayUSD, out.BudgetUSD = float64(spent)/1e6, float64(budget)/1e6
		out.JudgeSpendUSD, out.LeadSpendUSD = float64(judgeMicro)/1e6, float64(leadMicro)/1e6
		out.Why = WaitingWhy(out.Waiting)
		return nil
	})
	return out, err
}
