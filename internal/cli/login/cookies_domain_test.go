package login

import "testing"

func TestConvertExtensionCookiesKeepsOnlyPlatformDomains(t *testing.T) {
	raw := []interface{}{
		map[string]interface{}{"name": "sid", "value": "1", "domain": ".example.com", "path": "/", "hostOnly": false, "sameSite": "lax"},
		map[string]interface{}{"name": "ad", "value": "2", "domain": ".tracker.net", "path": "/"},
	}
	got, err := convertExtensionCookies(raw, platformCookieDomains("https://www.example.com/login"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "sid" {
		t.Fatalf("cookies = %+v, want only sid", got)
	}
}
