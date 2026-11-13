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
// its own rows (never agent text), as of AsOf. The 20% rule counts three things: the METERED spend of the mission
// session, the judge calls (a host-written ledger, unknown cost at the per-call cap) and the turn journals of the lead and its
// helpers, all since the person last acted. What it does not see exactly: a helper's turn that is still running is counted at
// its cap, a helper that ended may be counted twice (the stop then comes earlier), and the check is made before the next
// judge call, so the last call can take the spend past the limit by up to its cap ($0.50) plus the lead turn it earns. The
// counts of parked questions, wake-ups, judge calls and failed rounds bound the pursuit too.
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
		return "waiting for you: too many questions wait for your answer (a goal check that failed or errored counts as one)"
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
	case "meter-unreadable":
		return "waiting for you: the judge ledger or the turn journals could not be read, so the spend cannot be measured; it clears when they read again"
	case "judge-pin":
		return "waiting for you: the judge's model must have a different runtime/model name from the lead's (a name check: two names for one model are not recognised), and the lead's model must be known (pin the lead's model, or change the host's judge pin and restart it)"
	case "goal-check":
		return "a goal check is running: nothing else is judged or woken until it ends (it ends by itself, in at most about thirteen minutes)"
	case "goal-answer":
		return "waiting for you: the goal check passed its machine criteria and its checker, and the criteria only you can judge are waiting for your answers (task session goal check shows it, goal answer answers)"
	case "verifier-pin":
		return "waiting for you: the goal checker's model must have a different runtime/model name from the judge's and the lead's, and the lead's model must be known (change MONOAGENT_PURSUIT_VERIFIER, or pin the lead's model, and restart the coder host)"
	case "verifier-failing":
		return "waiting for you: the goal checker could not be used by several checks in a row (unavailable, an invalid answer, or another model than its pin); it is not tried again until you act"
	case "cannot-check":
		return "waiting for you: the goal cannot be checked here (the project folder cannot be used, or the checker could not be built); task session goal check names why"
	case "no-chat":
		return "waiting for you: this mission has no chat to wake"
	case "over-committed":
		return "waiting for you: over-committed: the mission's budget is exceeded by its own spend plus what was carved to helpers; raise the budget to resume"
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

// Who has to act for the pursuit to go on (the `needs` field of the status and list documents).
const (
	NeedsYou  = "you"  // the person: a limit is reached, the pursuit is paused, or a setting must change
	NeedsHost = "host" // the machine: no coder host runs the loop, or it has not looked yet, or a read clears by itself
	NeedsNone = "none" // nothing: the pursuit is working
)

// The states of the list document: where one pursuit stands, in one word.
const (
	StateRunning = "running"
	StatePaused  = "paused"
	StateWaiting = "waiting"
	StateOffHost = "off-host"
)

// StateOf is the one word for where a pursuit stands. Paused is the person's own choice and comes first; then a host that
// does not run the loop (nothing moves); then a waiting reason; otherwise running. Pure: host-written facts only.
func StateOf(paused bool, waiting string, hostRuns bool) string {
	switch {
	case paused:
		return StatePaused
	case !hostRuns:
		return StateOffHost
	case waiting != "":
		return StateWaiting
	}
	return StateRunning
}

// NeedsOf says who has to act. A waiting reason is the person's to clear (the WaitingWhy text says "waiting for you")
// except meter-unreadable, which clears when the ledger reads again; a host that does not run the loop, or has not looked at
// the pursuit yet, is the host's.
func NeedsOf(paused bool, waiting string, seen, hostRuns bool) string {
	switch {
	case paused:
		return NeedsYou
	case waiting != "" && waiting != "meter-unreadable" && waiting != "goal-check":
		return NeedsYou
	case !hostRuns, !seen, waiting == "meter-unreadable", waiting == "goal-check":
		return NeedsHost
	}
	return NeedsNone
}

// NextLine is, in plain words, what the person can do now. It is built from the same host-written facts as NeedsOf and
// never from agent text.
func NextLine(paused bool, waiting string, seen, hostRuns bool) string {
	switch {
	case paused:
		return "Resume it (task session pursuit resume) when you want work to go on; nothing is woken or judged while it is paused."
	case waiting == "parked", waiting == "spend", waiting == "wakes":
		return "Look at the mission and act on it (answer a question, approve a plan, or tell the lead what you want); your action resets the away counters and the pursuit goes on. If the goal is met, close the mission."
	case waiting == "budget-unknown":
		return "Give the mission a budget (task session budget); the pursuit cannot measure its spend without one."
	case waiting == "lead-uncapped":
		return "Set a budget for the lead's chat; without one a wake-up could cost without limit."
	case waiting == "judge-failing":
		return "Act on the mission to try the judge again; if it keeps failing, check the judge's model (coder host status)."
	case waiting == "judge-pin":
		return "Change the host's judge model (MONOAGENT_PURSUIT_JUDGE) so that it differs from the lead's, and restart the coder host."
	case waiting == "goal-check":
		return "Nothing to do now: a goal check is running and the pursuit goes on when it ends."
	case waiting == "goal-answer":
		return "Read the goal check (task session goal check) and answer the criteria that are yours to judge (task session goal answer); accepting is your decision alone."
	case waiting == "verifier-pin":
		return "Change the host's goal checker model (MONOAGENT_PURSUIT_VERIFIER) so that it differs from the judge's and the lead's, and restart the coder host; or turn the checker off by unsetting it."
	case waiting == "verifier-failing":
		return "Act on the mission to try the goal checker again; if it keeps failing, check its model (coder host status)."
	case waiting == "cannot-check":
		return "Read why (task session goal check, coder host status): the project folder, the checker or the confinement of project commands is missing. You decide when to close the mission."
	case waiting == "over-committed":
		return "Raise the mission's budget (task session budget); its spend plus what was carved to helpers is past it, so no worker can start and nothing is judged or woken."
	case waiting == "no-chat":
		return "This mission has no chat to wake; close it, or run the work from a chat."
	case waiting == "engine-parked":
		return "A stored strategy row failed verification and the strategy engine stopped; look at it before you go on (coder host status)."
	case waiting == "limits":
		return "The away limits are out of range; this pursuit cannot go on as it is."
	case waiting == "meter-unreadable":
		return "Nothing to do now: the spend cannot be read at the moment and the pursuit goes on when it can."
	case waiting != "":
		return "Look at the mission and act on it."
	case !hostRuns:
		return "Choose the judge model (`coder host pursuit judge set RUNTIME/MODEL`) and restart the coder host; nothing is judged or woken until a judge runs."
	case !seen:
		return "Nothing yet: the pursuit loop looks at a new pursuit within a few minutes."
	}
	return "Nothing needed: the pursuit is working. Close the mission yourself when the goal is met."
}
