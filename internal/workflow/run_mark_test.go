package workflow

import (
	"context"
	"reflect"
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
		"":     0, "  ": 0, "\t": 0,
		" chat ": 1, " coder": 2, "other ": 3,
		// Ranked by the kind before the slash, whatever follows it.
		"chat/coder": 1, "coder/chat": 2, "other/chat": 3, "chat/other": 1,
		// A kind with padding inside the slash is not "chat": unknown, so an agent kind (the safe side).
		" chat / x": 2,
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

// A mark that is not text (a hand-edited row) ranks as none: any real mark replaces it.
func TestARunMarkOverANonTextOldMark(t *testing.T) {
	for _, old := range []interface{}{5, true, []string{"other"}, nil} {
		data := map[string]interface{}{AICallerKey: old}
		got := withRunMark(WithRunMark(context.Background(), "chat"), data)
		if got[AICallerKey] != "chat" {
			t.Errorf("old mark %#v: the run is marked %v, want chat", old, got[AICallerKey])
		}
	}
}

// The data a mark is written into is a copy when the new mark wins, so the old run's data is never changed
// through the returned map; on a tie or without a mark the old data is returned as it is.
func TestARunMarkIsWrittenIntoACopy(t *testing.T) {
	same := func(a, b map[string]interface{}) bool {
		return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
	}
	data := map[string]interface{}{AICallerKey: "chat", "keep": 1}
	got := withRunMark(WithRunMark(context.Background(), "other"), data)
	if same(got, data) || data[AICallerKey] != "chat" || got[AICallerKey] != "other" || got["keep"] != 1 {
		t.Errorf("the winning mark was written into the old data: got %v, old %v", got, data)
	}
	got["extra"] = 1
	if _, ok := data["extra"]; ok {
		t.Errorf("the returned map aliases the old data")
	}
	if got := withRunMark(WithRunMark(context.Background(), "chat"), data); !same(got, data) || got[AICallerKey] != "chat" {
		t.Errorf("a tie must return the old data as it is: %v", got)
	}
	if got := withRunMark(context.Background(), data); !same(got, data) {
		t.Errorf("no mark must return the old data as it is: %v", got)
	}
}

// RetryExecution itself (not only the command around it) keeps the stricter mark and leaves the old run's row alone.
func TestRetryExecutionKeepsTheStricterMark(t *testing.T) {
	store := newFullEngineStore(t)
	eng := newTestEngine(t, store, NewNodeTypeRegistry())
	ctx := context.Background()
	wfID := createActiveManualWorkflow(t, ctx, eng, "core.set")
	for _, c := range []struct{ name, old, retrier, want string }{
		{"an other agent's run retried by a chat", "other", "chat", "other"},
		{"an unapproved workflow's run retried by a coder", UnapprovedWorkflowCaller, "coder", UnapprovedWorkflowCaller},
		{"a chat's run retried by a coder", "chat", "coder", "coder"},
		{"the user's run retried by an other agent", "", "other", "other"},
		{"an agent's run retried by the user", "coder/org", "", "coder/org"},
		{"a tie keeps the old mark with its suffix", "other/org", "other", "other/org"},
		{"a padded retrier mark", "chat", " other ", " other "},
	} {
		data := map[string]interface{}{"who": "zed"}
		if c.old != "" {
			data[AICallerKey] = c.old
		}
		orig := &WorkflowExecution{WorkflowID: wfID, ProfileID: "default", Status: "FAILED", TriggerType: "manual", TriggerNodeID: "t", TriggerData: data}
		if err := store.CreateExecution(ctx, orig); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		id, err := eng.RetryExecution(WithRunMark(ctx, c.retrier), orig.ID)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		again, err := store.GetExecution(ctx, id)
		if err != nil || again == nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got, _ := again.TriggerData[AICallerKey].(string); got != c.want || again.TriggerData["who"] != "zed" {
			t.Errorf("%s: the retry is marked %q, want %q (%v)", c.name, got, c.want, again.TriggerData)
		}
		old, err := store.GetExecution(ctx, orig.ID)
		if err != nil || old == nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got, _ := old.TriggerData[AICallerKey].(string); got != c.old {
			t.Errorf("%s: the old run's row was changed to %q", c.name, got)
		}
	}
}
