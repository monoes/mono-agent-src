//go:build doomsday

package doomsday

import (
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

var _ StrategyRunReader = (*store.SQLStore)(nil)

func (e *fenv) reader() StrategyReader { return NewStrategyReader(e.host.store, e.core, e.prof) }

func (e *fenv) rowCounts(t *testing.T) []int {
	t.Helper()
	var out []int
	for _, tb := range []string{"doomsday_strategy_directions", "doomsday_strategy_epochs", "doomsday_strategy_judgments", "doomsday_strategy_novelty",
		"doomsday_audit", "doomsday_runs"} {
		out = append(out, e.count(t, `SELECT count(*) FROM `+tb))
	}
	return out
}

func TestReaderShowsEpochsDirectionsAndStatus(t *testing.T) {
	e := newFenv(t, nil)
	e.propose(t, "Build the importer")
	e.model.set(types.CritPlausiblyMet)
	e.tickAfter(t, 31*time.Minute)
	r := e.reader()
	st, err := r.ReadStatus(bg, 0)
	if err != nil || st.Generation != e.gen || st.RunState != "running" || st.Epochs != 1 || st.LatestEpoch == nil || st.LatestEpoch.State != "applied" ||
		st.Directions["proposed"] != 1 || st.Criteria["plausibly-met"] != 1 {
		t.Fatalf("%+v %v", st, err)
	}
	ep, err := r.ReadEpochs(bg, 0, 10)
	if err != nil || ep.Total != 1 || len(ep.Epochs) != 1 || ep.Epochs[0].JudgmentHash == "" || len(ep.Epochs[0].JudgmentHash) != 12 {
		t.Fatalf("%+v %v", ep, err)
	}
	dv, err := r.ReadDirections(bg, e.gen, 10)
	if err != nil || dv.Total != 1 || dv.Directions[0].Title != "Build the importer" || len(dv.Directions[0].Key) != 12 {
		t.Fatalf("%+v %v", dv, err)
	}
}

func TestReaderBoundsTheListAndKeepsTheNewest(t *testing.T) {
	e := newFenv(t, nil)
	for _, title := range []string{"idea one", "idea two", "idea three"} {
		e.propose(t, title)
	}
	dv, err := e.reader().ReadDirections(bg, 0, 2)
	if err != nil || dv.Total != 3 || len(dv.Directions) != 2 || !dv.Truncated || dv.Directions[1].Title != "idea three" {
		t.Fatalf("%+v %v", dv, err)
	}
}

func TestReaderReportsTamperingInsteadOfHidingIt(t *testing.T) {
	e := newFenv(t, nil)
	e.propose(t, "Build the importer")
	e.tickAfter(t, 31*time.Minute)
	for _, q := range []string{`DROP TRIGGER doomsday_strategy_directions_forward_only`, `UPDATE doomsday_strategy_directions SET title = 'evil'`} {
		if _, err := e.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	r := e.reader()
	if v, err := r.ReadDirections(bg, 0, 10); err != nil || !v.Tampered || len(v.Directions) != 0 || strings.Contains(v.Note, "evil") {
		t.Fatalf("%+v %v", v, err)
	}
	if v, err := r.ReadStatus(bg, 0); err != nil || !v.Tampered {
		t.Fatalf("%+v %v", v, err)
	}
	// The epochs table was not edited: its verb still shows them.
	if v, err := r.ReadEpochs(bg, 0, 10); err != nil || v.Tampered || v.Total != 1 {
		t.Fatalf("%+v %v", v, err)
	}
}

// The verbs write nothing: no strategy table, no audit row and no run row changes across every verb, in both formats, and
// after a Stop and with a generation that does not exist.
func TestStrategyVerbsWriteNothingToTheDatabase(t *testing.T) {
	e := newFenv(t, nil)
	e.propose(t, "Build the importer")
	e.tickAfter(t, 31*time.Minute)
	before := e.rowCounts(t)
	for _, asJSON := range []bool{false, true} {
		d := StrategyDeps{Reader: e.reader, JSONOutput: func() bool { return asJSON }}
		for _, args := range [][]string{{"status"}, {"epochs"}, {"directions"}, {"status", "--generation", "99"}, {"epochs", "--generation", "99"}} {
			if out, err := runStrategy(t, d, args...); err != nil || out == "" {
				t.Fatalf("%v: %q %v", args, out, err)
			}
		}
	}
	same := func(when string) {
		t.Helper()
		after := e.rowCounts(t)
		for i := range before {
			if before[i] != after[i] {
				t.Fatalf("%s: row counts changed: %v -> %v", when, before, after)
			}
		}
	}
	same("running")
	e.stopRun(t)
	before = e.rowCounts(t)
	for _, v := range StrategyVerbs {
		if _, err := runStrategy(t, StrategyDeps{Reader: e.reader}, v); err != nil {
			t.Fatal(err)
		}
	}
	same("stopped")
}
