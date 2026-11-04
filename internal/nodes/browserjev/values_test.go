package browserjev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/jevpick"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

type formField struct {
	node             int
	label, role, typ string
}

// formDriver is a one-form site: fill fields (nodes from fields), a Submit
// button (node 99). After submit the page echoes every typed value — the
// worst case for leaking a secret into page text. Element values report
// what was typed, password fields included (the real snapshot skips them).
type formDriver struct {
	fields    []formField
	typed     map[int]string
	submitted bool
	executed  []string
}

func (f *formDriver) Observe(context.Context) (*jevpick.PageState, error) { return f.state(), nil }

func (f *formDriver) Fresh(p *jevpick.PageState, _ *jevpick.Action) (bool, error) {
	return p.Fingerprint == f.state().Fingerprint, nil
}

func (f *formDriver) InputType(a *jevpick.Action) string {
	for _, fl := range f.fields {
		if fl.node == a.Node {
			return fl.typ
		}
	}
	return ""
}

func (f *formDriver) Act(_ context.Context, _ *jevpick.PageState, a *jevpick.Action, text string) error {
	f.executed = append(f.executed, a.ID+":"+text)
	if a.ID == "go" {
		f.submitted = true
	} else if a.Kind == "fill" {
		if f.typed == nil {
			f.typed = map[int]string{}
		}
		f.typed[a.Node] = text
	}
	return nil
}

func (f *formDriver) state() *jevpick.PageState {
	p := &jevpick.PageState{URL: "https://example.test/form", Title: "Form", Text: "Please fill the form",
		Scroll: json.RawMessage(`{"y":0}`)}
	if f.submitted {
		p.URL, p.Text = "https://example.test/done", "Submitted:"
		for _, fl := range f.fields {
			p.Text += " " + fl.label + "=" + f.typed[fl.node]
		}
	} else {
		for i, fl := range f.fields {
			p.Actions = append(p.Actions, jevpick.Action{ID: fmt.Sprintf("f%d", i), Kind: "fill", Label: fl.label,
				Role: fl.role, Value: f.typed[fl.node], Node: fl.node})
		}
		p.Actions = append(p.Actions, jevpick.Action{ID: "go", Kind: "click", Label: "Submit", Role: "button", Node: 99})
	}
	p.Actions = append(p.Actions, jevpick.Action{ID: "wait", Kind: "wait", Label: "Wait for the page to update"})
	p.Fingerprint = jevpick.Fingerprint(p)
	return p
}

// formPolicy types into the first empty field, then submits, then DONE.
// Value questions are answered by pick(field label).
func formPolicy(t *testing.T, pick func(label string) string) *jevtest.Server {
	return jevtest.NewServer(t, func(req jev.Request) map[string]string {
		raw, _ := json.Marshal(req.State)
		if _, ok := req.Questions["value"]; ok {
			var st struct{ Field struct{ Label string } }
			_ = json.Unmarshal(raw, &st)
			return map[string]string{"value": pick(st.Field.Label)}
		}
		var st struct {
			Page     struct{ URL string }
			Elements []element
		}
		if err := json.Unmarshal(raw, &st); err != nil {
			t.Error(err)
		}
		if strings.Contains(st.Page.URL, "done") {
			return map[string]string{"operation": "DONE"}
		}
		for _, e := range st.Elements {
			if contains(e.Operations, "TYPE_TEXT") && e.Value == "" {
				return map[string]string{"operation": "TYPE_TEXT", "type_text_target": e.Index}
			}
		}
		return map[string]string{"operation": "CLICK", "click_target": st.Elements[len(st.Elements)-1].Index}
	})
}

func formNode(drv driver, writer textWriter) *Node {
	if writer == nil {
		writer = func(context.Context, map[string]any) (string, error) {
			return "", errors.New("the text writer must not be called")
		}
	}
	return testNode(drv, writer)
}

func runForm(t *testing.T, ctx context.Context, n *Node, goal string, values any) map[string]interface{} {
	t.Helper()
	out, err := n.Execute(ctx, workflow.NodeInput{}, map[string]interface{}{
		"url": "https://example.test/form", "goal": goal, "api_key": "k", "values": values,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return out[0].Items[0].JSON
}

func valueQuestions(reqs []jev.Request) int {
	n := 0
	for _, r := range reqs {
		if _, ok := r.Questions["value"]; ok {
			n++
		}
	}
	return n
}

func TestValueChosenSkipsWriter(t *testing.T) {
	srv := formPolicy(t, func(label string) string { return label })
	drv := &formDriver{fields: []formField{{10, "From", "textbox", "text"}, {11, "To", "textbox", "text"}}}
	res := runForm(t, testCtx(), formNode(drv, nil), "Book a train from Zurich to Basel",
		map[string]interface{}{"From": "Zurich", "To": "Basel"})
	if res["status"] != "done" {
		t.Fatalf("status = %v (%v)", res["status"], res["reason"])
	}
	if got := strings.Join(drv.executed, ","); got != "f0:Zurich,f1:Basel,go:" {
		t.Errorf("executed = %s", got)
	}
	if res["text_turns"] != 0 || res["value_requests"] != 2 || valueQuestions(srv.Requests()) != 2 {
		t.Errorf("text_turns=%v value_requests=%v", res["text_turns"], res["value_requests"])
	}
	steps := res["steps"].([]step)
	if steps[0].Text != "Zurich" || steps[0].ValueName != "From" {
		t.Errorf("plain values may appear in the output: %+v", steps[0])
	}
	// Even plain values are sent to Jev only as <value:NAME>.
	if all := srv.RequestJSON(); strings.Contains(all, "Zurich") || strings.Contains(all, "Basel") {
		t.Errorf("a configured value reached Jev: %s", all)
	}
	if !strings.Contains(srv.RequestJSON(), jsonPlaceholder("From")) {
		t.Error("expected <value:From> placeholders in later requests")
	}
}

func TestValueNoneCallsWriter(t *testing.T) {
	formPolicy(t, func(string) string { return "NONE" })
	drv := &formDriver{fields: []formField{{10, "Search", "searchbox", "search"}}}
	var writes int
	res := runForm(t, testCtx(), formNode(drv, func(context.Context, map[string]any) (string, error) {
		writes++
		return "trains", nil
	}), "Search for trains", `{"City": "Zurich"}`) // JSON text, as the GUI's code editor stores it
	if res["status"] != "done" || writes != 1 || res["text_turns"] != 1 {
		t.Fatalf("status=%v writes=%d text_turns=%v", res["status"], writes, res["text_turns"])
	}
	if drv.executed[0] != "f0:trains" {
		t.Errorf("executed = %v", drv.executed)
	}
}

// vaultCtx returns a context carrying a migrated DB whose profile p1 holds
// the given secrets.
func vaultCtx(t *testing.T, entries map[string]string) context.Context {
	t.Helper()
	keyring.MockInit()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "p1")
	for name, v := range entries {
		if _, err := secrets.Add(ctx, db.DB, "p1", "secret", name, map[string]string{"secret": v}, "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	return ctx
}

func TestSecretValueNeverReachesJev(t *testing.T) {
	const secret = "hunter2-S3cretValue"
	ctx := vaultCtx(t, map[string]string{"site-pw": secret})
	srv := formPolicy(t, func(label string) string {
		if label == "Password" {
			return "password"
		}
		return "user"
	})
	drv := &formDriver{fields: []formField{{10, "Username", "textbox", "text"}, {11, "Password", "textbox", "password"}}}
	res := runForm(t, ctx, formNode(drv, nil), "Sign in",
		map[string]interface{}{"user": "alice.example", "password": "@secret:site-pw"})
	if res["status"] != "done" {
		t.Fatalf("status = %v (%v)", res["status"], res["reason"])
	}
	if got := strings.Join(drv.executed, ","); got != "f0:alice.example,f1:"+secret+",go:" {
		t.Fatalf("executed = %s", got)
	}
	reqs := srv.Requests()
	// Two value requests, and decision requests after the secret was typed
	// (the page then shows it as an element value and in page text).
	if valueQuestions(reqs) != 2 || len(reqs) < 5 {
		t.Fatalf("requests = %d (value %d)", len(reqs), valueQuestions(reqs))
	}
	if all := srv.RequestJSON(); strings.Contains(all, secret) {
		t.Fatalf("the secret reached Jev: %s", all)
	}
	if !strings.Contains(reqsJSON(reqs[len(reqs)-1]), jsonPlaceholder("password")) {
		t.Error("the final request should show the secret as <value:password>")
	}
	out, _ := json.Marshal(res)
	if strings.Contains(string(out), secret) {
		t.Fatalf("the secret is in the node output: %s", out)
	}
	steps := res["steps"].([]step)
	if steps[1].Text != "<value:password>" || steps[0].Text != "alice.example" {
		t.Errorf("steps text = %q, %q", steps[0].Text, steps[1].Text)
	}

	// A 3-character secret (a CVV into a tel field) cannot be kept out of
	// page text, URLs and labels, so it is refused before anything is sent.
	t.Run("short secret refused", func(t *testing.T) {
		ctx := vaultCtx(t, map[string]string{"card-cvv": "123"})
		srv := formPolicy(t, func(string) string { return "cvv" })
		drv := &formDriver{fields: []formField{{10, "CVV", "textbox", "tel"}}}
		_, err := formNode(drv, nil).Execute(ctx, workflow.NodeInput{}, map[string]interface{}{
			"url": "https://example.test/form", "goal": "Pay", "api_key": "k",
			"values": map[string]interface{}{"cvv": "@secret:card-cvv"}})
		if !errors.Is(err, workflow.ErrInvalidConfig) || !strings.Contains(err.Error(), "secret value for cvv is shorter than 4 characters") {
			t.Fatalf("err = %v", err)
		}
		if len(srv.Requests()) != 0 || len(drv.executed) != 0 || strings.Contains(srv.RequestJSON(), "123") {
			t.Fatalf("a refused config still ran: requests=%d executed=%v", len(srv.Requests()), drv.executed)
		}
	})

	// At the 4-character minimum a secret (a PIN into a tel field) is typed
	// and replaced everywhere it shows up later — element value, the target
	// criteria, and the page text that echoes it after submit.
	t.Run("4-character secret", func(t *testing.T) {
		const pin = "Zq7x"
		ctx := vaultCtx(t, map[string]string{"card-pin": pin})
		srv := formPolicy(t, func(string) string { return "pin" })
		drv := &formDriver{fields: []formField{{10, "PIN", "textbox", "tel"}}}
		res := runForm(t, ctx, formNode(drv, nil), "Unlock the card", map[string]interface{}{"pin": "@secret:card-pin"})
		if res["status"] != "done" || strings.Join(drv.executed, ",") != "f0:"+pin+",go:" {
			t.Fatalf("status=%v executed=%v", res["status"], drv.executed)
		}
		if all := srv.RequestJSON(); strings.Contains(all, pin) || !strings.Contains(all, jsonPlaceholder("pin")) {
			t.Fatalf("the PIN reached Jev (or no placeholder): %s", all)
		}
	})
}

func TestShortPlainValueScrubbedInFieldValue(t *testing.T) {
	srv := formPolicy(t, func(label string) string { return "qty" })
	drv := &formDriver{fields: []formField{{10, "Quantity", "spinbutton", "number"}}}
	res := runForm(t, testCtx(), formNode(drv, nil), "Order 12 boxes", map[string]interface{}{"qty": "12"})
	if res["status"] != "done" || strings.Join(drv.executed, ",") != "f0:12,go:" {
		t.Fatalf("status=%v executed=%v", res["status"], drv.executed)
	}
	var sawField bool
	for _, r := range srv.Requests() {
		if _, ok := r.Questions["operation"]; !ok {
			continue
		}
		raw, _ := json.Marshal(r.State)
		var st struct {
			Elements      []element
			RecentActions []struct{ Text string } `json:"recent_actions"`
		}
		if err := json.Unmarshal(raw, &st); err != nil {
			t.Fatal(err)
		}
		for _, e := range st.Elements {
			if e.Label != "Quantity" {
				continue
			}
			if e.Value == "12" {
				t.Errorf("element value carries the typed value: %s", raw)
			}
			if e.Value == "<value:qty>" {
				sawField = true
			}
		}
		for _, a := range st.RecentActions {
			if a.Text == "12" {
				t.Errorf("history carries the typed value: %s", raw)
			}
		}
		// The target criteria carry current_value too.
		q, _ := json.Marshal(r.Questions)
		if strings.Contains(string(q), `"current_value":"12"`) {
			t.Errorf("current_value carries the typed value: %s", q)
		}
	}
	if !sawField {
		t.Error("expected the field's value as <value:qty> in a later request")
	}
	// Short plain values are not replaced inside text: the goal keeps "12".
	if !strings.Contains(srv.RequestJSON(), "Order 12 boxes") {
		t.Error("the goal's ordinary text was scrubbed")
	}
	if steps := res["steps"].([]step); steps[0].Text != "12" {
		t.Errorf("plain values stay in the node output: %+v", steps[0])
	}
}

func reqsJSON(r jev.Request) string { raw, _ := json.Marshal(r); return string(raw) }

// jsonPlaceholder is <value:name> as encoding/json writes it (HTML-escaped).
func jsonPlaceholder(name string) string {
	raw, _ := json.Marshal("<value:" + name + ">")
	return strings.Trim(string(raw), `"`)
}

func TestSecretRefusedForPlainSearchBox(t *testing.T) {
	ctx := vaultCtx(t, map[string]string{"tok": "tok-9f8e7d6c"})
	formPolicy(t, func(string) string { return "token" }) // p = 0.94 ≥ 0.9, but the field does not fit
	drv := &formDriver{fields: []formField{{10, "Search", "searchbox", "search"}}}
	var writes int
	res := runForm(t, ctx, formNode(drv, func(context.Context, map[string]any) (string, error) {
		writes++
		return "news", nil
	}), "Search for news", map[string]interface{}{"token": "@secret:tok"})
	if res["status"] != "done" || writes != 1 || drv.executed[0] != "f0:news" {
		t.Fatalf("status=%v writes=%d executed=%v", res["status"], writes, drv.executed)
	}
}

func TestSecretAcceptedWhenLabelNamesIt(t *testing.T) {
	ctx := vaultCtx(t, map[string]string{"tok": "tok-9f8e7d6c"})
	formPolicy(t, func(string) string { return "Token" })
	drv := &formDriver{fields: []formField{{10, "API token", "textbox", "text"}}}
	res := runForm(t, ctx, formNode(drv, nil), "Save my API token", map[string]interface{}{"Token": "@secret:tok"})
	if res["status"] != "done" || drv.executed[0] != "f0:tok-9f8e7d6c" {
		t.Fatalf("status=%v executed=%v", res["status"], drv.executed)
	}
}

func TestSecretFieldGate(t *testing.T) {
	for _, c := range []struct {
		name, label, typ string
		ok               bool
	}{
		{"pw", "Password", "password", true},
		{"mail", "Login", "email", true},
		{"phone", "Mobile", "tel", true},
		{"token", "Search", "search", false},
		{"Token", "Your API token", "text", true},
		{"token", "Search", "", false},
	} {
		if got := secretFieldOK(c.name, c.label, c.typ); got != c.ok {
			t.Errorf("%+v: got %v", c, got)
		}
	}
}

func TestValuesConfigErrors(t *testing.T) {
	ctx := vaultCtx(t, nil)
	n := formNode(&formDriver{}, nil)
	for name, c := range map[string]struct {
		ctx    context.Context
		values any
		want   string
	}{
		"missing secret": {ctx, map[string]interface{}{"password": "@secret:nope"}, `"password"`},
		"no vault":       {context.Background(), map[string]interface{}{"pw": "@secret:nope"}, `"pw"`},
		"bad json":       {ctx, `{"a":`, "JSON object"},
		"not a string":   {ctx, map[string]interface{}{"n": 3.0}, `"n"`},
		"empty":          {ctx, map[string]interface{}{"n": ""}, `"n"`},
		"reserved name":  {ctx, map[string]interface{}{"NONE": "x"}, `"NONE"`},
		"wrong type":     {ctx, []interface{}{"x"}, "object"},
	} {
		_, err := n.Execute(c.ctx, workflow.NodeInput{}, map[string]interface{}{
			"url": "u", "goal": "g", "api_key": "k", "values": c.values})
		if !errors.Is(err, workflow.ErrInvalidConfig) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want ErrInvalidConfig naming %s", name, err, c.want)
		}
	}
}

func TestScrubberSkipsShortValues(t *testing.T) {
	s := scrubber{values: []namedValue{{Name: "long", Value: "Zurich HB"}, {Name: "city", Value: "Zurich"}, {Name: "x", Value: "ab"}}}
	if got := s.str("from Zurich HB via Zurich, ab"); got != "from <value:long> via <value:city>, ab" {
		t.Errorf("scrubbed = %q", got)
	}
	if got := (scrubber{values: s.values, secretsOnly: true}).str("Zurich"); got != "Zurich" {
		t.Errorf("secretsOnly scrubbed a plain value: %q", got)
	}
	// A whole field value equal to a configured value is replaced at any length.
	if got := s.field(" ab "); got != "<value:x>" {
		t.Errorf("field(ab) = %q", got)
	}
	if got := s.field("abc"); got != "abc" {
		t.Errorf("field(abc) = %q", got)
	}
	if got := (scrubber{values: s.values, secretsOnly: true}).field("ab"); got != "ab" {
		t.Errorf("secretsOnly field scrubbed a plain value: %q", got)
	}
	// Select options are the page's own text: a short plain value stays.
	p := s.page(&jevpick.PageState{Actions: []jevpick.Action{
		{Kind: "select", Value: "ab", CurrentValue: "ab", Label: "Size → ab"},
		{Kind: "fill", Value: "ab"}, {Kind: "click", Value: "ab", Label: "Open Size"},
	}})
	if a := p.Actions; a[0].Value != "ab" || a[0].CurrentValue != "ab" || a[1].Value != "<value:x>" || a[2].Value != "<value:x>" {
		t.Errorf("page actions = %+v", a)
	}
}

func TestLowConfidenceStepsCounted(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jev.Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		n := calls.Add(1)
		want := map[string]string{"operation": "TYPE_TEXT", "type_text_target": "1"}
		if n == 2 {
			want = map[string]string{"operation": "CLICK", "click_target": "2"}
		} else if n > 2 {
			want = map[string]string{"operation": "DONE"}
		}
		answers := map[string]jev.Answer{}
		for id, q := range req.Questions {
			a, err := jevtest.Answer(q, want[id])
			if err != nil {
				t.Error(err)
			}
			if n == 1 {
				a.Confidence = 0.3
			}
			answers[id] = a
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "jev-test", "answers": answers})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("TYPESAFE_BASE_URL", "")
	jevtest.DefaultHost(t, srv.URL)
	res := runNode(t, testNode(&fakeDriver{}, zurich))
	if res["status"] != "done" || res["low_confidence_steps"] != 1 {
		t.Fatalf("status=%v low_confidence_steps=%v", res["status"], res["low_confidence_steps"])
	}
}

func TestNoValuesMeansNoValueRequests(t *testing.T) {
	srv := policyServer(t, nil)
	res := runNode(t, testNode(&fakeDriver{}, zurich))
	if valueQuestions(srv.Requests()) != 0 || res["value_requests"] != 0 || res["text_turns"] != 1 || res["low_confidence_steps"] != 0 {
		t.Fatalf("res = %+v", res)
	}
}
