package chat

import (
	"context"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/dynorg"
)

// SpawnSession is the lead's mission_session_spawn (dynorg.SessionSpawner): the same lead scope as the child
// tools, then the host's SpawnSession, which holds every guard. Nothing about the caller, the access or the
// budget is taken from anywhere else.
func (m *missionChildren) SpawnSession(ctx context.Context, a dynorg.SessionSpawnArgs) (dynorg.SessionSpawnResult, error) {
	store, scope, why := m.lead(ctx)
	if why != "" {
		return dynorg.SessionSpawnResult{}, errors.New(why)
	}
	out, err := store.SpawnSession(ctx, scope, m.profileID, coderhost.SessionSpawnRequest{Title: a.Title, Goal: a.Goal, Kind: a.Kind, Reason: a.Reason,
		TaskIDs: a.TaskIDs, BudgetUSD: a.BudgetUSD, Access: a.Access, Runtime: a.Runtime, Model: a.Model})
	switch {
	case errors.Is(err, coderhost.ErrSpawnAdvice):
		return dynorg.SessionSpawnResult{}, fmt.Errorf("%w; run it here, or repeat with a reason that applies", dynorg.ErrSpawnAdvised)
	case errors.Is(err, coderhost.ErrAtWorkingDepth):
		return dynorg.SessionSpawnResult{}, fmt.Errorf("%w: %w", dynorg.ErrSpawnDepth, err)
	case err != nil:
		return dynorg.SessionSpawnResult{}, err
	}
	return dynorg.SessionSpawnResult{ConversationID: out.ConversationID, TaskSessionID: out.TaskSessionID, Kind: out.Kind, Title: out.Title,
		Depth: out.Depth, Access: out.Access, BudgetUSD: out.BudgetUSD}, nil
}
