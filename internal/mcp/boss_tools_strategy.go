package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// The strategy layer's two session-callable typed operations, strategy.propose and strategy.report, as lead-facing tools.
// Like the other boss tools they are the lead whose token this process holds (an operator scope is not accepted) and they
// work only while a run is running for the profile. What they can do is narrow by construction: propose a direction (the
// novelty gate decides) and move a direction the caller owns to stalled or finished. They cannot request, approve or begin
// a finish, raise a limit, a budget or a credential, or touch an approval; the strategy.epoch operation (the judge intake)
// has no tool and no name on any model-facing list. The ops package refuses any other operation name.

// BossStrategyCaller runs one strategy operation (op is "strategy.propose" or "strategy.report", args the model's JSON
// object) for the verified lead session of the profile. The engine of a build with the mode sets it and maps the
// refusals of the enforcement onto coderhost's errors (ErrDoomsdayOff for no running run, ErrNotYours for a caller or
// charter that is not the live one, ErrLimit for a ceiling, ErrInvalid for bad arguments).
type BossStrategyCaller func(ctx context.Context, db *sql.DB, dbPath, profile string, session int64, op string, args json.RawMessage) (BossStrategyResult, error)

// BossStrategyResult is the typed answer of a strategy operation.
type BossStrategyResult struct {
	Op          string
	Accepted    bool
	DirectionID string
	Outcome     string
	MatchedID   string
	State       string
}

var (
	bossStrategyMu sync.RWMutex
	bossStrategy   BossStrategyCaller
)

// SetBossStrategyCaller sets (nil: removes) the caller. Only a build with the mode calls it.
func SetBossStrategyCaller(f BossStrategyCaller) {
	bossStrategyMu.Lock()
	bossStrategy = f
	bossStrategyMu.Unlock()
}

func bossStrategyCaller() BossStrategyCaller {
	bossStrategyMu.RLock()
	defer bossStrategyMu.RUnlock()
	return bossStrategy
}

// Operation names the tools pass to the caller; they are the ops package's, repeated here because this package does not
// import it, and a test pins that they agree.
const (
	bossStrategyProposeOp = "strategy.propose"
	bossStrategyReportOp  = "strategy.report"
)

func bossStrategyTools() []tool {
	write := map[string]bool{"readOnlyHint": false, "destructiveHint": false}
	return []tool{
		{
			name: "session_boss_strategy_propose", mutating: true, annotations: write, handler: toolSessionBossStrategyPropose,
			description: bossIntro + "Proposes a direction of work for the run. A proposal that repeats an open, pruned or earlier direction is refused with the direction it matches; a new one is stored for the strategy layer to rank. It changes no limit, budget or credential and cannot finish the run. Returns {accepted, directionId, outcome, matchedId}.",
			schema: objSchema(map[string]interface{}{
				"title":     strParam("A short title for the direction"),
				"rationale": strParam("Why this direction moves the run's criteria"),
				"criteria":  map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Ids of the criteria the direction serves"},
			}, "title", "rationale", "criteria"),
		},
		{
			name: "session_boss_strategy_report", mutating: true, annotations: write, handler: toolSessionBossStrategyReport,
			description: bossIntro + "Reports that a direction you own is stalled or finished. This is your claim about your own line of work: it verifies no criterion and cannot finish the run. Returns {accepted, directionId, state}.",
			schema: objSchema(map[string]interface{}{
				"direction": strParam("The direction id"),
				"state":     map[string]interface{}{"type": "string", "enum": []string{"stalled", "finished"}, "description": "stalled or finished"},
			}, "direction", "state"),
		},
	}
}

func toolSessionBossStrategyPropose(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	res, err := s.bossStrategyCall(ctx, bossStrategyProposeOp, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"accepted": res.Accepted, "directionId": res.DirectionID, "outcome": res.Outcome, "matchedId": res.MatchedID}, nil
}

func toolSessionBossStrategyReport(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	res, err := s.bossStrategyCall(ctx, bossStrategyReportOp, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"accepted": res.Accepted, "directionId": res.DirectionID, "state": res.State}, nil
}

// bossStrategyCall is the body of both tools: the verified lead, the build's caller, the raw arguments handed on as the
// model wrote them (the ops package decodes them strictly).
func (s *Server) bossStrategyCall(ctx context.Context, op string, args json.RawMessage) (BossStrategyResult, error) {
	st, sc, profile, err := s.leadScope(ctx)
	if err != nil {
		return BossStrategyResult{}, err
	}
	call := bossStrategyCaller()
	if call == nil {
		return BossStrategyResult{}, sessionToolErr(coderhost.ErrDoomsdayOff)
	}
	sid, err := st.LeadMission(sc, profile)
	if err != nil {
		return BossStrategyResult{}, sessionToolErr(err)
	}
	rt, err := s.runtime()
	if err != nil {
		return BossStrategyResult{}, err
	}
	res, err := call(ctx, rt.db.DB, expandHome(orDefaultDB(s.opts.DBPath)), profile, sid, op, args)
	if err != nil {
		return BossStrategyResult{}, sessionToolErr(err)
	}
	return res, nil
}
