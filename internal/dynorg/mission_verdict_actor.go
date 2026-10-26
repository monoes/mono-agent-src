package dynorg

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/tasks"
)

// verdictByUnexpectedActor is the marker in the comment and the session note written when a
// task was judged by somebody the Conductor did not assign.
const verdictByUnexpectedActor = "verdict_by_unexpected_actor"

// The binding of a verifier to its task is its environment; a shell-capable agent can
// unset it. The store refuses what it can; this is the check the store cannot make because
// the naming convention is dynorg's: the verdict on a mission task is the Conductor's
// verifier's (v<session>-t<task>) or the operator's ("you"). Anything else is not trusted:
// nothing is reverted, but the task does not count as settled until the operator judges it.

// untrustedVerdict names the actor of the task's latest verdict (since its last hand-back)
// when that is neither the verifier assigned to the task nor the operator, and nobody
// trusted spoke on the task after it.
func untrustedVerdict(d tasks.SessionTaskDetail, session int64) (actor string, ok bool) {
	want := missionVerifierName(session, d.Task.ID)
	verdict := -1
	for i, e := range d.Events {
		switch e.Kind {
		case "result", "question":
			verdict = -1
		case "verified", "verify_failed":
			verdict = i
		}
	}
	if verdict < 0 || d.Events[verdict].Actor == want || d.Events[verdict].Actor == operatorLabel {
		return "", false
	}
	for _, e := range d.Events[verdict+1:] {
		if e.Actor == operatorLabel {
			return "", false // the operator looked at it since
		}
	}
	return d.Events[verdict].Actor, true
}

// operatorLabel is how the operator appears in a task's history (tasks.Actor.Label of a human).
const operatorLabel = "you"

// flagUnexpectedVerdict writes down, once the verifier's run is over, that the task was judged
// by another actor: a comment on the task (counted as a run that ended without a verdict) and
// a note in the session's history.
func (c *Conductor) flagUnexpectedVerdict(ctx context.Context, mc *MissionRun, b *missionBind, who string) {
	text := fmt.Sprintf("verifier %s ended without a verdict (%s: the verdict was recorded as %q, not by %s, so it is not trusted; "+
		"the operator or the lead re-checks the task)", b.name, verdictByUnexpectedActor, boundText(who, 80), b.name)
	if _, err := mc.Store.SessionComment(ctx, mc.ProfileID, b.session, b.task.ID, text, b.actor); err != nil {
		_, _ = mc.Store.SessionComment(ctx, mc.ProfileID, b.session, b.task.ID, text, missionActor())
	}
	_ = mc.Store.AddSessionEvent(ctx, mc.ProfileID, b.session, tasks.EventNote,
		fmt.Sprintf("%s: task #%d was judged by %q instead of %s", verdictByUnexpectedActor, b.task.ID, boundText(who, 80), b.name), missionActor())
	c.notice("mission.verify", fmt.Sprintf("Task #%d was judged by %q, not by its verifier %s: the verdict is not trusted.", b.task.ID, boundText(who, 80), b.name), chatevents.SeverityWarning)
}

// verdictUntrusted reads the task and says whether its settled state rests on an untrusted verdict.
func (c *Conductor) verdictUntrusted(ctx context.Context, mc *MissionRun, sid int64, t tasks.Task) (string, bool) {
	d, err := mc.Store.SessionTask(ctx, mc.ProfileID, sid, t.ID, missionActor())
	if err != nil {
		return "", false
	}
	return untrustedVerdict(d, sid)
}
