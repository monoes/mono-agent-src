package dynorg

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

var (
	tbg      = context.Background()
	operator = tasks.Actor{Kind: tasks.Human}
	taskRE   = regexp.MustCompile(`doing task #(\d+)`)
	verifyRE = regexp.MustCompile(`verifying task #(\d+)`)
)

// missionClock is the task store's time, which a test moves to end leases.
type missionClock struct {
	mu sync.Mutex
	t  time.Time
}

func (m *missionClock) now() time.Time { m.mu.Lock(); defer m.mu.Unlock(); return m.t }
func (m *missionClock) advance(d time.Duration) {
	m.mu.Lock()
	m.t = m.t.Add(d)
	m.mu.Unlock()
}

// gateExec is a fake agent exec for mission workers: a run waits at its task's gate (or
// ends with the context), then answers with a report. fail[task] makes the next runs of that
// task crash.
type gateExec struct {
	mu      sync.Mutex
	gates   map[int64]chan struct{}
	open    map[int64]bool // tasks whose gate is open for good
	runs    map[int64]int
	fail    map[int64]int // task -> crashes still to come
	envs    map[int64]map[string]string
	prompts map[int64]string
	access  map[int64]string
	running int32
	maxRun  int32
	cost    float64
	// verify answers the runs of verifiers (a prompt that says "verifying task #N"); it
	// sees the options the run got. vruns records them per task.
	verify func(ctx context.Context, task int64, o monomind.ExecOptions) (*monomind.TurnResult, error)
	vruns  map[int64][]monomind.ExecOptions
	// budgets records the --budget-usd every run got.
	budgets []float64
	// onRun, when set, runs inside each task worker's exec after its start event, with the
	// event sink: a worker that streams usage while it works.
	onRun func(ctx context.Context, task int64, on func(monomind.Event))
	// models records the runtime/model the last run of each task's worker got.
	models map[int64]string
}

func newGateExec() *gateExec {
	return &gateExec{gates: map[int64]chan struct{}{}, open: map[int64]bool{}, runs: map[int64]int{}, fail: map[int64]int{},
		envs: map[int64]map[string]string{}, prompts: map[int64]string{}, access: map[int64]string{}, cost: 0.01}
}

func (g *gateExec) gate(id int64) chan struct{} {
	if g.gates[id] == nil {
		g.gates[id] = make(chan struct{})
	}
	return g.gates[id]
}

// release lets the runs of a task end (now and later).
func (g *gateExec) release(ids ...int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, id := range ids {
		if !g.open[id] {
			g.open[id] = true
			close(g.gate(id))
		}
	}
}

func (g *gateExec) runCount(id int64) int { g.mu.Lock(); defer g.mu.Unlock(); return g.runs[id] }

func (g *gateExec) exec(ctx context.Context, o monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
	g.mu.Lock()
	g.budgets = append(g.budgets, o.BudgetUSD)
	g.mu.Unlock()
	if vm := verifyRE.FindStringSubmatch(o.Prompt + o.SystemPrompt); vm != nil && g.verify != nil {
		id, _ := strconv.ParseInt(vm[1], 10, 64)
		g.mu.Lock()
		if g.vruns == nil {
			g.vruns = map[int64][]monomind.ExecOptions{}
		}
		g.vruns[id] = append(g.vruns[id], o)
		g.mu.Unlock()
		on(monomind.Event{Type: monomind.EventStart})
		return g.verify(ctx, id, o)
	}
	m := taskRE.FindStringSubmatch(o.Prompt + o.SystemPrompt)
	var id int64
	if m != nil {
		id, _ = strconv.ParseInt(m[1], 10, 64)
	}
	g.mu.Lock()
	g.runs[id]++
	g.envs[id], g.prompts[id], g.access[id] = o.Env, o.Prompt, string(o.Access)
	if g.models == nil {
		g.models = map[int64]string{}
	}
	g.models[id] = o.Runtime + "/" + o.Model
	gate := g.gate(id)
	crash := g.fail[id] > 0
	if crash {
		g.fail[id]--
	}
	g.mu.Unlock()
	n := atomic.AddInt32(&g.running, 1)
	for {
		mx := atomic.LoadInt32(&g.maxRun)
		if n <= mx || atomic.CompareAndSwapInt32(&g.maxRun, mx, n) {
			break
		}
	}
	defer atomic.AddInt32(&g.running, -1)
	on(monomind.Event{Type: monomind.EventStart})
	if g.onRun != nil {
		g.onRun(ctx, id, on)
	}
	if crash {
		return nil, fmt.Errorf("the runtime crashed")
	}
	select {
	case <-gate:
	case <-ctx.Done():
		return &monomind.TurnResult{SawDone: true, StopReason: monomind.StopCancelled}, nil
	}
	return &monomind.TurnResult{SawDone: true, ResultText: fmt.Sprintf("report for task %d", id), StopReason: monomind.StopEndTurn, CostUSD: g.cost, HasCostUSD: true}, nil
}

// missionRig is a conductor over a real task store (a database of its own).
type missionRig struct {
	t     *testing.T
	store *tasks.Store
	clk   *missionClock
	ex    *gateExec
	em    *recEmitter
	mc    *MissionRun
	c     *Conductor
	sid   int64
}

type rigOpts struct {
	lim      Limits
	timeout  time.Duration
	vtimeout time.Duration // MissionRun.VerifierTimeout
	lease    time.Duration
	budget   float64
	conv     string // the conversation the session belongs to
}

func newRig(t *testing.T, o rigOpts) *missionRig {
	t.Helper()
	db := openMissionDB(t)
	r := &missionRig{t: t, store: tasks.NewStore(db), clk: &missionClock{t: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}, ex: newGateExec(), em: &recEmitter{}}
	r.store.SetClock(r.clk.now)
	se, err := r.store.CreateSession(tbg, "default", tasks.SessionInput{Title: "mission", Goal: "ship the feature", BudgetUSD: o.budget, ConversationID: o.conv}, operator)
	if err != nil {
		t.Fatal(err)
	}
	r.sid = se.ID
	r.mc = &MissionRun{Store: r.store, ProfileID: "default", Session: se.ID, Poll: 10 * time.Millisecond, Lease: orDur(o.lease, time.Minute), TaskTimeout: o.timeout, VerifierTimeout: o.vtimeout}
	if o.lim.MaxAgents == 0 {
		o.lim = Limits{MaxAgents: 20, MaxConcurrent: 5}
	}
	r.c = r.conductor(o.lim)
	return r
}

// conductor starts a conductor over the rig's store (a second one is a restarted turn).
func (r *missionRig) conductor(lim Limits) *Conductor {
	return r.conductorFor(lim, nil)
}

// conductorFor is conductor for a lead of a conversation (Config.Mission): it works the
// session of that conversation only.
func (r *missionRig) conductorFor(lim Limits, m *MissionConfig) *Conductor {
	c := New(tbg, Config{Cwd: "/w", Limits: lim, Staffer: &Staffer{Roster: []Model{opus, haiku}, Lead: opus},
		ReadAccess: true, Exec: r.ex.exec, Emit: r.em, Missions: r.mc, Mission: m})
	r.t.Cleanup(c.Close)
	return c
}

// leadOf is the mission setup of the lead of a conversation, confirmed.
func (r *missionRig) leadOf(conv string) *MissionConfig {
	return &MissionConfig{Store: r.store, ProfileID: "default", ConversationID: conv, Actor: "mission-lead", Access: "coding", Confirmed: true}
}

type spec struct {
	title, files, exec string
	deps               []int64
	noEvidence         bool // a code task whose worker will attach nothing
}

// add plans a task into the (draft) session.
func (r *missionRig) add(s spec) tasks.Task {
	r.t.Helper()
	notes := "do it"
	if s.files != "" {
		notes += "\n\nFiles:\n- " + strings.ReplaceAll(s.files, ",", "\n- ")
	}
	exec := s.exec
	if exec == "" {
		exec = ExecWorker
	}
	tk, err := r.store.AddSessionTask(tbg, "default", r.sid, tasks.SessionTaskInput{Title: s.title, Notes: notes,
		Acceptance: "criteria of " + s.title, DependsOn: s.deps, SuggestedExecutor: exec}, operator)
	if err != nil {
		r.t.Fatal(err)
	}
	if s.files != "" && !s.noEvidence {
		// a code task needs evidence to reach review: stand in for the commit its worker makes
		art := tasks.ArtifactInput{Kind: "commit", Ref: "commit-of-" + strconv.FormatInt(tk.ID, 10)}
		if _, err := r.store.AddArtifact(tbg, "default", r.sid, tk.ID, art, operator); err != nil {
			r.t.Fatal(err)
		}
	}
	return tk
}

func (r *missionRig) approve() {
	r.t.Helper()
	if _, err := r.store.ApproveSession(tbg, "default", r.sid, operator); err != nil {
		r.t.Fatal(err)
	}
}

func (r *missionRig) task(id int64) tasks.Task {
	r.t.Helper()
	tk, _, err := r.store.Get(tbg, "default", id)
	if err != nil {
		r.t.Fatal(err)
	}
	return tk
}

func (r *missionRig) session() tasks.Session {
	r.t.Helper()
	se, err := r.store.GetSession(tbg, "default", r.sid, operator)
	if err != nil {
		r.t.Fatal(err)
	}
	return se
}

func (r *missionRig) call(c *Conductor, tool string, args string) string {
	r.t.Helper()
	out, err := c.Handle(tbg, tool, json.RawMessage(args))
	if err != nil {
		r.t.Fatalf("%s: %v", tool, err)
	}
	return out
}

func (r *missionRig) next(c *Conductor) StepResult {
	r.t.Helper()
	var res StepResult
	if err := json.Unmarshal([]byte(r.call(c, ToolMissionNext, `{}`)), &res); err != nil {
		r.t.Fatal(err)
	}
	return res
}

func (r *missionRig) startedIDs(res StepResult) []int64 {
	var out []int64
	for _, s := range res.Started {
		out = append(out, s.TaskID)
	}
	return out
}

func (r *missionRig) waitingReason(res StepResult, id int64) string {
	for _, w := range res.Waiting {
		if w.TaskID == id {
			return w.Reason
		}
	}
	return ""
}

// eventually polls cond for a few seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (r *missionRig) waitStatus(id int64, st tasks.Status) {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for r.task(id).Status != st {
		if time.Now().After(deadline) {
			d, _ := r.store.SessionTask(tbg, "default", r.sid, id, operator)
			var ev []string
			for _, e := range d.Events {
				ev = append(ev, e.Kind+" by "+e.Actor+": "+e.Note)
			}
			r.c.mu.Lock()
			var ws []string
			for _, wid := range r.c.order {
				w := r.c.workers[wid]
				ws = append(ws, fmt.Sprintf("%s=%s err=%q", wid, w.status, w.errText))
			}
			r.c.mu.Unlock()
			r.t.Fatalf("timed out waiting for task #%d to be %s: task %+v\nevents:\n%s\nworkers: %v", id, st, d.Task, strings.Join(ev, "\n"), ws)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// done is what the operator (or a verifier with auto-close) does to a reviewed task.
func (r *missionRig) done(id int64) {
	r.t.Helper()
	if _, err := r.store.Move(tbg, "default", id, tasks.StatusDone, tasks.Placement{}, operator); err != nil {
		r.t.Fatal(err)
	}
}

// countNotices counts the notice events of a code.
func countNotices(em *recEmitter, code string) int {
	n := 0
	for _, p := range em.find("notice") {
		if b, _ := json.Marshal(p); strings.Contains(string(b), `"`+code+`"`) {
			n++
		}
	}
	return n
}
