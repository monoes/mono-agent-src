package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// The boss's fourth typed operation, finish.begin (note 13.6 A3): the boss starts the finish of the run. Like the other boss
// tools it is the lead whose token this process holds (an operator scope is not accepted), it works only while a run is
// running for the profile, and it cannot start, stop or edit a run otherwise. What it begins is the Stop path with
// cause=finish: the run stops, and the operator's `finish` verbs freeze, verify and commit. It writes no result, no evidence
// and no decision.

// BossFinishResult is what finish.begin reports: accepted, or refused with the reason of the first precondition not met.
type BossFinishResult struct {
	Generation int64
	Accepted   bool
	Refused    string
	State      string
	Lines      []string
}

// BossFinishBeginner runs finish.begin for the verified lead session of the profile: the engine of a build with the mode sets
// it, and maps the refusals of the enforcement function onto coderhost's (ErrDoomsdayOff for no running run, ErrNotYours for
// a caller or charter that is not the live one); dbPath is the process's database.
type BossFinishBeginner func(ctx context.Context, db *sql.DB, dbPath, profile string, session int64) (BossFinishResult, error)

var (
	bossFinishMu sync.RWMutex
	bossFinish   BossFinishBeginner
)

// SetBossFinishBeginner sets (nil: removes) the beginner. Only a build with the mode calls it.
func SetBossFinishBeginner(f BossFinishBeginner) {
	bossFinishMu.Lock()
	bossFinish = f
	bossFinishMu.Unlock()
}

func bossFinisher() BossFinishBeginner {
	bossFinishMu.RLock()
	defer bossFinishMu.RUnlock()
	return bossFinish
}

func bossFinishTool() tool {
	return tool{
		name: "session_boss_finish_begin", mutating: true,
		annotations: map[string]bool{"readOnlyHint": false, "destructiveHint": false}, handler: toolSessionBossFinishBegin,
		description: bossIntro + "Begins the finish of the run: the run stops (cause finish) and the person then verifies it with `doomsday finish`. " +
			"Refused, with the reason and nothing changed, while the charter cannot finish (no criteria, or no distinct pinned verifier) or a person item is open. " +
			"Returns {generation, accepted, refused, state, lines}.",
		schema: objSchema(nil),
	}
}

func toolSessionBossFinishBegin(ctx context.Context, s *Server, _ json.RawMessage) (interface{}, error) {
	st, sc, profile, err := s.leadScope(ctx)
	if err != nil {
		return nil, err
	}
	begin := bossFinisher()
	if begin == nil {
		return nil, sessionToolErr(coderhost.ErrDoomsdayOff)
	}
	sid, err := st.LeadMission(sc, profile)
	if err != nil {
		return nil, sessionToolErr(err)
	}
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	// The finish ends the whole run, so it is the top boss's: a lead below another lead is not it.
	var parent int64
	if err := rt.db.DB.QueryRowContext(ctx, `SELECT COALESCE(parent_session_id, 0) FROM task_sessions WHERE profile_id = ? AND id = ?`, profile, sid).Scan(&parent); err != nil {
		return nil, sessionToolErr(fmt.Errorf("%w: the lead's session cannot be read", coderhost.ErrNotYours))
	}
	if parent != 0 {
		return nil, sessionToolErr(fmt.Errorf("%w: only the top lead of the run begins its finish", coderhost.ErrNotYours))
	}
	res, err := begin(ctx, rt.db.DB, expandHome(orDefaultDB(s.opts.DBPath)), profile, sid)
	if err != nil {
		return nil, sessionToolErr(err)
	}
	return map[string]any{"generation": res.Generation, "accepted": res.Accepted, "refused": res.Refused, "state": res.State, "lines": res.Lines}, nil
}
