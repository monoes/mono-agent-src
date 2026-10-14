package connections

import (
	"context"
	"strings"
	"testing"
)

func TestRefreshTokenUsesTailoredTokenURL(t *testing.T) {
	ts := newHealthTokenServer(t)
	registerHealthPlatform(t, "tenant-refresh", "https://unused.invalid/token", "cid")
	p := Registry["tenant-refresh"]
	p.OAuth.Tailor = func(cfg OAuthConfig, data map[string]interface{}) (OAuthConfig, error) {
		cfg.TokenURL = ts.URL + "/" + getStr(data, "subdomain") + "/oauth/tokens"
		return cfg, nil
	}
	db := newTestDB(t)
	s := NewStore(db)
	c := saveHealthOAuthConn(t, s, "tenant-refresh", map[string]interface{}{
		"access_token": "old", "refresh_token": "r", "subdomain": "acme",
	})
	if err := s.RefreshToken(context.Background(), c); err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if ts.paths[0] != "/acme/oauth/tokens" {
		t.Errorf("refresh went to %q", ts.paths[0])
	}
	got, _ := s.Get(context.Background(), c.ID)
	if got.Data["subdomain"] != "acme" || got.Data["access_token"] != "access-1" {
		t.Errorf("data after refresh = %v", got.Data)
	}
}

func TestFillConnectFields(t *testing.T) {
	fields := []CredentialField{{Key: "subdomain", Label: "Subdomain", Required: true}, {Key: "env", Label: "Env"}}
	data := map[string]interface{}{}
	if err := fillConnectFields(fields, data, map[string]string{"subdomain": " acme "}, nil); err != nil || data["subdomain"] != "acme" {
		t.Fatalf("given value: %v %v", err, data)
	}
	data = map[string]interface{}{}
	if err := fillConnectFields(fields, data, nil, map[string]interface{}{"subdomain": "old", "access_token": "x"}); err != nil || data["subdomain"] != "old" || data["access_token"] != nil {
		t.Fatalf("reconnect keeps fields only: %v %v", err, data)
	}
	if err := fillConnectFields(fields, map[string]interface{}{}, nil, nil); err == nil || !strings.Contains(err.Error(), "subdomain") {
		t.Errorf("missing required: err = %v", err)
	}
}

func TestConnectOAuthWithFieldsNeedsConnectFields(t *testing.T) {
	mgr, _ := newManagerDB(t)
	Registry["tenant-connect"] = PlatformDef{ID: "tenant-connect", Name: "Tenant", Methods: []AuthMethod{MethodOAuth},
		OAuth: &OAuthConfig{AuthURL: "https://{{sub}}.test/auth", TokenURL: "https://x.test/t", ClientID: "cid",
			ConnectFields: []CredentialField{{Key: "subdomain", Label: "Subdomain", Required: true}}}}
	t.Cleanup(func() { delete(Registry, "tenant-connect") })
	_, err := mgr.ConnectOAuthWithFields(context.Background(), "tenant-connect", nil, "", "", "", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "subdomain") {
		t.Errorf("err = %v", err)
	}
}

func TestSalesforceSandboxEnvironment(t *testing.T) {
	sf := Registry["salesforce"].OAuth
	cfg, err := tailorOAuth(*sf, map[string]interface{}{"environment": "Sandbox"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthURL != "https://test.salesforce.com/services/oauth2/authorize" || cfg.TokenURL != "https://test.salesforce.com/services/oauth2/token" {
		t.Errorf("sandbox config = %s %s", cfg.AuthURL, cfg.TokenURL)
	}
	cfg, _ = tailorOAuth(*sf, map[string]interface{}{})
	if cfg.AuthURL != sf.AuthURL || cfg.TokenURL != sf.TokenURL {
		t.Errorf("production changed: %s %s", cfg.AuthURL, cfg.TokenURL)
	}
}
