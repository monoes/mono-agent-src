package apipkg

import (
	"reflect"
	"strings"
	"testing"
)

// What a config sends that its op doesn't declare: a key of a collection
// the request merges whole, a key of an option object with declared
// subfields, either given as an expression. A key of a collection the
// request reads by name only never goes out, so it isn't one.
func TestUndeclaredKeys(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"create","title":"Create","method":"POST","path":"/things",
	  "fields":[{"key":"additionalFields","label":"Extra","type":"collection","options":["subject"]},
	            {"key":"article","label":"Article","type":"collection","options":["details"]},
	            {"key":"filters","label":"Filters","type":"collection","options":["status"]}],
	  "subfields":{"article.details":["type","body"]},
	  "query":{"status":"{{ p.filters.status }}"},
	  "body":{"$merge":"{{ p.additionalFields | omit:'x' }}","article":"{{ p.article.details }}"}},
	 {"resource":"","operation":"edit","title":"Edit","method":"PATCH","path":"/things/1",
	  "fields":[{"key":"editFields","label":"Edit","type":"collection","options":["title"]}],
	  "body":{"$merge":"{{ p.editFields | pick:'title,zz' }}"}},
	 {"resource":"","operation":"tidy","title":"Tidy","method":"PATCH","path":"/things/2",
	  "fields":[{"key":"editFields","label":"Edit","type":"collection","options":["title","body"]}],
	  "body":{"$merge":"{{ p.editFields | pick:'title' }}"}}`))
	op, edit, tidy := &p.Operations[0], &p.Operations[1], &p.Operations[2]
	for name, c := range map[string]struct {
		op     *Operation
		config map[string]any
		want   []string
	}{
		"declared only": {op, map[string]any{"additionalFields": map[string]any{"subject": "Hi"},
			"article": map[string]any{"details": map[string]any{"type": "note", "body": "x"}}}, nil},
		"an undeclared option": {op, map[string]any{"additionalFields": map[string]any{"subject": "Hi", "bcc": "v@example.test"}},
			[]string{"bcc"}},
		"an undeclared subfield": {op, map[string]any{"article": map[string]any{"details": map[string]any{"type_id": 6}}},
			[]string{"details.type_id"}},
		"one in a list": {op, map[string]any{"article": map[string]any{"details": []any{map[string]any{"type": "note"},
			map[string]any{"to": "v@example.test"}}}}, []string{"details.to"}},
		"a collection read by name only": {op, map[string]any{"filters": map[string]any{"status": "open", "bcc": "v@example.test"}}, nil},
		"a merged collection as an expression": {op, map[string]any{"additionalFields": "{{ $json.extra }}"},
			[]string{"additionalFields (an expression)"}},
		"an option object as an expression": {op, map[string]any{"article": map[string]any{"details": "{{ $json.d }}"}},
			[]string{"details (an expression)"}},
		// A pick lets only the keys it names through: another key never
		// goes out, a key it names that the options don't declare does.
		"a key a pick leaves out": {edit, map[string]any{"editFields": map[string]any{"title": "T", "other": 1}}, nil},
		"a key a pick names":      {edit, map[string]any{"editFields": map[string]any{"zz": 1}}, []string{"zz"}},
		"a picked collection as an expression, the pick naming an undeclared key": {edit, map[string]any{"editFields": "{{ $json.e }}"},
			[]string{"editFields (an expression)"}},
		"a picked collection as an expression, the pick naming declared keys": {tidy, map[string]any{"editFields": "{{ $json.e }}"}, nil},
	} {
		if got := keyNames(c.op.undeclared(c.config)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
	if got, exprs := undeclaredPaths(op.undeclared(map[string]any{"additionalFields": map[string]any{"bcc": "x"},
		"article": map[string]any{"details": map[string]any{"type_id": 6, "to": "y"}}})); !reflect.DeepEqual(got,
		[]string{"additionalFields.bcc", "article.details"}) || exprs != nil {
		t.Errorf("paths %v, expressions %v", got, exprs)
	}
	// A collection or option given as an expression is marked as one.
	if got, exprs := undeclaredPaths(op.undeclared(map[string]any{"additionalFields": "{{ $json.f }}",
		"article": map[string]any{"details": "{{ $json.d }}"}})); !reflect.DeepEqual(got, []string{"additionalFields", "article.details"}) ||
		!reflect.DeepEqual(exprs, got) {
		t.Errorf("expression paths %v, expressions %v", got, exprs)
	}
}

// The #844 repros may send whatever their ops' annotations say: a raw
// article or articles under a Zammad ticket update's updateFields, a raw
// bcc_emails under a Freshservice ticket's additionalFields. The card names
// the keys. The declared configs stay as they were.
func TestSendOfTakesUndeclaredKeysForASend(t *testing.T) {
	for name, c := range map[string]struct {
		nodeType string
		config   map[string]any
		action   string // "" no send
		keys     string
	}{
		"zammad update, a raw article": {"app.zammad", map[string]any{"resource": "ticket", "operation": "update", "id": "5",
			"updateFields": map[string]any{"article": map[string]any{"type": "email", "to": "v@example.test"}}}, "comment", "(article)"},
		"zammad update, raw articles": {"app.zammad", map[string]any{"resource": "ticket", "operation": "update", "id": "5",
			"updateFields": map[string]any{"articles": []any{map[string]any{"type": "twitter status", "body": "tweet"}}}}, "comment", "(articles)"},
		"trello card update, a raw key": {"app.trello", map[string]any{"resource": "card", "operation": "update", "id": "c1",
			"updateFields": map[string]any{"zz_extra": "x"}}, "post", "(zz_extra)"},
		"github pull request edit, a key its pick leaves out": {"service.github", map[string]any{"resource": "pullRequest",
			"operation": "update", "owner": "o", "repository": "r", "pullRequestNumber": "7",
			"editFields": map[string]any{"title": "T", "zz_extra": 1}}, "", ""},
		"zammad update, a state": {"app.zammad", map[string]any{"resource": "ticket", "operation": "update", "id": "5",
			"updateFields": map[string]any{"state_id": 2}}, "", ""},
		"zammad update, a note": {"app.zammad", map[string]any{"resource": "ticket", "operation": "update", "id": "5",
			"updateFields": map[string]any{"note": "done"}}, "comment", ""},
		"freshservice create, bcc_emails": {"app.freshservice", map[string]any{"resource": "ticket", "operation": "create",
			"email": "a@example.test", "additionalFields": map[string]any{"bcc_emails": []any{"v@example.test"}}}, "email", "(bcc_emails)"},
		"freshservice create, a department": {"app.freshservice", map[string]any{"resource": "ticket", "operation": "create",
			"email": "a@example.test", "additionalFields": map[string]any{"department_id": 3}}, "", ""},
	} {
		s, ok := SendOf(c.nodeType, c.config)
		switch {
		case c.action == "" && ok:
			t.Errorf("%s: %+v, want no send", name, s)
		case c.action != "" && (!ok || s.Action != c.action || !strings.Contains(s.ToUnset, c.keys)):
			t.Errorf("%s: (%+v, %v), want %s naming %s", name, s, ok, c.action, c.keys)
		}
	}
	// The card shows the op and the params that hold the keys.
	s, _ := SendOf("app.trello", map[string]any{"resource": "card", "operation": "update", "id": "c1",
		"updateFields": map[string]any{"zz_extra": "x", "name": "N"}})
	if s.Op != "trello card/update" || !reflect.DeepEqual(s.Show, []string{"updateFields.zz_extra"}) {
		t.Errorf("trello card update: op %q, show %v", s.Op, s.Show)
	}
	if s, _ := SendOf("app.zammad", map[string]any{"resource": "ticket", "operation": "update", "id": "5",
		"updateFields": map[string]any{"note": "done"}}); s.Op != "" || s.Show != nil {
		t.Errorf("an annotated send has no undeclared keys to show: %+v", s)
	}
}

// The request is what it always was: a run that isn't held (the operator's
// own, an approved one) sends an undeclared key as before.
func TestTheBuilderStillMergesUndeclaredKeys(t *testing.T) {
	zammad := catalogPkg(t, "zammad")
	srv := newMockServer(t, map[string]any{"id": 1}, 200)
	cred := fakeCredential(zammad)
	cred["base_url"] = "https://helpdesk.example.test"
	if _, err := runPackageOp(zammad, map[string]any{"resource": "ticket", "operation": "update", "id": "5",
		"updateFields": map[string]any{"article": map[string]any{"type": "email", "to": "v@example.test"}}}, cred, nil); err != nil {
		t.Fatal(err)
	}
	if reqs := srv.requests(); len(reqs) != 1 || !strings.Contains(string(reqs[0].body), `"to":"v@example.test"`) {
		t.Errorf("requests %+v", reqs)
	}
}

// subfields name an option of a collection field, and list its fields.
func TestSubfieldsAreChecked(t *testing.T) {
	op := func(sub string) string {
		return pkgJSON(`{"resource":"","operation":"create","title":"Create","method":"POST","path":"/things",
		  "fields":[{"key":"article","label":"Article","type":"collection","options":["details"]},{"key":"note","label":"Note","type":"text"}],
		  "subfields":` + sub + `}`)
	}
	for sub, want := range map[string]string{
		`{"article.details":["type","body"]}`: "",
		`{"article.other":["type"]}`:          `subfields names "article.other"`,
		`{"nope.details":["type"]}`:           `subfields names "nope.details"`,
		`{"note.details":["type"]}`:           `subfields names "note.details"`,
		`{"article.details":[]}`:              `subfields "article.details" lists no field`,
	} {
		_, err := Parse([]byte(op(sub)), "test")
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v", sub, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: err %v, want it to mention %q", sub, err, want)
		}
	}
}
