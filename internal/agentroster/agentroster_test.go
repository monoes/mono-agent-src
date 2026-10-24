package agentroster

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
)

func TestMain(m *testing.M) { testhome.Main(m) }

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db.DB
}

func done(text string) *monomind.TurnResult {
	return &monomind.TurnResult{SawDone: true, ResultText: text, StopReason: monomind.StopEndTurn}
}

func failed(code, msg string) *monomind.TurnResult {
	return &monomind.TurnResult{SawDone: true, Err: &monomind.ProtocolError{Code: code, Message: msg}}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		res    *monomind.TurnResult
		err    error
		status string
	}{
		{"plain ok", done("ok"), nil, StatusOK},
		{"ok with punctuation", done("**OK.**\n"), nil, StatusOK},
		{"chatty reply", done("Sure! ok, here you go"), nil, StatusOKUnexpected},
		{"auth code", failed(monomind.ErrAuth, "run codex login"), nil, StatusAuth},
		{"claude not logged in", failed(monomind.ErrRunnerError, "Not logged in · Please run /login"), nil, StatusAuth},
		// Real messages from runtimes with no login (2026-09-29, scratch HOME).
		{"grok not signed in", failed(monomind.ErrRunnerError, "GrokAgentRunner: grok failed (exit 1): Not signed in. To authenticate without a browser, run:\n  grok login --device-code"), nil, StatusAuth},
		{"crush no provider", failed(monomind.ErrRunnerError, "CrushAgentRunner: crush run failed (exit 1)\nstderr: ERROR No providers configured - please run 'crush' to set up a provider interactively."), nil, StatusAuth},
		{"pi no api key", failed(monomind.ErrRunnerError, "PiAgentRunner: pi failed (exit 1)\nstderr: No API key found for the selected model.\n\nUse /login to log into a provider"), nil, StatusAuth},
		// Protocol rev 27 (monomind 2.21): a key that was never set is
		// auth/fatal on every runtime; before, some came as runner-error.
		{"rev 27 missing key", &monomind.TurnResult{Err: &monomind.ProtocolError{Code: monomind.ErrAuth, Fatal: true, Message: "OpenCodeAgentRunner: missing API key for provider openrouter"}}, nil, StatusAuth},
		{"missing key as runner error", failed(monomind.ErrRunnerError, "missing API key for provider openrouter"), nil, StatusAuth},
		{"hermes no provider", failed(monomind.ErrRunnerError, "hermes: no inference provider configured"), nil, StatusAuth},
		// Text a runner only attached never decides the class.
		{"unclassified model text", failed(monomind.ErrRunnerError, "HermesAgentRunner: exit 1\n[output below is not classified]\nThe API returned 401 unauthorized"), nil, StatusError},
		{"quota code", failed(monomind.ErrQuota, "x"), nil, StatusQuota},
		{"rate-limited code", failed(monomind.ErrRateLimited, "slow down"), nil, StatusRateLimited},
		{"rate limit text", failed(monomind.ErrRunnerError, "429 Too Many Requests"), nil, StatusRateLimited},
		{"credits text", failed(monomind.ErrRunnerError, "credit balance is too low"), nil, StatusQuota},
		{"unknown model", failed(monomind.ErrRunnerError, "The model `gpt-9` does not exist or you do not have access to it"), nil, StatusModelUnavailable},
		{"model not found", failed(monomind.ErrRunnerError, "model_not_found"), nil, StatusModelUnavailable},
		{"missing binary", failed(monomind.ErrMissingBinary, "grok not on PATH"), nil, StatusMissingBinary},
		{"timeout code", failed(monomind.ErrTimeout, "timed out"), nil, StatusTimeout},
		{"timeout stop", &monomind.TurnResult{SawDone: true, StopReason: monomind.StopTimeout}, nil, StatusTimeout},
		{"cancelled", failed(monomind.ErrCancelled, ""), nil, StatusCancelled},
		{"other runner error", failed(monomind.ErrRunnerError, "segfault"), nil, StatusError},
		{"never finished", &monomind.TurnResult{ResultText: "ok"}, nil, StatusError},
		{"empty reply", done(""), nil, StatusError},
		{"error in the reply text", done("Not logged in · Please run /login"), nil, StatusAuth},
		{"exec failed to start", nil, errors.New("fork failed"), StatusError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := Classify(c.res, c.err)
			if got != c.status {
				t.Fatalf("status = %q, want %q", got, c.status)
			}
		})
	}
}

func TestStoreRoundTripManualAndOutcome(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	at := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)

	if err := AddManual(ctx, db, "codex", "gpt-x"); err != nil {
		t.Fatal(err)
	}
	r := Result{Runtime: "codex", Model: "gpt-x", Label: "GPT X", EffortLevels: []string{"low", "high"},
		Status: StatusOK, LatencyMs: 1400, CostUSD: 0.002, HasCost: true, Source: SourceListed, ValidatedAt: at}
	if err := Save(ctx, db, r); err != nil {
		t.Fatal(err)
	}
	got, err := List(ctx, db)
	if err != nil || len(got) != 1 {
		t.Fatalf("List = %v, %v", got, err)
	}
	g := got[0]
	if g.Source != SourceManual {
		t.Errorf("a listed save must keep the manual source, got %q", g.Source)
	}
	if g.Status != StatusOK || g.LatencyMs != 1400 || !g.HasCost || len(g.EffortLevels) != 2 || !g.ValidatedAt.Equal(at) {
		t.Errorf("round trip lost fields: %+v", g)
	}

	// A worker's transient 429 is counted but never demotes the row.
	if err := RecordOutcome(ctx, db, "codex", "gpt-x", StatusRateLimited, "429 Too Many Requests", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, _ = List(ctx, db); got[0].Status != StatusOK {
		t.Errorf("a rate limit must not demote the validated row, got %q", got[0].Status)
	}
	if err := RecordOutcome(ctx, db, "codex", "gpt-x", StatusQuota, "usage limit", at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, _ = List(ctx, db)
	if got[0].Status != StatusQuota {
		t.Errorf("a real quota failure must mark the row failed, got %q", got[0].Status)
	}
	if err := RecordOutcome(ctx, db, "codex", "gpt-x", StatusOK, "", at); err != nil {
		t.Fatal(err)
	}
	var s, f int
	if err := db.QueryRow(`SELECT successes, failures FROM agent_model_outcomes WHERE runtime='codex'`).Scan(&s, &f); err != nil {
		t.Fatal(err)
	}
	if s != 1 || f != 2 {
		t.Errorf("outcomes = %d/%d, want 1/2", s, f)
	}
}

func strp(s string) *string { return &s }

func scanOf(entries ...monomind.ScanEntry) *monomind.ScanResult {
	return &monomind.ScanResult{V: 1, Agents: entries}
}

func TestBuildStates(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	scan := scanOf(
		monomind.ScanEntry{ID: "claude", Installed: true, Version: strp("2.0"), LoginHint: strp("claude /login")},
		monomind.ScanEntry{ID: "codex", Installed: true, Version: strp("0.60")},
		monomind.ScanEntry{ID: "grok", Installed: false},
		monomind.ScanEntry{ID: "qwen", Installed: true},
	)
	results := []Result{
		{Runtime: "claude", Model: "fresh", Status: StatusOK, RuntimeVersion: "2.0", ValidatedAt: now.Add(-time.Hour)},
		{Runtime: "claude", Model: "old", Status: StatusOK, RuntimeVersion: "2.0", ValidatedAt: now.Add(-8 * 24 * time.Hour)},
		{Runtime: "claude", Model: "bad", Status: StatusAuth, ValidatedAt: now},
		{Runtime: "codex", Model: "upgraded", Status: StatusOKUnexpected, RuntimeVersion: "0.59", ValidatedAt: now},
		{Runtime: "codex", Model: "typed", Status: StatusUntested, Source: SourceManual},
		{Runtime: "grok", Model: "gone", Status: StatusOK, ValidatedAt: now},
		{Runtime: "claude", Model: "busy", Status: StatusRateLimited, RuntimeVersion: "2.0", ValidatedAt: now},
	}
	rs := Build(results, scan, now, 0)
	want := map[string]string{
		"claude/fresh": StateReady, "claude/old": StateStale, "claude/bad": StateFailed,
		"codex/upgraded": StateStale, "codex/typed": StateUntested, "grok/gone": StateFailed,
		"claude/busy": StateStale, // rate-limited: transient, re-checked
	}
	seen := map[string]bool{}
	for _, rr := range rs {
		seen[rr.Runtime] = true
		for _, e := range rr.Models {
			if w := want[rr.Runtime+"/"+e.Model]; e.State != w {
				t.Errorf("%s/%s state = %q, want %q", rr.Runtime, e.Model, e.State, w)
			}
		}
		if rr.Runtime == "claude" && (rr.Ready != 1 || rr.LoginHint == "") {
			t.Errorf("claude roster = %+v", rr)
		}
		for _, e := range rr.Models {
			if e.Model == "busy" && e.StaleReason != StatusRateLimited {
				t.Errorf("rate-limited stale reason = %q", e.StaleReason)
			}
		}
	}
	if !seen["qwen"] {
		t.Error("an installed runtime with no results must still be listed")
	}
}

func TestBuildPlan(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	scan := scanOf(
		monomind.ScanEntry{ID: "claude", Installed: true, Version: strp("2.0")},
		monomind.ScanEntry{ID: "crush", Installed: true},
		monomind.ScanEntry{ID: "codex", Installed: true},
		monomind.ScanEntry{ID: "grok", Installed: false},
	)
	list := func(_ context.Context, id, _ string) ([]monomind.RuntimeModel, error) {
		switch id {
		case "claude":
			return []monomind.RuntimeModel{{ID: "opus", Label: "Opus", EffortLevels: []string{"high"}}, {ID: "haiku", Label: "Haiku"}}, nil
		case "codex":
			return nil, errors.New("not logged in")
		}
		return nil, nil
	}
	previous := []Result{
		{Runtime: "claude", Model: "opus", Status: StatusOK, RuntimeVersion: "2.0", CostUSD: 0.01, HasCost: true, ValidatedAt: now},
		{Runtime: "crush", Model: "my-model", Status: StatusUntested, Source: SourceManual},
	}

	p := BuildPlan(context.Background(), scan, list, previous, PlanFilter{Now: now})
	got := map[string]bool{}
	for _, tg := range p.Targets {
		got[tg.Runtime+"/"+tg.Model] = true
	}
	for _, w := range []string{"claude/opus", "claude/haiku", "crush/default", "crush/my-model", "codex/default"} {
		if !got[w] {
			t.Errorf("plan is missing %s (targets %v)", w, got)
		}
	}
	// claude/opus has a stored cost; claude/haiku and codex/default are
	// priced from the built-in table; crush's two models are unknown.
	haiku, _ := TableTestCost("claude", "haiku")
	codexDefault, _ := TableTestCost("codex", DefaultModel)
	if want := 0.01 + haiku + codexDefault; p.Calls != 5 || math.Abs(p.EstCostUSD-want) > 1e-9 || p.UnknownCost != 2 || p.TableEstimated != 2 {
		t.Errorf("cost plan = calls %d, $%v (want $%v), unknown %d, table %d", p.Calls, p.EstCostUSD, want, p.UnknownCost, p.TableEstimated)
	}
	if len(p.Skipped) != 1 || p.Skipped[0].Runtime != "codex" {
		t.Errorf("skipped = %+v", p.Skipped)
	}

	p = BuildPlan(context.Background(), scan, list, previous, PlanFilter{Now: now, StaleOnly: true, Runtimes: []string{"claude", "grok"}})
	if len(p.Targets) != 1 || p.Targets[0].Model != "haiku" {
		t.Errorf("stale-only plan = %+v", p.Targets)
	}
	if len(p.Skipped) != 1 || p.Skipped[0].Runtime != "grok" {
		t.Errorf("an uninstalled runtime asked for must be reported, got %+v", p.Skipped)
	}

	p = BuildPlan(context.Background(), scan, list, previous, PlanFilter{Now: now, Runtimes: []string{"claude"}, Models: []string{"haiku", "typed-id"}})
	if len(p.Targets) != 2 || p.Targets[1].Source != SourceManual {
		t.Errorf("model filter plan = %+v", p.Targets)
	}
}

func TestRunSerialPerRuntimeAndSaves(t *testing.T) {
	var running sync.Map // runtime -> *int32
	var overlap atomic.Bool
	exec := func(ctx context.Context, o monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
		v, _ := running.LoadOrStore(o.Runtime, new(int32))
		n := v.(*int32)
		if atomic.AddInt32(n, 1) > 1 {
			overlap.Store(true)
		}
		defer atomic.AddInt32(n, -1)
		// Validated the way chat runs it: sandboxed (when monomind can).
		if o.MaxTurns != 1 || o.Prompt != TestPrompt || o.Cwd == "" || len(o.Tools) != 0 || o.Sandbox != monomind.TurnSandboxMode {
			t.Errorf("unexpected exec options %+v", o)
		}
		if o.Runtime == "b" && o.Model == "" {
			t.Error("a listed model must be passed as --model")
		}
		if o.Runtime == "a" && o.Model != "" {
			t.Error(`the "default" model must run without --model`)
		}
		time.Sleep(5 * time.Millisecond)
		on(monomind.Event{Type: monomind.EventAssistant, Text: "ok"})
		if o.Model == "broken" {
			return failed(monomind.ErrAuth, "login"), nil
		}
		if o.Model == "busy" {
			return failed(monomind.ErrRateLimited, "slow down"), nil
		}
		return done("ok"), nil
	}
	targets := []Target{
		{Runtime: "a", Model: DefaultModel},
		{Runtime: "b", Model: "m1"}, {Runtime: "b", Model: "m2"}, {Runtime: "b", Model: "broken"}, {Runtime: "b", Model: "busy"},
	}
	var saved []Result
	var lines []Line
	sum := Run(context.Background(), targets, RunOptions{RunID: "r1", Exec: exec, Concurrency: 2,
		Save: func(r Result) error { saved = append(saved, r); return nil }}, func(l Line) { lines = append(lines, l) })

	if overlap.Load() {
		t.Error("two tests of one runtime ran at the same time")
	}
	if sum.OK != 3 || sum.Failed != 2 || sum.Planned != 5 {
		t.Errorf("summary = %+v", sum)
	}
	if len(saved) != 5 {
		t.Errorf("saved %d results, want 5", len(saved))
	}
	for _, r := range saved {
		if r.Model == "busy" && r.Status != StatusRateLimited {
			t.Errorf("validate stored a rate limit as %q, want %q", r.Status, StatusRateLimited)
		}
	}
	if last := lines[len(lines)-1]; last.Type != "validate.done" || last.Summary == nil {
		t.Errorf("last line = %+v", last)
	}
	for _, r := range saved {
		if r.RunID != "r1" || r.ValidatedAt.IsZero() {
			t.Errorf("result missing run id or time: %+v", r)
		}
	}
}

func TestRunCancelStoresNothingForUnrun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	exec := func(ctx context.Context, o monomind.ExecOptions, _ func(monomind.Event)) (*monomind.TurnResult, error) {
		cancel()
		<-ctx.Done()
		return failed(monomind.ErrCancelled, "cancelled"), nil
	}
	var saved int
	sum := Run(ctx, []Target{{Runtime: "a", Model: "x"}, {Runtime: "a", Model: "y"}},
		RunOptions{Exec: exec, Save: func(Result) error { saved++; return nil }}, func(Line) {})
	if sum.Cancelled != 2 || saved != 0 {
		t.Errorf("summary %+v, saved %d; want 2 cancelled, 0 saved", sum, saved)
	}
}

func strPtr(s string) *string { return &s }
func i64(n int64) *int64      { return &n }
func f64(f float64) *float64  { return &f }

func TestRunUsesAgentTestJSONAndFallsBack(t *testing.T) {
	var execCalls, testCalls atomic.Int32
	exec := func(ctx context.Context, o monomind.ExecOptions, _ func(monomind.Event)) (*monomind.TurnResult, error) {
		execCalls.Add(1)
		return done("ok"), nil
	}
	test := func(ctx context.Context, runtime, model string, timeout time.Duration) (*monomind.AgentTestResult, error) {
		testCalls.Add(1)
		switch model {
		case "":
			t.Errorf("runtime %s: the \"default\" model must be passed as \"\"", runtime)
		case "broken":
			return nil, errors.New("unparseable output")
		case "unsupported":
			return nil, fmt.Errorf("%w: %w", ErrUseExec, monomind.ErrAgentTestUnsupported)
		case "slow":
			return nil, fmt.Errorf("no result within 45s: %w", context.DeadlineExceeded)
		case "gone":
			return &monomind.AgentTestResult{Status: "model_unavailable", LatencyMs: 300,
				Error: &struct {
					Code      string `json:"code"`
					Message   string `json:"message"`
					LoginHint string `json:"login_hint,omitempty"`
				}{Code: "model-unavailable", Message: "issue with the selected model"}}, nil
		case "nokey":
			return &monomind.AgentTestResult{Status: "error", LatencyMs: 400,
				Error: &struct {
					Code      string `json:"code"`
					Message   string `json:"message"`
					LoginHint string `json:"login_hint,omitempty"`
				}{Code: "runner-error", Message: "PiAgentRunner: pi failed (exit 1)\nstderr: No API key found for the selected model."}}, nil
		case "future":
			return &monomind.AgentTestResult{Status: "sandbox_denied", LatencyMs: 10,
				Error: &struct {
					Code      string `json:"code"`
					Message   string `json:"message"`
					LoginHint string `json:"login_hint,omitempty"`
				}{Code: "sandbox", Message: "write outside the workspace"}}, nil
		}
		if timeout != 30*time.Second {
			t.Errorf("timeout = %v", timeout)
		}
		return &monomind.AgentTestResult{Status: "ok", Reply: strPtr("ok"), LatencyMs: 1430, LatencyFirstMs: i64(812),
			InputTokens: i64(12), OutputTokens: i64(1), CostUSD: f64(0.0001), CostEstimated: true, RuntimeVersion: strPtr("0.52.0")}, nil
	}
	targets := []Target{{Runtime: "a", Model: "m"}, {Runtime: "a", Model: "gone"}, {Runtime: "b", Model: "broken"},
		{Runtime: "b", Model: "unsupported"}, {Runtime: "b", Model: "slow"}, {Runtime: "c", Model: "future"}, {Runtime: "d", Model: "nokey"}}
	got := map[string]Result{}
	var mu sync.Mutex
	sum := Run(context.Background(), targets, RunOptions{Exec: exec, Test: test, Timeout: 30 * time.Second,
		Save: func(r Result) error { mu.Lock(); got[r.Model] = r; mu.Unlock(); return nil }}, func(Line) {})

	if testCalls.Load() != 7 || execCalls.Load() != 1 {
		t.Errorf("test calls %d, exec calls %d; want 7 and 1 (only ErrUseExec falls back)", testCalls.Load(), execCalls.Load())
	}
	if r := got["m"]; r.Status != StatusOK || r.LatencyMs != 1430 || r.LatencyFirstMs != 812 || r.TokensIn != 12 ||
		!r.HasCost || !r.CostEstimated || r.RuntimeVersion != "0.52.0" {
		t.Errorf("mapped result = %+v", r)
	}
	if r := got["gone"]; r.Status != StatusModelUnavailable || r.Detail != "issue with the selected model" {
		t.Errorf("model_unavailable = %+v", r)
	}
	if r := got["broken"]; r.Status != StatusError || !strings.Contains(r.Detail, "unparseable output") {
		t.Errorf("an agent test that ran but gave no result must not re-run through exec: %+v", r)
	}
	if r := got["unsupported"]; r.Status != StatusOK {
		t.Errorf("fallback result = %+v", r)
	}
	if r := got["slow"]; r.Status != StatusTimeout {
		t.Errorf("deadline = %+v, want timeout", r)
	}
	if r := got["future"]; r.Status != StatusError || !strings.Contains(r.Detail, "sandbox_denied") || !strings.Contains(r.Detail, "write outside the workspace") {
		t.Errorf("unknown status = %+v, want both the status and monomind's message", r)
	}
	if r := got["nokey"]; r.Status != StatusAuth {
		t.Errorf("a sign-in message monomind calls error must still be auth, got %+v", r)
	}
	if sum.OK != 2 || sum.Failed != 5 {
		t.Errorf("summary = %+v", sum)
	}
}

func TestSaveKeepsCostEstimated(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if err := Save(ctx, db, Result{Runtime: "a", Model: "m", Status: StatusOK, CostUSD: 0.01, HasCost: true, CostEstimated: true, ValidatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, _ := List(ctx, db)
	if len(got) != 1 || !got[0].CostEstimated {
		t.Errorf("cost_estimated lost: %+v", got)
	}
}

func TestAgentTestFuncKeepsSandboxedRuntimesOnExec(t *testing.T) {
	if AgentTestFunc(monomind.NewCapabilitySet("2.18.4", "agent-exec"), "", nil) != nil {
		t.Error("without agent-test-json there must be no TestFunc")
	}
	test := AgentTestFunc(monomind.NewCapabilitySet("2.18.5", monomind.CapAgentTestJSON), "/nonexistent/monomind", nil)
	if test == nil {
		t.Fatal("agent-test-json must give a TestFunc")
	}
	// codex is sandboxed through the env path on this monomind: exec it.
	if _, err := test(context.Background(), "codex", "", time.Second); !errors.Is(err, ErrUseExec) {
		t.Errorf("codex: err = %v, want ErrUseExec", err)
	}
	// claude has no monomind sandbox: agent test (which fails here on the
	// fake binary path, proving it was tried).
	if _, err := test(context.Background(), "claude", "", time.Second); err == nil || errors.Is(err, ErrUseExec) {
		t.Errorf("claude: err = %v, want an agent test error", err)
	}

	// monomind 2.19.0 has agent exec --sandbox, but passes it only for a
	// mode the runtime's scan lists. claude lists only "full", so exec
	// would not sandbox it either: agent test. codex lists the mode: exec.
	scan := &monomind.ScanResult{Agents: []monomind.ScanEntry{
		{ID: "claude", Installed: true, SandboxModes: []string{monomind.SandboxFull}},
		{ID: "codex", Installed: true, SandboxModes: []string{monomind.SandboxReadOnly, monomind.SandboxWorkspaceWrite, monomind.SandboxFull}},
	}}
	v219 := AgentTestFunc(monomind.NewCapabilitySet("2.19.0", monomind.CapAgentTestJSON, monomind.CapAgentExecSandbox), "/nonexistent/monomind", SandboxModes(scan))
	if _, err := v219(context.Background(), "claude", "", time.Second); err == nil || errors.Is(err, ErrUseExec) {
		t.Errorf("2.19.0 claude (modes [full]): err = %v, want an agent test error", err)
	}
	if _, err := v219(context.Background(), "codex", "", time.Second); !errors.Is(err, ErrUseExec) {
		t.Errorf("2.19.0 codex (lists workspace-write): err = %v, want ErrUseExec", err)
	}
}

func TestApplyAgentTestRateLimited(t *testing.T) {
	var r Result
	applyAgentTest(&r, &monomind.AgentTestResult{Status: monomind.ErrRateLimited})
	if r.Status != StatusRateLimited {
		t.Errorf("status = %q, want %q", r.Status, StatusRateLimited)
	}
}

// An alias of a model already listed (monomind 2.21's alias_of) is not
// tested again: it runs, and bills, the same model (M1: claude default and
// opus were both tested and paid). It rides along with its canonical
// target, so its roster row is stored from that one test and a lookup by
// the alias id (a lead running "opus") still finds the model.
func TestBuildPlanSkipsAliases(t *testing.T) {
	scan := scanOf(monomind.ScanEntry{ID: "claude", Installed: true})
	list := func(context.Context, string, string) ([]monomind.RuntimeModel, error) {
		return []monomind.RuntimeModel{{ID: "default", Label: "Default"}, {ID: "opus", Label: "Opus", AliasOf: "default"}, {ID: "haiku", Label: "Haiku"}}, nil
	}
	p := BuildPlan(context.Background(), scan, list, nil, PlanFilter{Now: time.Now()})
	var got []string
	for _, tg := range p.Targets {
		got = append(got, tg.Model)
	}
	if strings.Join(got, ",") != "default,haiku" || p.Calls != 2 {
		t.Fatalf("targets = %v (calls %d), want default and haiku only", got, p.Calls)
	}
	if a := p.Targets[0].Aliases; len(a) != 1 || a[0].Model != "opus" {
		t.Fatalf("default's aliases = %+v", a)
	}

	// Asking for the alias by id tests its canonical model.
	p = BuildPlan(context.Background(), scan, list, nil, PlanFilter{Now: time.Now(), Runtimes: []string{"claude"}, Models: []string{"opus"}})
	if len(p.Targets) != 1 || p.Targets[0].Model != "default" {
		t.Fatalf("--model opus plan = %+v", p.Targets)
	}

	calls := 0
	var saved []Result
	exec := func(context.Context, monomind.ExecOptions, func(monomind.Event)) (*monomind.TurnResult, error) {
		calls++
		return done("ok"), nil
	}
	Run(context.Background(), p.Targets, RunOptions{Exec: exec, Save: func(r Result) error { saved = append(saved, r); return nil }}, func(Line) {})
	if calls != 1 || len(saved) != 2 || saved[1].Model != "opus" || saved[1].Status != StatusOK {
		t.Fatalf("exec calls %d, saved %+v", calls, saved)
	}
	roster := Build(saved, scan, time.Now(), 0)
	var opus *Entry
	for i := range roster[0].Models {
		if roster[0].Models[i].Model == "opus" {
			opus = &roster[0].Models[i]
		}
	}
	if opus == nil || opus.State != StateReady {
		t.Fatalf("roster lookup of the alias = %+v", opus)
	}
}

func TestBuildCarriesExecutionSupport(t *testing.T) {
	no, yes := false, true
	reason := "interactive-only"
	scan := scanOf(
		monomind.ScanEntry{ID: "tui-only", Installed: true, ExecutionSupported: &no, ExecutionUnsupportedReason: &reason},
		monomind.ScanEntry{ID: "claude", Installed: true, ExecutionSupported: &yes},
		monomind.ScanEntry{ID: "codex", Installed: true},
	)
	by := map[string]RuntimeRoster{}
	for _, rr := range Build(nil, scan, time.Now(), 0) {
		by[rr.Runtime] = rr
	}
	if f := by["tui-only"]; f.ExecutionSupported == nil || *f.ExecutionSupported || f.ExecutionUnsupportedReason != reason {
		t.Errorf("tui-only = %+v", f)
	}
	if c := by["claude"]; c.ExecutionSupported == nil || !*c.ExecutionSupported || c.ExecutionUnsupportedReason != "" {
		t.Errorf("claude = %+v", c)
	}
	if by["codex"].ExecutionSupported != nil {
		t.Errorf("codex carries a value monomind did not send")
	}
}
