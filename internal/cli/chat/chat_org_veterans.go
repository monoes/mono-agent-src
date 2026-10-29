package chat

import (
	"fmt"
	"os"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/dynorg"
)

// Workers kept per conversation (#230): each run of a worker is saved with
// the turn that ran it, and the next dynamic-org turn of the conversation
// loads the latest ones as idle veterans its lead can message.

// rememberWorker saves a worker after one of its runs.
func (j *turnJournal) rememberWorker(v dynorg.Veteran) {
	err := j.store.SaveOrgWorker(j.profileID, j.conversationID, ai.OrgWorker{
		AgentID: v.ID, ParentID: v.ParentID, TurnID: j.turnID, Role: v.Role, AgentType: v.AgentType, Category: v.Category,
		Access: v.Access, Skills: v.Skills, Runtime: v.Runtime, Model: v.Model, Effort: v.Effort, SessionID: v.Session,
		Cwd: v.Cwd, Report: v.Report, Outcome: v.Outcome, AllowSpawn: v.AllowSpawn,
		MissionSession: v.MissionSession, MissionTask: v.MissionTask,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: keeping worker %s for later turns: %v\n", v.ID, err)
	}
}

// veterans are the conversation's latest workers from earlier turns.
func (j *turnJournal) veterans() []dynorg.Veteran {
	ws, err := j.store.ListOrgWorkers(j.profileID, j.conversationID, dynorg.MaxVeterans)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: loading earlier workers: %v\n", err)
		return nil
	}
	out := make([]dynorg.Veteran, 0, len(ws))
	for _, w := range ws {
		if w.Outcome == ai.OrgWorkerRetired {
			continue // the operator retired it: the lead does not get it back (tasks.RetireWorker)
		}
		out = append(out, dynorg.Veteran{
			ID: w.AgentID, ParentID: w.ParentID, Role: w.Role, AgentType: w.AgentType, Category: w.Category,
			Access: w.Access, Skills: w.Skills, Runtime: w.Runtime, Model: w.Model, Effort: w.Effort, Session: w.SessionID,
			Cwd: w.Cwd, Report: w.Report, Outcome: w.Outcome, AllowSpawn: w.AllowSpawn,
			MissionSession: w.MissionSession, MissionTask: w.MissionTask,
		})
	}
	return out
}
