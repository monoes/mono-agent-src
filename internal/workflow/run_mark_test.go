package workflow

import (
	"context"
	"testing"
)

// A run started by a command that carries a mark (WithRunMark) over data that already has one (a retry) never
// ends looser than either: the stricter of the two wins, the old one on a tie, and no mark in ctx leaves the old
// one. Strictness, stricter first: other and unapproved-workflow, then every other agent kind, then chat, then
// none (issue 1455). The classes below are written out here, not read from the code under test.
func TestARunMarkNeverLoosensTheOldMark(t *testing.T) {
	class := map[string]int{
		"other": 3, "other/org": 3, UnapprovedWorkflowCaller: 3,
		"coder": 2, "apply": 2, "org": 2, "coder/org": 2, "some-future-kind": 2,
		"chat": 1,
		"":     0,
	}
	for old, oc := range class {
		for next, nc := range class {
			want := old
			if nc > oc {
				want = next
			}
			data := map[string]interface{}{"keep": 1}
			if old != "" {
				data[AICallerKey] = old
			}
			got := withRunMark(WithRunMark(context.Background(), next), data)
			gotMark, _ := got[AICallerKey].(string)
			if gotMark != want {
				t.Errorf("old %q, retrier %q: the run is marked %q, want %q", old, next, gotMark, want)
			}
			if got["keep"] != 1 {
				t.Errorf("old %q, retrier %q: the data lost a field: %v", old, next, got)
			}
			if gotOld, _ := data[AICallerKey].(string); gotOld != old {
				t.Errorf("old %q, retrier %q: the old run's data was changed to %q", old, next, gotOld)
			}
		}
	}
}
