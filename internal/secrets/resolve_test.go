package secrets

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"
)

func TestResolve_SuccessfulResolution(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	if _, err := Add(ctx, db.DB, "default", "secret", "e1", map[string]string{"secret": "v-alpha1"}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, err := Resolve(ctx, db.DB, "default", "@secret:e1")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "v-alpha1" {
		t.Fatalf("got %q, want %q", got, "v-alpha1")
	}
}

func TestResolve_NonSecretRefReturnedUnchanged(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	got, err := Resolve(ctx, db.DB, "default", "plain-value")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "plain-value" {
		t.Fatalf("got %q, want %q", got, "plain-value")
	}
}

func TestResolve_MissingSecretErrors(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	_, err := Resolve(ctx, db.DB, "default", "@secret:e0")
	if err == nil {
		t.Fatal("expected error for missing secret, got nil")
	}
}

// Covers an entry with exactly one field whose key isn't literally
// "secret" — Resolve should still return it, since there's no ambiguity
// about which field a bare @secret:name reference means.
func TestResolve_UsesSoleFieldWhenNotNamedSecret(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "e2", map[string]string{"token": "tok-one1"}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, err := Resolve(ctx, db.DB, "default", "@secret:e2")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "tok-one1" {
		t.Fatalf("got %q, want %q", got, "tok-one1")
	}
}

// Covers an entry with several fields, one of them keyed "secret" — that
// one wins for a bare reference, since it's unambiguous which value is
// meant.
func TestResolve_PrefersSecretKeyAmongMultipleFields(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "e3", map[string]string{
		"secret":  "v-pref1",
		"field_a": "fa-one1",
	}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, err := Resolve(ctx, db.DB, "default", "@secret:e3")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "v-pref1" {
		t.Fatalf("got %q, want %q", got, "v-pref1")
	}
}

// Covers an entry with several fields, none keyed "secret" — Resolve can't
// disambiguate and must error rather than silently pick one.
func TestResolve_ErrorsOnMultipleFieldsWithoutSecretKey(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "e4", map[string]string{
		"field_a": "fa-one1",
		"field_b": "fb-one1",
	}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if _, err := Resolve(ctx, db.DB, "default", "@secret:e4"); err == nil {
		t.Fatal("expected an error picking between two equally-plausible fields, got nil")
	}
}

func TestResolveConfig_SuccessfulResolution(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "e1", map[string]string{"secret": "v-alpha1"}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	config := map[string]interface{}{
		"api_key": "@secret:e1",
	}
	if err := ResolveConfig(ctx, db.DB, "default", maps.Clone(config), config); err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if config["api_key"] != "v-alpha1" {
		t.Fatalf("got %v, want %q", config["api_key"], "v-alpha1")
	}
}

// Verifies ResolveConfig's deliberate fail-open behavior (matching
// vault.ResolveConfig's @img- convention): a missing entry must not error
// out the whole config resolution, and must leave the original ref in
// place.
func TestResolveConfig_MissingSecretLeavesRefUnchanged(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()

	config := map[string]interface{}{
		"api_key": "@secret:e0",
	}
	if err := ResolveConfig(ctx, db.DB, "default", maps.Clone(config), config); err != nil {
		t.Fatalf("ResolveConfig must not error on a missing entry: %v", err)
	}
	if config["api_key"] != "@secret:e0" {
		t.Fatalf("expected ref left unchanged, got %v", config["api_key"])
	}
}

// Verifies a config with mixed matching/non-matching values only has the
// matching ones replaced.
func TestResolveConfig_OnlyReplacesMatchingValues(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "e1", map[string]string{"secret": "v-alpha1"}, "", "", ""); err != nil {
		t.Fatalf("Add: %v", err)
	}

	config := map[string]interface{}{
		"api_key":   "@secret:e1",
		"model":     "gpt-4",
		"max_calls": 5,
		"nested":    map[string]interface{}{"still": "untouched"},
	}
	if err := ResolveConfig(ctx, db.DB, "default", maps.Clone(config), config); err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	if config["api_key"] != "v-alpha1" {
		t.Fatalf("api_key: got %v, want %q", config["api_key"], "v-alpha1")
	}
	if config["model"] != "gpt-4" {
		t.Fatalf("model should be untouched, got %v", config["model"])
	}
	if config["max_calls"] != 5 {
		t.Fatalf("max_calls should be untouched, got %v", config["max_calls"])
	}
	nested, ok := config["nested"].(map[string]interface{})
	if !ok || nested["still"] != "untouched" {
		t.Fatalf("nested should be untouched, got %v", config["nested"])
	}
}

// A reference resolves only where the workflow holds it as it is. "@secret:"
// in a value an expression produced, at any depth, is refused before anything
// resolves, naming where it is and not what it says; the workflow's own
// references, nested ones too, pass (#845 review: secret references as data).
func TestResolveConfigRefusesAReferenceThatCameFromData(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "vs", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		stored, resolved map[string]interface{}
		path             string
	}{
		"the run's data in a field": {
			map[string]interface{}{"message": "{{ $json.msg }}"},
			map[string]interface{}{"message": "@secret:vs"}, `"message"`},
		"built by the expression": {
			map[string]interface{}{"message": `@sec{{ "ret:vs" }}`},
			map[string]interface{}{"message": "@secret:vs"}, `"message"`},
		"after spaces": {
			map[string]interface{}{"api_key": "{{ $json.k }}"},
			map[string]interface{}{"api_key": "  @secret:vs"}, `"api_key"`},
		"after an expression that came out empty": {
			map[string]interface{}{"message": "{{ $json.p }}@secret:vs"},
			map[string]interface{}{"message": "@secret:vs"}, `"message"`},
		"in an object an expression returned": {
			map[string]interface{}{"values": "{{ $json.values }}"},
			map[string]interface{}{"values": map[string]interface{}{"pin": "@secret:vs"}}, `"values.pin"`},
		"in a list": {
			map[string]interface{}{"args": []interface{}{"a", "{{ $json.b }}"}},
			map[string]interface{}{"args": []interface{}{"a", "@secret:vs"}}, `"args[1]"`},
		"next to the workflow's own reference": {
			map[string]interface{}{"x": "@secret:vs {{ $json.y }}"},
			map[string]interface{}{"x": "@secret:vs @secret:other"}, `"x"`},
		"in a list of strings an expression returned": {
			map[string]interface{}{"tags": "{{ split $json.t \",\" }}"},
			map[string]interface{}{"tags": []string{"a", "@secret:vs"}}, `"tags[1]"`},
		"in a map of strings an expression returned": {
			map[string]interface{}{"env": "{{ $json.env }}"},
			map[string]interface{}{"env": map[string]string{"TOKEN": "@secret:vs"}}, `"env.TOKEN"`},
		"in a field the workflow doesn't have": {
			map[string]interface{}{},
			map[string]interface{}{"token": "@secret:vs"}, `"token"`},
	} {
		resolved := maps.Clone(c.resolved)
		err := ResolveConfig(ctx, db.DB, "default", c.stored, resolved)
		if !errors.Is(err, ErrDataReference) || !strings.Contains(err.Error(), c.path) {
			t.Errorf("%s: %v, want a refusal that names %s", name, err, c.path)
			continue
		}
		if strings.Contains(err.Error(), "vsecret-777") || strings.Contains(err.Error(), "@secret:vs") {
			t.Errorf("%s: the refusal repeats the value: %v", name, err)
		}
		if strings.Contains(fmt.Sprint(resolved), "vsecret-777") {
			t.Errorf("%s: resolved %v", name, resolved)
		}
	}

	// The workflow's own references: at the top, which resolves, and nested,
	// which a node reads itself (mcp.call headers), next to data without one.
	stored := map[string]interface{}{"api_key": "@secret:vs", "message": "{{ $json.msg }}",
		"headers": map[string]interface{}{"Authorization": "Bearer @secret:vs"}}
	config := map[string]interface{}{"api_key": "@secret:vs", "message": "hello",
		"headers": map[string]interface{}{"Authorization": "Bearer @secret:vs"}}
	if err := ResolveConfig(ctx, db.DB, "default", stored, config); err != nil {
		t.Fatalf("the workflow's own references: %v", err)
	}
	if config["api_key"] != "vsecret-777" || config["message"] != "hello" {
		t.Errorf("config = %v, want api_key resolved", config)
	}
}

// A refused reference says which and why, once: the reason already says the
// vault isn't read, so the refusal doesn't repeat it (#845 code review, 5).
func TestARefusedReferenceSaysWhyOnce(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := RefuseResolving(context.Background(), "the chat wrote this workflow: approve it with `workflow activate wf-1`")
	if _, err := Add(ctx, db.DB, "default", "secret", "vs", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	v, err := Resolve(ctx, db.DB, "default", "@secret:vs")
	if want := "@secret:vs is not read from the vault: the chat wrote this workflow: approve it with `workflow activate wf-1`"; err == nil || err.Error() != want {
		t.Errorf("refusal %q, want %q", err, want)
	}
	if !errors.Is(err, ErrResolveRefused) || v != "@secret:vs" {
		t.Errorf("Resolve = %q, %v; want the reference back and ErrResolveRefused", v, err)
	}
}

// Text an expression produced that holds "@secret:" anywhere but at its
// start is no reference to any resolver here: it stays text, and the step
// runs with it unresolved. An outside sender can't halt an approved step
// with it (#845 code review, 2).
func TestTextThatMentionsASecretReferenceStaysText(t *testing.T) {
	db := newSecretsTestDB(t)
	ctx := context.Background()
	if _, err := Add(ctx, db.DB, "default", "secret", "vs", map[string]string{"secret": "vsecret-777"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	stored := map[string]interface{}{"body": "{{ $json.body }}", "subject": "{{ $json.p }}@secret:vs",
		"headers": map[string]interface{}{"Authorization": "Bearer {{ $json.t }}"}}
	config := map[string]interface{}{"body": "please see the @secret: docs, or x@secret:y", "subject": "re: @secret:vs",
		"headers": map[string]interface{}{"Authorization": "Bearer @secret:vs"}}
	if err := ResolveConfig(ctx, db.DB, "default", stored, config); err != nil {
		t.Fatalf("text that mentions a reference: %v", err)
	}
	if fmt.Sprint(config) != fmt.Sprint(map[string]interface{}{"body": "please see the @secret: docs, or x@secret:y", "subject": "re: @secret:vs",
		"headers": map[string]interface{}{"Authorization": "Bearer @secret:vs"}}) {
		t.Errorf("config = %v, want the text as it came", config)
	}
	// In a field whose node resolves references inside its text (mcp.call's
	// headers), the same header is one.
	if path, ok := DataReference(stored["headers"], config["headers"], true); !ok || path != "Authorization" {
		t.Errorf("a header an mcp.call would expand: %q, %v", path, ok)
	}
	if _, ok := DataReference(stored["body"], config["body"], false); ok {
		t.Error("text in the middle was taken for a reference")
	}
}
