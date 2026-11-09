package verifier

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/monomind"
)

const (
	judgePin   = "codex/gpt-judge"
	leadPin    = "claude/lead-model"
	checkerPin = "gemini/checker-1@2026"
)

func h64(s string) string { x := sha256.Sum256([]byte(s)); return hex.EncodeToString(x[:]) }

func crit(id, kind, spec, expected string) Criterion {
	return Criterion{ID: id, Hash: h64(id + kind + spec + expected), Kind: kind, Spec: spec, Expected: expected}
}

// fakeMeter records the reserve/settle pairs.
type fakeMeter struct {
	mu       sync.Mutex
	reserved []float64
	settled  []settlement
	err      error
}

type settlement struct {
	micro int64
	known bool
}

func (m *fakeMeter) Reserve(_ context.Context, capUSD float64) (func(int64, bool), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	m.reserved = append(m.reserved, capUSD)
	return func(micro int64, known bool) {
		m.mu.Lock()
		m.settled = append(m.settled, settlement{micro, known})
		m.mu.Unlock()
	}, nil
}

// reviewer is a scripted runner.
type reviewer struct {
	mu       sync.Mutex
	calls    int
	opts     monomind.ExecOptions
	runtime  string
	model    string
	text     string
	cost     *float64
	err      error
	noResult bool
}

func (r *reviewer) exec(_ context.Context, o monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
	r.mu.Lock()
	r.calls++
	r.opts = o
	r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	on(monomind.Event{Type: monomind.EventStart, Runtime: r.runtime, Model: r.model})
	res := &monomind.TurnResult{SawDone: true, ResultText: r.text}
	if r.cost != nil {
		res.CostUSD, res.HasCostUSD = *r.cost, true
	}
	return res, nil
}

func okReviewer() *reviewer {
	return &reviewer{runtime: "gemini", model: "checker-1", text: `{"verdict":"ok","reason":"fine"}`}
}

func ptr(f float64) *float64 { return &f }

type rig struct {
	t     *testing.T
	root  string
	base  string
	meter *fakeMeter
	rev   *reviewer
	cfg   Config
	k     *Checker
	netOK bool
}

// newRig builds a checker over a fresh subject folder and scratch base. netOK is what the platform probe answers.
func newRig(t *testing.T, netOK bool) *rig {
	t.Helper()
	r := &rig{t: t, root: t.TempDir(), base: t.TempDir(), meter: &fakeMeter{}, rev: okReviewer(), netOK: netOK}
	if err := os.WriteFile(filepath.Join(r.root, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.cfg = Config{Checker: checkerPin, ScratchBase: r.base, Meter: r.meter, Exec: r.rev.exec,
		Workspace: func() (string, error) { return "/ws", nil }, NetDenyAvailable: func() bool { return r.netOK }}
	return r
}

func (r *rig) checker() *Checker {
	r.t.Helper()
	k, err := New(r.cfg)
	if err != nil {
		r.t.Fatal(err)
	}
	r.k = k
	return k
}

func (r *rig) req(cs ...Criterion) Request {
	return Request{Root: r.root, GoalHash: h64("goal"), Round: 1, Criteria: cs, JudgePin: judgePin, LeadPin: leadPin}
}

func (r *rig) run(cs ...Criterion) Result {
	r.t.Helper()
	if r.k == nil {
		r.checker()
	}
	res, err := r.k.Run(context.Background(), r.req(cs...))
	if err != nil {
		r.t.Fatal(err)
	}
	return res
}

// needsRealSandbox skips a test that starts a program where the platform cannot start one without a network.
func needsRealSandbox(t *testing.T) {
	t.Helper()
	if !platformOK || !(finish.Sandbox{}).NetEnforceable() {
		t.Skip("no unprivileged network namespace here: programs are not started")
	}
}

func scratchEntries(t *testing.T, base string) []string {
	t.Helper()
	ents, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

func fileExists(c string) Criterion { return crit(c, "file", "a.txt", "exists") }

func mustContain(t *testing.T, got string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(got, s) {
			t.Fatalf("missing %q in %q", s, got)
		}
	}
}

func dump(r Result) string { return fmt.Sprintf("%+v", r) }

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
