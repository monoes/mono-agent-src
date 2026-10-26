package coderhost

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/testdb"
)

func newStoreT(t *testing.T) *Store {
	t.Helper()
	db := testdb.Open(t)
	t.Cleanup(func() { db.Close() })
	return NewStore(db.DB)
}

// master makes a coder conversation at access in a real folder and makes it a master.
func master(t *testing.T, st *Store, access string) (Caller, string) {
	t.Helper()
	dir := t.TempDir()
	a, _ := ai.NewAIStore(st.db)
	conv, err := a.CreateConversationAccess("p", "agent", "", "claude", "", "m1", access, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnsureSession("p", conv.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRole("p", conv.ID, "master", ""); err != nil {
		t.Fatal(err)
	}
	return Caller{ProfileID: "p", ConversationID: conv.ID}, dir
}

func spawn(t *testing.T, st *Store, c Caller) string {
	t.Helper()
	out, err := st.Spawn(c, SpawnRequest{})
	if err != nil {
		t.Fatal(err)
	}
	return out.ConversationID
}

func asChild(id string) Caller { return Caller{ProfileID: "p", ConversationID: id} }

func TestSpawnInheritsTheParentsRuntimeFolderAndLinksTheChild(t *testing.T) {
	st := newStoreT(t)
	m, dir := master(t, st, "workspace")
	out, err := st.Spawn(m, SpawnRequest{Message: "start here"})
	if err != nil {
		t.Fatal(err)
	}
	x, _, _ := st.GetSession("p", out.ConversationID)
	if x.Role != "child" || x.Parent != m.ConversationID {
		t.Fatalf("session = %+v", x)
	}
	a, _ := ai.NewAIStore(st.db)
	conv, _ := a.GetConversation(out.ConversationID, "p")
	if conv.RuntimeID != "claude" || conv.Model != "m1" || conv.Cwd != dir || conv.Access != "workspace" {
		t.Fatalf("conversation = %+v", conv)
	}
	cmd, _, _ := st.GetCommand(out.CommandID)
	if cmd.Kind != KindSend || !strings.HasPrefix(cmd.TurnID, MasterTurnPrefix) || cmd.WantsMission {
		t.Fatalf("first message = %+v", cmd)
	}
	if s, _ := st.GetSettings("p", out.ConversationID); s.MaxChildren != 0 {
		t.Fatalf("a child does not make children: %+v", s)
	}
}

func TestSpawnFolderMustStayInsideTheMastersFolder(t *testing.T) {
	st := newStoreT(t)
	m, dir := master(t, st, "coding")
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(dir, "link"))
	if out, err := st.Spawn(m, SpawnRequest{Cwd: "sub"}); err != nil || !strings.HasSuffix(out.Cwd, "sub") {
		t.Fatalf("inside folder: %+v %v", out, err)
	}
	for _, bad := range []string{"..", "../x", outside, "link"} {
		if _, err := st.Spawn(m, SpawnRequest{Cwd: bad}); err == nil {
			t.Fatalf("folder %q was accepted", bad)
		}
	}
}

func TestAChildNeverHasMoreAccessThanItsMaster(t *testing.T) {
	cases := []struct{ parent, asked, want string }{
		{"full", "", "workspace"},
		{"workspace", "", "workspace"},
		{"coding", "", "coding"},
		{"workspace", "coding", "coding"},
	}
	for _, c := range cases {
		st := newStoreT(t)
		m, _ := master(t, st, c.parent)
		out, err := st.Spawn(m, SpawnRequest{Access: c.asked})
		if err != nil || out.Access != c.want {
			t.Fatalf("%+v: got %+v %v", c, out, err)
		}
		if _, access, _, _ := st.ConversationInfo("p", out.ConversationID); access != c.want {
			t.Fatalf("%+v: stored access %s", c, access)
		}
	}
	for _, c := range []struct{ parent, asked string }{{"coding", "workspace"}, {"coding", "full"}, {"full", "full"}, {"workspace", "full"}, {"full", "scoped"}, {"coding", "bogus"}} {
		st := newStoreT(t)
		m, _ := master(t, st, c.parent)
		if _, err := st.Spawn(m, SpawnRequest{Access: c.asked}); err == nil {
			t.Fatalf("%s master spawned a %s child", c.parent, c.asked)
		}
		if kids, _ := st.ListChildren(m); len(kids) != 0 {
			t.Fatalf("a refused spawn left %d children", len(kids))
		}
	}
}

func TestAChildsBudgetIsCarvedOutOfTheMastersRemainingBudget(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	if err := st.SetBudget("p", m.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	a, err := st.Spawn(m, SpawnRequest{BudgetUSD: 6})
	if err != nil || a.BudgetUSD != 6 {
		t.Fatal(a, err)
	}
	if _, err := st.Spawn(m, SpawnRequest{BudgetUSD: 5}); !errors.Is(err, ErrLimit) {
		t.Fatalf("5 of the 4 left: %v", err)
	}
	if _, err := st.Spawn(m, SpawnRequest{BudgetUSD: 0}); !errors.Is(err, ErrLimit) {
		t.Fatalf("a child with no cap under a capped master: %v", err)
	}
	if _, err := st.Spawn(m, SpawnRequest{BudgetUSD: 4}); err != nil {
		t.Fatalf("the last 4: %v", err)
	}
	if carved, _ := st.Carved("p", m.ConversationID); carved != 10 {
		t.Fatalf("carved = %v", carved)
	}
	if err := st.SetBudget("p", m.ConversationID, 9); err == nil {
		t.Fatal("the cap was lowered below what the children hold")
	}
	if kids, _ := st.ListChildren(m); len(kids) != 2 {
		t.Fatalf("refused spawns left sessions behind: %d", len(kids))
	}
	st2 := newStoreT(t)
	m2, _ := master(t, st2, "coding")
	if _, err := st2.Spawn(m2, SpawnRequest{BudgetUSD: 3}); err != nil {
		t.Fatalf("an uncapped master may cap a child: %v", err)
	}
	if _, err := st2.Spawn(m2, SpawnRequest{BudgetUSD: -1}); err == nil {
		t.Fatal("a negative budget was accepted")
	}
}

func TestSpawnLimits(t *testing.T) {
	t.Run("max_children", func(t *testing.T) {
		st := newStoreT(t)
		m, _ := master(t, st, "coding")
		s, _ := st.GetSettings("p", m.ConversationID)
		s.MaxChildren = 2
		st.SaveSettings("p", m.ConversationID, s)
		spawn(t, st, m)
		spawn(t, st, m)
		if _, err := st.Spawn(m, SpawnRequest{}); !errors.Is(err, ErrLimit) {
			t.Fatalf("third child: %v", err)
		}
	})
	t.Run("depth", func(t *testing.T) {
		st := newStoreT(t)
		m, _ := master(t, st, "coding")
		s, _ := st.GetSettings("p", m.ConversationID)
		s.MaxDepth = 1
		st.SaveSettings("p", m.ConversationID, s)
		if _, err := st.Spawn(m, SpawnRequest{}); !errors.Is(err, ErrLimit) {
			t.Fatalf("max_depth 1: %v", err)
		}
		// a child is not a master, and even a master placed under another would pass the depth limit of 2
		m2, _ := master(t, st, "coding")
		kid := spawn(t, st, m2)
		if _, err := st.Spawn(asChild(kid), SpawnRequest{}); err == nil {
			t.Fatal("a child spawned a child")
		}
		st.db.Exec(`UPDATE coder_sessions SET role = 'master' WHERE conversation_id = ?`, kid) // a damaged tree: still level 2
		if _, err := st.Spawn(asChild(kid), SpawnRequest{}); !errors.Is(err, ErrLimit) {
			t.Fatalf("level 3: %v", err)
		}
	})
	t.Run("rate", func(t *testing.T) {
		st := newStoreT(t)
		now := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
		st.now = func() time.Time { return now }
		m, _ := master(t, st, "coding")
		s, _ := st.GetSettings("p", m.ConversationID)
		s.MaxChildren = 20
		st.SaveSettings("p", m.ConversationID, s)
		for i := 0; i < 6; i++ { // six per window: the number is part of the documented limit
			id := spawn(t, st, m)
			st.SetRole("p", id, "solo", "") // the rate counts spawns, not children alive
		}
		if _, err := st.Spawn(m, SpawnRequest{}); !errors.Is(err, ErrLimit) {
			t.Fatalf("one too many: %v", err)
		}
		now = now.Add(SpawnRateWindow + time.Second)
		spawn(t, st, m)
	})
}

func TestOnlyADescendantCanBeControlled(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	a, b := spawn(t, st, m), spawn(t, st, m)
	other, _ := master(t, st, "coding")
	stranger := spawn(t, st, other)
	m2, _ := master(t, st, "coding")
	_ = m2

	type attempt struct {
		who    Caller
		target string
		why    string
	}
	refused := []attempt{
		{asChild(a), b, "a peer"},
		{asChild(b), a, "a peer"},
		{asChild(a), m.ConversationID, "an ancestor"},
		{m, m.ConversationID, "itself"},
		{asChild(a), a, "itself"},
		{m, stranger, "an unrelated session"},
		{m, "no-such-session", "nothing"},
		{m, "", "an empty target"},
		{Caller{}, a, "no caller"},
	}
	for _, r := range refused {
		if _, err := st.SendToChild(r.who, r.target, "hi"); err == nil {
			t.Errorf("send to %s was allowed", r.why)
		}
		for _, k := range []Kind{KindPause, KindResume, KindStop} {
			if _, err := st.ControlChild(r.who, r.target, k); err == nil {
				t.Errorf("%s of %s was allowed", k, r.why)
			}
		}
		if _, _, err := st.SetChild(r.who, r.target, []byte(`{"checkin_minutes":5}`)); err == nil {
			t.Errorf("set on %s was allowed", r.why)
		}
		if r.target != r.who.ConversationID {
			if _, err := st.StatusOf(r.who, r.target); err == nil {
				t.Errorf("status of %s was allowed", r.why)
			}
		}
	}
	if st.PendingFor("p", b) != 0 || st.PendingFor("p", a) != 0 || st.PendingFor("p", m.ConversationID) != 0 {
		t.Fatal("a refused call queued a command")
	}
	if !errors.Is(func() error { _, e := st.SendToChild(asChild(a), b, "x"); return e }(), ErrNotYours) {
		t.Fatal("the refusal is ErrNotYours")
	}
	// the master reaches its own, and a session reads its own status
	if _, err := st.SendToChild(m, a, "work"); err != nil {
		t.Fatal(err)
	}
	for _, k := range []Kind{KindPause, KindResume, KindStop} {
		if _, err := st.ControlChild(m, b, k); err != nil {
			t.Fatal(k, err)
		}
	}
	if _, _, err := st.SetChild(m, a, []byte(`{"checkin_minutes":5}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.StatusOf(asChild(a), a); err != nil {
		t.Fatal("a session's own status", err)
	}
	if kids, err := st.ListChildren(m); err != nil || len(kids) != 2 {
		t.Fatalf("list = %v %v", kids, err)
	}
	if kids, _ := st.ListChildren(asChild(a)); len(kids) != 0 {
		t.Fatal("a child sees no one below it")
	}
}

func TestDescendantsReachFurtherThanChildrenAndCyclesEnd(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	a := spawn(t, st, m)
	for _, id := range []string{"g", "x", "y"} {
		st.EnsureSession("p", id)
	}
	st.db.Exec(`UPDATE coder_sessions SET parent_conversation_id = ? WHERE conversation_id = 'g'`, a) // m -> a -> g
	if !st.IsDescendant("p", m.ConversationID, "g") || st.IsDescendant("p", "g", m.ConversationID) || st.IsDescendant("p", a, m.ConversationID) {
		t.Fatal("the walk goes up from the target and only up")
	}
	st.db.Exec(`UPDATE coder_sessions SET parent_conversation_id = 'y' WHERE conversation_id = 'x'`)
	st.db.Exec(`UPDATE coder_sessions SET parent_conversation_id = 'x' WHERE conversation_id = 'y'`)
	if st.IsDescendant("p", m.ConversationID, "x") || st.IsDescendant("p", "zzz", "x") {
		t.Fatal("a loop is not under anything")
	}
	if st.IsDescendant("other-profile", m.ConversationID, a) {
		t.Fatal("trees do not cross profiles")
	}
}

func TestToolsNeverChangeOperatorOnlySettings(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	a := spawn(t, st, m)
	before, _ := st.GetSettings("p", a)
	for _, p := range []string{
		`{"max_children":9}`, `{"max_depth":1}`, `{"max_concurrent":5}`, `{"checkin_limit":500}`, `{"mesh_accept":"project"}`,
		`{"autonomy_preset":"decide-and-log"}`, `{"autonomy_classes":{"ambiguity":{"who":"model","fallback":"take_default"}}}`,
		`{"checkin_minutes":5,"mesh_accept":"none"}`, // a mixed patch is refused whole
	} {
		_, _, err := st.SetChild(m, a, []byte(p))
		if !errors.Is(err, ErrOperatorOnly) {
			t.Errorf("%s: %v", p, err)
		}
	}
	if after, _ := st.GetSettings("p", a); !reflect.DeepEqual(before, after) {
		t.Fatalf("settings changed by refused patches: %+v -> %+v", before, after)
	}
	if _, keys, err := st.SetChild(m, a, []byte(`{"checkin_minutes":5,"active_hours":"08:00-18:00","mesh_alias":"w1"}`)); err != nil || len(keys) != 3 {
		t.Fatal(keys, err)
	}
	if _, _, err := st.SetChild(m, a, []byte(`{"nope":1}`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown setting: %v", err)
	}
}

// Every setting is either the operator's or adjustable by a master, so a setting
// added later is a decision, not an accident.
func TestEverySettingIsClassified(t *testing.T) {
	typ := reflect.TypeOf(Settings{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if oneOf(name, OperatorOnly) == oneOf(name, AgentAdjustable) {
			t.Errorf("setting %q must be in exactly one of OperatorOnly and AgentAdjustable", name)
		}
	}
}

func TestMasterMadeSendsAreNotAPersonsSend(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	a := spawn(t, st, m)
	cmd, err := st.SendToChild(m, a, "do it")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cmd.TurnID, MasterTurnPrefix) || cmd.WantsMission || !HostMade(cmd.TurnID) {
		t.Fatalf("command = %+v", cmd)
	}
	if _, err := st.Enqueue(Command{ProfileID: "p", ConversationID: a, Kind: KindSend, TurnID: cmd.TurnID + "x", Message: "m", WantsMission: true}); err == nil {
		t.Fatal("a master-made send asked for a mission")
	}
	if _, err := st.Confirm(cmd, 0); err == nil {
		t.Fatal("a master-made send was confirmed")
	}
	if _, ok := st.PeekConfirm(cmd); ok {
		t.Fatal("a master-made send has a confirmation")
	}
	if st.ConsumeConfirm("p", a, cmd.TurnID, "1.abc", 0) {
		t.Fatal("a master-made turn consumed a confirmation")
	}
}

func TestMasterMadeTurnsCountInTheHostMadeStreakAndTheHostStopsThem(t *testing.T) {
	h, l, st := newHost(t)
	m, _ := master(t, st, "coding")
	a := spawn(t, st, m)
	s, _ := st.GetSettings("p", a)
	s.CheckinLimit = 2
	st.SaveSettings("p", a, s)
	base := time.Now().Add(-time.Hour)
	addTurn(t, st, a, "person-1", "completed", "hi", base)
	addTurn(t, st, a, "master-1", "completed", "x", base.Add(time.Minute))
	addTurn(t, st, a, "checkin-1", "completed", "x", base.Add(2*time.Minute))
	if n, _, _, _ := st.CheckinStreak("p", a); n != 2 {
		t.Fatalf("streak = %d, master- and checkin- turns both count", n)
	}
	if _, err := st.SendToChild(m, a, "again"); err != nil {
		t.Fatal(err)
	}
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if len(l.started) != 0 {
		t.Fatalf("a master drove the child past the limit: %v", l.started)
	}
	// a person's message resets it: the next master send runs
	addTurn(t, st, a, "person-2", "completed", "hello", base.Add(3*time.Minute))
	if _, err := st.SendToChild(m, a, "now"); err != nil {
		t.Fatal(err)
	}
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if len(l.started) != 1 || !strings.HasSuffix(l.started[0], ":now") {
		t.Fatalf("started = %v", l.started)
	}
}

func TestAMasterSendDoesNotRunForASessionThatIsNoLongerAChild(t *testing.T) {
	h, l, st := newHost(t)
	m, _ := master(t, st, "coding")
	a := spawn(t, st, m)
	cmd, _ := st.SendToChild(m, a, "go")
	if err := st.SetRole("p", a, "solo", ""); err != nil {
		t.Fatal(err)
	}
	_ = h.Tick()
	if got, _, _ := st.GetCommand(cmd.ID); got.Status != CmdFailed || len(l.started) != 0 {
		t.Fatalf("command = %+v started = %v", got, l.started)
	}
}
