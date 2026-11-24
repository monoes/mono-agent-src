package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// session_boss_set_model: the boss changes the model of a session below it that is not capable of its task. Bounded in
// coderhost.BossSetModel (the roster's working models of the session's own runtime, never the boss's own session, within the
// session's budget, a few times at most) and written to the session's decisions ledger. It changes no budget, cap or charter line.
func bossSetModelTool() tool {
	return tool{
		name: "session_boss_set_model", mutating: true, annotations: map[string]bool{"readOnlyHint": false, "destructiveHint": false}, handler: toolSessionBossSetModel,
		description: bossIntro + "Changes the model a session below you runs its next turns on, when you see its model is not capable of the task. Only to a model the roster lists as working for the " +
			"session's own runtime, never your own session, never above the session's budget (the budgets and the run's caps are untouched), at most " + fmt.Sprint(coderhost.MaxBossModelChanges) +
			" times per session; every change is recorded in that session's decisions ledger with your reason. Returns {session, runtime, from, to}.",
		schema: objSchema(map[string]interface{}{
			"session": intParam("The id of the session below you"),
			"model":   strParam("The model to run it on, as the roster names it"),
			"reason":  strParam("Why its current model is not capable of the task"),
		}, "session", "model", "reason"),
	}
}

func toolSessionBossSetModel(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		Session int64  `json:"session"`
		Model   string `json:"model"`
		Reason  string `json:"reason"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	st, sc, profile, err := s.bossScope(ctx)
	if err != nil {
		return nil, err
	}
	ch, err := st.BossSetModel(ctx, sc, profile, a.Session, a.Model, a.Reason)
	if err != nil {
		return nil, sessionToolErr(err)
	}
	return map[string]any{"session": ch.Session, "runtime": ch.Runtime, "from": ch.From, "to": ch.To}, nil
}
