package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// The boss's read-only view of the strategy layer's finish-ready marker. The strategy layer never begins a finish and never
// acts as the boss: it only reads the run's tables and records that the run LOOKS ready (a fresh judgment in which every
// criterion is plausibly met, nothing newer that says otherwise). This tool shows that reading to the verified top lead of
// the run, who alone decides whether to call session_boss_finish_begin, which checks the boss itself. The tool writes
// nothing and has no authority.

// BossFinishReadyResult is the reading. Reason names why it is not ready when it is not.
type BossFinishReadyResult struct {
	Ready              bool
	Reason             string
	Epoch              int64
	JudgmentAgeSeconds int64
	MarkerRecorded     bool
	GaveUp             bool
}

// BossFinishReadyReader reads the marker for the verified top lead `session` of the profile. The engine of a build with the
// mode sets it and maps the refusals onto coderhost's (ErrDoomsdayOff for no running run, ErrNotYours for a session that is
// not the run's boss).
type BossFinishReadyReader func(ctx context.Context, db *sql.DB, dbPath, profile string, session int64) (BossFinishReadyResult, error)

var (
	bossReadyMu sync.RWMutex
	bossReady   BossFinishReadyReader
)

// SetBossFinishReadyReader sets (nil: removes) the reader. Only a build with the mode calls it.
func SetBossFinishReadyReader(f BossFinishReadyReader) {
	bossReadyMu.Lock()
	bossReady = f
	bossReadyMu.Unlock()
}

func bossFinishReadyReader() BossFinishReadyReader {
	bossReadyMu.RLock()
	defer bossReadyMu.RUnlock()
	return bossReady
}

func bossFinishReadyTool() tool {
	return tool{
		name: "session_boss_finish_ready", mutating: true,
		annotations: map[string]bool{"readOnlyHint": true, "destructiveHint": false}, handler: toolSessionBossFinishReady,
		description: bossIntro + "Shows whether the run reads as ready for a finish: the latest judging round found every criterion plausibly met, recently, with nothing newer saying otherwise. " +
			"This is a reading of the judge's opinion, not a verification and not a request; nothing is begun. Only you decide whether to call session_boss_finish_begin. " +
			"Returns {ready, reason, epoch, judgmentAgeSeconds, markerRecorded, gaveUp}.",
		schema: objSchema(nil),
	}
}

func toolSessionBossFinishReady(ctx context.Context, s *Server, _ json.RawMessage) (interface{}, error) {
	st, sc, profile, err := s.leadScope(ctx)
	if err != nil {
		return nil, err
	}
	read := bossFinishReadyReader()
	if read == nil {
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
	var parent int64
	if err := rt.db.DB.QueryRowContext(ctx, `SELECT COALESCE(parent_session_id, 0) FROM task_sessions WHERE profile_id = ? AND id = ?`, profile, sid).Scan(&parent); err != nil {
		return nil, sessionToolErr(fmt.Errorf("%w: the lead's session cannot be read", coderhost.ErrNotYours))
	}
	if parent != 0 {
		return nil, sessionToolErr(fmt.Errorf("%w: only the top lead of the run reads its finish-ready state", coderhost.ErrNotYours))
	}
	res, err := read(ctx, rt.db.DB, expandHome(orDefaultDB(s.opts.DBPath)), profile, sid)
	if err != nil {
		return nil, sessionToolErr(err)
	}
	return map[string]any{"ready": res.Ready, "reason": res.Reason, "epoch": res.Epoch, "judgmentAgeSeconds": res.JudgmentAgeSeconds,
		"markerRecorded": res.MarkerRecorded, "gaveUp": res.GaveUp}, nil
}
