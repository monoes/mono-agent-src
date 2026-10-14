package apipkg

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// hookPkg is an installed package with a webhook block using a login
// step, an id from a header, a for_each delete and a token check.
const hookPkg = `{
  "$schema": "apipkg.v1", "id": "hooktest", "title": "Hook test", "base_url": "https://api.hooktest.example",
  "n8n_trigger_types": ["n8n-nodes-base.hooktestTrigger"],
  "credentials": [{"platform": "hooktest", "methods": ["api_key"],
    "fields": [{"key": "api_key", "label": "Key", "secret": true}],
    "inject": {"headers": {"X-Key": "{{ cred.api_key }}"}}}],
  "operations": [],
  "webhook": {
    "fields": [{"key": "events", "label": "Events", "type": "array", "required": true},
               {"key": "scope", "label": "Scope", "type": "text", "default": "team"}],
    "create": [{"method": "POST", "path": "/hooks", "for_each": "{{ p.events }}",
                "body": {"url": "{{ hook.url }}", "event": "{{ el }}", "scope": "{{ p.scope }}"}}],
    "id": "{{ res | pluck:'id' | join:',' }}",
    "extra": {"ids": "{{ res | pluck:'id' }}"},
    "delete": [{"method": "DELETE", "path": "/hooks/{{ el }}", "for_each": "{{ hook.extra.ids }}"}],
    "check": {"method": "GET", "path": "/hooks", "exists_when": "{{ res.hooks | find:'id',hook.extra.ids.0 }}"},
    "verify": {"signature": "{{ hdr.X-Token }}", "alg": "token"}
  }
}`

func TestWebhookRegistrarLifecycle(t *testing.T) {
	installPkg(t, map[string]string{"hooktest": hookPkg})
	registerWebhooks()
	r, ok := workflow.WebhookRegistrarFor("hooktest").(webhookRegistrar)
	if !ok {
		t.Fatalf("hooktest is not registered as an apipkg webhook registrar")
	}
	if _, ours := workflow.WebhookRegistrarFor("typeform").(webhookRegistrar); ours {
		t.Error("the engine's built-in typeform registrar must stay the engine's")
	}
	cfg := map[string]any{"events": []any{"a", "b"}, "credential": map[string]any{"api_key": "k1"}}
	hook := testHook(cfg)

	srv := newMockServer(t, nil, 0, map[string]any{"id": "h1"}, map[string]any{"id": "h2"})
	got, err := r.Register(context.Background(), hook)
	if err != nil {
		t.Fatal(err)
	}
	reqs := srv.requests()
	if len(reqs) != 2 || reqs[1].header.Get("X-Key") != "k1" || !strings.Contains(string(reqs[1].body), `"event":"b"`) ||
		!strings.Contains(string(reqs[0].body), `"scope":"team"`) {
		t.Fatalf("create requests: %+v", reqs)
	}
	if got.ID != "h1,h2" || len(got.Extra["ids"].([]any)) != 2 {
		t.Fatalf("hook: %+v", got)
	}

	srv = newMockServer(t, map[string]any{"hooks": []any{map[string]any{"id": "h1"}}}, 0)
	if ok, err := r.Exists(context.Background(), hook, got); !ok || err != nil {
		t.Errorf("exists: %v %v", ok, err)
	}
	srv = newMockServer(t, map[string]any{"hooks": []any{}}, 0)
	if ok, err := r.Exists(context.Background(), hook, got); ok || err != nil {
		t.Errorf("exists_when false should mean gone: %v %v", ok, err)
	}

	srv = newMockServer(t, nil, http.StatusNotFound)
	if err := r.Unregister(context.Background(), hook, got); err != nil {
		t.Errorf("404s on delete count as removed: %v", err)
	}
	if n := len(srv.requests()); n != 2 {
		t.Errorf("one delete per stored id, got %d requests", n)
	}
	srv = newMockServer(t, nil, http.StatusInternalServerError)
	if err := r.Unregister(context.Background(), hook, got); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("a 500 on delete is an error: %v", err)
	}

	h := http.Header{}
	h.Set("X-Token", "test-secret")
	if err := r.VerifyDelivery(h, []byte("{}"), hook, got); err != nil {
		t.Errorf("verify: %v", err)
	}
	h.Set("X-Token", "nope")
	if err := r.VerifyDelivery(h, []byte("{}"), hook, got); err == nil {
		t.Error("a wrong token must fail")
	}
	if err := r.VerifyDelivery(http.Header{}, []byte("{}"), hook, got); err == nil {
		t.Error("a missing token must fail")
	}
}

func TestWebhookRegisterErrors(t *testing.T) {
	installPkg(t, map[string]string{"hooktest": hookPkg})
	r := webhookRegistrar{id: "hooktest"}
	if _, err := r.Register(context.Background(), testHook(map[string]any{"events": []any{"a"}})); err == nil ||
		!strings.Contains(err.Error(), "credential_id") {
		t.Errorf("no credential: %v", err)
	}
	cfg := map[string]any{"credential": map[string]any{"api_key": "k"}}
	if _, err := r.Register(context.Background(), testHook(cfg)); err == nil || !strings.Contains(err.Error(), `"events"`) {
		t.Errorf("missing required field: %v", err)
	}
	cfg["events"] = []any{"a"}
	newMockServer(t, map[string]any{"nothing": true}, 0)
	if _, err := r.Register(context.Background(), testHook(cfg)); err == nil || !strings.Contains(err.Error(), "no hook id") {
		t.Errorf("answer without an id: %v", err)
	}
	if _, err := (webhookRegistrar{id: "gone"}).Register(context.Background(), testHook(cfg)); err == nil {
		t.Error("a package that is not installed must fail")
	}
}

func TestWebhookVerifyHMAC(t *testing.T) {
	v := &WebhookVerify{Signature: "{{ hdr.X-Sig }}", Prefix: "sha256="}
	body := []byte(`{"a":1}`)
	sc := func(sig string) Scope {
		return Scope{Hook: map[string]any{"secret": "s3"}, Hdr: headerMap{"X-Sig": sig}, Req: map[string]any{"body": string(body)}}
	}
	h := hmac.New(sha256.New, []byte("s3"))
	h.Write(body)
	mac := hex.EncodeToString(h.Sum(nil))
	if err := v.check(sc("sha256="+strings.Repeat("0", 64)), body); err == nil {
		t.Error("a wrong signature must fail")
	}
	if err := v.check(sc("sha256="+mac), body); err != nil {
		t.Errorf("valid signature: %v", err)
	}
	if err := v.check(sc(mac), body); err == nil || !strings.Contains(err.Error(), "sha256=") {
		t.Errorf("missing prefix: %v", err)
	}
	if err := (&WebhookVerify{Signature: "{{ hdr.X-Sig }}", Secret: "{{ hook.extra.none }}"}).check(sc("x"), body); err != nil {
		t.Errorf("an empty secret skips the check: %v", err)
	}
}

func TestWebhookValidation(t *testing.T) {
	for repl, want := range map[[2]string]string{
		{`"event": "{{ el }}"`, `"event": "{{ p.nope }}"`}:                      `"nope" is not one of the op's fields`,
		{`"alg": "token"`, `"alg": "md4"`}:                                      `verify.alg: "md4" is not one of`,
		{`"for_each": "{{ hook.extra.ids }}"`, `"for_each": "{{ cred.nope }}"`}: `"nope" is not a credential field`,
	} {
		_, err := Parse([]byte(strings.Replace(hookPkg, repl[0], repl[1], 1)), "bad.json")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q in %v", want, err)
		}
	}
	// hook.* belongs to the webhook block only.
	op := strings.Replace(hookPkg, `"operations": []`,
		`"operations": [{"resource": "", "operation": "x", "title": "X", "method": "GET", "path": "/{{ hook.id }}"}]`, 1)
	if _, err := Parse([]byte(op), "op.json"); err == nil || !strings.Contains(err.Error(), "only available in the package's webhook block") {
		t.Errorf("hook.* in an op: %v", err)
	}
}

func TestMapFilter(t *testing.T) {
	sc := Scope{P: map[string]any{"e": "b", "l": []any{"a", "z"}}}
	for tmpl, want := range map[string]any{
		"{{ p.e | map:'a=1,b=2' }}":   "2",
		"{{ p.l | map:'a=x#y' }}":     []any{"x#y", "z"},
		"{{ p.e | map:'q=1' }}":       "b",
		"{{ p.none | map:'a=1' }}":    nil,
		"{{ p.e | map:'b=7' | int }}": int64(7),
	} {
		got, err := renderString(tmpl, sc)
		if err != nil || stringify(got) != stringify(want) {
			t.Errorf("%s = %#v (%v), want %#v", tmpl, got, err, want)
		}
	}
}

// \{{ and \}} are literal braces (Zendesk's own placeholders in a body).
func TestTemplateBraceEscapes(t *testing.T) {
	sc := Scope{P: map[string]any{"f": "ticket.id"}}
	for tmpl, want := range map[string]string{
		`\{{ticket.id\}}`:             "{{ticket.id}}",
		`\{{{{ p.f }}\}}`:             "{{ticket.id}}",
		`a \{{x\}} {{ p.f }} \{{y\}}`: "a {{x}} ticket.id {{y}}",
		`only \}} closes`:             "only }} closes",
	} {
		got, err := renderText(tmpl, sc)
		if err != nil || got != want {
			t.Errorf("%s = %q (%v), want %q", tmpl, got, err, want)
		}
	}
	if _, err := parseTemplate(`\{{ {{ p.f `); err == nil {
		t.Error("an unescaped {{ still needs its }}")
	}
}
