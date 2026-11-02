package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/storage"
)

// The default configuration (nothing enabled by the operator), a launcher that mints, a master whose
// max_children is above zero and a task the lead marked: nothing spawns, through the conductor's spawner.
func TestADefaultConfigSpawnsNothingWithAMintingLauncherAnAllowanceAndAMark(t *testing.T) {
	resetChildReadiness(t)
	m := newMasterTurn(t)
	sid, ids := missionOfMaster(t, m, 1, true)
	db, err := storage.NewDatabase(m.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	RegisterChildTokenMinter(newChildTokenMinter(db.DB))
	mc := newMissionChildren(db.DB, "default", m.master)
	if !mc.Ready() {
		t.Fatal("the launcher should mint")
	}
	cs := coderhost.NewStore(db.DB)
	if p, _ := cs.GetPolicy("default", sid); p.MaxChildren <= 0 {
		t.Fatalf("policy = %+v: the case is a master with max_children > 0", p)
	}
	ctx := context.Background()
	if err := mc.Mark(ctx, sid, ids[0], "", 0); err != nil {
		t.Fatalf("a mark is still stored: %v", err)
	}
	if f, _ := mc.Facts(ctx, sid, ids[0]); f.Row != "marked" || !strings.Contains(f.Blocked, "off (operator setting)") {
		t.Fatalf("facts = %+v: want the mark kept and the task blocked as off", f)
	}
	if err := mc.Spawn(ctx, sid, ids[0], "brief"); err == nil {
		t.Fatal("spawned with the operator's switch off")
	}
	if kids, _ := cs.ChildrenOfSession("default", sid); len(kids) != 1 || kids[0].State != coderhost.ChildMarked || kids[0].Attempts != 0 {
		t.Fatalf("rows = %+v", kids)
	}
	// the operator enables: the same mark spawns
	if err := cs.SetMissionChildrenEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := mc.Spawn(ctx, sid, ids[0], "brief"); err != nil {
		t.Fatalf("after enable: %v", err)
	}
}

// Neither a coder session token nor a mission binding can flip the switch, whatever the operator check says.
func TestNoAgentProcessFlipsTheMissionChildrenSwitch(t *testing.T) {
	m := newMasterTurn(t) // holds the session token
	m.cfg.RequireOperator = func(string) error { return nil }
	for _, verb := range []string{"enable", "disable"} {
		if out, code := runSessionsCLI(t, m.cfg, "mission-children", verb); code != 3 {
			t.Errorf("%s with a session token: exit %d %s, want the operator refusal (3)", verb, code, out)
		}
	}
	t.Setenv(coderhost.SessionTokenEnv, "")
	t.Setenv(agenttokens.EnvMissionToken, "some-token")
	for _, verb := range []string{"enable", "disable"} {
		if out, code := runSessionsCLI(t, m.cfg, "mission-children", verb); code != 3 {
			t.Errorf("%s with a mission binding: exit %d %s", verb, code, out)
		}
	}
	if on := switchOf(t, m.dbPath); on {
		t.Fatal("an agent process turned child sessions on")
	}
}

func switchOf(t *testing.T, dbPath string) bool {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	on, err := coderhost.NewStore(db.DB).MissionChildrenEnabled()
	if err != nil {
		t.Fatal(err)
	}
	return on
}

// The operator enables and disables, and `list` shows the state.
func TestOperatorEnablesAndDisablesMissionChildrenAndListShowsIt(t *testing.T) {
	m := newMasterTurn(t)
	m.cfg.RequireOperator = func(string) error { return nil }
	t.Setenv(coderhost.SessionTokenEnv, "")
	t.Setenv(agenttokens.EnvMissionToken, "")
	out, code := runSessionsCLI(t, m.cfg, "mission-children", "list")
	if code != 0 || !strings.Contains(out, `"missionChildrenEnabled":false`) {
		t.Fatalf("default list: %d %s", code, out)
	}
	if out, code := runSessionsCLI(t, m.cfg, "mission-children", "enable"); code != 0 || !switchOf(t, m.dbPath) {
		t.Fatalf("enable: %d %s", code, out)
	}
	if out, code := runSessionsCLI(t, m.cfg, "mission-children", "list"); code != 0 || !strings.Contains(out, `"missionChildrenEnabled":true`) {
		t.Fatalf("list after enable: %d %s", code, out)
	}
	if out, code := runSessionsCLI(t, m.cfg, "mission-children", "disable"); code != 0 || switchOf(t, m.dbPath) {
		t.Fatalf("disable: %d %s", code, out)
	}
}
