package jevpick

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
)

// pickPage is a fake tab for Pick: it serves a fixed snapshot, a freshness
// probe that can report staleness, and records mark/unmark scripts.
type pickPage struct {
	state     PageState
	staleLeft int // report this many freshness probes as changed
	gone      bool
	marked    map[string]int // marker → node
	scripts   []string
}

func newPickPage() *pickPage {
	return &pickPage{
		state: PageState{
			URL: "https://example.test/", Title: "Shop", Text: "Welcome to the shop",
			Marker: json.RawMessage(`["m"]`), PageKey: json.RawMessage(`["k"]`),
			Guards: map[string]json.RawMessage{"10": json.RawMessage(`["g10"]`), "20": json.RawMessage(`["g20"]`), "30": json.RawMessage(`["g30"]`), "40": json.RawMessage(`["g40"]`)},
			Actions: []Action{
				{ID: "e1", Kind: "fill", Label: "Search products", Role: "searchbox", Node: 10},
				{ID: "e2", Kind: "click", Label: "Search products", Role: "searchbox", Node: 10},
				{ID: "e3", Kind: "click", Label: "Add to cart", Role: "button", Node: 20},
				{ID: "e4", Kind: "click", Label: "Sign in", Role: "link", Node: 30},
				{ID: "e5", Kind: "select", Label: "Size → Large", Value: "l", CurrentValue: "Small", Node: 40},
				{ID: "e6", Kind: "select", Label: "Size → Medium", Value: "m", CurrentValue: "Small", Node: 40},
				{ID: "wait", Kind: "wait", Label: "Wait for the page to update"},
			},
		},
		marked: map[string]int{},
	}
}

var (
	markRe   = regexp.MustCompile(`nodes\.get\((\d+)\); if \(!e\?\.isConnected\) return false; e\.setAttribute\("data-monoagent-jev","([0-9a-f]+)"\)`)
	unmarkRe = regexp.MustCompile(`querySelectorAll\('\[data-monoagent-jev="([0-9a-f]+)"\]'\)`)
)

func (f *pickPage) CDP(method string, params map[string]interface{}) (map[string]interface{}, error) {
	if method != "Runtime.evaluate" {
		return map[string]interface{}{}, nil
	}
	expr := params["expression"].(string)
	f.scripts = append(f.scripts, expr)
	var value interface{}
	switch {
	case strings.Contains(expr, "c.pageKey(),c.guard"), strings.Contains(expr, "state?.marker"):
		if f.staleLeft > 0 {
			f.staleLeft--
			value = []interface{}{"changed", "changed"}
			if strings.Contains(expr, "state?.marker") {
				value = []interface{}{"changed"}
			}
			break
		}
		if strings.Contains(expr, "state?.marker") {
			value = []interface{}{"m"}
			break
		}
		node := regexp.MustCompile(`nodes\.get\((\d+)\)`).FindStringSubmatch(expr)[1]
		var g interface{}
		_ = json.Unmarshal(f.state.Guards[node], &g)
		value = []interface{}{[]interface{}{"k"}, g}
	case markRe.MatchString(expr):
		m := markRe.FindStringSubmatch(expr)
		if f.gone {
			value = false
			break
		}
		var node int
		_ = json.Unmarshal([]byte(m[1]), &node)
		f.marked[m[2]] = node
		value = true
	case unmarkRe.MatchString(expr):
		delete(f.marked, unmarkRe.FindStringSubmatch(expr)[1])
		value = true
	case expr == snapshotJS:
		raw, _ := json.Marshal(f.state)
		_ = json.Unmarshal(raw, &value)
	}
	return map[string]interface{}{"result": map[string]interface{}{"value": value}}, nil
}

func testClient(t *testing.T) *jev.Client {
	t.Helper()
	c, err := jev.NewClient("test-key", "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func targetCriteria(t *testing.T, srv *jevtest.Server, i int) map[string]any {
	t.Helper()
	reqs := srv.Requests()
	if len(reqs) <= i {
		t.Fatalf("only %d requests", len(reqs))
	}
	crit, ok := reqs[i].Questions["target"].Criteria.(map[string]any)
	if !ok {
		t.Fatalf("criteria = %#v", reqs[i].Questions["target"].Criteria)
	}
	return crit
}

func TestPickByIntentMarksChosenNode(t *testing.T) {
	// Kind any: 1 = search box (node 10), 2 = Add to cart, 3 = Sign in, 4 = Size.
	srv := jevtest.NewServer(t, jevtest.Fixed(map[string]string{"target": "2"}))
	page := newPickPage()
	got, err := Pick(context.Background(), testClient(t), page,
		Target{Intent: "add the product to the cart", Kind: "any", Hint: "was #add", Context: map[string]any{"step": "buy"}}, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if got.Node != 20 || got.Label != "Add to cart" || got.Role != "button" || got.Probability != 0.94 || got.Confidence != 0.9 {
		t.Errorf("picked = %+v", got)
	}
	if page.marked[got.Marker] != 20 || len(got.Marker) != 16 {
		t.Errorf("marker %q not set on node 20: %v", got.Marker, page.marked)
	}
	crit := targetCriteria(t, srv, 0)
	if len(crit) != 5 || crit["NONE"] == nil {
		t.Errorf("criteria = %v, want 4 nodes + NONE (one entry per node)", crit)
	}
	if el := crit["4"].(map[string]any); el["element"] != "[4] Size" || el["current_value"] != "Small" {
		t.Errorf("select criterion = %v", el)
	}
	req := srv.Requests()[0]
	instr := req.Questions["target"].Instructions.(map[string]any)
	if instr["intent"] != "add the product to the cart" || instr["hint"] != "was #add" || !strings.Contains(instr["rules"].(string), "Choose NONE") {
		t.Errorf("instructions = %v", instr)
	}
	state := req.State.(map[string]any)
	if state["context"].(map[string]any)["step"] != "buy" || state["page"].(map[string]any)["title"] != "Shop" {
		t.Errorf("state = %v", state)
	}
}

func TestPickNoneIsNoMatch(t *testing.T) {
	srv := jevtest.NewServer(t, jevtest.Fixed(map[string]string{"target": "NONE"}))
	page := newPickPage()
	_, err := Pick(context.Background(), testClient(t), page, Target{Intent: "checkout", Kind: "click"}, 0.5)
	if !errors.Is(err, ErrNoMatch) {
		t.Fatalf("err = %v, want ErrNoMatch", err)
	}
	if len(page.marked) != 0 || srv.Calls() != 1 {
		t.Errorf("marked = %v calls = %d", page.marked, srv.Calls())
	}
}

func TestPickBelowThresholdIsNoMatch(t *testing.T) {
	jevtest.NewServer(t, jevtest.Fixed(map[string]string{"target": "2"})) // jevtest picks at 0.94
	page := newPickPage()
	_, err := Pick(context.Background(), testClient(t), page, Target{Intent: "add to cart", Kind: "click"}, 0.95)
	if !errors.Is(err, ErrNoMatch) || len(page.marked) != 0 {
		t.Fatalf("err = %v marked = %v, want ErrNoMatch", err, page.marked)
	}
}

// A provider that gives no probability (ProbNone) never clears the
// threshold, whatever its answer says (jev.GateValue).
func TestPickWithoutAProbabilityIsNoMatch(t *testing.T) {
	fb := jevtest.NewFakeBackend(jevtest.Fixed(map[string]string{"target": "2"}))
	fb.Caps.Probability = jev.ProbNone
	page := newPickPage()
	_, err := Pick(context.Background(), jev.NewBackendClient("local", "m", fb), page, Target{Intent: "add to cart", Kind: "click"}, 0.5)
	if !errors.Is(err, ErrNoMatch) || len(page.marked) != 0 || fb.Calls() != 1 {
		t.Fatalf("err = %v marked = %v calls = %d, want ErrNoMatch", err, page.marked, fb.Calls())
	}
	fb.Caps.Probability = jev.ProbLogprob
	if _, err := Pick(context.Background(), jev.NewBackendClient("local", "m", fb), newPickPage(), Target{Intent: "add to cart", Kind: "click"}, 0.5); err != nil {
		t.Fatalf("with a probability: %v", err)
	}
}

func TestPickKindFiltersCriteria(t *testing.T) {
	srv := jevtest.NewServer(t, jevtest.Fixed(map[string]string{"target": "1"}))
	page := newPickPage()
	got, err := Pick(context.Background(), testClient(t), page, Target{Intent: "search box", Kind: "fill"}, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	crit := targetCriteria(t, srv, 0)
	if len(crit) != 2 || crit["1"].(map[string]any)["element"] != "[1] Search products" || got.Node != 10 {
		t.Errorf("fill criteria = %v picked = %+v", crit, got)
	}

	if _, err := Pick(context.Background(), testClient(t), page, Target{Intent: "sign in", Kind: "click"}, 0.5); err != nil {
		t.Fatal(err)
	}
	crit = targetCriteria(t, srv, 1)
	var labels []string
	for id, c := range crit {
		if id != "NONE" {
			labels = append(labels, c.(map[string]any)["element"].(string))
		}
	}
	if len(crit) != 4 || strings.Contains(strings.Join(labels, ","), "Size") {
		t.Errorf("click criteria = %v (select must be excluded)", crit)
	}
}

func TestPickReobservesOnceWhenStale(t *testing.T) {
	srv := jevtest.NewServer(t, jevtest.Fixed(map[string]string{"target": "2"}))
	page := newPickPage()
	page.staleLeft = 1
	got, err := Pick(context.Background(), testClient(t), page, Target{Intent: "add to cart", Kind: "click"}, 0.5)
	if err != nil || got.Node != 20 || srv.Calls() != 2 {
		t.Fatalf("picked = %+v err = %v calls = %d", got, err, srv.Calls())
	}

	page = newPickPage()
	page.staleLeft = 2
	if _, err := Pick(context.Background(), testClient(t), page, Target{Intent: "add to cart", Kind: "click"}, 0.5); !errors.Is(err, ErrStale) {
		t.Fatalf("err = %v, want ErrStale", err)
	}
	if len(page.marked) != 0 {
		t.Errorf("stale pick marked %v", page.marked)
	}
}

func TestMarkUnmark(t *testing.T) {
	page := newPickPage()
	marker, err := Mark(page, 30)
	if err != nil {
		t.Fatal(err)
	}
	if page.marked[marker] != 30 {
		t.Fatalf("mark script not sent: %v", page.scripts)
	}
	if err := Unmark(page, marker); err != nil {
		t.Fatal(err)
	}
	if len(page.marked) != 0 {
		t.Errorf("unmark left %v", page.marked)
	}
	if err := Unmark(page, `x"]`); err == nil {
		t.Error("Unmark accepted a non-hex marker")
	}
	page.gone = true
	if _, err := Mark(page, 99); err == nil {
		t.Error("Mark of a gone node succeeded")
	}
}
