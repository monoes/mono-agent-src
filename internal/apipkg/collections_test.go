package apipkg

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// A collection param passes only its declared options into the request,
// and an option with declared subfields only those: an undeclared key is
// refused, naming the op, the key and what is declared, before any request
// goes out.
func TestACollectionPassesOnlyWhatItDeclares(t *testing.T) {
	p := mustParse(t, pkgJSON(`{"resource":"","operation":"create","title":"Create","method":"POST","path":"/things",
	  "fields":[{"key":"additionalFields","label":"Extra","type":"collection","options":["subject"]},
	            {"key":"article","label":"Article","type":"collection","options":["details"]}],
	  "subfields":{"article.details":["type","body"]},
	  "body":{"$merge":"{{ p.additionalFields }}","article":"{{ p.article.details }}"}}`))
	var bodies []string
	pagedServer(t, func(n int, r *http.Request, w http.ResponseWriter) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		w.Write([]byte(`{}`))
	})
	cred := map[string]any{"token": "t"}
	declared := map[string]any{"operation": "create", "additionalFields": map[string]any{"subject": "Hi"},
		"article": map[string]any{"details": map[string]any{"type": "note", "body": "x"}}}
	if _, err := runPackageOp(p, declared, cred, nil); err != nil || len(bodies) != 1 ||
		!strings.Contains(bodies[0], `"subject":"Hi"`) || !strings.Contains(bodies[0], `"type":"note"`) {
		t.Fatalf("declared: %v %q", err, bodies)
	}
	for name, c := range map[string]struct {
		params map[string]any
		want   string
	}{
		"an undeclared option": {map[string]any{"additionalFields": map[string]any{"subject": "Hi", "bcc": "x@example.test"}},
			`apipkg demo create: additionalFields has no option "bcc" (its options: subject)`},
		"an undeclared subfield": {map[string]any{"article": map[string]any{"details": map[string]any{"type_id": 6, "body": "x"}}},
			`apipkg demo create: article.details has no field "type_id" (its fields: type, body)`},
		"one in a list": {map[string]any{"article": map[string]any{"details": []any{map[string]any{"type": "note"},
			map[string]any{"to": "x@example.test"}}}}, `article.details has no field "to"`},
	} {
		bodies = nil
		params := map[string]any{"operation": "create"}
		for k, v := range c.params {
			params[k] = v
		}
		_, err := runPackageOp(p, params, cred, nil)
		if err == nil || !strings.Contains(err.Error(), c.want) || len(bodies) != 0 {
			t.Errorf("%s: err %v, %d request(s); want it refused with %q", name, err, len(bodies), c.want)
		}
	}
}

// credFor is a fake credential of p with a base URL that is one (Zammad's
// requests go to the connection's).
func credFor(p *Package) map[string]any {
	cred := fakeCredential(p)
	cred["base_url"] = "https://helpdesk.example.test"
	return cred
}

// The #844 repros reach the API no more: a Zammad ticket update's raw
// article or articles, a ticket create's type_id (with or without a type),
// a Freshservice ticket's bcc_emails or to are refused. What the ops
// declare passes, and a Zammad article with only a body is a note.
func TestRawKeysThroughCollectionsAreRefused(t *testing.T) {
	zammad, freshservice := catalogPkg(t, "zammad"), catalogPkg(t, "freshservice")
	for name, c := range map[string]struct {
		p      *Package
		params map[string]any
		want   string // "" passes
	}{
		"zammad update, a note": {zammad, map[string]any{"resource": "ticket", "operation": "update", "id": "5",
			"updateFields": map[string]any{"note": "done"}}, ""},
		"zammad update, a raw article": {zammad, map[string]any{"resource": "ticket", "operation": "update", "id": "5",
			"updateFields": map[string]any{"article": map[string]any{"type": "email", "to": "v@example.test", "body": "x"}}},
			`apipkg zammad ticket.update: updateFields has no option "article"`},
		"zammad update, raw articles": {zammad, map[string]any{"resource": "ticket", "operation": "update", "id": "5",
			"updateFields": map[string]any{"articles": []any{map[string]any{"type": "twitter status", "body": "tweet"}}}},
			`updateFields has no option "articles"`},
		"zammad create, a body": {zammad, map[string]any{"resource": "ticket", "operation": "create", "title": "T", "group": "Users",
			"customer": "a@example.test", "article": map[string]any{"articleDetails": map[string]any{"body": "x"}}}, ""},
		"zammad create, type_id": {zammad, map[string]any{"resource": "ticket", "operation": "create", "title": "T", "group": "Users",
			"customer": "a@example.test", "article": map[string]any{"articleDetails": map[string]any{"type_id": 6, "body": "tweet"}}},
			`article.articleDetails has no field "type_id"`},
		"zammad create, a note with type_id": {zammad, map[string]any{"resource": "ticket", "operation": "create", "title": "T",
			"group": "Users", "customer": "a@example.test",
			"article": map[string]any{"articleDetails": map[string]any{"type": "note", "type_id": 6, "body": "tweet"}}},
			`article.articleDetails has no field "type_id"`},
		"freshservice create, CCs": {freshservice, map[string]any{"resource": "ticket", "operation": "create", "email": "a@example.test",
			"subject": "S", "description": "D", "additionalFields": map[string]any{"cc_emails": "b@example.test"}}, ""},
		"freshservice create, bcc_emails": {freshservice, map[string]any{"resource": "ticket", "operation": "create",
			"email": "a@example.test", "subject": "S", "description": "D",
			"additionalFields": map[string]any{"bcc_emails": []any{"v@example.test"}}}, `additionalFields has no option "bcc_emails"`},
		"freshservice create, to": {freshservice, map[string]any{"resource": "ticket", "operation": "create", "email": "a@example.test",
			"subject": "S", "description": "D", "additionalFields": map[string]any{"to": "v@example.test"}}, `additionalFields has no option "to"`},
	} {
		srv := newMockServer(t, map[string]any{"id": 1}, 200)
		_, err := runPackageOp(c.p, c.params, credFor(c.p), nil)
		switch {
		case c.want == "" && (err != nil || len(srv.requests()) == 0):
			t.Errorf("%s: %v, %d request(s); want it to pass", name, err, len(srv.requests()))
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want) || len(srv.requests()) != 0):
			t.Errorf("%s: err %v, %d request(s); want it refused with %q", name, err, len(srv.requests()), c.want)
		}
	}
}

// CheckConfig finds the same keys ahead of a run (workflow validate,
// activation, n8n import), with the run's message. A collection or option
// given as an expression, or an op the expressions choose, is the run's to
// check.
func TestCheckConfigBeforeARun(t *testing.T) {
	for name, c := range map[string]struct {
		config map[string]any
		want   string // "" passes
	}{
		"a raw article": {map[string]any{"resource": "ticket", "operation": "update", "id": "5",
			"updateFields": map[string]any{"article": map[string]any{"type": "email"}}},
			`apipkg zammad ticket.update: updateFields has no option "article"`},
		"a type_id": {map[string]any{"resource": "ticket", "operation": "create", "title": "T",
			"article": map[string]any{"articleDetails": map[string]any{"type_id": 6}}}, `article.articleDetails has no field "type_id"`},
		"a declared note": {map[string]any{"resource": "ticket", "operation": "update", "id": "5",
			"updateFields": map[string]any{"note": "done"}}, ""},
		"updateFields from an expression": {map[string]any{"resource": "ticket", "operation": "update", "id": "5",
			"updateFields": "{{ $json.fields }}"}, ""},
		"articleDetails from an expression": {map[string]any{"resource": "ticket", "operation": "create", "title": "T",
			"article": map[string]any{"articleDetails": "{{ $json.article }}"}}, ""},
		"an op from an expression": {map[string]any{"resource": "ticket", "operation": "{{ $json.op }}",
			"updateFields": map[string]any{"article": "x"}}, ""},
	} {
		err := CheckConfig("app.zammad", c.config)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	if err := CheckConfig("core.set", map[string]any{"additionalFields": map[string]any{"x": 1}}); err != nil {
		t.Errorf("a node no API package serves: %v", err)
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
