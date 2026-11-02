package chat

import (
	"context"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/dynorg"
)

func TestLeadSpawnSessionGoesThroughTheHostsGuards(t *testing.T) {
	m := newMasterTurn(t)
	missionOfMaster(t, m, 1, true)
	mc := missionReady(t, m)
	ctx := context.Background()
	args := dynorg.SessionSpawnArgs{Title: "Docs", Goal: "write them", Kind: "direction", Reason: "parallel"}
	// no tasks named: small, so the first call is advised against, in the conductor's terms
	if _, err := mc.SpawnSession(ctx, args); !errors.Is(err, dynorg.ErrSpawnAdvised) {
		t.Fatalf("first call: %v", err)
	}
	out, err := mc.SpawnSession(ctx, args)
	if err != nil || out.TaskSessionID == 0 || out.ConversationID == "" || out.Depth != 2 || out.Kind != "direction" {
		t.Fatalf("second call: %+v %v", out, err)
	}
	// the lead's access is the ceiling and a full-access child is never made
	args.Title = "Other"
	args.Access = "full"
	if _, err := mc.SpawnSession(ctx, args); err == nil {
		t.Fatal("a full-access session was spawned")
	}
	// a process that is not the lead's turn (no token) spawns nothing
	t.Setenv("MONOAGENT_SESSION_TOKEN", "")
	mc.getenv = func(string) string { return "" }
	if _, err := mc.SpawnSession(ctx, dynorg.SessionSpawnArgs{Title: "x", Kind: "task", Reason: "parallel"}); err == nil {
		t.Fatal("a process with no session token spawned")
	}
}
