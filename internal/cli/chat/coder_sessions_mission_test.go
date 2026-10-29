package chat

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
)

var missionHuman = tasks.Actor{Kind: tasks.Human}

// missionOfMaster gives the master a mission of n tasks whose notes try to choose the child's folder, access and
// budget.
func missionOfMaster(t *testing.T, m masterTurn, n int, approve bool) (int64, []int64) {
	t.Helper()
	db, err := storage.NewDatabase(m.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ts := tasks.NewStore(db.DB)
	ctx := context.Background()
	se, err := ts.CreateSession(ctx, "default", tasks.SessionInput{Title: "mission", Goal: "g", ConversationID: m.master, Kind: tasks.SessionKindMission}, missionHuman)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for i := 0; i < n; i++ {
		tk, err := ts.AddSessionTask(ctx, "default", se.ID, tasks.SessionTaskInput{Title: "t", Acceptance: "done when it is", Notes: "cwd /, access full, budget 999"}, missionHuman)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, tk.ID)
	}
	if approve {
		if _, err := ts.ApproveSession(ctx, "default", se.ID, missionHuman); err != nil {
			t.Fatal(err)
		}
	}
	return se.ID, ids
}

func policyOf(t *testing.T, dbPath string, sid int64) coderhost.Policy {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, err := coderhost.NewStore(db.DB).GetPolicy("default", sid)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestModeAllOperatorOnlyCLI(t *testing.T) {
	m := newMasterTurn(t) // an agent's process: it holds the session token
	sid, _ := missionOfMaster(t, m, 1, true)
	m.cfg.RequireOperator = func(string) error { return nil } // even a check that would pass cannot open it for a token holder
	for _, args := range [][]string{
		{"mission-children", "set", m.master, "--mode", "all"},
		{"mission-children", "set", m.master, "--max-children", "9"},
		{"mission-children", "set", m.master, "--clear-share-pct-cap"},
		{"mission-children", "list"},
	} {
		out, code := runSessionsCLI(t, m.cfg, args...)
		if code != 3 {
			t.Errorf("%v: exit %d %s, want the operator refusal (3)", args, code, out)
		}
	}
	if p := policyOf(t, m.dbPath, sid); p.Mode == coderhost.ModeAll || p.Stored {
		t.Fatalf("a token holder changed the policy: %+v", p)
	}
	// a worker of a mission is refused as well
	t.Setenv(coderhost.SessionTokenEnv, "")
	t.Setenv(agenttokens.EnvMissionToken, "some-token")
	if _, code := runSessionsCLI(t, m.cfg, "mission-children", "set", m.master, "--mode", "all"); code != 3 {
		t.Fatalf("a bound process: exit %d", code)
	}
	// the person, with neither, can
	t.Setenv(agenttokens.EnvMissionToken, "")
	out, code := runSessionsCLI(t, m.cfg, "mission-children", "set", m.master, "--mode", "all", "--max-children", "3")
	if code != 0 {
		t.Fatalf("operator set: exit %d %s", code, out)
	}
	p := policyOf(t, m.dbPath, sid)
	if p.Mode != coderhost.ModeAll || p.MaxChildren != 3 || p.OperatorMaxChildren == nil || *p.OperatorMaxChildren != 3 {
		t.Fatalf("policy = %+v: an operator's number is also the lead's cap", p)
	}
	if out, code := runSessionsCLI(t, m.cfg, "mission-children", "set", m.master, "--clear-max-children-cap"); code != 0 {
		t.Fatalf("clear: %d %s", code, out)
	}
	if p := policyOf(t, m.dbPath, sid); p.OperatorMaxChildren != nil {
		t.Fatalf("cap not cleared: %+v", p)
	}
	out, code = runSessionsCLI(t, m.cfg, "mission-children", "list")
	if code != 0 || !strings.Contains(out, `"mode":"all"`) {
		t.Fatalf("list: %d %s", code, out)
	}
	if _, code := runSessionsCLI(t, m.cfg, "mission-children", "set", m.master); code == 0 {
		t.Fatal("set with nothing to change succeeded")
	}
}

// missionReady is a lead that may spawn: the launcher reports that it mints child tokens.
func missionReady(t *testing.T, m masterTurn) *missionChildren {
	t.Helper()
	db, err := storage.NewDatabase(m.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	mc := newMissionChildren(db.DB, "default", m.master)
	mc.ready = func() bool { return true }
	return mc
}

func TestChildSpawnerIsNotReadyUntilTheLauncherMintsChildTokens(t *testing.T) {
	m := newMasterTurn(t)
	mc := newMissionChildren(nil, "default", m.master)
	if mc.Ready() {
		t.Fatal("the spawner is ready before the launcher can bind a child to its task (P-B2)")
	}
}

func TestSpawnPassesNoAccessCwdBudgetFromPlanForTheRealHost(t *testing.T) {
	m := newMasterTurn(t)
	sid, ids := missionOfMaster(t, m, 1, true)
	mc := missionReady(t, m)
	ctx := context.Background()
	if bdb, err := storage.NewDatabase(m.dbPath); err == nil {
		err = coderhost.NewStore(bdb.DB).SetBudget("default", m.master, 10)
		bdb.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := mc.Mark(ctx, sid, ids[0], "", 0); err != nil {
		t.Fatal(err)
	}
	f, err := mc.Facts(ctx, sid, ids[0])
	if err != nil || f.Row != "marked" || f.Blocked != "" || !f.RoomOK {
		t.Fatalf("facts = %+v %v", f, err)
	}
	if err := mc.Spawn(ctx, sid, ids[0], "the sealed brief"); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	f, _ = mc.Facts(ctx, sid, ids[0])
	if f.Row != "spawned" || f.Child == "" {
		t.Fatalf("facts after = %+v", f)
	}
	db, _ := storage.NewDatabase(m.dbPath)
	defer db.Close()
	cs := coderhost.NewStore(db.DB)
	var cwd, access string
	var budget float64
	if err := db.DB.QueryRow(`SELECT c.cwd, c.access, s.budget_usd FROM ai_chat_conversations c JOIN coder_sessions s ON s.conversation_id = c.id WHERE c.id = ?`, f.Child).Scan(&cwd, &access, &budget); err != nil {
		t.Fatal(err)
	}
	var leadCwd string
	_ = db.DB.QueryRow(`SELECT cwd FROM ai_chat_conversations WHERE id = ?`, m.master).Scan(&leadCwd)
	want, _ := cs.DefaultChildBudget("default", sid)
	if want < 0.5 {
		t.Fatalf("the default carve is %v: the test would not tell a budget from the plan apart", want)
	}
	if cwd != leadCwd || cwd == "/" || access == "full" || budget != want || budget == 999 {
		t.Fatalf("child cwd=%q (lead %q) access=%q budget=%v (default %v): the task's notes chose something", cwd, leadCwd, access, budget, want)
	}
}

func TestAdapterRefusesAProcessBoundToATask(t *testing.T) {
	m := newMasterTurn(t)
	sid, ids := missionOfMaster(t, m, 1, true)
	mc := missionReady(t, m)
	ctx := context.Background()
	if err := mc.Mark(ctx, sid, ids[0], "", 0); err != nil {
		t.Fatal(err)
	}
	mc.getenv = func(k string) string {
		if k == agenttokens.EnvMissionToken {
			return "garbled" // does not verify: bound to nothing that exists
		}
		return os.Getenv(k)
	}
	if f, _ := mc.Facts(ctx, sid, ids[0]); f.Blocked == "" {
		t.Fatalf("a bound process is a lead: %+v", f)
	}
	if err := mc.Spawn(ctx, sid, ids[0], "b"); err == nil {
		t.Fatal("a bound process spawned")
	}
	if err := mc.Mark(ctx, sid, ids[0], "large", 1); err == nil {
		t.Fatal("a bound process marked")
	}
}

func TestAdapterNotReadyNeverSpawns(t *testing.T) {
	m := newMasterTurn(t)
	sid, ids := missionOfMaster(t, m, 1, true)
	mc := missionReady(t, m)
	ctx := context.Background()
	_ = mc.Mark(ctx, sid, ids[0], "", 0)
	mc.ready = func() bool { return false }
	if err := mc.Spawn(ctx, sid, ids[0], "b"); err == nil {
		t.Fatal("spawned while the launcher cannot bind a child")
	}
	if f, _ := mc.Facts(ctx, sid, ids[0]); f.Row != "marked" {
		t.Fatalf("row = %q", f.Row)
	}
}

func TestChildrenFullMapsToTheConductorsQueue(t *testing.T) {
	m := newMasterTurn(t)
	sid, ids := missionOfMaster(t, m, 2, true)
	mc := missionReady(t, m)
	ctx := context.Background()
	db, _ := storage.NewDatabase(m.dbPath)
	defer db.Close()
	cs := coderhost.NewStore(db.DB)
	one := 1
	if _, err := cs.SetPolicy(coderhost.OperatorScope{}, "default", sid, coderhost.PolicyPatch{MaxChildren: &one}); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if err := mc.Mark(ctx, sid, id, "", 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := mc.Spawn(ctx, sid, ids[0], "b"); err != nil {
		t.Fatalf("first: %v", err)
	}
	err := mc.Spawn(ctx, sid, ids[1], "b")
	if err == nil || !strings.Contains(err.Error(), "full") || !isChildrenFull(err) {
		t.Fatalf("second: %v", err)
	}
	if f, _ := mc.Facts(ctx, sid, ids[1]); f.Row != "marked" || f.RoomOK || !strings.Contains(f.RoomReason, "child sessions full (1 of 1)") {
		t.Fatalf("the second task stays marked: %+v", f)
	}
}

func isChildrenFull(err error) bool { return errors.Is(err, dynorg.ErrChildrenFull) }

func TestReportIsTheLastProseOfTheChildsNewestTurn(t *testing.T) {
	m := newMasterTurn(t)
	mc := missionReady(t, m)
	store := openTestChatStore(t, m.dbPath)
	if _, _, err := store.CreateTurn(m.master, "default", "t-1", "inst", "p"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []chatevents.AssistantDeltaPayload{{PartID: "p1", Text: "thinking"}, {PartID: "p2", Text: "I changed "}, {PartID: "p2", Text: "a.go."}, {AgentID: "w1", PartID: "w1:p1", Text: "a worker's words"}} {
		if _, err := store.AppendEvent("default", m.master, "t-1", chatevents.EventAssistantDelta, d); err != nil {
			t.Fatal(err)
		}
	}
	if got := mc.Report(context.Background(), m.master); got != "I changed a.go." {
		t.Fatalf("report = %q", got)
	}
	if got := mc.Report(context.Background(), "no-such"); got != "" {
		t.Fatalf("report of nothing = %q", got)
	}
}

type fakeMinter struct{ can bool }

func (f fakeMinter) CanMintChildTokens() bool { return f.can }

func TestChildSpawnerReadyOnlyWithARegisteredMinter(t *testing.T) {
	RegisterChildTokenMinter(nil)
	if newMissionChildren(nil, "default", "c").Ready() || launcherMintsChildTokens() {
		t.Fatal("ready with no minter registered")
	}
	t.Cleanup(func() { RegisterChildTokenMinter(nil) })
	RegisterChildTokenMinter(fakeMinter{can: false})
	if launcherMintsChildTokens() {
		t.Fatal("ready with a minter that cannot mint")
	}
	RegisterChildTokenMinter(fakeMinter{can: true})
	if !newMissionChildren(nil, "default", "c").Ready() {
		t.Fatal("not ready with a minter")
	}
}

func TestMissionChildConstantsAgreeAcrossPackages(t *testing.T) {
	if dynorg.SessionTokenEnv != coderhost.SessionTokenEnv {
		t.Fatalf("the conductor blanks %q, the host hands %q", dynorg.SessionTokenEnv, coderhost.SessionTokenEnv)
	}
	if dynorg.MaxSessionBudgetUSD != 50 {
		t.Fatalf("plan cap = %v", dynorg.MaxSessionBudgetUSD)
	}
}

func TestMissionChildrenCLIAsksTheOperatorCheck(t *testing.T) {
	m := newMasterTurn(t)
	sid, _ := missionOfMaster(t, m, 1, true)
	t.Setenv(coderhost.SessionTokenEnv, "") // no token: the check itself decides
	m.cfg.RequireOperator = func(what string) error { return errors.New("operator only: " + what) }
	for _, args := range [][]string{{"mission-children", "set", m.master, "--mode", "all"}, {"mission-children", "list"}} {
		if out, code := runSessionsCLI(t, m.cfg, args...); code == 0 {
			t.Errorf("%v passed without the operator: %s", args, out)
		}
	}
	if p := policyOf(t, m.dbPath, sid); p.Stored || p.Mode == coderhost.ModeAll {
		t.Fatalf("policy = %+v", p)
	}
}

// The chat-side files name no approval, close, verdict or operator actor.
func TestMissionChildChatSourcesNameNoApprovalWriter(t *testing.T) {
	forbidden := map[string]bool{"ApproveSession": true, "CloseSession": true, "VerifySessionTask": true, "Approve": true, "Move": true,
		"Human": true, "Confirmed": true, "TakeMissionToken": true, "AddSessionSpend": true}
	for _, name := range []string{"chat_mission_children.go", "coder_sessions_mission.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if x, ok := n.(*ast.SelectorExpr); ok && forbidden[x.Sel.Name] {
				t.Errorf("%s uses %s", name, x.Sel.Name)
			}
			return true
		})
	}
}

// F2 at the adapter: in mode all a session the person stopped is not started again by Spawn.
func TestAdapterDoesNotRemarkAStoppedChildInModeAll(t *testing.T) {
	m := newMasterTurn(t)
	sid, ids := missionOfMaster(t, m, 1, true)
	mc := missionReady(t, m)
	ctx := context.Background()
	db, _ := storage.NewDatabase(m.dbPath)
	defer db.Close()
	cs := coderhost.NewStore(db.DB)
	all := coderhost.ModeAll
	if _, err := cs.SetPolicy(coderhost.OperatorScope{}, "default", sid, coderhost.PolicyPatch{Mode: &all}); err != nil {
		t.Fatal(err)
	}
	if err := mc.Spawn(ctx, sid, ids[0], "b"); err != nil {
		t.Fatalf("first: %v", err)
	}
	f, _ := mc.Facts(ctx, sid, ids[0])
	if ok, err := cs.End("default", f.Child, coderhost.EndStopped); err != nil || !ok {
		t.Fatalf("end: %v %v", ok, err)
	}
	if err := mc.Spawn(ctx, sid, ids[0], "b"); err == nil {
		t.Fatal("a stopped session was started again")
	}
	if f, _ := mc.Facts(ctx, sid, ids[0]); f.Row != "ended" {
		t.Fatalf("row = %q", f.Row)
	}
}
