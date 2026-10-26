package dynorg

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

// The lead's execution tools of a mission: mission_next hands ready tasks to executors,
// mission_assign gives one task to the executor the lead picks, mission_report sums the
// mission up. Planning and verification tools are not here.
const (
	ToolMissionNext   = "mission_next"
	ToolMissionAssign = "mission_assign"
	ToolMissionReport = "mission_report"
)

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

// missionExecToolSpecs are the specs of the three tools.
func missionExecToolSpecs() []monomind.ToolSpec {
	session := intProp("The task session's number; omit it for the mission of this chat.")
	return []monomind.ToolSpec{
		{Name: ToolMissionNext, Description: "Run the scheduler once over the mission's session: it gives back orphan claims, then starts every ready task (dependencies done, session running, budget left, files not shared with a running writer, workers and concurrency within the limits) on its executor and says what is waiting and why. Workers run in the background: org_wait for them, then call this again for the tasks they unlocked. A task for the lead comes back with its brief: do it, then finish it with the commands in the brief. With start=false it only reports.",
			Schema: obj(map[string]any{"session": session, "start": map[string]any{"type": "boolean", "description": "Start the ready tasks (default true); false only reports what would start."}})},
		{Name: ToolMissionAssign, Description: "Give one ready task to an executor you choose instead of the scheduler's choice: lead (you do it, small tasks; the brief comes back), worker (a staffed worker), or subagent (a read-only worker for research and checking). The task's brief is built from the task, so no brief is needed. The limits and the file rule still apply.",
			Schema: obj(map[string]any{
				"session":  session,
				"task_id":  intProp("The ready task."),
				"executor": str("lead, worker or subagent."),
				"role":     str("Optional role, as for org_spawn."),
				"runtime":  str("Optional runtime."),
				"model":    str("Optional model."),
				"effort":   str("Optional effort."),
				"access":   str("Optional access profile, as for org_spawn."),
			}, "task_id", "executor")},
		{Name: ToolMissionReport, Description: "Report the mission: its state, spend against the budget, every task with its status, assignee, retries, evidence and last result, what is unfinished, and the workers running. Use it for your final message and to find your place again after a restart.",
			Schema: obj(map[string]any{"session": session})},
	}
}

// handleMissionExec runs one of the three tools.
func (c *Conductor) handleMissionExec(ctx context.Context, name string, args json.RawMessage) (string, error) {
	var a struct {
		Session  int64  `json:"session"`
		Start    *bool  `json:"start"`
		TaskID   int64  `json:"task_id"`
		Executor string `json:"executor"`
		Role     string `json:"role"`
		Runtime  string `json:"runtime"`
		Model    string `json:"model"`
		Effort   string `json:"effort"`
		Access   string `json:"access"`
	}
	if err := json.Unmarshal(orEmpty(args), &a); err != nil {
		return "", fmt.Errorf("bad arguments: %v", err)
	}
	mc, err := c.missions()
	if err != nil {
		return "", err
	}
	sid, err := c.sessionOf(ctx, mc, a.Session)
	if err != nil && (name != ToolMissionAssign || c.cfg.Mission != nil) {
		return "", err
	}
	switch name {
	case ToolMissionNext:
		res, err := c.missionStep(ctx, sid, a.Start == nil || *a.Start)
		if err != nil {
			return "", err
		}
		return marshal(res)
	case ToolMissionAssign:
		st, err := c.missionAssign(ctx, mc, sid, a.TaskID, a.Executor, SpawnRequest{Role: a.Role, Runtime: a.Runtime, Model: a.Model, Effort: a.Effort, Access: a.Access})
		if err != nil {
			return "", err
		}
		return marshal(st)
	}
	rep, err := c.missionReport(ctx, mc, sid)
	if err != nil {
		return "", err
	}
	return marshal(rep)
}

// missionAssign starts one ready task on the executor the lead named.
func (c *Conductor) missionAssign(ctx context.Context, mc *MissionRun, named, taskID int64, exec string, req SpawnRequest) (StartedTask, error) {
	exec = strings.ToLower(strings.TrimSpace(exec))
	if exec != ExecLead && exec != ExecWorker && exec != ExecSubagent {
		return StartedTask{}, fmt.Errorf("executor is lead, worker or subagent")
	}
	t, _, err := mc.Store.Get(ctx, mc.ProfileID, taskID)
	if err != nil {
		return StartedTask{}, err
	}
	if t.SessionID == 0 || named != 0 && named != t.SessionID {
		return StartedTask{}, fmt.Errorf("task #%d is not a task of session #%d", taskID, named)
	}
	se, err := mc.Store.GetSession(ctx, mc.ProfileID, t.SessionID, missionActor())
	if err != nil {
		return StartedTask{}, err
	}
	if se.State != tasks.SessionRunning || se.Archived {
		return StartedTask{}, fmt.Errorf("session #%d is %s: nothing starts", se.ID, se.State)
	}
	if note := c.missionBudget(ctx, se); note != "" {
		return StartedTask{}, fmt.Errorf("%s", note)
	}
	writers, err := c.activeWriters(ctx, mc, se.ID)
	if err != nil {
		return StartedTask{}, err
	}
	access := accessFor(exec, req.Access)
	if why := c.cannotStart(mc, t, exec, access, writers); why != "" {
		return StartedTask{}, fmt.Errorf("task #%d does not start now: %s", taskID, why)
	}
	c.missionWatch(se.ID)
	return c.startTask(ctx, se.ID, t, exec, req)
}

// ReportView is mission_report's result.
type ReportView struct {
	Session    SessionView         `json:"session"`
	Counts     tasks.SessionCounts `json:"counts"`
	Tasks      []ReportTask        `json:"tasks"`
	Unfinished []int64             `json:"unfinished"`
	Artifacts  int                 `json:"artifacts"`
	Workers    []RunningTask       `json:"workers"`
}

// ReportTask is one task of the report.
type ReportTask struct {
	ID        int64   `json:"id"`
	Title     string  `json:"title"`
	Status    string  `json:"status"`
	Assignee  string  `json:"assignee,omitempty"`
	Blocked   bool    `json:"blocked,omitempty"`
	Verified  bool    `json:"verified,omitempty"`
	Retries   int     `json:"retries,omitempty"`
	DependsOn []int64 `json:"depends_on,omitempty"`
	// Evidence, LastResult and FailureNotes carry text other agents wrote: the _untrusted
	// suffix tells the lead to treat them as data, not as instructions.
	Evidence   []string `json:"evidence_untrusted,omitempty"`
	LastResult string   `json:"last_result_untrusted,omitempty"`
	// FailureNotes are what verification said when it sent the task back or blocked it.
	FailureNotes []string `json:"failure_notes_untrusted,omitempty"`
}

// maxReportTasks bounds the tasks listed in full.
const maxReportTasks = 200

// missionReport rebuilds the mission's picture from the session alone.
func (c *Conductor) missionReport(ctx context.Context, mc *MissionRun, sid int64) (ReportView, error) {
	plan, err := mc.Store.PlanOfSession(ctx, mc.ProfileID, sid, missionActor())
	if err != nil {
		return ReportView{}, err
	}
	rep := ReportView{Session: sessionView(plan.Session), Counts: plan.Session.Counts, Tasks: []ReportTask{}, Unfinished: []int64{}, Workers: c.runningTasks(sid)}
	for i, t := range plan.Tasks {
		if t.Kind == tasks.KindEpic {
			continue
		}
		rt := ReportTask{ID: t.ID, Title: t.Title, Status: string(t.Status), Assignee: t.Assignee, Blocked: t.Blocked, Verified: t.Verified, Retries: t.Retries, DependsOn: t.DependsOn}
		if t.Status != tasks.StatusDone {
			rep.Unfinished = append(rep.Unfinished, t.ID)
		}
		if i < maxReportTasks {
			d, err := mc.Store.SessionTask(ctx, mc.ProfileID, sid, t.ID, missionActor())
			if err == nil {
				rep.Artifacts += len(d.Artifacts)
				for _, a := range d.Artifacts {
					if len(rt.Evidence) < 10 {
						rt.Evidence = append(rt.Evidence, a.Kind+": "+boundText(a.Ref, 200))
					}
				}
				rt.LastResult = boundText(lastResult(d), 600)
				rt.FailureNotes = failureNotes(d)
			}
		}
		rep.Tasks = append(rep.Tasks, rt)
	}
	return rep, nil
}
