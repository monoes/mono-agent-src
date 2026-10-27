package dynorg

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

// Integrating and closing a mission (plan 7.7, 7.8). mission_finish is the lead's one call
// for it; it is a separate tool (not a flag of mission_report, which only reads). Each call
// does the next step and says what is left:
//
//  1. some task is not done or verified, or is blocked: nothing is created, the unfinished
//     tasks come back with the verifiers' failure notes;
//  2. every other task is done or verified and the plan has no integration task: one is
//     created (kind check, titled "Integration check", acceptance = the final checks the
//     planner wrote in its first check task, else a rubric to run the project's own build
//     and tests). The planner should write that task itself (mission_prompt.go);
//  3. the integration task is still open: it is the unfinished list (run it, verify it);
//  4. everything is done or verified: writer branches not merged yet are listed (the lead
//     org_merges them, one at a time, then calls again); with none left the final report is
//     attached to the integration task as an artifact and mission.report is emitted (once):
//     the close is PROPOSED, the session stays running and the operator closes it.
//
// The conductor never closes a session: only the operator does (task session close, or the
// Close button). Its proposal rests on conditions that verifiers other than the executors
// established.

// ToolMissionFinish integrates and closes a mission.
const ToolMissionFinish = "mission_finish"

// integrationTitle is the title of the task that runs the final checks.
const integrationTitle = "Integration check"

// defaultFinalChecks is the acceptance of a created integration task when the plan has no
// check task to take it from.
const defaultFinalChecks = "rubric: the project builds and all its tests and linters pass: run its own build, test and lint commands on the merged tree and read the output; report any failure"

// maxFinalReport bounds the report text.
const maxFinalReport = 30000

func missionRunToolSpecs() []monomind.ToolSpec {
	session := intProp("The task session's number; omit it for the mission of this chat.")
	return []monomind.ToolSpec{
		{Name: ToolMissionVerify, Description: "Start the verifier of a task that a worker handed to review: another agent, on another model when one is ready, read only, bound to that one task. It checks the task's acceptance and records its own verdict (pass: the task is verified; fail: it goes back to Ready with the note, up to the retry cap, then it is blocked for the operator). org_wait for it. You never record a verdict yourself.",
			Schema: obj(map[string]any{"session": session, "task_id": intProp("A task in Review (not blocked, not verified).")}, "task_id")},
		{Name: ToolMissionFinish, Description: "Integrate the mission and propose its close. Call it when mission_next has nothing left to start. It lists any task that is not done or verified (blocked ones with the verifiers' failure notes) and stops there. When every task is, it creates the integration check task if the plan has none, then waits for it; then it lists writer branches you have not org_merge'd (merge them one at a time and call again); then it writes the final report (an artifact, and the mission.report card; again if work is reopened and the checks settle anew) and proposes the close: the session stays running and ONLY the operator closes it (task session close, or the Close button). When it says ready_to_close, do not call it again.",
			Schema: obj(map[string]any{"session": session})},
	}
}

// MissionRunToolSpecs are the lead's execution tools of a mission: mission_next,
// mission_assign, mission_report, mission_verify and mission_finish. They are offered only
// to a lead whose mission the user confirmed and whose conversation already has a session
// (the plan is written, so there is something to run).
func MissionRunToolSpecs(ctx context.Context, m *MissionConfig) []monomind.ToolSpec {
	if !m.Planning() {
		return nil
	}
	if se, err := m.latest(ctx); err != nil || se == nil {
		return nil
	}
	return append(missionExecToolSpecs(), missionRunToolSpecs()...)
}

// Unfinished is a task that stops the mission from closing.
type Unfinished struct {
	ID           int64    `json:"id"`
	Title        string   `json:"title"`
	Status       string   `json:"status"`
	Blocked      bool     `json:"blocked,omitempty"`
	Retries      int      `json:"retries,omitempty"`
	FailureNotes []string `json:"failure_notes_untrusted,omitempty"`
}

// FinishView is mission_finish's result. Finished is true only once the operator has closed
// the session; a mission that is ready has ReadyToClose and CloseProposed instead.
type FinishView struct {
	Session         SessionView  `json:"session"`
	Finished        bool         `json:"finished"`
	ReadyToClose    bool         `json:"ready_to_close,omitempty"`
	CloseProposed   bool         `json:"close_proposed,omitempty"`
	Unfinished      []Unfinished `json:"unfinished,omitempty"`
	IntegrationTask int64        `json:"integration_task,omitempty"`
	CreatedTask     bool         `json:"created_integration_task,omitempty"`
	MergesRemaining []string     `json:"merges_remaining,omitempty"`
	Report          string       `json:"report,omitempty"`
	Next            string       `json:"next"`
}

// settled says whether a task needs nothing more: done, or verified and waiting in Review
// for the operator to close (and not blocked).
func settled(t tasks.Task) bool {
	return t.Status == tasks.StatusDone || (t.Status == tasks.StatusReview && t.Verified && !t.Blocked)
}

func isIntegration(t tasks.Task) bool {
	return t.Kind == tasks.KindCheck && strings.HasPrefix(strings.ToLower(strings.TrimSpace(t.Title)), strings.ToLower("Integration"))
}

func (c *Conductor) handleMissionFinish(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Session int64 `json:"session"`
	}
	if err := json.Unmarshal(orEmpty(args), &a); err != nil {
		return "", fmt.Errorf("bad arguments: %v", err)
	}
	mc, err := c.missions()
	if err != nil {
		return "", err
	}
	if m := c.cfg.Mission; m != nil && !m.Planning() {
		return "", fmt.Errorf("missions are not on for this conversation: the user has not confirmed one")
	}
	sid, err := c.sessionOf(ctx, mc, a.Session)
	if err != nil {
		return "", err
	}
	v, err := c.missionFinishStep(ctx, mc, sid)
	if err != nil {
		return "", err
	}
	return marshal(v)
}

// missionFinishStep does the next step of closing the mission.
func (c *Conductor) missionFinishStep(ctx context.Context, mc *MissionRun, sid int64) (FinishView, error) {
	se, err := mc.Store.GetSession(ctx, mc.ProfileID, sid, missionActor())
	if err != nil {
		return FinishView{}, err
	}
	v := FinishView{Session: sessionView(se), Unfinished: []Unfinished{}}
	if se.State == tasks.SessionDone {
		v.Finished, v.Next = true, "The mission is already closed as done: tell the user how it went (mission_report)."
		return v, nil
	}
	if se.State != tasks.SessionRunning || se.Archived {
		return v, fmt.Errorf("session #%d is %s: only a running mission is finished", sid, se.State)
	}
	retried := c.verifyRetries(ctx, mc, sid, true)
	plan, err := mc.Store.PlanOfSession(ctx, mc.ProfileID, sid, missionActor())
	if err != nil {
		return v, err
	}
	var integration *tasks.Task
	var firstCheck string
	for i, t := range plan.Tasks {
		if t.Kind == tasks.KindCheck && firstCheck == "" {
			firstCheck = t.Acceptance
		}
		if integration == nil && isIntegration(t) {
			integration = &plan.Tasks[i]
		}
	}
	var others []tasks.Task
	for _, t := range plan.Tasks {
		if t.Kind != tasks.KindEpic && (integration == nil || t.ID != integration.ID) {
			others = append(others, t)
		}
	}
	for _, t := range others {
		if !settled(t) {
			v.Unfinished = append(v.Unfinished, c.unfinished(ctx, mc, sid, t))
		} else if who, bad := c.verdictUntrusted(ctx, mc, sid, t); bad {
			u := c.unfinished(ctx, mc, sid, t)
			u.FailureNotes = append(u.FailureNotes, untrustedNote(who))
			v.Unfinished = append(v.Unfinished, u)
		}
	}
	if len(v.Unfinished) > 0 {
		v.Next = "Not every task is done or verified. Run mission_next for what is ready, mission_verify for what waits in review, and look at the blocked tasks' failure notes: a blocked task needs the operator (or mission_replan to add the missing work). Then call mission_finish again."
		if len(retried) > 0 {
			v.Next = strings.Join(retried, "; ") + ". " + v.Next
		}
		return v, nil
	}
	if integration == nil {
		made, err := c.createIntegration(ctx, mc, sid, others, firstCheck)
		if err != nil {
			return v, err
		}
		v.IntegrationTask, v.CreatedTask = made.ID, true
		v.MergesRemaining = c.mergesRemaining(ctx, mc, sid, plan.Tasks)
		v.Next = "Every task is done or verified. The integration check task #" + fmt.Sprint(made.ID) + " was added: " + mergeAdvice(v.MergesRemaining) + "run mission_next to start it, verify it, then call mission_finish again."
		return v, nil
	}
	v.IntegrationTask = integration.ID
	if who, bad := c.verdictUntrusted(ctx, mc, sid, *integration); bad && settled(*integration) {
		u := c.unfinished(ctx, mc, sid, *integration)
		u.FailureNotes = append(u.FailureNotes, untrustedNote(who))
		v.Unfinished = append(v.Unfinished, u)
		v.Next = "The integration check was judged by " + who + ", not by its verifier: the operator re-checks it before the mission closes."
		return v, nil
	}
	if !settled(*integration) {
		v.Unfinished = append(v.Unfinished, c.unfinished(ctx, mc, sid, *integration))
		v.MergesRemaining = c.mergesRemaining(ctx, mc, sid, plan.Tasks)
		v.Next = "Only the integration check is left: " + mergeAdvice(v.MergesRemaining) + "run it with mission_next (or mission_assign), verify it with mission_verify, then call mission_finish again."
		return v, nil
	}
	if v.MergesRemaining = c.mergesRemaining(ctx, mc, sid, plan.Tasks); len(v.MergesRemaining) > 0 {
		v.Next = "Every task is done or verified, but writer branches are not merged yet: " + mergeAdvice(v.MergesRemaining) + "then call mission_finish again (re-run the checks if a merge changed the tree)."
		return v, nil
	}
	if why := c.integrationStale(ctx, mc, sid, plan, *integration); why != "" {
		if err := requeueIntegration(ctx, mc.Store, mc.ProfileID, *integration); err != nil {
			return v, fmt.Errorf("%s, and the integration check could not be requeued: %w", why, err)
		}
		_, _ = mc.Store.SessionComment(ctx, mc.ProfileID, sid, integration.ID, "Run and verify the checks again: "+why+".", missionActor())
		v.Unfinished = append(v.Unfinished, c.unfinished(ctx, mc, sid, *integration))
		v.Next = "The integration check ran before the tree last changed (" + why + "), so it was sent back to Ready: run it with mission_next, verify it with mission_verify, then call mission_finish again."
		return v, nil
	}
	return c.proposeMissionClose(ctx, mc, se, plan, v)
}

func untrustedNote(who string) string {
	return verdictByUnexpectedActor + ": judged by " + boundText(who, 80) + ", not by its verifier; the operator re-checks it"
}

func mergeAdvice(merges []string) string {
	if len(merges) == 0 {
		return ""
	}
	return "org_merge these writers one at a time first (" + strings.Join(merges, "; ") + "); "
}

func (c *Conductor) unfinished(ctx context.Context, mc *MissionRun, sid int64, t tasks.Task) Unfinished {
	u := Unfinished{ID: t.ID, Title: t.Title, Status: string(t.Status), Blocked: t.Blocked, Retries: t.Retries}
	if d, err := mc.Store.SessionTask(ctx, mc.ProfileID, sid, t.ID, missionActor()); err == nil {
		u.FailureNotes = failureNotes(d)
	}
	return u
}

// createIntegration adds the integration check task: it waits for every task that is done
// (a task that is only verified is settled and needs no edge), it is the lead's own to run,
// and its notes name the merges still to make.
func (c *Conductor) createIntegration(ctx context.Context, mc *MissionRun, sid int64, others []tasks.Task, firstCheck string) (tasks.Task, error) {
	acceptance := strings.TrimSpace(firstCheck)
	if acceptance == "" {
		acceptance = defaultFinalChecks
	}
	var deps []int64
	for _, t := range others {
		if t.Status == tasks.StatusDone {
			deps = append(deps, t.ID)
		}
	}
	notes := "The final gate of the mission: the checks below run on the merged tree, after every writer's branch is merged into the chat folder (org_merge, one at a time). Report each command and its result."
	return mc.Store.AddSessionTask(ctx, mc.ProfileID, sid, tasks.SessionTaskInput{
		Title: integrationTitle, Notes: notes, Acceptance: acceptance, Kind: tasks.KindCheck, DependsOn: deps, SuggestedExecutor: ExecLead,
	}, missionActor())
}

// proposeMissionClose writes the final report and proposes the close to the operator: the
// session stays running. A repeat call writes nothing more and says it is still waiting.
func (c *Conductor) proposeMissionClose(ctx context.Context, mc *MissionRun, se tasks.Session, plan tasks.SessionPlan, v FinishView) (FinishView, error) {
	rep, payload := c.finalReport(ctx, mc, se, plan)
	payload.State, payload.CloseProposed = string(se.State), true
	proposed, err := c.proposeClose(ctx, mc, se.ID, rep, func() {
		if c.cfg.Emit != nil {
			c.cfg.Emit.Emit(chatevents.EventMissionReport, payload)
		}
	})
	if err != nil {
		return v, err
	}
	v.ReadyToClose, v.CloseProposed = true, true
	if !proposed {
		v.Next = "The mission is ready and the operator has not closed it yet; it is still waiting for the operator. Do not call mission_finish again: give the user a short summary and stop."
		return v, nil
	}
	v.Report = boundText(rep, 4000)
	v.Next = "The mission is ready; the operator closes it (task session close " + fmt.Sprint(se.ID) + " or the Close button); do not call mission_finish again. The final report is on the conversation: give the user a short summary of what was built and how it was checked."
	return v, nil
}

// finalReport is the report text and its event payload.
func (c *Conductor) finalReport(ctx context.Context, mc *MissionRun, se tasks.Session, plan tasks.SessionPlan) (string, chatevents.MissionReportPayload) {
	p := chatevents.MissionReportPayload{SessionID: se.ID, Title: se.Title, Unfinished: []int64{}, SpentUSD: se.SpentUSD, BudgetUSD: se.BudgetUSD}
	var b strings.Builder
	fmt.Fprintf(&b, "# Mission report: %s\n\n", mdCode(se.Title, 200))
	if g := strings.TrimSpace(se.Goal); g != "" {
		b.WriteString("Goal: " + mdCode(g, 1500) + "\n\n")
	}
	var lines []string
	for _, t := range plan.Tasks {
		if t.Kind == tasks.KindEpic {
			continue
		}
		p.TaskCount++
		state := "done"
		switch {
		case t.Status == tasks.StatusDone:
			p.Done++
		case settled(t):
			p.Verified++
			state = "verified"
		default:
			p.Unfinished = append(p.Unfinished, t.ID)
			state = string(t.Status)
		}
		line := fmt.Sprintf("- #%d %s [%s]", t.ID, mdCode(t.Title, 150), state)
		if d, err := mc.Store.SessionTask(ctx, mc.ProfileID, se.ID, t.ID, missionActor()); err == nil {
			for _, a := range d.Artifacts {
				if a.Kind != "report" {
					p.Artifacts++
					line += fmt.Sprintf("\n  - %s: %s", a.Kind, mdCode(a.Ref, 160))
				}
			}
			if r := strings.TrimSpace(lastResult(d)); r != "" {
				// worker-written: in a code block, so it cannot add headings, links or fences
				line += "\n  - result:\n\n" + indentLines(codeBlock(boundText(r, 300)), "    ")
			}
		}
		lines = append(lines, line)
	}
	fmt.Fprintf(&b, "Tasks: %d (%d done, %d verified). Evidence artifacts: %d. Spent $%.2f", p.TaskCount, p.Done, p.Verified, p.Artifacts, se.SpentUSD)
	if se.BudgetUSD > 0 {
		fmt.Fprintf(&b, " of $%.2f", se.BudgetUSD)
	}
	b.WriteString(".\n\n## Tasks\n\n" + strings.Join(lines, "\n") + "\n")
	p.Report = boundText(b.String(), maxFinalReport)
	return p.Report, p
}

// indentLines prefixes every line of s.
func indentLines(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}

// mdCode is text as one inline code span: the report is rendered as markdown for the operator
// who reads it before closing the mission, and titles, goals and references come from agents,
// so no link, image, tag, heading or emphasis in them may take effect.
func mdCode(text string, max int) string {
	t := strings.ReplaceAll(boundText(oneLine(text), max), "`", "'")
	if strings.TrimSpace(t) == "" {
		return "`(none)`"
	}
	return "`" + t + "`"
}
