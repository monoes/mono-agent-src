package extension

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

type fakeFiller struct {
	facts   []FormFact
	answer  string
	factErr error
	prompt  string
	queries []string
	runtime string
}

func (f *fakeFiller) Facts(_ context.Context, _ string, q []string) ([]FormFact, error) {
	f.queries = q
	return f.facts, f.factErr
}

func (f *fakeFiller) Complete(_ context.Context, runtime, _ string, prompt string) (string, error) {
	f.runtime, f.prompt = runtime, prompt
	return f.answer, nil
}

func fillSrv(f FormFiller) *Server {
	srv := NewServer("127.0.0.1:0", zerolog.Nop())
	srv.SetFormFiller(f)
	return srv
}

func fld(id, label, typ string, extra map[string]any) map[string]any {
	m := map[string]any{"id": id, "label": label, "type": typ, "name": id}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestFormFillValuesAreCheckedAgainstTheirFields(t *testing.T) {
	f := &fakeFiller{
		facts: []FormFact{{Source: "/docs/passport.pdf", Text: "Name: Ada Lovelace. Born 1815."}},
		answer: "```json\n" + `{"values":{
			"first":{"value":"Ada","fact":1},
			"title":{"value":"ms.","fact":1},
			"country":{"value":"Atlantis","fact":1},
			"ghost":{"value":"x","fact":1},
			"bio":{"value":"` + strings.Repeat("a", 400) + `","fact":1}}}` + "\n```",
	}
	got, err := callChat(t, fillSrv(f), MethodFormFill, map[string]any{
		"profile": "p1",
		"fields": []any{
			fld("first", "First Name", "text", nil),
			fld("title", "Title", "select", map[string]any{"options": []any{"Mr.", "Ms.", "Dr."}}),
			fld("country", "Country", "select", map[string]any{"options": []any{"Germany", "Ireland"}}),
			fld("bio", "Bio", "text", nil),
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := got.(*FormFillReply)
	if len(r.Values) != 2 {
		t.Fatalf("want first+title, got %+v", r.Values)
	}
	if r.Values[0] != (FormValue{ID: "first", Value: "Ada", Source: "passport.pdf"}) {
		t.Errorf("first: %+v", r.Values[0])
	}
	if r.Values[1].Value != "Ms." {
		t.Errorf("an option is matched case-blind and returned as listed: %+v", r.Values[1])
	}
	if r.Skipped != 2 {
		t.Errorf("skipped = %d, want 2", r.Skipped)
	}
}

func TestFormFillNeverShowsSecretFieldsToTheModel(t *testing.T) {
	f := &fakeFiller{facts: []FormFact{{Source: "a.txt", Text: "Ada"}}, answer: `{"values":{}}`}
	got, err := callChat(t, fillSrv(f), MethodFormFill, map[string]any{
		"profile": "p1",
		"fields": []any{
			fld("pw", "Password", "password", nil),
			fld("cc", "Card number", "text", nil),
			fld("code", "Code", "text", map[string]any{"autocomplete": "one-time-code"}),
			fld("agree", "I agree", "checkbox", nil),
			fld("first", "First name", "text", nil),
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.(*FormFillReply).Skipped != 5 {
		t.Errorf("skipped = %d, want 5 (four withheld, one unanswered)", got.(*FormFillReply).Skipped)
	}
	for _, id := range []string{`"pw"`, `"cc"`, `"code"`, `"agree"`} {
		if strings.Contains(f.prompt, id) {
			t.Errorf("prompt mentions %s", id)
		}
	}
	if !strings.Contains(f.prompt, `"first"`) {
		t.Error("prompt lacks the fillable field")
	}
}

func TestFormFillWithNoFactsDoesNotAskTheModel(t *testing.T) {
	f := &fakeFiller{answer: "should not be used"}
	got, err := callChat(t, fillSrv(f), MethodFormFill, map[string]any{
		"profile": "p1", "fields": []any{fld("first", "First name", "text", nil)},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r := got.(*FormFillReply); len(r.Values) != 0 || r.Skipped != 1 {
		t.Errorf("%+v", r)
	}
	if f.prompt != "" {
		t.Error("the model was asked with nothing to go on")
	}
}

func TestFormFillIDQueryOnlyWhenFormAsksForIDs(t *testing.T) {
	f := &fakeFiller{facts: []FormFact{{Source: "a", Text: "x"}}, answer: `{"values":{}}`}
	srv := fillSrv(f)
	call := func(label string) {
		if _, err := callChat(t, srv, MethodFormFill, map[string]any{
			"profile": "p1", "fields": []any{fld("f", label, "text", nil)},
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	call("First name")
	n := len(f.queries)
	call("Tax identification number")
	if len(f.queries) != n+1 {
		t.Errorf("queries %d then %d", n, len(f.queries))
	}
}

func TestFormFillRefusals(t *testing.T) {
	srv := fillSrv(&fakeFiller{})
	for name, p := range map[string]map[string]any{
		"no profile":  {"fields": []any{fld("a", "A", "text", nil)}},
		"no fields":   {"profile": "p1"},
		"bad id":      {"profile": "p1", "fields": []any{fld("a b", "A", "text", nil)}},
		"dup id":      {"profile": "p1", "fields": []any{fld("a", "A", "text", nil), fld("a", "B", "text", nil)}},
		"bad runtime": {"profile": "p1", "runtime": "x y", "fields": []any{fld("a", "A", "text", nil)}},
	} {
		if _, err := callChat(t, srv, MethodFormFill, p, nil); errCode(err) != CodeInvalidInput {
			t.Errorf("%s: %v", name, err)
		}
	}
	bad := &fakeFiller{facts: []FormFact{{Source: "a", Text: "x"}}, answer: "no json here"}
	if _, err := callChat(t, fillSrv(bad), MethodFormFill, map[string]any{
		"profile": "p1", "fields": []any{fld("a", "A", "text", nil)},
	}, nil); errCode(err) != CodeUnavailable {
		t.Errorf("unusable answer: %v", err)
	}
	down := &fakeFiller{factErr: errors.New("monomind missing")}
	if _, err := callChat(t, fillSrv(down), MethodFormFill, map[string]any{
		"profile": "p1", "fields": []any{fld("a", "A", "text", nil)},
	}, nil); errCode(err) != CodeUnavailable {
		t.Errorf("search down: %v", err)
	}
}
