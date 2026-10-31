package dynorg

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/agentpurposes"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
)

// keptWorkers collects what Config.Remember is handed, as the store would.
type keptWorkers struct {
	mu   sync.Mutex
	byID map[string]Veteran
	ids  []string
}

func (k *keptWorkers) remember(v Veteran) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.byID == nil {
		k.byID = map[string]Veteran{}
	}
	if _, ok := k.byID[v.ID]; !ok {
		k.ids = append(k.ids, v.ID)
	}
	k.byID[v.ID] = v
}

func (k *keptWorkers) list() []Veteran {
	k.mu.Lock()
	defer k.mu.Unlock()
	var out []Veteran
	for _, id := range k.ids {
		out = append(out, k.byID[id])
	}
	return out
}

func newVeteranConductor(t *testing.T, ex *treeExec, cwd string, lim Limits, kept *keptWorkers) (*Conductor, *recEmitter) {
	t.Helper()
	em := &recEmitter{}
	c := New(context.Background(), Config{Cwd: cwd, Limits: lim, Staffer: &Staffer{Roster: []Model{treeModel}, Lead: treeModel},
		ReadAccess: true, Exec: ex.exec, Emit: em, Remember: kept.remember})
	t.Cleanup(c.Close)
	return c, em
}

func TestVeteransAreKeptLoadedAndResumed(t *testing.T) {
	kept := &keptWorkers{}
	// Turn 1: a worker, and a worker with a sub-worker.
	c1, _ := newVeteranConductor(t, &treeExec{}, "/w", treeLimits, kept)
	c1.Spawn(context.Background(), SpawnRequest{Brief: "map the cache", Access: ProfileResearch, Wait: true})
	c1.Spawn(context.Background(), SpawnRequest{Brief: brief("fix it", m{"brief": "test it", "wait": true}), Access: ProfileCoding, AllowSpawn: true, Wait: true})
	c1.Close()
	vets := kept.list()
	if len(vets) != 3 {
		t.Fatalf("kept %d workers, want 3: %+v", len(vets), vets)
	}
	w1 := kept.byID["w1"]
	if w1.Session != "sess-opus" || w1.Cwd != "/w" || w1.Runtime != "claude" || w1.Model != "opus" || w1.Role == "" ||
		w1.Access != ProfileResearch || w1.Report != "did map the cache" || w1.Outcome != chatevents.AgentDone {
		t.Errorf("w1 kept as %+v", w1)
	}
	if v := kept.byID["w3"]; v.ParentID != "w2" || !kept.byID["w2"].AllowSpawn {
		t.Errorf("the tree wasn't kept: w2 %+v, w3 %+v", kept.byID["w2"], v)
	}

	// Turn 2: they come back idle. They hold no slot and don't count
	// toward the turn's workers, and new workers are numbered after them.
	ex := &treeExec{}
	c2, em := newVeteranConductor(t, ex, "/w", Limits{MaxAgents: 1, MaxConcurrent: 1}, kept)
	// A sub-worker listed before its parent (its run ended first) still
	// finds it.
	slices.Reverse(vets)
	c2.AddVeterans(vets)
	if n := len(c2.slots); n != 0 {
		t.Errorf("veterans took %d slots", n)
	}
	roster := c2.Roster()
	if len(roster.Workers) != 3 || roster.Limits["spawned"] != 0 {
		t.Fatalf("roster = %+v, limits %v", roster.Workers, roster.Limits)
	}
	for _, w := range roster.Workers {
		if !w.Veteran || w.Status != chatevents.AgentIdle {
			t.Errorf("veteran %s = %+v", w.ID, w)
		}
	}
	infos := c2.Wait(context.Background(), nil, 0)
	if len(infos) != 3 || infos[0].Report != "" || infos[0].Status != chatevents.AgentIdle {
		t.Errorf("org_wait on veterans = %+v", infos)
	}
	var spawned []chatevents.AgentSpawnedPayload
	for _, p := range em.find(chatevents.EventAgentSpawned) {
		spawned = append(spawned, p.(chatevents.AgentSpawnedPayload))
	}
	if len(spawned) != 3 || !spawned[0].Veteran || spawned[2].AgentID != "w3" || spawned[2].ParentID != "w2" {
		t.Errorf("veterans journaled as %+v", spawned)
	}
	if got := em.types("w1"); !slices.Equal(got, []string{"agent.spawned", "agent.status:idle"}) {
		t.Errorf("w1 events = %v", got)
	}

	info, err := c2.Spawn(context.Background(), SpawnRequest{Brief: "new work", Wait: true})
	if err != nil || info.ID != "w4" {
		t.Fatalf("new worker = %+v, %v", info, err)
	}
	// The lead messages a veteran: its session resumes.
	if _, err := c2.Message(context.Background(), "w1", "and the eviction?"); err != nil {
		t.Fatal(err)
	}
	infos = c2.Wait(context.Background(), []string{"w1"}, 0)
	if infos[0].Status != chatevents.AgentDone || infos[0].Report != "did and the eviction?" {
		t.Errorf("w1 after the follow-up = %+v", infos[0])
	}
	o, _ := ex.callFor("and the eviction?")
	if o.Resume != "sess-opus" || o.Access != "read" {
		t.Errorf("veteran exec: resume %q, access %q", o.Resume, o.Access)
	}
	if got := em.types("w1"); !slices.Contains(got, "agent.status:queued") || !slices.Contains(got, "agent.finished") {
		t.Errorf("w1 events = %v", got)
	}
	// Only its parent may message a veteran sub-worker.
	if _, err := c2.Message(context.Background(), "w3", "more"); err == nil {
		t.Error("the lead messaged a veteran sub-worker")
	}
	assertClean(t, c2)
}

func TestVeteranInAnotherFolderIsReBriefed(t *testing.T) {
	ex := &treeExec{}
	c, _ := newVeteranConductor(t, ex, "/elsewhere", treeLimits, &keptWorkers{})
	c.AddVeterans([]Veteran{
		{ID: "w2", Role: "Researcher", Access: ProfileResearch, Runtime: "claude", Model: "opus", Session: "sess-old", Cwd: "/w", Report: "the cache is in cache.go"},
		{ID: "w9", ParentID: "w5", Role: "Orphan", Access: ProfileCoding, Runtime: "claude", Model: "opus", Session: "s"},
	})
	if r := c.Roster(); len(r.Workers) != 1 {
		t.Errorf("a sub-worker without its parent was loaded: %+v", r.Workers)
	}
	c.Message(context.Background(), "w2", "where exactly?")
	c.Wait(context.Background(), nil, 0)
	o, _ := ex.callFor("Your earlier report")
	if o.Resume != "" || !strings.Contains(o.Prompt, "the cache is in cache.go") || !strings.Contains(o.Prompt, "where exactly?") {
		t.Errorf("a session from another folder must not resume: resume %q prompt %q", o.Resume, o.Prompt)
	}
	if info, _ := c.Spawn(context.Background(), SpawnRequest{Brief: "next"}); info.ID != "w3" {
		t.Errorf("new worker id = %s, want w3", info.ID)
	}
}

func TestVeteranWhoseModelIsNotReadyIsRefused(t *testing.T) {
	ex := &treeExec{}
	c, _ := newVeteranConductor(t, ex, "/w", treeLimits, &keptWorkers{})
	c.AddVeterans([]Veteran{{ID: "w1", Role: "Coder", Access: ProfileCoding, Runtime: "gone", Model: "m", Session: "s", Cwd: "/w", Report: "r"}})
	if r := c.Roster(); len(r.Workers) != 1 || !strings.Contains(r.Workers[0].Error, "no longer ready") {
		t.Errorf("roster = %+v", r.Workers)
	}
	if _, err := c.Message(context.Background(), "w1", "more"); err == nil || !strings.Contains(err.Error(), "no longer ready") {
		t.Errorf("a veteran on a model that isn't ready was messaged: %v", err)
	}
	if len(ex.calls) != 0 {
		t.Errorf("it ran: %+v", ex.calls)
	}
}

// A research worker's veteran sub-worker never runs unconfined: not when
// its model left the roster, not when its model no longer confines it,
// and not even if a run of it were started anyway.
func TestResearchVeteranSubWorkerStaysConfined(t *testing.T) {
	for _, tc := range []struct {
		name  string
		child Veteran
		want  string
	}{
		{"model gone", Veteran{Runtime: "codex", Model: "gone"}, "no longer ready"},
		{"model no longer confines", Veteran{Runtime: "codex", Model: "x"}, "can't run confined any more"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ex := &treeExec{}
			em := &recEmitter{}
			c := New(context.Background(), Config{Cwd: "/w", Limits: treeLimits, Staffer: &Staffer{Roster: []Model{treeModel, openModel}, Lead: treeModel},
				ReadAccess: true, Exec: ex.exec, Emit: em})
			t.Cleanup(c.Close)
			child := tc.child
			child.ID, child.ParentID, child.Role, child.Access, child.Session, child.Cwd, child.Report = "w2", "w1", "Researcher", ProfileResearch, "s2", "/w", "r2"
			c.AddVeterans([]Veteran{
				{ID: "w1", Role: "Researcher", Access: ProfileResearch, Runtime: "claude", Model: "opus", Session: "s1", Cwd: "/w", AllowSpawn: true},
				child,
			})
			c.mu.Lock()
			w1, w2 := c.workers["w1"], c.workers["w2"]
			c.mu.Unlock()
			if !w2.mustConfine {
				t.Fatal("the veteran sub-worker of a research worker must be confined")
			}
			if _, err := c.message(context.Background(), w1, "w2", "more"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message = %v, want %q", err, tc.want)
			}
			// Fail closed even past the check: the run is refused before
			// any exec, and it never takes the write lease.
			c.mu.Lock()
			ctx, cancel, done := c.prepareRunLocked(w2)
			c.mu.Unlock()
			c.launch(w2, ctx, cancel, done, "more", "s2", false)
			<-done
			if info := c.Wait(context.Background(), []string{"w2"}, 0)[0]; info.Status != chatevents.AgentFailed || !strings.Contains(info.Error, "can't run confined on codex/") {
				t.Errorf("w2 = %+v", info)
			}
			if len(ex.calls) != 0 {
				t.Errorf("an unconfined exec ran: access %q", ex.calls[0].Access)
			}
			for _, p := range em.find(chatevents.EventAgentStatus) {
				if s := p.(chatevents.AgentStatusPayload); s.AgentID == "w2" && (slices.Contains(s.Leases, "write") || s.To == chatevents.AgentWaitingLease ||
					s.Confinement == chatevents.ConfinementWriteLease) {
					t.Errorf("w2 went for the write lease: %+v", s)
				}
			}
			assertClean(t, c)
		})
	}
}

// With isolated writers, a writing veteran gets its own worktree when the
// lead messages it, like a new writer, and is re-briefed there: its
// session ran in another folder.
func TestWritingVeteranGetsAWorktreeWhenWritersAreIsolated(t *testing.T) {
	needGit(t)
	repo := newRepo(t)
	fx := &fileExec{}
	var mu sync.Mutex
	var resumes []string
	em := &recEmitter{}
	c := New(context.Background(), Config{
		Cwd: repo, Limits: treeLimits, Staffer: &Staffer{Roster: []Model{opus}, Lead: opus}, ReadAccess: true, Emit: em,
		Writers: WritersIsolated, TurnID: "t2",
		Exec: func(ctx context.Context, o monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
			mu.Lock()
			resumes = append(resumes, o.Resume)
			mu.Unlock()
			return fx.exec(ctx, o, on)
		},
	})
	defer c.Close()
	c.AddVeterans([]Veteran{{ID: "w1", Role: "Coder", Access: ProfileCoding, Runtime: "claude", Model: "opus", Session: "s1", Cwd: repo, Report: "wrote a.txt"}})
	if _, err := c.Message(context.Background(), "w1", "now b.txt"); err != nil {
		t.Fatal(err)
	}
	c.Wait(context.Background(), []string{"w1"}, 0)
	fx.mu.Lock()
	cwds := slices.Clone(fx.cwds)
	fx.mu.Unlock()
	if len(cwds) != 1 || realPath(t, cwds[0]) == realPath(t, repo) {
		t.Errorf("the veteran writer ran in %v, not its own worktree", cwds)
	}
	if len(resumes) != 1 || resumes[0] != "" {
		t.Errorf("resumed %v in a folder its session never ran in", resumes)
	}
	if !branchExists(t, repo, branchName("t2", "w1")) {
		t.Error("no branch for the veteran writer")
	}
}

// A research worker's veteran sub-worker on a model that still confines
// it runs with --access read and says so (agent.status confinement).
func TestConfinedVeteranSubWorkerReportsAccessRead(t *testing.T) {
	ex := &treeExec{}
	em := &recEmitter{}
	c := New(context.Background(), Config{Cwd: "/w", Limits: treeLimits, Staffer: &Staffer{Roster: []Model{treeModel, openModel}, Lead: treeModel},
		ReadAccess: true, Exec: ex.exec, Emit: em})
	defer c.Close()
	c.AddVeterans([]Veteran{
		{ID: "w1", Role: "Researcher", Access: ProfileResearch, Runtime: "claude", Model: "opus", Session: "s1", Cwd: "/w", AllowSpawn: true},
		{ID: "w2", ParentID: "w1", Role: "Researcher", Access: ProfileResearch, Runtime: "claude", Model: "opus", Session: "s2", Cwd: "/w", Report: "r2"},
	})
	c.mu.Lock()
	w1 := c.workers["w1"]
	c.mu.Unlock()
	if _, err := c.message(context.Background(), w1, "w2", "more"); err != nil {
		t.Fatal(err)
	}
	if info := c.Wait(context.Background(), []string{"w2"}, 0)[0]; info.Status != chatevents.AgentDone {
		t.Fatalf("w2 = %+v", info)
	}
	if o, _ := ex.callFor("more"); o.Access != monomind.AccessRead || o.Resume != "s2" {
		t.Errorf("w2 exec: access %q, resume %q", o.Access, o.Resume)
	}
	saw := false
	for _, p := range em.find(chatevents.EventAgentStatus) {
		s := p.(chatevents.AgentStatusPayload)
		if s.AgentID != "w2" {
			continue
		}
		if s.Confinement == chatevents.ConfinementWriteLease || slices.Contains(s.Leases, "write") {
			t.Errorf("w2 reported the write lease: %+v", s)
		}
		saw = saw || s.Confinement == chatevents.ConfinementAccessRead
	}
	if !saw {
		t.Error("w2 never reported access-read confinement")
	}
	assertClean(t, c)
}

// A follow-up that is refused (scope, budget) creates no worktree for a
// writing veteran.
func TestRefusedFollowUpCreatesNoWorktree(t *testing.T) {
	needGit(t)
	repo := newRepo(t)
	em := &recEmitter{}
	c := New(context.Background(), Config{
		Cwd: repo, Limits: Limits{MaxAgents: 4, MaxConcurrent: 3, BudgetUSD: 0.01}, Staffer: &Staffer{Roster: []Model{opus}, Lead: opus},
		ReadAccess: true, Emit: em, Writers: WritersIsolated, TurnID: "t3", Exec: (&fileExec{}).exec,
	})
	defer c.Close()
	c.AddVeterans([]Veteran{
		{ID: "w1", Role: "Coder", Access: ProfileCoding, Runtime: "claude", Model: "opus", Session: "s1", Cwd: repo, Report: "r1", AllowSpawn: true},
		{ID: "w2", ParentID: "w1", Role: "Coder", Access: ProfileCoding, Runtime: "claude", Model: "opus", Session: "s2", Cwd: repo, Report: "r2"},
	})
	// The lead can't message w1's sub-worker.
	if _, err := c.Message(context.Background(), "w2", "more"); err == nil {
		t.Fatal("the lead messaged a veteran sub-worker")
	}
	// The budget is spent.
	c.mu.Lock()
	c.cost = 0.02
	c.mu.Unlock()
	if _, err := c.Message(context.Background(), "w1", "more"); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("message with the budget spent = %v", err)
	}
	for _, id := range []string{"w1", "w2"} {
		if branchExists(t, repo, branchName("t3", id)) {
			t.Errorf("a refused follow-up created %s's branch", id)
		}
		c.mu.Lock()
		w := c.workers[id]
		tried, tree := w.treeDone, w.worktree
		c.mu.Unlock()
		if tried || tree != "" {
			t.Errorf("%s: treeDone %v, worktree %q", id, tried, tree)
		}
	}
}

// keptCoder runs a built-in coder to the end under no operator sets and
// returns the veteran the conversation keeps of it.
func keptCoder(t *testing.T) Veteran {
	t.Helper()
	kept := &keptWorkers{}
	c, _ := newVeteranConductor(t, &treeExec{}, "/w", treeLimits, kept)
	if _, err := c.Spawn(context.Background(), SpawnRequest{Brief: "fix it", Role: "coder", Wait: true}); err != nil {
		t.Fatal(err)
	}
	c.Close()
	return kept.byID["w1"]
}

func veteranTurn(t *testing.T, ex *treeExec, fn func() (map[string]agentpurposes.Setting, error)) *Conductor {
	t.Helper()
	c := New(context.Background(), Config{Cwd: "/w", Limits: treeLimits, ReadAccess: true, Exec: ex.exec, Emit: &recEmitter{},
		Staffer: &Staffer{Roster: []Model{treeModel, openModel}, Lead: treeModel, PurposesFn: fn}})
	t.Cleanup(c.Close)
	return c
}

func sets(m map[string]agentpurposes.Setting) func() (map[string]agentpurposes.Setting, error) {
	return func() (map[string]agentpurposes.Setting, error) { return m, nil }
}

// A veteran is rechecked against the operator's CURRENT sets at each
// follow-up, not just for readiness: narrowing a set after the veteran was
// staffed refuses it, and the reason names the purpose and `agent purposes`.
func TestAVeteranIsRecheckedAgainstTheCurrentPurposeSets(t *testing.T) {
	v := keptCoder(t)
	if !v.Builtin || v.Purposes != "coding" {
		t.Errorf("the veteran kept no purpose data: %+v", v)
	}
	narrow := func(pair string) map[string]agentpurposes.Setting {
		return map[string]agentpurposes.Setting{"coding": set("any", pair)}
	}
	ex := &treeExec{}
	c := veteranTurn(t, ex, sets(narrow("codex/x"))) // the veteran runs on claude/opus
	c.AddVeterans([]Veteran{v})
	_, err := c.Message(context.Background(), "w1", "more")
	if err == nil || !strings.Contains(err.Error(), "coding") || !strings.Contains(err.Error(), "agent purposes") {
		t.Errorf("a veteran outside the narrowed set was messaged: %v", err)
	}
	if len(ex.calls) != 0 {
		t.Errorf("it ran: %+v", ex.calls)
	}
	// The set is read again at every follow-up: widened back, it runs.
	cur := narrow("codex/x")
	c = veteranTurn(t, ex, func() (map[string]agentpurposes.Setting, error) { return cur, nil })
	c.AddVeterans([]Veteran{v})
	if _, err := c.Message(context.Background(), "w1", "more"); err == nil {
		t.Error("refused veteran ran")
	}
	cur = narrow("claude/opus")
	if _, err := c.Message(context.Background(), "w1", "more"); err != nil {
		t.Errorf("a veteran whose model is still in the set was refused: %v", err)
	}
	c.Wait(context.Background(), nil, 0)
}

// An unreadable setting or an unreadable purpose record refuses the
// follow-up: nothing is read as unrestricted.
func TestAVeteranFollowUpFailsClosed(t *testing.T) {
	v := keptCoder(t)
	open := sets(map[string]agentpurposes.Setting{})
	bad := v
	bad.Purposes = "coding,not-a-purpose"
	for name, tc := range map[string]struct {
		v  Veteran
		fn func() (map[string]agentpurposes.Setting, error)
	}{
		"settings unreadable":    {v, func() (map[string]agentpurposes.Setting, error) { return nil, errors.New("corrupt row") }},
		"purpose record corrupt": {bad, open},
	} {
		ex := &treeExec{}
		c := veteranTurn(t, ex, tc.fn)
		c.AddVeterans([]Veteran{tc.v})
		if _, err := c.Message(context.Background(), "w1", "more"); err == nil {
			t.Errorf("%s: the follow-up ran", name)
		}
		if len(ex.calls) != 0 {
			t.Errorf("%s: it ran: %+v", name, ex.calls)
		}
	}
}

// A veteran kept by an older build has no purpose data: it is unmapped, so
// every purpose the operator restricted applies to it.
func TestAVeteranWithoutPurposeDataIsUnmapped(t *testing.T) {
	old := keptCoder(t)
	old.Builtin, old.Purposes = false, ""
	restrictResearch := sets(map[string]agentpurposes.Setting{"research": set("any", "codex/x")})
	c := veteranTurn(t, &treeExec{}, restrictResearch)
	c.AddVeterans([]Veteran{old})
	if _, err := c.Message(context.Background(), "w1", "more"); err == nil || !strings.Contains(err.Error(), "research") {
		t.Errorf("an old veteran escaped a restricted set it never had a purpose for: %v", err)
	}
	c = veteranTurn(t, &treeExec{}, sets(nil))
	c.AddVeterans([]Veteran{old})
	if _, err := c.Message(context.Background(), "w1", "more"); err != nil {
		t.Errorf("nothing restricted, yet an old veteran was refused: %v", err)
	}
	c.Wait(context.Background(), nil, 0)
}

// A worker staffed earlier in THIS turn is rechecked at its follow-up like a
// veteran: the operator narrows a set (or its row turns corrupt) after the
// first run, and the worker is refused although it never left the turn.
func TestASameTurnWorkerIsRecheckedAtItsFollowUp(t *testing.T) {
	for _, name := range []string{"narrowed after the first run", "corrupt after the first run", "model still in the set"} {
		cur := sets(nil)
		ex := &treeExec{}
		c := veteranTurn(t, ex, func() (map[string]agentpurposes.Setting, error) { return cur() })
		if _, err := c.Spawn(context.Background(), SpawnRequest{Brief: "fix it", Role: "coder", Wait: true}); err != nil {
			t.Fatal(err)
		}
		c.mu.Lock()
		ran, other := c.workers["w1"].model.Key(), openModel.Key()
		if ran == other {
			other = treeModel.Key()
		}
		c.mu.Unlock()
		refused := true
		switch name {
		case "narrowed after the first run":
			cur = sets(map[string]agentpurposes.Setting{"coding": set("any", other)})
		case "corrupt after the first run":
			cur = func() (map[string]agentpurposes.Setting, error) { return nil, errors.New("corrupt row") }
		default:
			cur, refused = sets(map[string]agentpurposes.Setting{"coding": set("any", ran)}), false
		}
		n := len(ex.calls)
		_, err := c.Message(context.Background(), "w1", "more")
		c.Wait(context.Background(), nil, 0)
		switch {
		case refused && (err == nil || len(ex.calls) != n):
			t.Errorf("%s: same-turn worker ran outside the current sets: err=%v calls %d->%d", name, err, n, len(ex.calls))
		case !refused && (err != nil || len(ex.calls) != n+1):
			t.Errorf("%s: a worker still inside its set was refused: err=%v calls %d->%d", name, err, n, len(ex.calls))
		}
	}
}

// A same-turn worker whose purpose record names an unknown id is refused.
func TestASameTurnWorkerWithAnUnknownPurposeIsRefused(t *testing.T) {
	ex := &treeExec{}
	c := veteranTurn(t, ex, sets(nil))
	if _, err := c.Spawn(context.Background(), SpawnRequest{Brief: "fix it", Role: "coder", Wait: true}); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.workers["w1"].staff.sides = []string{"not-a-purpose"}
	c.mu.Unlock()
	n := len(ex.calls)
	if _, err := c.Message(context.Background(), "w1", "more"); err == nil || len(ex.calls) != n {
		t.Errorf("unknown purpose id ran: err=%v calls %d->%d", err, n, len(ex.calls))
	}
}
