package dynorg

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// recordOf parses the note an evaluation wrote and fails the test when it is not whole.
func recordOf(t *testing.T, note string) map[string]any {
	t.Helper()
	if len(note) > tasks.MaxCommentBytes {
		t.Fatalf("the note is %d bytes, over the event limit", len(note))
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(note, evaluationNote)), &rec); err != nil {
		t.Fatalf("the recorded note is not valid JSON: %v", err)
	}
	for _, k := range []string{"tool", "k", "weights", "candidates"} {
		if _, ok := rec[k]; !ok {
			t.Fatalf("the recorded note lost %q", k)
		}
	}
	return rec
}

// Twenty candidates with 48-character titles of < (six bytes each once JSON-escaped) used to be cut at 8 KiB while the
// call said recorded:true.
func TestMissionEvaluationRecordIsWholeForEscapableTitles(t *testing.T) {
	r, c := evalRig(t, 6)
	cs := candidatesJSON(MaxCandidates)
	for i, x := range cs {
		x["title"] = fmt.Sprintf("%s%02d", strings.Repeat("<", 48), i)
	}
	var cur []map[string]any
	for i := 14; i < 20; i++ {
		cur = append(cur, map[string]any{"title": cs[i]["title"], "session_id": i})
	}
	v, err := r.evaluate(c, ToolMissionReevaluate, evalArgs(map[string]any{"reason": strings.Repeat("<", 100), "current": cur, "k": 6,
		"reviewer": map[string]string{"verdict": "agree", "note": strings.Repeat("<", 60), "runtime": strings.Repeat("<", 60), "model": strings.Repeat("<", 60)}}, cs))
	if err != nil {
		t.Fatal(err)
	}
	n := r.sessionNotes()
	if !v.Recorded || len(n) != 1 {
		t.Fatalf("recorded=%v notes=%d", v.Recorded, len(n))
	}
	rec := recordOf(t, n[0])
	if len(rec["candidates"].([]any)) != MaxCandidates {
		t.Fatal("the recorded ranking lost candidates")
	}
	if rec["shrunk"] != nil {
		t.Fatal("the escapes were not avoided: the record had to be shrunk")
	}
	if _, ok := rec["reviewer"]; !ok {
		t.Fatal("the recorded note lost the reviewer's verdict")
	}
}

// Whatever does not fit is shrunk in a stated order and says so; a record that cannot fit at all is an error, never a
// cut note. (The full form of an ordinary call fits 8 KiB once the escapes are gone, so the limit is lowered here.)
func TestMissionEvaluationRecordShrinksAndSaysSoOrFails(t *testing.T) {
	var a evaluateArgs
	if err := json.Unmarshal([]byte(evalArgs(map[string]any{"reviewer": map[string]string{"verdict": "adjust", "note": strings.Repeat("n", 40)}}, candidatesJSON(MaxCandidates))), &a); err != nil {
		t.Fatal(err)
	}
	for i := range a.Candidates {
		a.Candidates[i].Title = fmt.Sprintf("%s%02d", strings.Repeat("t", 48), i)
	}
	w := map[string]float64{CritSuccess: 3, CritCost: 1, CritRisk: 1, CritEffort: 1}
	view := EvaluateView{K: 3, Weights: w, Ranking: Rank(a.Candidates, w, 3)}
	full, err := evaluationRecordWithin(1<<20, ToolMissionEvaluate, a, view)
	if err != nil || recordOf(t, full)["shrunk"] != nil {
		t.Fatalf("the full form: %v", err)
	}
	note, err := evaluationRecordWithin(len(full)-1, ToolMissionEvaluate, a, view)
	if err != nil {
		t.Fatal(err)
	}
	if len(note) >= len(full) || recordOf(t, note)["shrunk"] == nil {
		t.Fatalf("a record over the limit was not shrunk and marked (%d bytes, full %d)", len(note), len(full))
	}
	if n, err := evaluationRecordWithin(60, ToolMissionEvaluate, a, view); err == nil {
		t.Fatalf("a record of %d bytes was returned for a limit of 60", len(n))
	}
}
