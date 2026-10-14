package connections

import (
	"context"
	"testing"
)

func testPlatform(id string) PlatformDef {
	return PlatformDef{
		ID: id, Name: "Acme", Category: "app", ConnectVia: "API",
		Methods: []AuthMethod{MethodAPIKey, MethodOAuth},
		Fields: map[AuthMethod][]CredentialField{MethodAPIKey: {
			{Key: "access_token", Label: "Token", Secret: true, Required: true}}},
		OAuth: &OAuthConfig{AuthURL: "https://acme.test/a", TokenURL: "https://acme.test/t"},
	}
}

func TestRegisterPlatformRules(t *testing.T) {
	if err := RegisterPlatform(testPlatform("github")); err == nil {
		t.Error("a built-in id must be refused")
	}
	if err := RegisterPlatform(PlatformDef{ID: ""}); err == nil {
		t.Error("an empty id must be refused")
	}
	if err := RegisterPlatform(PlatformDef{ID: "nomethods"}); err == nil {
		t.Error("a platform without methods must be refused")
	}
	if !IsBuiltin("github") || IsBuiltin("acme_dyn") {
		t.Error("IsBuiltin")
	}
}

func TestDynamicPlatformLookups(t *testing.T) {
	t.Cleanup(func() { UnregisterPlatform("acme_dyn") })
	calls := 0
	AddPlatformSource(func() {
		calls++
		if err := RegisterPlatform(testPlatform("acme_dyn")); err != nil {
			t.Error(err)
		}
	})
	p, ok := Get("acme_dyn")
	if !ok || p.OAuth == nil || p.Fields[MethodAPIKey][0].Key != "access_token" {
		t.Fatalf("Get: %+v %v", p, ok)
	}
	Get("acme_dyn")
	if calls != 1 {
		t.Errorf("a platform source runs once, ran %d times", calls)
	}
	in := func(list []PlatformDef) bool {
		for _, x := range list {
			if x.ID == "acme_dyn" {
				return true
			}
		}
		return false
	}
	if !in(All()) || !in(ByConnectVia("API")) {
		t.Error("All/ByConnectVia must include run-time platforms")
	}
	if g, _ := Get("github"); g.Name == "" {
		t.Error("built-ins still resolve")
	}
}

// The connect flow (SaveFields), listing and GetOrResolve accept a run-time
// platform, and its secret field goes where built-in secrets go.
func TestDynamicPlatformConnectAndResolve(t *testing.T) {
	if err := RegisterPlatform(testPlatform("acme_dyn2")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { UnregisterPlatform("acme_dyn2") })
	mgr, db := newManagerDB(t)
	ctx := context.Background()
	conn, err := mgr.SaveFields(ctx, "acme_dyn2", MethodAPIKey, map[string]interface{}{"access_token": "sekret"}, "")
	if err != nil {
		t.Fatalf("SaveFields: %v", err)
	}
	if conn.Label != "Acme" {
		t.Errorf("label: %q", conn.Label)
	}
	got, err := NewStore(db).GetOrResolve(ctx, "acme_dyn2", "default")
	if err != nil || got.Data["access_token"] != "sekret" {
		t.Fatalf("GetOrResolve: %+v %v", got, err)
	}
	list, err := mgr.List(ctx, "acme_dyn2", "")
	if err != nil || len(list) != 1 {
		t.Errorf("List: %v %v", list, err)
	}
}
