package missionpursuit

import (
	"context"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// MaxIDsPerState caps the direction ids listed per state; the counts stay exact.
const MaxIDsPerState = 50

// DirectionStates are the states a direction can be in, in the order they are shown.
var DirectionStates = []string{"proposed", "active", "stalled", "finished", "pruned"}

// StrategyView is what the strategy engine recorded for a mission, as the status shows it. Host-written facts only:
// counts, ids the host generated, enums and times. No agent-authored text is in it (see DirectionTitle for that).
type StrategyView struct {
	Epochs         EpochSummary   `json:"epochs"`
	Directions     DirectionStats `json:"directions"`
	LastJudgeRound *JudgeRound    `json:"last_judge_round,omitempty"`
}

// EpochSummary counts the epochs and names the latest.
type EpochSummary struct {
	Count  int          `json:"count"`
	Latest *LatestEpoch `json:"latest,omitempty"`
}

// LatestEpoch is the newest epoch: why it started and where it stands.
type LatestEpoch struct {
	Seq       int64  `json:"seq"`
	Trigger   string `json:"trigger"`
	State     string `json:"state"`
	StartedAt string `json:"started_at"`
}

// DirectionStats counts the directions by state and lists their ids (at most MaxIDsPerState per state).
type DirectionStats struct {
	Counts map[string]int      `json:"counts"`
	IDs    map[string][]string `json:"ids"`
}

// JudgeRound is the last judge round: when its epoch started and how the loop recorded it ending.
type JudgeRound struct {
	At      string `json:"at"`
	Outcome string `json:"outcome"`
}

// DirectionTitle is the one piece of AGENT text a direction carries: the judge proposed it. It is returned apart from the
// StrategyView so that nothing host-written can pick it up by accident.
type DirectionTitle struct {
	ID    string
	State string
	Title string // raw; the caller cleans and cuts it before showing it
}

// StrategyView reads the epochs and directions of a mission (read only, plain SELECTs). The last judge round's outcome is
// taken from the progress record the loop wrote.
func (s *Store) StrategyView(ctx context.Context, profileID string, session int64, pr Progress) (StrategyView, []DirectionTitle, error) {
	v := StrategyView{Directions: DirectionStats{Counts: map[string]int{}, IDs: map[string][]string{}}}
	var titles []DirectionTitle
	for _, st := range DirectionStates {
		v.Directions.Counts[st] = 0
		v.Directions.IDs[st] = []string{}
	}
	err := s.read(ctx, func(x tasks.Queryer) error {
		var id int64
		if err := x.QueryRowContext(ctx, `SELECT id FROM task_sessions WHERE id = ? AND profile_id = ?`, session, profileID).Scan(&id); err != nil {
			return notFound(err, session)
		}
		var started int64
		var n int
		if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_strategy_epochs WHERE session_id = ?`, session).Scan(&n); err != nil {
			return fmt.Errorf("missionpursuit: counting the epochs: %w", err)
		}
		v.Epochs.Count = n
		if n > 0 {
			var l LatestEpoch
			if err := x.QueryRowContext(ctx, `SELECT epoch_seq, trigger, state, started_at FROM mission_strategy_epochs WHERE session_id = ? ORDER BY epoch_seq DESC LIMIT 1`, session).
				Scan(&l.Seq, &l.Trigger, &l.State, &started); err != nil {
				return fmt.Errorf("missionpursuit: reading the latest epoch: %w", err)
			}
			l.StartedAt = time.Unix(started, 0).UTC().Format(time.RFC3339)
			v.Epochs.Latest = &l
			if pr.LastRound != "" {
				v.LastJudgeRound = &JudgeRound{At: l.StartedAt, Outcome: pr.LastRound}
			}
		}
		rows, err := x.QueryContext(ctx, `SELECT direction_id, state, title FROM mission_strategy_directions WHERE session_id = ? ORDER BY rowid`, session)
		if err != nil {
			return fmt.Errorf("missionpursuit: reading the directions: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var d DirectionTitle
			if err := rows.Scan(&d.ID, &d.State, &d.Title); err != nil {
				return err
			}
			v.Directions.Counts[d.State]++
			if len(v.Directions.IDs[d.State]) < MaxIDsPerState {
				v.Directions.IDs[d.State] = append(v.Directions.IDs[d.State], d.ID)
				titles = append(titles, d)
			}
		}
		return rows.Err()
	})
	return v, titles, err
}
