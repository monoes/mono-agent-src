package dynorg

import (
	"context"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/tasks"
)

// A verifier that ends without a verdict (it crashed, timed out, or never called
// `task verify`) leaves its task in Review with a comment and nothing else. The scheduler
// starts another one, up to MissionRun.MaxVerifyAttempts endings per task and session, then
// blocks the task for the operator. The count is the verifier's own no-verdict comments in
// the task's history, so a new Conductor on the same session counts the same.

// noVerdictMarker is in the comment verifierFinished writes when a run ended unjudged.
const noVerdictMarker = "ended without a verdict"

// noVerdictBlockNote is the note on a task blocked after its verifiers kept failing.
const noVerdictBlockNote = "verifier produced no verdict"

// defaultMaxVerifyAttempts is how many verifier runs per task may end without a verdict
// before the task is blocked.
const defaultMaxVerifyAttempts = 2

func (m *MissionRun) verifyAttempts() int {
	if m.MaxVerifyAttempts > 0 {
		return m.MaxVerifyAttempts
	}
	return defaultMaxVerifyAttempts
}

// verifierStartedMarker is in the comment missionVerify writes, as the verifier, before
// its run starts. A run that was started and left neither a verdict nor an ending comment
// died with its process (a crash, a kill, a Conductor dropped): it counts as one run that
// ended without a verdict once no verifier of the task is live.
const verifierStartedMarker = "verifier run started"

// verifierAbandonedMarker closes a started marker whose run never began (the spawn failed).
const verifierAbandonedMarker = "verifier run did not start"

// verifierEndings counts the runs of the task's verifier that ended without a verdict, and
// says whether the latest of them came after the work was last handed to review (an ending
// before a new hand-back belongs to an earlier round, which a verdict sent back). A started
// run with no verdict and no ending counts too when live is false: its process is gone.
func verifierEndings(d tasks.SessionTaskDetail, session int64, live bool) (n int, current bool) {
	name := missionVerifierName(session, d.Task.ID)
	lastResult, lastEnding, startedAt := -1, -1, -1
	open := false
	for i, e := range d.Events {
		switch {
		case e.Kind == "result" || e.Kind == "question":
			lastResult, open = i, false
		case e.Kind == "verified" || e.Kind == "verify_failed" || e.Kind == "blocked":
			open = false // judged, or the operator's to judge
		case e.Kind == "comment" && e.Actor == name && strings.Contains(e.Note, verifierStartedMarker):
			if open {
				n++ // the earlier run was never closed: its process died
				lastEnding = startedAt
			}
			open, startedAt = true, i
		case e.Kind == "comment" && e.Actor == name && strings.Contains(e.Note, verifierAbandonedMarker):
			open = false
		case e.Kind == "comment" && e.Actor == name && strings.Contains(e.Note, noVerdictMarker):
			open = false
			n++
			lastEnding = i
		}
	}
	if open && !live {
		n++
		lastEnding = startedAt
	}
	return n, lastEnding > lastResult
}

// verifyRetries looks at the tasks of a session waiting in Review with no verdict after a
// verifier ended and no verifier running. With start it starts another verifier for each
// (below the cap); at the cap it blocks the task for the operator. It returns what it did,
// for the lead's note.
func (c *Conductor) verifyRetries(ctx context.Context, mc *MissionRun, sid int64, start bool) []string {
	review, err := mc.Store.SessionTasks(ctx, mc.ProfileID, sid, tasks.Filter{Statuses: []tasks.Status{tasks.StatusReview}, Limit: 500}, missionActor())
	if err != nil {
		return nil
	}
	var notes []string
	for _, t := range review {
		if t.Kind == tasks.KindEpic || t.Blocked || t.Verified || c.verifierLive(t.ID) {
			continue
		}
		d, err := mc.Store.SessionTask(ctx, mc.ProfileID, sid, t.ID, missionActor())
		if err != nil || lastHandBackKind(d) == "question" {
			continue
		}
		n, current := verifierEndings(d, sid, false)
		if !current {
			continue
		}
		if n >= mc.verifyAttempts() {
			if _, err := mc.Store.BlockSessionTask(ctx, mc.ProfileID, sid, t.ID, fmt.Sprintf("%s: %d verifier runs ended without one; the operator judges it", noVerdictBlockNote, n), missionActor()); err == nil {
				notes = append(notes, fmt.Sprintf("task #%d is blocked: %s (%d runs)", t.ID, noVerdictBlockNote, n))
				c.notice("mission.verify", fmt.Sprintf("Task #%d is blocked for the operator: %s after %d runs.", t.ID, noVerdictBlockNote, n), chatevents.SeverityError)
			}
			continue
		}
		if !start {
			continue
		}
		if _, err := c.missionVerify(ctx, mc, sid, t.ID); err != nil {
			notes = append(notes, fmt.Sprintf("task #%d: a new verifier could not start: %v", t.ID, err))
			continue
		}
		notes = append(notes, fmt.Sprintf("task #%d: its verifier ended without a verdict, a new one started (attempt %d of %d)", t.ID, n+1, mc.verifyAttempts()))
	}
	return notes
}
