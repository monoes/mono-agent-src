package surfaces

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// MissionSurfaceID is the surface id of a mission's plan/progress card.
func MissionSurfaceID(sessionID int64) string { return "mission-" + strconv.FormatInt(sessionID, 10) }

// MissionReportSurfaceID is the surface id of a mission's report card.
func MissionReportSurfaceID(sessionID int64) string {
	return "mission-report-" + strconv.FormatInt(sessionID, 10)
}

// MissionPlan builds the plan card. budgetUSD is the mission's budget (0 =
// none set). The card binds its numbers to /mission/{done,total,spent,
// budget,estimated,summary} from the start, so MissionProgressUpdate moves
// its bars in place. Buttons: mission.approve, mission.edit, mission.stop,
// each with context {sessionId}.
func MissionPlan(p chatevents.MissionPlanPayload, budgetUSD float64) ([][]byte, error) {
	return missionCard(p, budgetUSD, false)
}

// MissionRunning replaces an approved plan card with the card of the running
// mission: the same bars bound to the same paths (so MissionProgressUpdate
// keeps working) and a Stop button instead of Approve/Edit/Stop. It returns the
// delete of the old card and the new one.
func MissionRunning(p chatevents.MissionPlanPayload, budgetUSD float64) ([][]byte, error) {
	del, err := Resolved(MissionSurfaceID(p.SessionID))
	if err != nil {
		return nil, err
	}
	card, err := missionCard(p, budgetUSD, true)
	if err != nil {
		return nil, err
	}
	return append(del, card...), nil
}

func missionCard(p chatevents.MissionPlanPayload, budgetUSD float64, running bool) ([][]byte, error) {
	if p.SessionID <= 0 {
		return nil, fmt.Errorf("surfaces: mission session id %d is not positive", p.SessionID)
	}
	sid := strconv.FormatInt(p.SessionID, 10)
	est, budget := money(p.EstimatedBudgetUSD), money(budgetUSD)
	b := newBuilder()
	b.text("title", elide(firstNonEmpty(p.Title, "Mission plan"), 200), "body")
	if running {
		b.badge("round", "Running", "success")
	} else {
		b.badge("round", fmt.Sprintf("Plan, round %d", max(p.Round, 0)), "info")
	}
	b.text("goal", elide(p.Goal, 1500), "caption")
	b.text("tasks-line", fmt.Sprintf("%d tasks", max(p.TaskCount, 0)), "body")
	body := []string{"title", "round", "goal", "tasks-line"}
	if budget > 0 {
		b.add("est", "Progress", map[string]any{"value": bind("/mission/estimated"), "max": bind("/mission/budget"),
			"label": "Estimated of budget (USD)"})
	} else {
		b.text("est", fmt.Sprintf("Estimated cost: $%.2f (no budget set)", est), "body")
	}
	body = append(body, "est")
	b.add("prog", "Progress", map[string]any{"value": bind("/mission/done"), "max": bind("/mission/total"), "label": "Tasks done"})
	b.boundText("summary", "/mission/summary", "caption")
	body = append(body, "prog", "summary", "div")
	b.add("div", "Divider", nil)
	b.data["mission"] = map[string]any{"done": 0, "total": max(p.TaskCount, 0), "spent": 0,
		"budget": budget, "estimated": est, "summary": firstNonEmpty(map[bool]string{true: "Starting"}[running], "Waiting for approval")}
	ctx := func() map[string]any { return map[string]any{"sessionId": sid} }
	if running {
		b.button(btnStop, "Stop", "danger", "mission.stop", ctx())
		b.row("actions", btnStop)
	} else {
		b.button(btnApprove, "Approve and start", "primary", "mission.approve", ctx())
		b.button(btnEdit, "Edit plan", "default", "mission.edit", ctx())
		b.button(btnStop, "Stop", "danger", "mission.stop", ctx())
		b.row("actions", btnApprove, btnEdit, btnStop)
	}
	body = append(body, "actions")
	b.column("body", body...)
	b.add("root", "Card", map[string]any{"child": "body"})
	return b.create(MissionSurfaceID(p.SessionID))
}

// MissionProgressUpdate returns the envelope that moves a plan card's bars.
// taskStates maps task id to its state; the card shows the count per state
// (sorted by state name) in its summary line. Numbers that are negative or
// not finite are sent as 0.
func MissionProgressUpdate(sessionID int64, done, total int, spent, budget float64, taskStates map[int64]string) ([][]byte, error) {
	counts := map[string]int{}
	for _, s := range taskStates {
		counts[elide(strings.TrimSpace(s), 30)]++
	}
	names := make([]string, 0, len(counts))
	for s := range counts {
		names = append(names, s)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, s := range names {
		parts = append(parts, fmt.Sprintf("%s %d", firstNonEmpty(s, "unknown"), counts[s]))
	}
	summary := strings.Join(parts, " · ")
	if summary == "" {
		summary = "No task states yet"
	}
	raw, err := marshal(map[string]any{"version": a2ui.Version, "updateDataModel": map[string]any{
		"surfaceId": MissionSurfaceID(sessionID), "path": "/mission",
		"value": map[string]any{"done": max(done, 0), "total": max(total, 0), "spent": money(spent),
			"budget": money(budget), "estimated": 0, "summary": elide(summary, 300)}}})
	if err != nil {
		return nil, fmt.Errorf("surfaces: %w", err)
	}
	if _, err := a2ui.ParseMessage(raw, a2ui.DefaultLimits); err != nil {
		return nil, err
	}
	return [][]byte{raw}, nil
}

// MissionReport builds the final report card. With CloseProposed it carries
// a mission.close button (context {sessionId}). The report text is cut to the
// string cap with Truncated.
func MissionReport(p chatevents.MissionReportPayload) ([][]byte, error) {
	if p.SessionID <= 0 {
		return nil, fmt.Errorf("surfaces: mission session id %d is not positive", p.SessionID)
	}
	spent, budget := money(p.SpentUSD), money(p.BudgetUSD)
	b := newBuilder()
	b.text("title", elide(firstNonEmpty(p.Title, "Mission report"), 200), "body")
	b.badge("state", elide(firstNonEmpty(p.State, "unknown"), 40), reportTone(p.State))
	b.row("head", "title", "state")
	b.text("counts", fmt.Sprintf("%d tasks · %d done · %d verified · %d unfinished · %d artifacts",
		p.TaskCount, p.Done, p.Verified, len(p.Unfinished), p.Artifacts), "caption")
	body := []string{"head", "counts"}
	if budget > 0 {
		b.add("spend", "Progress", map[string]any{"value": bind("/mission/spent"), "max": bind("/mission/budget"), "label": "Spent of budget (USD)"})
	} else {
		b.text("spend", fmt.Sprintf("Spent: $%.2f (no budget set)", spent), "body")
	}
	b.data["mission"] = map[string]any{"spent": spent, "budget": budget}
	b.data["report"] = map[string]any{"text": elide(p.Report, a2ui.DefaultLimits.MaxStringBytes)}
	b.boundText("report", "/report/text", "body")
	body = append(body, "spend", "div", "report")
	b.add("div", "Divider", nil)
	if p.CloseProposed {
		b.button(btnClose, "Close mission", "primary", "mission.close", map[string]any{"sessionId": strconv.FormatInt(p.SessionID, 10)})
		body = append(body, btnClose)
	}
	b.column("body", body...)
	b.add("root", "Card", map[string]any{"child": "body"})
	return b.create(MissionReportSurfaceID(p.SessionID))
}

func reportTone(state string) string {
	switch strings.ToLower(state) {
	case "done", "closed", "completed":
		return "success"
	case "failed", "stopped", "cancelled":
		return "danger"
	}
	return "info"
}
