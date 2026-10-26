package mcp

// Proofs over the whole family of task tools (spec 8 and 13): the text a person, an agent or a
// capture wrote reaches an agent only under a name that ends in _untrusted; the tools see one
// profile's board; and two sessions that race for the work never get the same task.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/tasks"
)

// mark is in every text written below by the operator, an agent or a capture.
const mark = "ZQX"

// walkStrings calls fn with every string of a JSON value and the key that holds it (the items of
// an array have the array's key).
func walkStrings(v any, key string, fn func(key, s string)) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			walkStrings(e, k, fn)
		}
	case []any:
		for _, e := range x {
			walkStrings(e, key, fn)
		}
	case string:
		fn(key, x)
	}
}

func TestTaskTextReachesAnAgentOnlyUnderUntrustedNames(t *testing.T) {
	f := newTaskFixture(t, taskSetup{})
	var ids []int64
	for _, n := range []string{"a", "b", "c"} {
		tk, _, err := f.Store.Add(context.Background(), "default", tasks.AddInput{
			Title: mark + "title" + n, Notes: mark + "notes" + n, Ready: true,
			SourceURL: "https://example.com/" + mark + "url", SourceTitle: mark + "page", SourceApp: mark + "app",
		}, tasks.Actor{Kind: tasks.Human})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, tk.ID)
	}
	a, b, c := ids[0], ids[1], ids[2]
	keys := map[string]bool{}
	var marked []string
	check := func(tool string, args map[string]any) {
		t.Helper()
		var doc map[string]any
		if err := json.Unmarshal([]byte(mustCall(t, f.Server, tool, args)), &doc); err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		if doc["note"] != untrustedNote {
			t.Errorf("%s: note = %v", tool, doc["note"])
		}
		walkStrings(doc, "", func(key, s string) {
			if !strings.Contains(s, mark) {
				return
			}
			keys[key] = true
			marked = append(marked, s)
			if !strings.HasSuffix(key, "_untrusted") {
				t.Errorf("%s: %q holds text a person or an agent wrote: %q", tool, key, s)
			}
		})
	}
	refused := func(tool string, args map[string]any) {
		t.Helper()
		if _, err := f.call(tool, args); err == nil || strings.Contains(err.Error(), mark) {
			t.Errorf("%s %v: %v, want a refusal that repeats no task text", tool, args, err)
		}
	}

	check("task_list", nil)
	check("task_next", nil)
	check("task_get", map[string]any{"id": a})
	check("task_claim", map[string]any{"id": a})
	check("task_comment", map[string]any{"id": a, "text": mark + "comment"})
	check("task_finish", map[string]any{"id": a, "question": mark + "question"})
	check("task_claim", map[string]any{"next": true}) // b, now the top of Ready
	check("task_finish", map[string]any{"id": b, "result": mark + "result"})
	check("task_claim", map[string]any{"id": c})
	check("task_release", map[string]any{"id": c, "note": mark + "release"})
	check("task_add", map[string]any{"title": mark + "addedtitle", "notes": mark + "addednotes"})
	for _, id := range ids {
		check("task_get", map[string]any{"id": id})
	}
	refused("task_claim", map[string]any{"id": a})                   // in Review: not_ready
	refused("task_comment", map[string]any{"id": b, "text": "more"}) // not held: not_claimant
	refused("task_get", map[string]any{"id": 999999})                // not_found

	want := sortedCopy([]string{"title_untrusted", "notes_untrusted", "source_url_untrusted", "source_title_untrusted", "source_app_untrusted", "note_untrusted"})
	if got := sortedKeys(keys); !equalStrings(got, want) {
		t.Errorf("the marked text came back under %v, want exactly %v", got, want)
	}
	all := strings.Join(marked, "\n")
	for _, s := range []string{"comment", "question", "result", "release", "addedtitle", "addednotes"} {
		if !strings.Contains(all, mark+s) {
			t.Errorf("%s%s never came back: the walk proves nothing for it", mark, s)
		}
	}
}

func TestTheTaskToolsSeeOneProfile(t *testing.T) {
	f := newTaskFixture(t, taskSetup{})
	mine := f.add("default", "mine", true)
	theirs := f.add(workProfileID, "theirs", true)
	for _, c := range []struct {
		name string
		args map[string]any
	}{
		{"task_get", map[string]any{"id": theirs.ID}},
		{"task_claim", map[string]any{"id": theirs.ID}},
		{"task_comment", map[string]any{"id": theirs.ID, "text": "x"}},
		{"task_finish", map[string]any{"id": theirs.ID, "result": "x"}},
		{"task_release", map[string]any{"id": theirs.ID}},
	} {
		_, err := f.call(c.name, c.args)
		if err == nil || !strings.HasPrefix(err.Error(), "not_found: ") || strings.Contains(err.Error(), workProfileID) {
			t.Errorf("%s of another profile's task: %v, want not_found and nothing of that profile", c.name, err)
		}
	}
	every := map[string]any{"status": []string{"inbox", "ready", "in_progress", "review", "done", "archived"}}
	if got := listed(f.doc("task_list", every)); !idsAre(got, mine.ID) {
		t.Errorf("every column of this profile: %v, want only #%d", got, mine.ID)
	}
	if d := f.doc("task_claim", map[string]any{"next": true}); taskOf(d) != mine.ID {
		t.Fatalf("next: %v", d)
	}
	if d := f.doc("task_next", nil); d["task"] != nil {
		t.Errorf("task_next with only another profile's task ready: %v", d["task"])
	}
	if d := f.doc("task_claim", map[string]any{"next": true}); d["task"] != nil {
		t.Errorf("task_claim next with only another profile's task ready: %v", d["task"])
	}
	if tk, _, err := f.Store.Get(context.Background(), workProfileID, theirs.ID); err != nil || tk.Status != tasks.StatusReady || tk.Claim != nil {
		t.Errorf("the other profile's task was touched: %+v, %v", tk, err)
	}
}

// Two sessions (two servers over one database, as two Claude Code windows are) race for six
// tasks with ten claims: every task goes to exactly one of them, under its own name.
func TestTwoClaimantsNeverGetTheSameTask(t *testing.T) {
	f := newTaskFixture(t, taskSetup{})
	other := f.server(taskSetup{}, "bbbb")
	servers := []*Server{f.Server, other}
	for _, s := range servers { // open both databases first: the race is the claims, not the migrations
		if _, err := s.runtime(); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 6; i++ {
		f.add("default", fmt.Sprintf("job %d", i), true)
	}
	type outcome struct {
		by  string
		id  int64
		err error
	}
	outcomes := make(chan outcome, 10)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		s := servers[i%2]
		wg.Add(1)
		go func() {
			defer wg.Done()
			text, err := callToolDefault(context.Background(), s, "task_claim", json.RawMessage(`{"next": true}`))
			if err != nil {
				outcomes <- outcome{err: err}
				return
			}
			var d struct {
				Task *struct {
					ID int64 `json:"id"`
				} `json:"task"`
			}
			if err := json.Unmarshal([]byte(text), &d); err != nil {
				outcomes <- outcome{err: err}
				return
			}
			o := outcome{by: s.taskActor().Name}
			if d.Task != nil {
				o.id = d.Task.ID
			}
			outcomes <- o
		}()
	}
	wg.Wait()
	close(outcomes)
	holder := map[int64]string{}
	empty := 0
	for o := range outcomes {
		switch {
		case o.err != nil:
			t.Errorf("a claim failed: %v", o.err)
		case o.id == 0:
			empty++
		case holder[o.id] != "":
			t.Errorf("task %d was given to %s and to %s", o.id, holder[o.id], o.by)
		default:
			holder[o.id] = o.by
		}
	}
	if len(holder) != 6 || empty != 4 {
		t.Errorf("%d tasks claimed and %d calls found nothing, want 6 and 4", len(holder), empty)
	}
	for id, by := range holder {
		tk, events, err := f.Store.Get(context.Background(), "default", id)
		if err != nil {
			t.Fatal(err)
		}
		claims := 0
		for _, e := range events {
			if e.Kind == "claimed" {
				claims++
			}
		}
		if tk.Claim == nil || tk.Claim.By != by || claims != 1 {
			t.Errorf("task %d: held by %+v after %d claims, want %s once", id, tk.Claim, claims, by)
		}
	}

	// By id, a task another session holds is refused, and the refusal names the holder.
	contested := f.add("default", "contested", true)
	mustCall(t, f.Server, "task_claim", map[string]any{"id": contested.ID})
	if _, err := callAPITool(t, other, "task_claim", map[string]any{"id": contested.ID}); err == nil ||
		!strings.HasPrefix(err.Error(), "claimed: ") || !strings.Contains(err.Error(), f.Server.taskActor().Name) {
		t.Errorf("a held task claimed by another session: %v", err)
	}
}

// Grant mode (an org role's tool provider) serves the role's automations and nothing of the user's
// own board: no task tool is listed, and each is refused by name (the twin of
// TestGrantModeRefusesEveryAPIToolByName).
func TestGrantModeServesNoTaskTool(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{Wait: true})
	liveHeartbeat(t)
	var names []string
	for _, tl := range allTools() {
		if strings.HasPrefix(tl.name, "task_") {
			names = append(names, tl.name)
		}
	}
	if len(names) != 13 {
		t.Fatalf("%d tools start with task_: %v", len(names), names)
	}
	lines := []string{request(100, "tools/list", map[string]interface{}{})}
	for i, name := range names {
		lines = append(lines, callToolReq(i+1, name, map[string]any{}))
	}
	resps := serveLines(t, f.server, lines...)
	for i, name := range names {
		text, isErr := toolText(t, respByID(t, resps, strconv.Itoa(i+1)))
		if !isErr || !strings.HasPrefix(text, codeRefusedGrant) {
			t.Errorf("%s in grant mode: %q (error %v), want a refusal %s", name, text, isErr, codeRefusedGrant)
		}
	}
	var res struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(respByID(t, resps, "100")["result"], &res); err != nil {
		t.Fatal(err)
	}
	for _, tl := range res.Tools {
		if strings.HasPrefix(tl.Name, "task_") {
			t.Errorf("grant mode lists %s", tl.Name)
		}
	}
}
