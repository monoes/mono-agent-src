package agent

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/aimemory"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

func memCtx(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	db := testdb.Open(t).DB
	return vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "p1"), db
}

func runMemory(t *testing.T, ctx context.Context, in []workflow.Item, cfg map[string]interface{}) []workflow.Item {
	t.Helper()
	out, err := (&MemoryNode{}).Execute(ctx, workflow.NodeInput{Items: in}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return out[0].Items
}

func session(id string) []workflow.Item {
	return []workflow.Item{{JSON: map[string]interface{}{"sessionId": id, "chatInput": "hello " + id}}}
}

func stored(t *testing.T, db *sql.DB, profile, s string) []string {
	t.Helper()
	msgs, err := aimemory.Recent(context.Background(), db, profile, s, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Role + ":" + m.Content
	}
	return out
}

func TestMemoryInsertAppendAndOverride(t *testing.T) {
	var _ workflow.PerItemConfigResolver = &MemoryNode{}
	ctx, db := memCtx(t)
	out := runMemory(t, ctx, session("a"), map[string]interface{}{"operation": "insert",
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": "{{ $json.chatInput }}"},
			map[string]interface{}{"role": "ai", "content": "hi"},
		}})
	if len(out) != 1 || out[0].JSON["success"] != true {
		t.Fatalf("insert output = %+v", out)
	}
	runMemory(t, ctx, session("a"), map[string]interface{}{"operation": "insert",
		"messages": `[{"role": "system", "content": "be brief"}]`})
	if got, want := stored(t, db, "p1", "a"), []string{"human:hello a", "ai:hi", "system:be brief"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after append = %v, want %v", got, want)
	}
	runMemory(t, ctx, session("a"), map[string]interface{}{"operation": "insert", "insert_mode": "override",
		"messages": []interface{}{map[string]interface{}{"role": "human", "content": "fresh"}}})
	if got, want := stored(t, db, "p1", "a"), []string{"human:fresh"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after override = %v", got)
	}
	if _, err := (&MemoryNode{}).Execute(ctx, workflow.NodeInput{Items: session("a")}, map[string]interface{}{
		"operation": "insert", "messages": []interface{}{map[string]interface{}{"role": "robot", "content": "x"}}}); err == nil {
		t.Fatal("unknown role accepted")
	}
}

func TestMemoryGetFormatsAndGrouping(t *testing.T) {
	ctx, db := memCtx(t)
	for _, m := range [][2]string{{"human", "q1"}, {"ai", "a1"}, {"human", "q2"}, {"ai", "a2"}, {"human", "q3"}} {
		if err := aimemory.Append(ctx, db, "p1", "s", m[0], m[1]); err != nil {
			t.Fatal(err)
		}
	}
	out := runMemory(t, ctx, session("s"), map[string]interface{}{"operation": "get"})
	if len(out) != 1 || out[0].JSON["messagesCount"] != 5 || out[0].JSON["sessionId"] != "s" {
		t.Fatalf("grouped = %+v", out)
	}
	msgs := out[0].JSON["messages"].([]interface{})
	if first := msgs[0].(map[string]interface{}); first["role"] != "human" || first["content"] != "q1" || len(first) != 2 {
		t.Fatalf("simple message = %+v", first)
	}

	out = runMemory(t, ctx, session("s"), map[string]interface{}{"operation": "get", "format": "pairs"})
	pairs := out[0].JSON["messages"].([]interface{})
	want := []interface{}{
		map[string]interface{}{"human": "q1", "ai": "a1"},
		map[string]interface{}{"human": "q2", "ai": "a2"},
		map[string]interface{}{"human": "q3"},
	}
	if !reflect.DeepEqual(pairs, want) || out[0].JSON["messagesCount"] != 3 {
		t.Fatalf("pairs = %+v", pairs)
	}

	out = runMemory(t, ctx, session("s"), map[string]interface{}{"operation": "get", "format": "raw", "group": false})
	if len(out) != 5 || out[4].JSON["content"] != "q3" || out[4].JSON["seq"] != int64(5) || out[4].JSON["created_at"] == "" {
		t.Fatalf("raw ungrouped = %+v", out)
	}
	if out := runMemory(t, ctx, session("empty"), map[string]interface{}{"operation": "get", "group": false}); len(out) != 0 {
		t.Fatalf("empty session ungrouped = %+v", out)
	}
}

func TestMemoryDeleteLastNAndAll(t *testing.T) {
	ctx, db := memCtx(t)
	for _, c := range []string{"1", "2", "3", "4"} {
		_ = aimemory.Append(ctx, db, "p1", "s", "human", c)
	}
	out := runMemory(t, ctx, session("s"), map[string]interface{}{"operation": "delete"}) // last 2 by default
	if out[0].JSON["success"] != true {
		t.Fatalf("delete output = %+v", out)
	}
	if got := stored(t, db, "p1", "s"); !reflect.DeepEqual(got, []string{"human:1", "human:2"}) {
		t.Fatalf("after delete last 2 = %v", got)
	}
	runMemory(t, ctx, session("s"), map[string]interface{}{"operation": "delete", "delete_mode": "last_n", "count": float64(1)})
	if got := stored(t, db, "p1", "s"); !reflect.DeepEqual(got, []string{"human:1"}) {
		t.Fatalf("after delete last 1 = %v", got)
	}
	_ = aimemory.Append(ctx, db, "p1", "s", "human", "x")
	runMemory(t, ctx, session("s"), map[string]interface{}{"operation": "delete", "delete_mode": "all"})
	if got := stored(t, db, "p1", "s"); len(got) != 0 {
		t.Fatalf("after delete all = %v", got)
	}
}

func TestMemorySessionKeyPerItemAndMissing(t *testing.T) {
	ctx, db := memCtx(t)
	in := []workflow.Item{
		{JSON: map[string]interface{}{"user": "ann", "text": "from ann"}},
		{JSON: map[string]interface{}{"user": "bob", "text": "from bob"}},
	}
	runMemory(t, ctx, in, map[string]interface{}{"operation": "insert", "session_key": "chat-{{ $json.user }}",
		"messages": []interface{}{map[string]interface{}{"role": "human", "content": "{{ $json.text }}"}}})
	if got := stored(t, db, "p1", "chat-ann"); !reflect.DeepEqual(got, []string{"human:from ann"}) {
		t.Fatalf("ann = %v", got)
	}
	if got := stored(t, db, "p1", "chat-bob"); !reflect.DeepEqual(got, []string{"human:from bob"}) {
		t.Fatalf("bob = %v", got)
	}
	_, err := (&MemoryNode{}).Execute(ctx, workflow.NodeInput{Items: in}, map[string]interface{}{"operation": "get"})
	if err == nil || !strings.Contains(err.Error(), "session") {
		t.Fatalf("missing sessionId: err = %v", err)
	}
}

// agent.ask with memory: the session's earlier messages go in front of the
// prompt, and each turn is appended afterwards.
func TestAskMemoryPrependsHistoryAndAppendsTurns(t *testing.T) {
	ctx, db := memCtx(t)
	var prompts []string
	fakeTurns(t, func(o monomind.ExecOptions) (*monomind.TurnResult, error) {
		prompts = append(prompts, o.Prompt)
		if strings.Contains(o.Prompt, "What is my name") && strings.Contains(o.Prompt, "my name is Ada") {
			return &monomind.TurnResult{ResultText: "Your name is Ada."}, nil
		}
		return &monomind.TurnResult{ResultText: "Nice to meet you."}, nil
	})
	cfg := func() map[string]interface{} {
		return map[string]interface{}{"runtime": "claude", "prompt": "{{ $json.chatInput }}",
			"memory": map[string]interface{}{"session_key": "{{ $json.sessionId }}", "window": float64(10)}}
	}
	ask := func(text string) workflow.Item {
		out, err := (&AskNode{}).Execute(ctx, workflow.NodeInput{Items: []workflow.Item{
			{JSON: map[string]interface{}{"sessionId": "c1", "chatInput": text}}}}, cfg())
		if err != nil {
			t.Fatal(err)
		}
		return out[0].Items[0]
	}
	ask("Hi, my name is Ada.")
	if prompts[0] != "Hi, my name is Ada." {
		t.Fatalf("first prompt carries history: %q", prompts[0])
	}
	res := ask("What is my name?")
	if !strings.Contains(prompts[1], "User: Hi, my name is Ada.") || !strings.Contains(prompts[1], "Assistant: Nice to meet you.") ||
		!strings.HasSuffix(prompts[1], "What is my name?") {
		t.Fatalf("second prompt = %q", prompts[1])
	}
	if res.JSON["agent_response"] != "Your name is Ada." {
		t.Fatalf("answer = %v", res.JSON["agent_response"])
	}
	want := []string{"human:Hi, my name is Ada.", "ai:Nice to meet you.", "human:What is my name?", "ai:Your name is Ada."}
	if got := stored(t, db, "p1", "c1"); !reflect.DeepEqual(got, want) {
		t.Fatalf("session = %v", got)
	}

	// window 2 sends only the last exchange.
	c := cfg()
	c["memory"] = `{"window": 2}`
	if _, err := (&AskNode{}).Execute(ctx, workflow.NodeInput{Items: []workflow.Item{
		{JSON: map[string]interface{}{"sessionId": "c1", "chatInput": "again"}}}}, c); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompts[2], "Hi, my name is Ada.") || !strings.Contains(prompts[2], "User: What is my name?") {
		t.Fatalf("windowed prompt = %q", prompts[2])
	}

	// enabled:false is no memory at all.
	c["memory"] = map[string]interface{}{"enabled": false}
	if _, err := (&AskNode{}).Execute(ctx, workflow.NodeInput{Items: []workflow.Item{
		{JSON: map[string]interface{}{"sessionId": "c1", "chatInput": "plain"}}}}, c); err != nil {
		t.Fatal(err)
	}
	if prompts[3] != "plain" || len(stored(t, db, "p1", "c1")) != 6 {
		t.Fatalf("disabled memory: prompt %q, stored %d", prompts[3], len(stored(t, db, "p1", "c1")))
	}
}

func TestAskMemoryPerItemSessionsIsolatedAndOrdered(t *testing.T) {
	ctx, db := memCtx(t)
	var mu sync.Mutex
	var prompts []string
	fakeTurns(t, func(o monomind.ExecOptions) (*monomind.TurnResult, error) {
		mu.Lock()
		prompts = append(prompts, o.Prompt)
		mu.Unlock()
		last := o.Prompt[strings.LastIndex(o.Prompt, "\n")+1:]
		return &monomind.TurnResult{ResultText: "re: " + last}, nil
	})
	var in []workflow.Item
	for _, m := range [][2]string{{"x", "x1"}, {"y", "y1"}, {"x", "x2"}, {"y", "y2"}, {"x", "x3"}} {
		in = append(in, workflow.Item{JSON: map[string]interface{}{"sessionId": m[0], "chatInput": m[1]}})
	}
	out, err := (&AskNode{}).Execute(ctx, workflow.NodeInput{Items: in}, map[string]interface{}{
		"runtime": "claude", "prompt": "{{ $json.chatInput }}", "concurrency": float64(4),
		"memory": map[string]interface{}{}, // an empty object is no memory
	})
	if err != nil || len(out[0].Items) != 5 {
		t.Fatal(err)
	}
	if got := stored(t, db, "p1", "x"); len(got) != 0 {
		t.Fatalf("an empty memory object stored %v", got)
	}

	_, err = (&AskNode{}).Execute(ctx, workflow.NodeInput{Items: in}, map[string]interface{}{
		"runtime": "claude", "prompt": "{{ $json.chatInput }}", "concurrency": float64(4),
		"memory": map[string]interface{}{"enabled": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantX := []string{"human:x1", "ai:re: x1", "human:x2", "ai:re: x2", "human:x3", "ai:re: x3"}
	if got := stored(t, db, "p1", "x"); !reflect.DeepEqual(got, wantX) {
		t.Fatalf("session x = %v", got)
	}
	wantY := []string{"human:y1", "ai:re: y1", "human:y2", "ai:re: y2"}
	if got := stored(t, db, "p1", "y"); !reflect.DeepEqual(got, wantY) {
		t.Fatalf("session y = %v", got)
	}
	for _, p := range prompts[5:] {
		if strings.HasSuffix(p, "x3") && (!strings.Contains(p, "User: x2") || strings.Contains(p, "y1")) {
			t.Fatalf("x3 prompt = %q", p)
		}
		if strings.HasSuffix(p, "y2") && (!strings.Contains(p, "User: y1") || strings.Contains(p, "x")) {
			t.Fatalf("y2 prompt = %q", p)
		}
	}
}

func TestAskMemoryNeedsSessionKeyAndDB(t *testing.T) {
	fakeTurns(t, func(o monomind.ExecOptions) (*monomind.TurnResult, error) {
		return &monomind.TurnResult{ResultText: "ok"}, nil
	})
	cfg := map[string]interface{}{"runtime": "claude", "prompt": "hi", "memory": map[string]interface{}{"window": float64(4)}}
	ctx, _ := memCtx(t)
	_, err := (&AskNode{}).Execute(ctx, workflow.NodeInput{Items: items(1)}, cfg)
	if err == nil || !strings.Contains(err.Error(), "session_key") {
		t.Fatalf("no sessionId: err = %v", err)
	}
	_, err = (&AskNode{}).Execute(context.Background(), workflow.NodeInput{Items: session("s")}, cfg)
	if err == nil || !strings.Contains(err.Error(), "database") {
		t.Fatalf("no db: err = %v", err)
	}
	cfg["memory"] = map[string]interface{}{"window": "lots"}
	if _, err := (&AskNode{}).Execute(ctx, workflow.NodeInput{Items: session("s")}, cfg); err == nil {
		t.Fatal("bad window accepted")
	}
}
