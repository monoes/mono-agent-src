package coderhost

import (
	"context"

	"github.com/monoes/mono-agent/internal/tasks"
)

// missionOverCommitted says whether the mission session, or a budgeted ancestor of it, is committed past its budget. It is a
// read of the tasks store's commitment walk (nothing is written); a read that fails is "no": the run itself is refused anyway.
func (s *Store) missionOverCommitted(m pursuitMission) bool {
	oc, err := tasks.NewStore(s.db).OverCommitOf(context.Background(), m.Profile, m.ID)
	return err == nil && oc.Over
}
