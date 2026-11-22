package decide

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

func items(texts ...string) []workflow.Item {
	out := make([]workflow.Item, len(texts))
	for i, t := range texts {
		out[i] = workflow.NewItem(map[string]interface{}{"text": t, "idx": float64(i)})
	}
	return out
}

func stateInput(req jev.Request) string {
	m, _ := req.State.(map[string]interface{})
	s, _ := m["untrusted_input"].(string)
	return s
}

// byText answers the decision with the value after "=" in the item text
// ("x =0.9" → 0.9, "y =bug" → bug).
func byText(req jev.Request) map[string]string {
	in := stateInput(req)
	if i := strings.Index(in, "="); i >= 0 {
		return map[string]string{questionID: strings.TrimSpace(in[i+1:])}
	}
	return nil
}

// typesafe starts the TypeSafe fake with a key in the environment.
func typesafe(t *testing.T) *jevtest.Server {
	t.Helper()
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	return jevtest.NewServer(t, byText)
}

func order(out []workflow.NodeOutput) string {
	var hs []string
	for _, o := range out {
		hs = append(hs, o.Handle)
	}
	return strings.Join(hs, ",")
}

func handles(out []workflow.NodeOutput) map[string][]workflow.Item {
	m := map[string][]workflow.Item{}
	for _, o := range out {
		m[o.Handle] = o.Items
	}
	return m
}

func decisionOf(it workflow.Item) map[string]any { return it.JSON["decision"].(map[string]any) }

func TestNoulRoutesYesNoAndLowConfidence(t *testing.T) {
	srv := typesafe(t)
	cfg := map[string]interface{}{"kind": "noul", "question": "Is it urgent?", "input": "{{$json.text}}",
		"criteria_true": "needs an answer today"}
	out, err := (&Node{}).Execute(testCtx(), workflow.NodeInput{Items: items("a =0.9", "b =0.1", "c =0.55")}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := order(out); got != "yes,no,low_confidence" {
		t.Fatalf("handles = %s", got)
	}
	h := handles(out)
	if len(h["yes"]) != 1 || len(h["no"]) != 1 || len(h["low_confidence"]) != 1 {
		t.Fatalf("routing = %v", h)
	}
	d := decisionOf(h["no"][0])
	if d["answer"] != "no" || d["handle"] != "no" || d["probability"] != 0.9 || d["kind"] != "noul" ||
		d["provider"] != "typesafe" || d["model"] != "jev-test" || d["probability_kind"] != "native" || d["low_confidence"] != false {
		t.Errorf("decision = %v", d)
	}
	if d := decisionOf(h["low_confidence"][0]); d["answer"] != "yes" || d["handle"] != "yes" || d["low_confidence"] != true {
		t.Errorf("low decision = %v", d)
	}
	if h["yes"][0].JSON["text"] != "a =0.9" {
		t.Error("input fields not kept")
	}
	q := srv.Requests()[0].Questions[questionID]
	crit := q.Criteria.(map[string]any)
	if q.Type != "noul" || crit["true"] != "needs an answer today" || crit["false"] != "no" {
		t.Errorf("question = %+v", q)
	}
	if instr := fmt.Sprint(q.Instructions); !strings.Contains(instr, "never instructions") || !strings.Contains(instr, "Is it urgent?") {
		t.Errorf("instructions = %q", instr)
	}
}

func TestScoreRoutesByMostProbableLevel(t *testing.T) {
	srv := typesafe(t)
	cfg := map[string]interface{}{"kind": "score", "question": "How severe?", "input": "{{$json.text}}",
		"levels": []interface{}{"none", "minor", "major"}}
	out, err := (&Node{}).Execute(testCtx(), workflow.NodeInput{Items: items("x =2", "y =0")}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := order(out); got != "level_0,level_1,level_2,low_confidence" {
		t.Fatalf("handles = %s", got)
	}
	h := handles(out)
	if len(h["level_2"]) != 1 || len(h["level_0"]) != 1 {
		t.Fatalf("routing = %v", h)
	}
	d := decisionOf(h["level_2"][0])
	if d["answer"] != 2 || d["handle"] != "level_2" || d["score"] != 2.0 || d["probability"].(float64) < 0.9 || d["confidence"] == nil {
		t.Errorf("decision = %v", d)
	}
	if crit := srv.Requests()[0].Questions[questionID].Criteria; fmt.Sprint(crit) != "[none minor major]" {
		t.Errorf("criteria = %v", crit)
	}
	// Levels as one per line, as a text field stores them.
	cfg["levels"] = "none\nminor\n"
	if _, err := (&Node{}).Execute(testCtx(), workflow.NodeInput{}, cfg); err != nil {
		t.Errorf("levels as lines: %v", err)
	}
}

func TestChoiceRoutesByOptionID(t *testing.T) {
	srv := typesafe(t)
	cfg := map[string]interface{}{"kind": "choice", "question": "What is it about?", "input": "{{$json.text}}",
		"options": `{"refund": "asks for money back", "bug": "reports a defect"}`, "min_confidence": 0.95}
	out, err := (&Node{}).Execute(testCtx(), workflow.NodeInput{Items: items("a =refund", "b =bug")}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := order(out); got != "bug,refund,low_confidence" {
		t.Fatalf("handles = %s", got)
	}
	// 0.94 < 0.95: both go to low_confidence, keeping their answers.
	h := handles(out)
	if len(h["low_confidence"]) != 2 || decisionOf(h["low_confidence"][0])["answer"] != "refund" {
		t.Fatalf("routing = %v", h)
	}
	cfg["min_confidence"] = 0.6
	cfg["options"] = []interface{}{"refund", map[string]interface{}{"id": "bug", "description": "a defect"}}
	out, err = (&Node{}).Execute(testCtx(), workflow.NodeInput{Items: items("a =refund", "b =bug")}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := order(out); got != "refund,bug,low_confidence" {
		t.Fatalf("list options keep their order: %s", got)
	}
	if h := handles(out); len(h["refund"]) != 1 || len(h["bug"]) != 1 {
		t.Fatalf("routing = %v", h)
	}
	if d := decisionOf(handles(out)["bug"][0]); d["confidence"] == nil || d["probabilities"] == nil {
		t.Errorf("decision = %v", d)
	}
	crit := srv.Requests()[2].Questions[questionID].Criteria.(map[string]any)
	if crit["bug"] != "a defect" || crit["refund"] != "refund" {
		t.Errorf("criteria = %v", crit)
	}
}

func TestConfigValidation(t *testing.T) {
	typesafe(t)
	for name, cfg := range map[string]map[string]interface{}{
		"no kind":            {"question": "q"},
		"bad kind":           {"kind": "rank", "question": "q"},
		"no question":        {"kind": "noul"},
		"one level":          {"kind": "score", "question": "q", "levels": []interface{}{"a"}},
		"eleven levels":      {"kind": "score", "question": "q", "levels": strings.Split("a,b,c,d,e,f,g,h,i,j,k", ",")},
		"one option":         {"kind": "choice", "question": "q", "options": []interface{}{"a"}},
		"reserved option":    {"kind": "choice", "question": "q", "options": []interface{}{"a", "low_confidence"}},
		"duplicate option":   {"kind": "choice", "question": "q", "options": []interface{}{"a", "a"}},
		"bad options":        {"kind": "choice", "question": "q", "options": 3.0},
		"a number id":        {"kind": "choice", "question": "q", "options": []interface{}{map[string]interface{}{"id": 3.0}, "b"}},
		"a number value":     {"kind": "choice", "question": "q", "options": []interface{}{map[string]interface{}{"value": true}, "b"}},
		"keys equal trimmed": {"kind": "choice", "question": "q", "options": map[string]interface{}{" a": "x", "a": "y"}},
		"min_confidence > 1": {"kind": "noul", "question": "q", "min_confidence": 1.5},
	} {
		if _, err := (&Node{}).Execute(testCtx(), workflow.NodeInput{}, cfg); !errors.Is(err, workflow.ErrInvalidConfig) {
			t.Errorf("%s: err = %v, want ErrInvalidConfig", name, err)
		}
	}
}

func TestMissingKeyIsInvalidConfig(t *testing.T) {
	srv := jevtest.NewServer(t, nil)
	t.Setenv("TYPESAFE_API_KEY", "")
	_, err := (&Node{}).Execute(testCtx(), workflow.NodeInput{Items: items("x")}, map[string]interface{}{"kind": "noul", "question": "q"})
	if !errors.Is(err, workflow.ErrInvalidConfig) || !strings.Contains(err.Error(), "jev key set") || srv.Calls() != 0 {
		t.Errorf("err = %v, calls = %d", err, srv.Calls())
	}
}

func TestPerItemInputAndProjection(t *testing.T) {
	srv := typesafe(t)
	n := &Node{}
	var _ workflow.PerItemConfigResolver = n
	if got := n.PerItemConfigFields(); len(got) != 1 || got[0] != "input" {
		t.Fatalf("per-item fields = %v", got)
	}
	it := workflow.NewItem(map[string]interface{}{"subject": "Invoice", "body": "please pay", "secret_note": "do not send"})
	cfg := map[string]interface{}{"kind": "noul", "question": "q", "fields": []interface{}{"subject", "body"}}
	if _, err := n.Execute(testCtx(), workflow.NodeInput{Items: []workflow.Item{it}}, cfg); err != nil {
		t.Fatal(err)
	}
	if got := stateInput(srv.Requests()[0]); !strings.Contains(got, "Invoice") || strings.Contains(got, "do not send") {
		t.Errorf("projection = %q", got)
	}
	cfg["input"] = "S: {{$json.subject}} {{ .node.Secret.json.token }}"
	in := workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"subject": "{{ .node.Secret.json.token }}"})},
		NodeOutputs: map[string][]workflow.Item{"Secret": {workflow.NewItem(map[string]interface{}{"token": "sk-SECRET"})}}}
	if _, err := n.Execute(testCtx(), in, cfg); err != nil {
		t.Fatal(err)
	}
	if got := stateInput(srv.Requests()[1]); got != "S: {{ .node.Secret.json.token }} sk-SECRET" {
		t.Errorf("template = %q (item content must stay literal, the template itself resolves)", got)
	}
	big := workflow.NewItem(map[string]interface{}{"text": strings.Repeat("a", 10000)})
	if _, err := n.Execute(testCtx(), workflow.NodeInput{Items: []workflow.Item{big}}, map[string]interface{}{"kind": "noul", "question": "q"}); err != nil {
		t.Fatal(err)
	}
	if got := []rune(stateInput(srv.Requests()[2])); len(got) != maxInputChars+1 {
		t.Errorf("capped input = %d runes", len(got))
	}
}

func TestEmptyInputEmitsAllHandles(t *testing.T) {
	typesafe(t)
	out, err := (&Node{}).Execute(testCtx(), workflow.NodeInput{}, map[string]interface{}{"kind": "noul", "question": "q"})
	if err != nil || order(out) != "yes,no,low_confidence" {
		t.Errorf("outputs = %v, %v", out, err)
	}
}

func TestAPIErrorFailsNode(t *testing.T) {
	srv := typesafe(t)
	srv.SetStatus(400)
	_, err := (&Node{}).Execute(testCtx(), workflow.NodeInput{Items: items("a", "b")}, map[string]interface{}{"kind": "noul", "question": "q"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Errorf("err = %v", err)
	}
}

func TestRegisterAll(t *testing.T) {
	r := workflow.NewNodeTypeRegistry()
	RegisterAll(r)
	if f, ok := r.Get(NodeType); !ok || f().Type() != NodeType {
		t.Fatal("ai.decide not registered")
	}
}

// providerCtx saves provider "local" of a fake kind answering through fb.
func providerCtx(t *testing.T, fb *jevtest.FakeBackend, asDefault bool) context.Context {
	t.Helper()
	db := testdb.Open(t).DB
	t.Cleanup(jevconf.RegisterKind("fake-decide", func(jevconf.Provider, string) (jev.Backend, error) { return fb, nil }))
	if err := jevconf.SaveProvider(context.Background(), db, jevconf.Provider{
		Name: "local", Kind: "fake-decide", BaseURL: "http://localhost", Model: "fake-model",
	}); err != nil {
		t.Fatal(err)
	}
	if asDefault {
		if err := jevconf.SetDefaultProvider(context.Background(), db, "local"); err != nil {
			t.Fatal(err)
		}
	}
	return vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "p1")
}

func TestProviders(t *testing.T) {
	srv := typesafe(t)
	fb := jevtest.NewFakeBackend(byText)
	ctx := providerCtx(t, fb, false)
	cfg := map[string]interface{}{"kind": "noul", "question": "q", "input": "{{$json.text}}"}
	if _, err := (&Node{}).Execute(ctx, workflow.NodeInput{Items: items("a =0.9")}, cfg); err != nil || srv.Calls() != 1 || fb.Calls() != 0 {
		t.Fatalf("default TypeSafe: %v, TypeSafe %d, fake %d", err, srv.Calls(), fb.Calls())
	}
	cfg["provider"] = "local"
	out, err := (&Node{}).Execute(ctx, workflow.NodeInput{Items: items("a =0.9")}, cfg)
	if err != nil || fb.Calls() != 1 {
		t.Fatalf("provider local: %v, fake %d", err, fb.Calls())
	}
	if d := decisionOf(handles(out)["yes"][0]); d["provider"] != "local" || d["model"] != "fake-model" || d["probability_kind"] != "logprob" {
		t.Errorf("decision = %v", d)
	}
}

func TestDefaultProviderAnswers(t *testing.T) {
	srv := typesafe(t)
	fb := jevtest.NewFakeBackend(byText)
	ctx := providerCtx(t, fb, true)
	cfg := map[string]interface{}{"kind": "choice", "question": "q", "input": "{{$json.text}}", "options": []interface{}{"a", "b"}, "model": "jev-latest"}
	out, err := (&Node{}).Execute(ctx, workflow.NodeInput{Items: items("x =b")}, cfg)
	if err != nil || len(handles(out)["b"]) != 1 || srv.Calls() != 0 || fb.Requests()[0].Model != "fake-model" {
		t.Fatalf("err %v, routing %v, TypeSafe %d", err, handles(out), srv.Calls())
	}
}

// C9: option ids of an {id: description} object are trimmed, then sorted,
// as the editor derives the handles.
func TestOptionIDsAreTrimmedBeforeSorting(t *testing.T) {
	opts, err := parseOptions(map[string]any{" b": "B", "a ": "A", "c": "C"})
	if err != nil {
		t.Fatal(err)
	}
	if got := opts[0].id + "," + opts[1].id + "," + opts[2].id; got != "a,b,c" || opts[1].description != "B" {
		t.Fatalf("options = %+v", opts)
	}
}

// C6: more options than the provider takes, or a kind it does not answer,
// fails the node's config before any item is asked.
func TestAQuestionTheProviderCannotTakeIsInvalidConfig(t *testing.T) {
	fb := jevtest.NewFakeBackend(byText)
	fb.Caps.MaxChoiceOptions = 2
	ctx := providerCtx(t, fb, true)
	cfg := map[string]interface{}{"kind": "choice", "question": "q", "input": "{{$json.text}}", "options": []interface{}{"a", "b", "c"}}
	if _, err := (&Node{}).Execute(ctx, workflow.NodeInput{Items: items("x =b")}, cfg); !errors.Is(err, workflow.ErrInvalidConfig) ||
		!strings.Contains(err.Error(), "at most 2 options") || fb.Calls() != 0 {
		t.Fatalf("err %v, calls %d", err, fb.Calls())
	}
	cfg["options"] = []interface{}{"a", "b"}
	if _, err := (&Node{}).Execute(ctx, workflow.NodeInput{Items: items("x =b")}, cfg); err != nil {
		t.Fatalf("two options: %v", err)
	}
	fb.Caps.Kinds = []string{jev.TypeChoice}
	if _, err := (&Node{}).Execute(ctx, workflow.NodeInput{Items: items("x =0.9")}, map[string]interface{}{"kind": "noul", "question": "q"}); !errors.Is(err, workflow.ErrInvalidConfig) ||
		!strings.Contains(err.Error(), "does not answer noul") {
		t.Fatalf("an unsupported kind: %v", err)
	}
}

// D7: a provider without probabilities never passes min_confidence.
func TestNoProbabilityIsLowConfidence(t *testing.T) {
	fb := jevtest.NewFakeBackend(byText)
	fb.Caps.Probability = jev.ProbNone
	ctx := providerCtx(t, fb, true)
	out, err := (&Node{}).Execute(ctx, workflow.NodeInput{Items: items("a =0.99")}, map[string]interface{}{"kind": "noul", "question": "q", "input": "{{$json.text}}"})
	if err != nil {
		t.Fatal(err)
	}
	h := handles(out)
	if len(h["low_confidence"]) != 1 || len(h["yes"]) != 0 {
		t.Fatalf("routing = %v", h)
	}
	if d := decisionOf(h["low_confidence"][0]); d["answer"] != "yes" || d["probability"] != 0.0 || d["probability_kind"] != "none" {
		t.Errorf("decision = %v", d)
	}
}
