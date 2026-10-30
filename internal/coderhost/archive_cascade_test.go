package coderhost

import (
	"context"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// Archiving a mission with the operator's cascade (a research session below it still open) ends its
// task children's coder sessions on the host's next ticks, with no change to the host: the mission
// child pass already treats an archived mission as closed.
func TestArchivingAMissionSubtreeStopsItsChildCoderSessions(t *testing.T) {
	f := newHostFix(t)
	ctx := context.Background()
	sub, err := f.ts.CreateSession(ctx, "p", tasks.SessionInput{Title: "research", ParentSessionID: f.se.ID, Kind: tasks.SessionKindResearch}, opActor)
	if err != nil {
		t.Fatal(err)
	}
	f.h.MissionChildEvery = time.Nanosecond // every tick runs the pass
	running := f.task(t, 0, 0)
	if err := f.h.Tick(); err != nil { // its turn starts
		t.Fatal(err)
	}
	f.addTasks(t, 1)
	idle := f.task(t, 1, 0)
	if _, err := f.st.db.Exec(`UPDATE coder_session_commands SET status = 'done' WHERE conversation_id = ?`, idle.ConversationID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ts.ArchiveSession(ctx, "p", f.se.ID, opActor); err == nil {
		t.Fatal("a plain archive went through with an open session below it")
	}
	ids, err := f.ts.ArchiveSessionCascade(ctx, "p", f.se.ID, opActor)
	if err != nil || len(ids) != 2 || ids[0] != sub.ID || ids[1] != f.se.ID {
		t.Fatalf("cascade: %v %v", ids, err)
	}
	if err := f.h.Tick(); err != nil { // the pass: the idle child is stopped, the running one gets a stop
		t.Fatal(err)
	}
	if got := state(t, f.st, idle.ConversationID).State; got != StateStopped {
		t.Fatalf("the idle child coder session is %s after the next tick", got)
	}
	if err := f.h.Tick(); err != nil { // the queued stop runs
		t.Fatal(err)
	}
	f.h.Wait()
	f.h.missionChildPass()
	if r := f.row(t, 0); r.State != ChildEnded || r.EndReason != EndLeadStop {
		t.Fatalf("the running child's row = %+v, want ended/lead_stopped", r)
	}
	if got := state(t, f.st, running.ConversationID).State; got == StateRunning {
		t.Fatalf("the running child coder session is still %s", got)
	}
}
