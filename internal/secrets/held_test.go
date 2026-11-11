package secrets

import (
	"context"
	"errors"
	"testing"
)

func TestHasLiteralRefSeesEveryWritingOfAReference(t *testing.T) {
	for _, s := range []string{"@secret:x", "a @SECRET:x", "@ secret : x", "%40secret%3Ax", "%40Secret:x", `{"k":"@secret:x"}`} {
		if !HasLiteralRef(s) {
			t.Errorf("%q is not seen", s)
		}
	}
	for _, s := range []string{"", "secret: x", "me@secret.example", "@sec", "ret:x"} {
		if HasLiteralRef(s) {
			t.Errorf("%q is seen as a reference", s)
		}
	}
}

func TestResolveHeldRefusesWithoutAValidMACAndLiteralRefs(t *testing.T) {
	tok := HeldToken("x", "0123456789abcdef0123456789abcdef")
	deny := HeldOptions{Verify: func(string, string) bool { return false }, Dry: true}
	if _, err := ResolveHeld(context.Background(), nil, "p", "a "+tok, deny); !errors.Is(err, ErrHeldRefused) {
		t.Errorf("a token that fails its MAC: %v", err)
	}
	if _, err := ResolveHeld(context.Background(), nil, "p", "a @secret:x", deny); !errors.Is(err, ErrLiteralRef) {
		t.Errorf("a literal reference: %v", err)
	}
	if _, err := ResolveHeld(context.Background(), nil, "p", "a [[held-secret:x:zz]]", deny); !errors.Is(err, ErrHeldRefused) {
		t.Errorf("a malformed token: %v", err)
	}
	if out, err := ResolveHeld(context.Background(), nil, "p", "plain", deny); err != nil || out != "plain" {
		t.Errorf("plain text: %q %v", out, err)
	}
}

func TestUnresolveTrackedRefusesAnEncodedForm(t *testing.T) {
	a := &accessState{seen: map[string]struct{}{}, forms: map[string]trackedForm{}, names: map[string]struct{}{}}
	a.track("vs", "vsecret-777")
	ctx := context.WithValue(context.Background(), accessKey{}, a)
	tok := func(n string) string { return "<" + n + ">" }
	if out, ok := UnresolveTracked(ctx, "k vsecret-777 k", tok); !ok || out != "k <vs> k" {
		t.Errorf("raw: %q %v", out, ok)
	}
	if _, ok := UnresolveTracked(ctx, "k vsecret_777", tok); ok {
		t.Error("a derived form was accepted")
	}
	if out, ok := UnresolveTracked(context.Background(), "k vsecret-777", tok); !ok || out != "k vsecret-777" {
		t.Error("outside agent access the text must be untouched")
	}
}
