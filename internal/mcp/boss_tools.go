package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sync"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// The boss's typed operations of an open-ended run (spec 6, note 13.4 4B), as lead-facing tools: the first three mirror
// session_reply_ask, the plan approval and the budget grant, but every one goes through coderhost's route, which
// enforces the live charter's per-ask limits and spends an approval first. The fourth, finish.begin, is in
// boss_tools_finish.go (no approval; the finish's own preconditions are its enforcement). The caller is the lead whose token this
// process holds (leadScope), exactly as for the other session tools; an operator scope cannot reach the route.
//
// The mode is off by default. A build without it never sets an installer, and a build with it installs the route per
// call, which reads the profile's run from the database: no running run, no installer, or an installer that fails is
// "not running" and nothing is answered, approved or granted. These tools cannot start, stop or edit a run.

const bossIntro = "Boss of an open-ended run: you lead the session a child's plan, ask or budget request is addressed to. Works only while a run is running for the profile; " +
	"otherwise refused (not_yours: not running). Inside the limits the run's charter lifted for this operation it takes effect; over a limit, or for a class not lifted, " +
	"it is parked as a person item and nothing changes (limit). It never starts, stops or edits a run. Refusals: not_yours, limit, invalid_input, no_session. "

// BossRouteInstaller puts the open run's route on a coder store: the engine of a build with the mode sets it. dbPath
// and profile are the process's database and the call's profile.
type BossRouteInstaller func(ctx context.Context, st *coderhost.Store, db *sql.DB, dbPath, profile string) error

var (
	bossMu      sync.RWMutex
	bossInstall BossRouteInstaller
)

// SetBossRouteInstaller sets (nil: removes) the installer. Only a build with the mode calls it.
func SetBossRouteInstaller(f BossRouteInstaller) {
	bossMu.Lock()
	bossInstall = f
	bossMu.Unlock()
}

func bossInstaller() BossRouteInstaller {
	bossMu.RLock()
	defer bossMu.RUnlock()
	return bossInstall
}

func bossTools() []tool {
	write := map[string]bool{"readOnlyHint": false, "destructiveHint": false}
	return append([]tool{
		{
			name: "session_boss_answer_ask", mutating: true, annotations: write, handler: toolSessionBossAnswerAsk,
			description: bossIntro + "Answers a critical ask addressed to you (ask_id), where otherwise only the person answers. The answer reaches the asker sealed, as quoted data. Returns {askId, status, commandId, turnId, session}.",
			schema: objSchema(map[string]interface{}{
				"ask_id": intParam("The critical ask id addressed to you"),
				"answer": strParam("Your answer, at most 4000 bytes"),
			}, "ask_id", "answer"),
		},
		{
			name: "session_boss_approve_plan", mutating: true, annotations: write, handler: toolSessionBossApprovePlan,
			description: bossIntro + "Approves the draft plan of a child session of yours, as read: rev and digest must be those you read, a plan that changed since is refused. Returns {session, state}.",
			schema: objSchema(map[string]interface{}{
				"session": intParam("The id of the child session whose draft plan you approve"),
				"rev":     intParam("The plan revision you read"),
				"digest":  strParam("The plan digest you read"),
			}, "session", "rev", "digest"),
		},
		{
			name: "session_boss_grant_budget", mutating: true, annotations: write, handler: toolSessionBossGrantBudget,
			description: bossIntro + "Grants an open budget request of a session below you, for amount_usd (above 0), inside the per-grant and run limits of the charter and the host's own limits. Returns {id, status, grantedUsd}.",
			schema: objSchema(map[string]interface{}{
				"request_id": intParam("The budget request id"),
				"amount_usd": map[string]interface{}{"type": "number", "description": "How much to grant, in USD (above 0)"},
			}, "request_id", "amount_usd"),
		},
		bossFinishTool(),
		bossFinishReadyTool(),
	}, bossStrategyTools()...)
}

// bossScope is the lead's store and scope with the open run's route installed on the store. No installer, or one
// that fails, is the mode not running.
func (s *Server) bossScope(ctx context.Context) (*coderhost.Store, coderhost.LeadScope, string, error) {
	st, sc, profile, err := s.leadScope(ctx)
	if err != nil {
		return nil, sc, "", err
	}
	install := bossInstaller()
	if install == nil {
		return nil, sc, "", sessionToolErr(coderhost.ErrDoomsdayOff)
	}
	rt, err := s.runtime()
	if err != nil {
		return nil, sc, "", err
	}
	if err := install(ctx, st, rt.db.DB, expandHome(orDefaultDB(s.opts.DBPath)), profile); err != nil {
		return nil, sc, "", sessionToolErr(fmt.Errorf("%w: %v", coderhost.ErrDoomsdayOff, err))
	}
	return st, sc, profile, nil
}

func orDefaultDB(p string) string {
	if p == "" {
		return "~/.monoagent/monoagent.db"
	}
	return p
}

func toolSessionBossAnswerAsk(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		AskID  int64  `json:"ask_id"`
		Answer string `json:"answer"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	st, sc, profile, err := s.bossScope(ctx)
	if err != nil {
		return nil, err
	}
	ask, cmd, err := st.BossAnswerAsk(ctx, sc, profile, a.AskID, coderhost.ClassAskCritical, a.Answer)
	if err != nil {
		return nil, sessionToolErr(err)
	}
	return map[string]any{"askId": ask.ID, "status": "answered", "commandId": cmd.ID, "turnId": cmd.TurnID, "session": cmd.ConversationID}, nil
}

func toolSessionBossApprovePlan(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		Session int64  `json:"session"`
		Rev     int64  `json:"rev"`
		Digest  string `json:"digest"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	if a.Session <= 0 || a.Rev <= 0 || a.Digest == "" {
		return nil, sessionToolErr(fmt.Errorf("%w: session, rev and digest of the plan you read are required", coderhost.ErrInvalid))
	}
	st, sc, profile, err := s.bossScope(ctx)
	if err != nil {
		return nil, err
	}
	sess, err := st.BossApprovePlan(ctx, sc, profile, a.Session, a.Rev, a.Digest)
	if err != nil {
		return nil, sessionToolErr(err)
	}
	return map[string]any{"session": sess.ID, "state": sess.State}, nil
}

func toolSessionBossGrantBudget(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		RequestID int64   `json:"request_id"`
		AmountUSD float64 `json:"amount_usd"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	micro := math.Round(a.AmountUSD * 1e6)
	if a.RequestID <= 0 || !(micro >= 1 && micro <= math.MaxInt32*1e6) {
		return nil, sessionToolErr(fmt.Errorf("%w: request_id and an amount_usd above 0 are required", coderhost.ErrInvalid))
	}
	st, sc, profile, err := s.bossScope(ctx)
	if err != nil {
		return nil, err
	}
	b, err := st.BossGrantBudget(ctx, sc, profile, a.RequestID, int64(micro))
	if err != nil {
		return nil, sessionToolErr(err)
	}
	return map[string]any{"id": b.ID, "status": b.Status, "grantedUsd": b.GrantedUSD}, nil
}
