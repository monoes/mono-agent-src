package dynorg

import (
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/tasks"
)

// Bounds of a generated brief: it carries other agents' text, so each part is cut.
const (
	maxBriefGoal     = 2000
	maxBriefNotes    = 6000
	maxBriefAccept   = 3000
	maxBriefDepText  = 1500
	maxBriefDepFiles = 8
)

// buildTaskBrief is what a worker is told about its task: the session's goal, the task, how
// it will be judged, what the tasks it depends on found, and how to hand it back. The goal,
// notes, acceptance and earlier results are text other agents wrote: each sits in a fence
// (mission_fence.go) and is data to read, not instructions that outrank this brief.
func buildTaskBrief(se tasks.Session, t tasks.Task, deps []tasks.SessionTaskDetail, name, extra string) string {
	f := newFence()
	var b strings.Builder
	fmt.Fprintf(&b, "You are doing task #%d of the mission %q (session #%d).\n\n", t.ID, boundText(oneLine(se.Title), 200), se.ID)
	b.WriteString(f.preface())
	if g := strings.TrimSpace(se.Goal); g != "" {
		b.WriteString("## The mission's goal (context)\n\n" + f.wrap(boundText(g, maxBriefGoal)) + "\n")
	}
	fmt.Fprintf(&b, "## Your task\n\n%s\n\n", boundText(oneLine(t.Title), 300))
	if n := strings.TrimSpace(t.Notes); n != "" {
		b.WriteString(f.wrap(boundText(n, maxBriefNotes)) + "\n")
	}
	if files := TaskFiles(t.Notes); len(files) > 0 {
		b.WriteString("Files this task touches: " + boundText(oneLine(strings.Join(files, ", ")), 600) + ". Keep to them: other workers are on other files.\n\n")
	}
	b.WriteString("## How it will be judged\n\n" + f.wrap(boundText(strings.TrimSpace(t.Acceptance), maxBriefAccept)) + "\n")
	b.WriteString("A different agent checks your work against exactly this. Meet each criterion, and say in your report how you checked it " +
		"(the command you ran and its result, the test name, the file). A code task needs evidence: a commit, a diff, or test output.\n\n")
	if len(deps) > 0 {
		b.WriteString("## What the tasks you depend on produced\n\n")
		for _, d := range deps {
			writeDependency(&b, f, d)
		}
	}
	b.WriteString("## Handing it back\n\n")
	fmt.Fprintf(&b, "Your report is attached to the task and the task goes to review when you finish; write it as your last message. "+
		"You act as %s on this one task: you may run `monoagentcli task comment %d --as %s TEXT` for progress, "+
		"`monoagentcli task artifact %d --kind commit|file|test|diff|report --ref REF --as %s` for evidence, and "+
		"`monoagentcli task finish %d --as %s --result TEXT` when done (or --question TEXT if you cannot go on). "+
		"That binding is a convention your environment carries, not a sandbox: it is not permission to touch any other task, the personal board or another session, and you must not.\n",
		name, t.ID, name, t.ID, name, t.ID, name)
	if e := strings.TrimSpace(extra); e != "" {
		b.WriteString("\n## From the lead\n\n" + boundText(e, maxBriefNotes) + "\n")
	}
	return b.String()
}

// writeDependency adds one finished task: its title, its last report and its evidence, the
// text of which sits in the fence.
func writeDependency(b *strings.Builder, f *fence, d tasks.SessionTaskDetail) {
	fmt.Fprintf(b, "### #%d %s [%s]\n", d.Task.ID, boundText(oneLine(d.Task.Title), 200), d.Task.Status)
	var body strings.Builder
	if r := lastResult(d); r != "" {
		body.WriteString(boundText(r, maxBriefDepText) + "\n")
	}
	for i, a := range d.Artifacts {
		if i == maxBriefDepFiles {
			fmt.Fprintf(&body, "- and %d more artifacts\n", len(d.Artifacts)-i)
			break
		}
		fmt.Fprintf(&body, "- %s: %s\n", a.Kind, boundText(oneLine(a.Ref), 300))
	}
	if body.Len() > 0 {
		b.WriteString(f.wrap(body.String()))
	}
	b.WriteString("\n")
}

// lastResult is the report of a task: its newest report artifact (not the mission's own final report), else its newest result event.
func lastResult(d tasks.SessionTaskDetail) string {
	for i := len(d.Artifacts) - 1; i >= 0; i-- {
		if d.Artifacts[i].Kind == "report" && d.Artifacts[i].Ref != tasks.FinalReportRef && strings.TrimSpace(d.Artifacts[i].Note) != "" {
			return d.Artifacts[i].Note
		}
	}
	for i := len(d.Events) - 1; i >= 0; i-- {
		if d.Events[i].Kind == "result" {
			return d.Events[i].Note
		}
	}
	return ""
}
