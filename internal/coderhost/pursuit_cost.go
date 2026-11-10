package coderhost

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"

	"github.com/monoes/mono-agent/internal/monomind"
)

// LeadPin is the runtime/model a conversation runs, as the app recorded it, or "" when it is not known (no model named: the
// runtime's own default, which the host cannot tell from the judge's).
func LeadPin(db *sql.DB, profileID, conversationID string) string {
	return LeadPinIn(context.Background(), db, profileID, conversationID)
}

// LeadPinIn is LeadPin read through q (a transaction's connection, for `pursuit begin`).
func LeadPinIn(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, profileID, conversationID string) string {
	var rt, md string
	if err := q.QueryRowContext(ctx, `SELECT runtime_id, model FROM ai_chat_conversations WHERE id = ? AND profile_id = ?`, conversationID, profileID).Scan(&rt, &md); err != nil {
		return ""
	}
	if md = strings.TrimSpace(md); md == "" {
		return ""
	}
	if rt = strings.TrimSpace(rt); rt == "" {
		rt = monomind.DefaultCoderRuntime
	}
	return rt + "/" + md
}

// CostInput is what the dollars table is computed from: the host's per-wake cap X, the mission's budget (0 = not known) and
// the away limits the person chose (percent of the budget, wake-ups since the person last acted).
type CostInput struct {
	WakeCapUSD       float64
	MissionBudgetUSD float64
	Pct              int
	Wakes            int
}

// CostRow is one line of the table. Known is false where the figure cannot be given (a mission with no known budget).
type CostRow struct {
	Key   string  `json:"key"`
	USD   float64 `json:"usd"`
	Known bool    `json:"known"`
	Text  string  `json:"text"`
}

// CostTable is the dollars table: its rows and the plain text built from them. Every figure comes from the constants of the
// loop (PursuitJudgeCallCapUSD, MaxPursuitWakesPerHour, MaxPursuitJudgeCallsPerHour) and from the input, so the verb help, the
// docs and the app's dialog cannot say different numbers.
type CostTable struct {
	Rows []CostRow `json:"rows"`
	Text string    `json:"text"`
}

// UsdText writes a dollar amount as the table does: $2, $0.50, $22.50.
func UsdText(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("$%d", int64(v))
	}
	return fmt.Sprintf("$%.2f", v)
}

// PursuitCostRows is the worst-case cost table of a pursuit.
func PursuitCostRows(in CostInput) CostTable {
	x, j := in.WakeCapUSD, PursuitJudgeCallCapUSD
	missionHour := float64(in.Wakes) * (x + j)
	hostWakes, hostJudges := float64(MaxPursuitWakesPerHour), float64(MaxPursuitJudgeCallsPerHour)
	hostHour := hostWakes*x + hostJudges*j
	rows := []CostRow{
		{Key: "mission_hour", USD: missionHour, Known: true,
			Text: fmt.Sprintf("One hour on this mission: about %s (%d wake-ups x %s + %d judge calls x %s).", UsdText(missionHour), in.Wakes, UsdText(x), in.Wakes, UsdText(j))},
		{Key: "host_hour", USD: hostHour, Known: true,
			Text: fmt.Sprintf("One hour on this whole computer: about %s (%d wake-ups x %s + %d judge calls x %s).", UsdText(hostHour), MaxPursuitWakesPerHour, UsdText(x), MaxPursuitJudgeCallsPerHour, UsdText(j))},
	}
	if in.MissionBudgetUSD > 0 && !math.IsInf(in.MissionBudgetUSD, 0) && !math.IsNaN(in.MissionBudgetUSD) {
		share := float64(in.Pct) * in.MissionBudgetUSD / 100
		until := share + x + j
		rows = append(rows, CostRow{Key: "until_stop", USD: until, Known: true,
			Text: fmt.Sprintf("Until your away limits stop it, on this mission's budget of %s: about %s (%d percent = %s, plus one last wake-up %s and one last judge call %s). This is the only hard limit.",
				UsdText(in.MissionBudgetUSD), UsdText(until), in.Pct, UsdText(share), UsdText(x), UsdText(j))})
	} else {
		rows = append(rows, CostRow{Key: "until_stop", Text: "The mission has no known budget, so it will wait for you straight away."})
	}
	var b strings.Builder
	fmt.Fprintf(&b, "What this can cost you, at worst. The wake-up cap is %s and the judge is asked to stay under %s a call.\n", UsdText(x), UsdText(j))
	for _, r := range rows {
		b.WriteString("- " + r.Text + "\n")
	}
	b.WriteString("- Helpers the lead starts are NOT covered by the wake-up cap. Money the lead hands to helpers counts toward the percent limit, so a big hand-out makes the pursuit wait for you.\n")
	b.WriteString("- The caps are requests to the AI service, checked from the cost it reports afterwards. One call can overshoot.\n")
	return CostTable{Rows: rows, Text: b.String()}
}
