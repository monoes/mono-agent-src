package workflowfix

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/workflow"
)

type fakeBackend struct {
	wf      *workflow.Workflow
	saved   int
	revoked int
}

func (f *fakeBackend) Load(_ context.Context, profile, id string) (*workflow.Workflow, error) {
	if profile != "p1" || f.wf == nil || f.wf.ID != id {
		return nil, nil
	}
	cp := *f.wf
	cp.Nodes = append([]workflow.WorkflowNode(nil), f.wf.Nodes...)
	for i := range cp.Nodes { // the store hands out its own copy of the config
		c := map[string]any{}
		for k, v := range cp.Nodes[i].Config {
			c[k] = v
		}
		cp.Nodes[i].Config = c
	}
	return &cp, nil
}

func (f *fakeBackend) SaveNodesGuarded(_ context.Context, _, _ string, nodes []workflow.WorkflowNode, g workflow.SaveGuard) error {
	have, _ := workflow.ReviewHash(f.wf)
	if have != g.ExpectHash {
		return workflow.ErrChangedSincePreview
	}
	if g.RevokeApprovals {
		f.revoked++
	}
	f.wf.Nodes = nodes
	f.saved++
	return nil
}

func (f *fakeBackend) ExecutionWorkflow(_ context.Context, _, id string) (string, error) {
	switch id {
	case "ex-1":
		return "wf-1", nil
	case "ex-2":
		return "wf-2", nil
	}
	return "", nil
}

// bg is a context for task at-1, whose target is workflow wf-1 and run ex-1.
func bg() context.Context { return aitask.WithTaskID(context.Background(), "at-1") }

func rig(t *testing.T, active bool) *fakeBackend {
	t.Helper()
	b := &fakeBackend{wf: &workflow.Workflow{ID: "wf-1", Name: "Digest", IsActive: active,
		Nodes: []workflow.WorkflowNode{
			{ID: "t", Type: "trigger.manual", Name: "Start", Config: map[string]any{}},
			{ID: "set", Type: "core.set", Name: "Shape", Config: map[string]any{"assignments": "a=1"}},
			{ID: "http", Type: "http.request", Name: "Call", Config: map[string]any{"url": "https://example.test/a", "method": "POST"}},
		},
		Connections: []workflow.WorkflowConnection{
			{ID: "c1", SourceNodeID: "t", TargetNodeID: "set", SourceHandle: "main", TargetHandle: "main"},
			{ID: "c2", SourceNodeID: "set", TargetNodeID: "http", SourceHandle: "main", TargetHandle: "main"},
		}}}
	prevS, prevK, prevT := Store, NodeTypeKnown, TargetFor
	Store, NodeTypeKnown = b, nil
	TargetFor = func(context.Context, string, string) (Target, error) {
		return Target{WorkflowID: "wf-1", ExecutionID: "ex-1"}, nil
	}
	t.Cleanup(func() { Store, NodeTypeKnown, TargetFor = prevS, prevK, prevT })
	return b
}

func result(kind string, edits ...map[string]any) []byte {
	fix := map[string]any{"kind": kind, "description": "d"}
	if edits != nil {
		fix["edits"] = edits
	}
	b, _ := json.Marshal(map[string]any{"summary": "s", "diagnosis": "d", "workflowId": "wf-1", "executionId": "ex-1", "fix": fix})
	return b
}

func edit(node string, config map[string]any) map[string]any {
	return map[string]any{"node": node, "config": config}
}

func TestPlanShowsBeforeAndAfterAndDeepMerges(t *testing.T) {
	rig(t, false)
	p, err := Plan(bg(), "p1", result(KindWorkflowEdit, edit("http", map[string]any{"url": "https://example.test/b"})))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Edits) != 1 || p.Edits[0].Path != "url" || p.Edits[0].Before != "https://example.test/a" ||
		p.Edits[0].After != "https://example.test/b" || p.Edits[0].NodeName != "Call" {
		t.Fatalf("%+v", p.Edits)
	}
	if got := deepMerge(map[string]any{"a": map[string]any{"x": 1, "y": 2}}, map[string]any{"a": map[string]any{"y": 3}}); got["a"].(map[string]any)["x"] != 1 || got["a"].(map[string]any)["y"] != 3 {
		t.Fatalf("not a deep merge: %v", got)
	}
}

func TestPlanRefusals(t *testing.T) {
	b := rig(t, false)
	ctx := bg()
	cases := map[string]struct {
		res  []byte
		code string
	}{
		"unknown node":   {result(KindWorkflowEdit, edit("nope", map[string]any{"url": "x"})), CodeUnknownNode},
		"advice":         {result(KindAdvice), CodeNoEdit},
		"secret key":     {result(KindWorkflowEdit, edit("http", map[string]any{"auth_token": "abc"})), CodeSecretKey},
		"nested secret":  {result(KindWorkflowEdit, edit("http", map[string]any{"headers": map[string]any{"Authorization": "x"}})), CodeSecretKey},
		"secret value":   {result(KindWorkflowEdit, edit("http", map[string]any{"url": "https://u:pw12345678@h.test/?x=1"})), CodeSecretKey},
		"no change":      {result(KindWorkflowEdit, edit("http", map[string]any{"method": "POST"})), CodeNoChange},
		"invalid config": {result(KindWorkflowEdit, edit("http", map[string]any{"method": "TELEPORT"})), CodeInvalid},
		"other workflow": {[]byte(`{"workflowId":"other","executionId":"ex-1","fix":{"kind":"workflow-edit","edits":[{"node":"http","config":{"url":"x"}}]}}`), CodeWrongTarget},
	}
	for name, c := range cases {
		if _, err := Plan(ctx, "p1", c.res); !IsCode(err, c.code) {
			t.Errorf("%s: err = %v, want %s", name, err, c.code)
		}
	}
	// Another profile's workflow is not found.
	if _, err := Plan(ctx, "p2", result(KindWorkflowEdit, edit("http", map[string]any{"url": "x"}))); !IsCode(err, CodeNotFound) {
		t.Errorf("other profile: %v", err)
	}
	// Nothing was written by any of them.
	if b.saved != 0 {
		t.Errorf("a refused plan saved %d times", b.saved)
	}
	// A credential reference is a saved connection, not a secret.
	if _, err := Plan(ctx, "p1", result(KindWorkflowEdit, edit("http", map[string]any{"credential_id": "cred-1"}))); err != nil {
		t.Errorf("credential_id refused: %v", err)
	}
}

func TestPlanWarnings(t *testing.T) {
	rig(t, false)
	p, err := Plan(bg(), "p1", result(KindWorkflowEdit,
		edit("http", map[string]any{"url": "{{ $json.url }}", "credential_id": "cred-1"}),
		edit("set", map[string]any{"assignments": "a=2"})))
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, w := range p.Warnings {
		have[w.Code] = true
	}
	for _, want := range []string{WarnNewOutbound, WarnCredentialKey, WarnExpressionAdd} {
		if !have[want] {
			t.Errorf("no %s warning: %+v", want, p.Warnings)
		}
	}
	// A read-only node's edit warns of nothing outbound.
	p, err = Plan(bg(), "p1", result(KindWorkflowEdit, edit("set", map[string]any{"assignments": "a=3"})))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range p.Warnings {
		if w.Code == WarnNewOutbound {
			t.Errorf("a core.set edit warned %+v", w)
		}
	}
}

func TestApplyStoresTheEditsOnceAndRefusesTheSecond(t *testing.T) {
	b := rig(t, false)
	res := result(KindWorkflowEdit, edit("http", map[string]any{"url": "https://example.test/b"}))
	n, err := Apply(bg(), "p1", res, Options{ExpectHash: hashOf(b)})
	if err != nil || n != 1 || b.saved != 1 || b.wf.Nodes[2].Config["url"] != "https://example.test/b" {
		t.Fatalf("n=%d err=%v saved=%d", n, err, b.saved)
	}
	if _, err := Apply(bg(), "p1", res, Options{ExpectHash: hashOf(b)}); !IsCode(err, CodeNoChange) || b.saved != 1 {
		t.Fatalf("second apply: %v (saved %d)", err, b.saved)
	}
}

func TestAnActiveWorkflowIsAWarningAndApplyNeedsAllowActive(t *testing.T) {
	b := rig(t, true)
	res := result(KindWorkflowEdit, edit("set", map[string]any{"assignments": "a=2"}))
	p, err := Plan(bg(), "p1", res)
	if err != nil || !p.Active || !strings.Contains(warnCodes(p), WarnWorkflowActive) {
		t.Fatalf("%v %+v", err, p)
	}
	if _, err := Apply(bg(), "p1", res, Options{ExpectHash: hashOf(b)}); !IsCode(err, CodeWorkflowActive) || b.saved != 0 {
		t.Fatalf("apply on an active workflow: %v (saved %d)", err, b.saved)
	}
	if n, err := Apply(bg(), "p1", res, Options{AllowActive: true, ExpectHash: hashOf(b)}); err != nil || n != 1 || b.saved != 1 {
		t.Fatalf("--allow-active: %d %v", n, err)
	}
}

func TestPlanWithoutABackendRefuses(t *testing.T) {
	rig(t, false)
	Store = nil
	if _, err := Plan(bg(), "p1", result(KindWorkflowEdit, edit("set", map[string]any{"assignments": "a=2"}))); !IsCode(err, CodeNoBackend) {
		t.Fatal(err)
	}
}

func warnCodes(p Preview) string {
	var out []string
	for _, w := range p.Warnings {
		out = append(out, w.Code)
	}
	return strings.Join(out, ",")
}

func TestAWorkflowChangedAfterTheProposalIsStale(t *testing.T) {
	b := rig(t, false)
	res := result(KindWorkflowEdit, edit("set", map[string]any{"assignments": "a=2"}))
	done := time.Now()
	b.wf.UpdatedAt = done.Add(-time.Hour)
	if _, err := PlanSince(bg(), "p1", res, done); err != nil {
		t.Fatalf("an unchanged workflow is stale: %v", err)
	}
	b.wf.UpdatedAt = done.Add(time.Second)
	if _, err := PlanSince(bg(), "p1", res, done); !IsCode(err, CodeStale) {
		t.Fatalf("workflow edited after the proposal: %v", err)
	}
	// A node edited on its own (workflow node set) counts, whatever the workflow's own time says.
	b.wf.UpdatedAt = done.Add(-time.Hour)
	b.wf.Nodes[1].UpdatedAt = done.Add(time.Second)
	if _, err := Apply(bg(), "p1", res, Options{Since: done, ExpectHash: hashOf(b)}); !IsCode(err, CodeStale) || b.saved != 0 {
		t.Fatalf("apply on a stale proposal: %v (saved %d)", err, b.saved)
	}
	// Without a time (the check at completion) there is nothing to compare.
	if _, err := Plan(bg(), "p1", res); err != nil {
		t.Fatal(err)
	}
}

func hashOf(b *fakeBackend) string { h, _ := workflow.ReviewHash(b.wf); return h }

func resultFor(workflowID, executionID string, edits ...map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"summary": "s", "diagnosis": "d", "workflowId": workflowID, "executionId": executionID,
		"fix": map[string]any{"kind": KindWorkflowEdit, "description": "d", "edits": edits}})
	return b
}

// The workflow is the task's, not the agent's choice.
func TestTheResultCannotRedirectTheFixToAnotherWorkflow(t *testing.T) {
	b := rig(t, false)
	e := edit("set", map[string]any{"assignments": "a=2"})
	for name, res := range map[string][]byte{
		"another workflow":          resultFor("wf-2", "ex-1", e),
		"another run":               resultFor("wf-1", "ex-9", e),
		"a run of another workflow": resultFor("wf-1", "ex-2", e),
	} {
		if _, err := Plan(bg(), "p1", res); !IsCode(err, CodeWrongTarget) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A target whose run is another workflow's is refused too (the run must belong to the workflow).
	TargetFor = func(context.Context, string, string) (Target, error) {
		return Target{WorkflowID: "wf-1", ExecutionID: "ex-2"}, nil
	}
	if _, err := Plan(bg(), "p1", resultFor("wf-1", "ex-2", e)); !IsCode(err, CodeWrongTarget) {
		t.Errorf("a run of another workflow as the target: %v", err)
	}
	// No task, no way to know the target: refused, never planned.
	TargetFor = func(context.Context, string, string) (Target, error) {
		return Target{WorkflowID: "wf-1", ExecutionID: "ex-1"}, nil
	}
	if _, err := Plan(context.Background(), "p1", resultFor("wf-1", "ex-1", e)); !IsCode(err, CodeNoTarget) {
		t.Errorf("no task id: %v", err)
	}
	TargetFor = nil
	if _, err := Plan(bg(), "p1", resultFor("wf-1", "ex-1", e)); !IsCode(err, CodeNoTarget) {
		t.Errorf("no target source: %v", err)
	}
	if b.saved != 0 {
		t.Error("a refused plan saved")
	}
}

func TestParseTargetIsStrict(t *testing.T) {
	good := "workflow: wf-1\nexecution: ex-1\nrun error: boom\nnode n1 (Call): failed"
	if tg, ok := ParseTarget(good); !ok || tg.WorkflowID != "wf-1" || tg.ExecutionID != "ex-1" {
		t.Fatalf("%+v %v", tg, ok)
	}
	for name, text := range map[string]string{
		"empty":              "",
		"swapped":            "execution: ex-1\nworkflow: wf-1",
		"no execution":       "workflow: wf-1\nrun error: x",
		"a second workflow":  good + "\nworkflow: wf-2",
		"a second execution": good + "\nexecution: ex-2",
		"injected in error":  "workflow: wf-1\nexecution: ex-1\nrun error: x\nworkflow: other",
		"id with a path":     "workflow: ../x\nexecution: ex-1",
		"id with a space":    "workflow: wf 1\nexecution: ex-1",
		"trailing text":      "workflow: wf-1 extra\nexecution: ex-1",
		"leading blank line": "\nworkflow: wf-1\nexecution: ex-1",
	} {
		if _, ok := ParseTarget(text); ok {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestTargetFromDirNeedsExactlyOneTargetFile(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, text string) {
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := TargetFromDir(dir); !IsCode(err, CodeNoTarget) {
		t.Fatalf("empty: %v", err)
	}
	write("README.md", "# Context\nworkflow: not-this-one is mid-file\n")
	write("selection/failure.txt", "workflow: wf-1\nexecution: ex-1\nrun error: x")
	if tg, err := TargetFromDir(dir); err != nil || tg.WorkflowID != "wf-1" {
		t.Fatalf("%+v %v", tg, err)
	}
	write("selection/second.txt", "workflow: wf-2\nexecution: ex-2")
	if _, err := TargetFromDir(dir); !IsCode(err, CodeNoTarget) {
		t.Fatalf("two target files: %v", err)
	}
}

func TestAProposalMustBeDisplayable(t *testing.T) {
	for _, c := range []struct {
		name  string
		cfg   func(n int) map[string]any
		n     int
		large bool
	}{
		{"50 leaf changes", leaves, 50, false},
		{"51 leaf changes", leaves, 51, true},
		{"2000 characters", longValue, 2000, false},
		{"2001 characters", longValue, 2001, true},
	} {
		rig(t, false)
		_, err := Plan(bg(), "p1", result(KindWorkflowEdit, edit("set", c.cfg(c.n))))
		if c.large != IsCode(err, CodeTooLarge) || (!c.large && err != nil) {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
	// A long value that is only the BEFORE side counts as well.
	b := rig(t, false)
	b.wf.Nodes[1].Config["assignments"] = strings.Repeat("y", 2001)
	if _, err := Plan(bg(), "p1", result(KindWorkflowEdit, edit("set", map[string]any{"assignments": "short"}))); !IsCode(err, CodeTooLarge) {
		t.Errorf("a long before value: %v", err)
	}
}

func leaves(n int) map[string]any {
	m := map[string]any{}
	for i := 0; i < n; i++ {
		m[fmt.Sprintf("k%02d", i)] = i
	}
	return m
}

func longValue(n int) map[string]any { return map[string]any{"assignments": strings.Repeat("x", n)} }

func TestApplyIsBoundToTheReviewedDefinition(t *testing.T) {
	b := rig(t, false)
	res := result(KindWorkflowEdit, edit("set", map[string]any{"assignments": "a=2"}))
	p, err := Plan(bg(), "p1", res)
	if err != nil || p.DefinitionHash == "" {
		t.Fatalf("%v %+v", err, p)
	}
	if _, err := Apply(bg(), "p1", res, Options{}); !IsCode(err, CodeExpectHash) || b.saved != 0 {
		t.Fatalf("an apply without the reviewed hash: %v", err)
	}
	// Edited between preview and apply.
	b.wf.Nodes[2].Config["url"] = "https://example.test/other"
	if _, err := Apply(bg(), "p1", res, Options{ExpectHash: p.DefinitionHash}); !IsCode(err, CodeChanged) || b.saved != 0 {
		t.Fatalf("edited after the preview: %v (saved %d)", err, b.saved)
	}
	// Activated between preview and apply: the hash covers the active flag.
	b = rig(t, false)
	p, _ = Plan(bg(), "p1", res)
	b.wf.IsActive = true
	if _, err := Apply(bg(), "p1", res, Options{ExpectHash: p.DefinitionHash, AllowActive: true}); !IsCode(err, CodeChanged) || b.saved != 0 {
		t.Fatalf("activated after the preview: %v (saved %d)", err, b.saved)
	}
	// The matching hash applies, and the write revokes the workflow's approvals.
	b = rig(t, false)
	p, _ = Plan(bg(), "p1", res)
	if n, err := Apply(bg(), "p1", res, Options{ExpectHash: p.DefinitionHash}); err != nil || n != 1 || b.saved != 1 || b.revoked != 1 {
		t.Fatalf("n=%d err=%v saved=%d revoked=%d", n, err, b.saved, b.revoked)
	}
}

// nest builds {k0:{k1:{...:leaf}}}.
func nest(keys []string, leaf any) map[string]any {
	if len(keys) == 1 {
		return map[string]any{keys[0]: leaf}
	}
	return map[string]any{keys[0]: nest(keys[1:], leaf)}
}

func keysOfDepth(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("k%d", i)
	}
	return out
}

// An addition that is an empty container is a change like any other: shown, counted, checked.
func TestEmptyContainerAdditionsAreShownCountedAndChecked(t *testing.T) {
	rig(t, false)
	empties := func(n int) map[string]any {
		m := map[string]any{}
		for i := 0; i < n; i++ {
			m[fmt.Sprintf("e%02d", i)] = map[string]any{}
		}
		return m
	}
	if _, err := Plan(bg(), "p1", result(KindWorkflowEdit, edit("set", empties(50)))); err != nil {
		t.Fatalf("50 empty objects: %v", err)
	}
	if _, err := Plan(bg(), "p1", result(KindWorkflowEdit, edit("set", empties(51)))); !IsCode(err, CodeTooLarge) {
		t.Fatalf("51 empty objects: %v", err)
	}
	p, err := Plan(bg(), "p1", result(KindWorkflowEdit, edit("set", map[string]any{"assignments": "a=2", "extra": map[string]any{}, "list": []any{}})))
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]Edit{}
	for _, e := range p.Edits {
		paths[e.Path] = e
	}
	for _, want := range []string{"assignments", "extra", "list"} {
		if _, ok := paths[want]; !ok {
			t.Errorf("no edit for %q: %+v", want, p.Edits)
		}
	}
	if e := paths["extra"]; e.Before != nil || !reflect.DeepEqual(e.After, map[string]any{}) {
		t.Errorf("extra = %+v, want before absent and after {}", e)
	}
	// A secret-looking key under an added empty object, and as an added empty object itself.
	for name, cfg := range map[string]map[string]any{
		"under an added object": {"headers": map[string]any{"password": map[string]any{}}},
		"as the added object":   {"api_key": map[string]any{}},
	} {
		if _, err := Plan(bg(), "p1", result(KindWorkflowEdit, edit("http", cfg))); !IsCode(err, CodeSecretKey) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestKeysPathsAndDepthAreBounded(t *testing.T) {
	rig(t, false)
	try := func(cfg map[string]any) error {
		_, err := Plan(bg(), "p1", result(KindWorkflowEdit, edit("set", cfg)))
		return err
	}
	// One key: 128 / 129 characters.
	if err := try(map[string]any{strings.Repeat("k", 128): 1}); err != nil {
		t.Errorf("128-character key: %v", err)
	}
	if err := try(map[string]any{strings.Repeat("k", 129): 1}); !IsCode(err, CodeTooLarge) {
		t.Errorf("129-character key: %v", err)
	}
	// A long key inside an added container, and inside a list value.
	if err := try(map[string]any{"a": map[string]any{strings.Repeat("k", 129): 1}}); !IsCode(err, CodeTooLarge) {
		t.Errorf("a long key in an added object: %v", err)
	}
	if err := try(map[string]any{"a": []any{map[string]any{strings.Repeat("k", 129): 1}}}); !IsCode(err, CodeTooLarge) {
		t.Errorf("a long key in a list value: %v", err)
	}
	// The whole dotted path: 512 / 513 (8 keys, 7 dots).
	path := func(last int) []string {
		keys := make([]string, 8)
		for i := 0; i < 7; i++ {
			keys[i] = strings.Repeat("p", 64)
		}
		keys[7] = strings.Repeat("q", last)
		return keys
	}
	if err := try(nest(path(57), 1)); err != nil {
		t.Errorf("512-character path: %v", err)
	}
	if err := try(nest(path(58), 1)); !IsCode(err, CodeTooLarge) {
		t.Errorf("513-character path: %v", err)
	}
	// Depth: 8 levels of keys is fine, 9 is not; a scalar leaf at depth 8, a container under it is 9.
	if err := try(nest(keysOfDepth(8), 1)); err != nil {
		t.Errorf("depth 8: %v", err)
	}
	if err := try(nest(keysOfDepth(9), 1)); !IsCode(err, CodeTooLarge) {
		t.Errorf("depth 9: %v", err)
	}
	if err := try(nest(keysOfDepth(8), []any{1})); !IsCode(err, CodeTooLarge) {
		t.Errorf("a list under depth 8: %v", err)
	}
	// Absurd nesting is refused without recursing through it.
	if err := try(nest(keysOfDepth(5000), 1)); !IsCode(err, CodeTooLarge) {
		t.Errorf("depth 5000: %v", err)
	}
}

// Secret checks look inside every added or replaced container, lists and nesting included.
func TestSecretChecksAreRecursive(t *testing.T) {
	rig(t, false)
	token := "ghp_" + strings.Repeat("a1B2c3D4e5", 4)
	if aitask.RedactText(token) == token {
		t.Skip("the redactor does not treat the sample as a token")
	}
	for name, cfg := range map[string]map[string]any{
		"a key in a list element":                          {"items": []any{map[string]any{"api_key": "x"}}},
		"a key in a nested list":                           {"items": []any{[]any{map[string]any{"password": "x"}}}},
		"a scalar replaced by an object with a secret key": {"assignments": map[string]any{"password": "x"}},
		"a token-shaped string in a nested list":           {"items": []any{[]any{token}}},
		"a token-shaped string in a nested object":         {"a": map[string]any{"b": map[string]any{"c": token}}},
		"an invisible character hiding the key":            {"a": map[string]any{"api​_key": "x"}},
	} {
		_, err := Plan(bg(), "p1", result(KindWorkflowEdit, edit("set", cfg)))
		if !IsCode(err, CodeSecretKey) && !IsCode(err, CodeInvalidKey) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A reference to a saved connection is not a secret, in a list or an object.
	for _, cfg := range []map[string]any{
		{"credential_id": "cred-1"},
		{"items": []any{map[string]any{"credential_id": "cred-1"}}},
	} {
		if _, err := Plan(bg(), "p1", result(KindWorkflowEdit, edit("http", cfg))); err != nil {
			t.Errorf("credential_id refused in %v: %v", cfg, err)
		}
	}
	if !looksSecretKey("api​_key") || looksSecretKey("credential_id") || looksSecretKey("url") {
		t.Error("looksSecretKey")
	}
}

func TestKeyHygiene(t *testing.T) {
	rig(t, false)
	for name, cfg := range map[string]map[string]any{
		"a zero-width character":                     {"a​b": 1},
		"a bidi override":                            {"ab‮": 1},
		"a control character":                        {"a\x01b": 1},
		"a decomposed key":                           {"é": 1},
		"a case-only collision with an existing key": {"ASSIGNMENTS": "x"},
		"a spacing collision with an existing key":   {" assignments ": "x"},
		"two keys of the patch that fold alike":      {"Foo": 1, "foo": 2},
		"a bad key inside an added object":           {"a": map[string]any{"b​c": 1}},
		"a bad key inside a list element":            {"a": []any{map[string]any{"b‮c": 1}}},
		"a collision inside an existing object":      {"headers": map[string]any{"X": 1, "x": 2}},
	} {
		if _, err := Plan(bg(), "p1", result(KindWorkflowEdit, edit("set", cfg))); !IsCode(err, CodeInvalidKey) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A composed key and the existing key itself are fine.
	if _, err := Plan(bg(), "p1", result(KindWorkflowEdit, edit("set", map[string]any{"é": 1, "assignments": "a=2"}))); err != nil {
		t.Errorf("a normal edit: %v", err)
	}
}

// A dot or a backslash inside a key is escaped in the label, so two different paths never render alike.
func TestPathLabelsAreUnambiguous(t *testing.T) {
	rig(t, false)
	p, err := Plan(bg(), "p1", result(KindWorkflowEdit, edit("set", map[string]any{"a.b": 1, "a": map[string]any{"b": 2}, `c\d`: 3})))
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, e := range p.Edits {
		if paths[e.Path] {
			t.Errorf("two edits render as %q", e.Path)
		}
		paths[e.Path] = true
	}
	for _, want := range []string{`a\.b`, `a.b`, `c\\d`} {
		if !paths[want] {
			t.Errorf("no edit labelled %q: %v", want, paths)
		}
	}
}
