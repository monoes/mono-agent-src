package doomsday

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/monomind"
)

// The finish verbs against the real store, the real witness (sequencer), real token revocation and a fake kernel. The
// fakes named here are the kernel and the model only: criteria really run (with a recording Placer in place of the
// cgroup placement, which MONOAGENT_DOOMSDAY_REAL_CGROUP=1 tests separately), the ledger is the real SQLite one.

const finishCharterJSON = `{"goal":"ship it","limits":{"budget_micro":5000000},
"criteria":[{"kind":"command","command":"echo hello","expected":"hello"},{"kind":"rubric","text":"the docs read well"}],
"pins":{"boss":{"provider":"p","model":"m","version":"1"},"verifier":{"provider":"p","model":"v","version":"2"},"judging_policy":"j1"}}`

type finRig struct {
	*wireRig
	fe      *FinishEngine
	subject string
	placed  int
	ended   int
	say     string // what the model answers
	model   string // what the runner reports
}

func newFinRig(t *testing.T, charter string) *finRig {
	t.Helper()
	skipWithoutSh(t)
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.w.DB = db.DB
	w.charter = writeCharter(t, charter)
	r := &finRig{wireRig: w, say: `{"verdict":"ok","reason":"fine"}`, model: "v", subject: t.TempDir()}
	if err := os.WriteFile(filepath.Join(r.subject, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.fe = w.w.FinishEngine()
	t.Cleanup(func() { finish.RemoveTree(r.fe.area()) }) // sealed snapshots are read-only trees
	r.fe.VerifierExec = func(_ context.Context, _ monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
		on(monomind.Event{Type: monomind.EventStart, Runtime: "p", Model: r.model})
		return &monomind.TurnResult{SawDone: true, ResultText: r.say}, nil
	}
	scratch := t.TempDir()
	r.fe.SandboxFor = func(int64) func(context.Context, int64) (finish.Sandbox, func(), error) {
		return func(context.Context, int64) (finish.Sandbox, func(), error) {
			return finish.Sandbox{Scratch: scratch, Place: func(*exec.Cmd) (func(), error) { r.placed++; return func() {}, nil }}, func() { r.ended++ }, nil
		}
	}
	w.deps.Engine = func() Engine { return r.fe }
	return r
}

func (r *finRig) do(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return run(t, r.deps, "", args...)
}

func (r *finRig) mustDo(t *testing.T, args ...string) string {
	t.Helper()
	out, err := r.do(t, args...)
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return out
}

func TestFinishFromBeginToFinishedThroughTheVerbs(t *testing.T) {
	r := newFinRig(t, finishCharterJSON)
	r.start(t)
	out := r.mustDo(t, "finish", "--begin")
	if !strings.Contains(out, "finish begun") || !strings.Contains(out, "quiescent") {
		t.Fatalf("%s", out)
	}
	if runs, _ := r.w.Store.OpenRuns(context.Background(), "p1"); len(runs) != 0 {
		t.Fatalf("the run is still open after the finish's stop: %v", runs)
	}
	if run, _ := r.w.Store.GetRun(context.Background(), "p1", 1); run.Cause != store.FinishCause {
		t.Fatalf("the stop does not carry cause=finish: %+v", run)
	}
	// the first call names the subject; a rubric keeps the finish waiting
	out = r.mustDo(t, "finish", "--subject", r.subject)
	if !strings.Contains(out, "NOT finished: verifying") || !strings.Contains(out, "c1") || strings.Contains(out, "finished (") {
		t.Fatalf("%s", out)
	}
	if r.placed != 1 || r.ended != 1 {
		t.Fatalf("placed=%d ended=%d", r.placed, r.ended)
	}
	if st := r.mustDo(t, "status"); !strings.Contains(st, "verifying") {
		t.Fatalf("status does not show the finish: %s", st)
	}
	list := r.mustDo(t, "rubric")
	i := strings.Index(list, "dossier hash: ")
	if i < 0 || !strings.Contains(list, "the docs read well") {
		t.Fatalf("%s", list)
	}
	hash := strings.Fields(list[i+len("dossier hash: "):])[0]
	if _, err := r.do(t, "rubric", "c1", "approve", "--dossier", "00000000"); err == nil {
		t.Fatal("a decision on a dossier that was not shown")
	}
	if _, err := r.do(t, "rubric", "c1", "approve"); err == nil {
		t.Fatal("a decision without a dossier hash")
	}
	if out = r.mustDo(t, "rubric", "c1", "approve", "--dossier", hash[:12], "--note", "ok"); !strings.Contains(out, "does not finish by itself") {
		t.Fatal(out)
	}
	if out = r.mustDo(t, "finish"); !strings.HasPrefix(out, "finished (round 1)") {
		t.Fatalf("%s", out)
	}
	if st := r.mustDo(t, "status"); !strings.Contains(st, "finished") {
		t.Fatalf("status: %s", st)
	}
	if out, err := r.do(t, "finish"); err != nil || !strings.HasPrefix(out, "finished") {
		t.Fatalf("a finished finish is not idempotent: %v %s", err, out) // the recorded subject is kept
	}
	if _, err := os.Stat(filepath.Join(r.fe.area(), "snapshots", "g1-r1")); err == nil {
		t.Fatal("the sealed snapshot of a finished finish was kept")
	}
}

func TestFinishWithoutDistinctVerifierReviewIsRefusedAndParks(t *testing.T) {
	r := newFinRig(t, strings.Replace(finishCharterJSON, `"rubric","text":"the docs read well"`, `"command","command":"true","expected":"exit:0"`, 1))
	r.start(t)
	r.mustDo(t, "finish", "--begin")
	r.model = "other" // the runner resolved the pin to another model
	out := r.mustDo(t, "finish", "--subject", r.subject)
	if !strings.Contains(out, "NOT finished: parked") || !strings.Contains(out, "verifier-changed") || strings.Contains(out, "finished (") {
		t.Fatalf("%s", out)
	}
	r.model = "v"
	r.say = `{"verdict":"block","reason":"too-easy"}`
	if out = r.mustDo(t, "finish", "--reverify"); !strings.Contains(out, "verifier-blocked") {
		t.Fatalf("a block did not park: %s", out)
	}
	r.say = `approved`
	if out = r.mustDo(t, "finish", "--reverify"); !strings.Contains(out, "NOT finished") || strings.Contains(out, "finished (") {
		t.Fatalf("free text finished: %s", out)
	}
	if _, err := r.do(t, "finish", "--reverify"); err == nil {
		t.Fatal("a fourth round was allowed")
	}
}

func TestBeginIsRefusedByItsPreconditionsAndRecordsTheDenial(t *testing.T) {
	r := newFinRig(t, strings.Replace(finishCharterJSON, `"criteria":[{"kind":"command","command":"echo hello","expected":"hello"},{"kind":"rubric","text":"the docs read well"}],`, "", 1))
	r.start(t)
	out, err := r.do(t, "finish", "--begin")
	if err == nil || !strings.Contains(out, "NOT begun") || !strings.Contains(out, "no criteria") {
		t.Fatalf("%v %s", err, out)
	}
	if n := count2(t, r, `SELECT COUNT(*) FROM doomsday_audit WHERE kind = 'denied-attempt'`); n != 1 {
		t.Fatalf("%d denial rows", n)
	}
	if runs, _ := r.w.Store.OpenRuns(context.Background(), "p1"); len(runs) != 1 || runs[0].State != store.StateRunning {
		t.Fatalf("a refused begin changed the run: %v", runs)
	}
	// nothing to finish: no run
	r2 := newFinRig(t, finishCharterJSON)
	if out, _ := r2.do(t, "finish", "--begin"); !strings.Contains(out, "no running run") {
		t.Fatalf("%s", out)
	}
	// an open person item blocks it
	r3 := newFinRig(t, finishCharterJSON)
	r3.start(t)
	if _, err := r3.w.DB.Exec(`INSERT INTO coder_session_questions (id, profile_id, conversation_id, class, status, created_at) VALUES (1,'p1','c','risky_action','open','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	if _, err := r3.w.DB.Exec(`INSERT INTO coder_session_person_items (profile_id, conversation_id, kind, ref_id, created_at) VALUES ('p1','c','parked_question',1,'2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	out, err = r3.do(t, "finish", "--begin")
	if err == nil || !strings.Contains(out, "person item") {
		t.Fatalf("an open person item did not block the finish: %v %s", err, out)
	}
	if runs, _ := r3.w.Store.OpenRuns(context.Background(), "p1"); len(runs) != 1 || runs[0].State != store.StateRunning {
		t.Fatalf("a refused begin changed the run: %v", runs)
	}
}

func count2(t *testing.T, r *finRig, q string) int {
	t.Helper()
	var n int
	if err := r.w.DB.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestStopDuringVerifyingEndsTheFinishForGood(t *testing.T) {
	r := newFinRig(t, finishCharterJSON)
	r.start(t)
	r.mustDo(t, "finish", "--begin")
	r.mustDo(t, "finish", "--subject", r.subject) // verifying, waiting for the rubric
	if out := r.mustDo(t, "stop"); !strings.Contains(out, "finish was ended") {
		t.Fatalf("%s", out)
	}
	if out, err := r.do(t, "finish"); err != nil || strings.Contains(out, "finished (") {
		t.Fatalf("%v %s", err, out)
	}
	if n := count2(t, r, `SELECT COUNT(*) FROM doomsday_audit WHERE kind = 'finish.commit'`); n != 0 {
		t.Fatal("a stopped finish committed")
	}
	list, _ := r.do(t, "rubric")
	i := strings.Index(list, "dossier hash: ")
	if i >= 0 {
		t.Fatalf("a stopped finish still lists a rubric: %s", list)
	}
}

func TestSubjectRulesAndNoFinishOfAnOrdinaryStop(t *testing.T) {
	r := newFinRig(t, finishCharterJSON)
	r.start(t)
	if out, err := r.do(t, "finish", "--subject", r.subject); err == nil {
		t.Fatalf("a running run was frozen: %s", out)
	}
	r.mustDo(t, "finish", "--begin")
	if _, err := r.do(t, "finish"); err == nil || !strings.Contains(err.Error(), "--subject") {
		t.Fatalf("the first call needs the subject: %v", err)
	}
	if _, err := r.do(t, "finish", "--subject", r.w.RunDir.Dir); err == nil {
		t.Fatal("the harness's own state was accepted as a subject")
	}
	if _, err := r.do(t, "finish", "--subject", filepath.Join(r.subject, "a.txt")); err == nil {
		t.Fatal("a file was accepted as a subject")
	}
	r.mustDo(t, "finish", "--subject", r.subject)
	if _, err := r.do(t, "finish", "--subject", t.TempDir()); err == nil || !strings.Contains(err.Error(), "cannot be changed") {
		t.Fatalf("the subject was changed: %v", err)
	}
}

// T_finish: a round that does not end within its deadline parks (reason deadline) instead of hanging or
// leaving the finish half-written.
func TestARoundThatOutlivesTFinishParksWithADeadlineReason(t *testing.T) {
	r := newFinRig(t, strings.Replace(finishCharterJSON, `"rubric","text":"the docs read well"`, `"command","command":"true","expected":"exit:0"`, 1))
	r.start(t)
	r.mustDo(t, "finish", "--begin")
	r.fe.RoundTimeout = 3 * time.Second
	r.fe.SandboxFor = func(int64) func(context.Context, int64) (finish.Sandbox, func(), error) {
		return func(ctx context.Context, _ int64) (finish.Sandbox, func(), error) {
			<-ctx.Done() // the verification unit never comes up
			return finish.Sandbox{}, nil, ctx.Err()
		}
	}
	out := r.mustDo(t, "finish", "--subject", r.subject)
	if !strings.Contains(out, "NOT finished: parked") || !strings.Contains(out, "deadline") {
		t.Fatalf("%s", out)
	}
}

func TestFinishVerbsAreOperatorOnlyAndOffWithoutTheMode(t *testing.T) {
	r := newFinRig(t, finishCharterJSON)
	r.operator = clicore.OperatorOnly("not you")
	for _, args := range [][]string{{"finish"}, {"finish", "--begin"}, {"finish", "--reverify"}, {"rubric"}, {"rubric", "c1", "approve", "--dossier", "abcdef12"}} {
		if _, err := r.do(t, args...); err == nil || exitOf(err) != 3 {
			t.Fatalf("%v was not refused as operator-only (exit 3) for a caller that is not the user: %v", args, err)
		}
	}
	// an install without the mode carries no FinishOps: every finish verb refuses as off
	off := newRig()
	off.deps.Engine = nil
	for _, args := range [][]string{{"finish", "--begin"}, {"finish"}, {"rubric"}} {
		if _, err := run(t, off.deps, "", args...); err == nil || !strings.Contains(err.Error(), "not available") {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if n := count2(t, r, `SELECT COUNT(*) FROM doomsday_audit WHERE kind LIKE 'finish.%'`); n != 0 {
		t.Fatalf("%d finish rows written by refused callers", n)
	}
}
