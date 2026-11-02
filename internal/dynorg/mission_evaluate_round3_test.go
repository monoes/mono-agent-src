package dynorg

import (
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// Round 3 of group 2 (codex delta): the title is a label and the session id is the identity; the identities are
// rechecked in the record's own transaction; a current list must be the mission's actual open directions; tiny
// weights rank by the scores.

func reeval(r *missionRig, c *Conductor, current []map[string]any, cands []map[string]any) (EvaluateView, error) {
	return r.evaluate(c, ToolMissionReevaluate, evalArgs(map[string]any{"reason": "x", "current": current}, cands))
}

func TestALongDirectionTitleCanBeReevaluated(t *testing.T) {
	r, c := evalRig(t, 3)
	long := strings.Repeat("é", tasks.MaxTitleRunes-6) + " tail!"
	d := r.direction(long)
	if d.Title != long {
		t.Fatalf("stored title cut: %d runes", len([]rune(d.Title)))
	}
	cands := candidatesJSON(3)
	cands[0]["title"] = long
	v, err := reeval(r, c, []map[string]any{cur(long, d.ID)}, cands)
	if err != nil {
		t.Fatalf("a %d-rune direction cannot be re-evaluated: %v", tasks.MaxTitleRunes, err)
	}
	if len(v.Decisions) == 0 || v.Decisions[0].SessionID != d.ID {
		t.Fatalf("decisions %+v", v.Decisions)
	}
	// the title is only a label: a different label for the same id is not a refusal, the stored title is what is shown
	v, err = reeval(r, c, []map[string]any{cur("a label of my own", d.ID)}, cands)
	if err != nil {
		t.Fatalf("a wrong label refused: %v", err)
	}
	for _, x := range v.Decisions {
		if x.SessionID == d.ID && x.Title != long {
			t.Errorf("shown title %q", x.Title)
		}
	}
	// a swapped id (the label names another open direction) is still refused
	d2 := r.direction("dir 1")
	if _, err := reeval(r, c, []map[string]any{cur(long, d2.ID), cur("dir 1", d.ID)}, cands); err == nil {
		t.Fatal("swapped ids accepted")
	}
}

func TestCandidateTitleCapIsTheSessionTitleCap(t *testing.T) {
	if MaxTitleChars != tasks.MaxTitleRunes {
		t.Fatalf("candidate cap %d, session cap %d", MaxTitleChars, tasks.MaxTitleRunes)
	}
}

func TestReevaluateRefusesWhenTheDirectionChangesBeforeTheWrite(t *testing.T) {
	for name, change := range map[string]func(r *missionRig, d tasks.Session){
		"stopped": func(r *missionRig, d tasks.Session) {
			if _, err := r.store.StopSession(tbg, "default", d.ID, operator); err != nil {
				r.t.Fatal(err)
			}
		},
		"retitled": func(r *missionRig, d tasks.Session) {
			if _, err := r.store.RenameSession(tbg, "default", d.ID, "something else", operator); err != nil {
				r.t.Fatal(err)
			}
		},
		"archived": func(r *missionRig, d tasks.Session) {
			if _, err := r.store.ArchiveSession(tbg, "default", d.ID, operator); err != nil {
				r.t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, c := evalRig(t, 3)
			d0, d1 := r.direction("dir 0"), r.direction("dir 1")
			c.beforeRecord = func() { change(r, d1) }
			_, err := reeval(r, c, []map[string]any{cur("dir 0", d0.ID), cur("dir 1", d1.ID)}, candidatesJSON(3))
			if err == nil {
				t.Fatal("recorded a keep/drop for a direction that was no longer open")
			}
			if n := len(r.sessionNotes()); n != 0 {
				t.Fatalf("%d records stand", n)
			}
		})
	}
}

func TestReevaluateRefusesWhenADirectionIsAddedBeforeTheWrite(t *testing.T) {
	r, c := evalRig(t, 3)
	d0 := r.direction("dir 0")
	c.beforeRecord = func() { r.direction("dir 1") }
	if _, err := reeval(r, c, []map[string]any{cur("dir 0", d0.ID)}, candidatesJSON(3)); err == nil {
		t.Fatal("recorded a decision set that omits a direction opened meanwhile")
	}
	if n := len(r.sessionNotes()); n != 0 {
		t.Fatalf("%d records stand", n)
	}
}

func TestReevaluateRefusesAnIncompleteCurrentList(t *testing.T) {
	r, c := evalRig(t, 3)
	d0, d1 := r.direction("dir 0"), r.direction("dir 1")
	reached := false
	c.beforeRecord = func() { reached = true } // the refusal must come before the ranking, not from the write's own check
	for name, cur := range map[string][]map[string]any{
		"omits a live one": {cur("dir 0", d0.ID)},
		"empty":            {},
		"nil":              nil,
	} {
		_, err := reeval(r, c, cur, candidatesJSON(5))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("#%d", d1.ID)) && name == "omits a live one" {
			t.Errorf("%s: the omitted id is not named: %v", name, err)
		}
		if name != "omits a live one" && !strings.Contains(err.Error(), fmt.Sprintf("#%d", d0.ID)) {
			t.Errorf("%s: the omitted ids are not named: %v", name, err)
		}
	}
	if reached {
		t.Fatal("an incomplete list got as far as the write")
	}
	if n := len(r.sessionNotes()); n != 0 {
		t.Fatalf("%d records stand", n)
	}
	if _, err := reeval(r, c, []map[string]any{cur("dir 0", d0.ID), cur("dir 1", d1.ID)}, candidatesJSON(5)); err != nil {
		t.Fatalf("the complete list: %v", err)
	}
}

func TestReevaluateWithNoOpenDirectionsAcceptsAnEmptyList(t *testing.T) {
	r, c := evalRig(t, 3)
	if _, err := reeval(r, c, []map[string]any{}, candidatesJSON(3)); err != nil {
		t.Fatal(err)
	}
	stopped := r.direction("dir 9")
	if _, err := r.store.StopSession(tbg, "default", stopped.ID, operator); err != nil {
		t.Fatal(err)
	}
	if _, err := reeval(r, c, []map[string]any{}, candidatesJSON(3)); err != nil {
		t.Fatalf("a stopped direction is not open: %v", err)
	}
}

func TestTinyWeightsStillRankBySuccess(t *testing.T) {
	w := map[string]float64{CritSuccess: 5e-324}
	cs := []DirCandidate{cand("risky-better", 1.4, 5, 8, 5), cand("safe-worse", 1.0, 5, 2, 5), cand("mid", 1.2, 5, 5, 5), cand("low", 0.2, 5, 1, 5)}
	want := []string{"risky-better", "mid", "safe-worse", "low"}
	ww, err := ValidateWeights(w)
	if err != nil {
		t.Fatal(err)
	}
	perm(len(cs), func(p []int) {
		in := make([]DirCandidate, len(p))
		for i, j := range p {
			in[i] = cs[j]
		}
		got := titles(Rank(in, ww, 2))
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("order %v ranked %v, want %v", p, got, want)
		}
	})
}

// perm calls f with every permutation of 0..n-1.
func perm(n int, f func([]int)) {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	var rec func(k int)
	rec = func(k int) {
		if k == n {
			f(append([]int(nil), p...))
			return
		}
		for i := k; i < n; i++ {
			p[k], p[i] = p[i], p[k]
			rec(k + 1)
			p[k], p[i] = p[i], p[k]
		}
	}
	rec(0)
}
