package chromecookies

import (
	"encoding/json"
	"testing"

	"github.com/monoes/mono-agent/internal/testhome"
)

func TestMain(m *testing.M) { testhome.Main(m) }

// The JSON shape is what crawler_sessions.cookies_json already stores: the key names are the
// contract, and the optional flags are left out when false/zero.
func TestCookieJSONShape(t *testing.T) {
	full, err := json.Marshal(Cookie{Name: "sid", Value: "v", Domain: ".example.com", Path: "/", Secure: true, HTTPOnly: true, Expires: 1.5})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(full, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"name", "value", "domain", "path", "secure", "httpOnly", "expires"} {
		if _, ok := m[k]; !ok {
			t.Errorf("full cookie lacks key %q: %s", k, full)
		}
	}
	if len(m) != 7 {
		t.Errorf("full cookie has %d keys, want 7: %s", len(m), full)
	}

	min, err := json.Marshal(Cookie{Name: "sid", Value: "v", Domain: "example.com", Path: "/"})
	if err != nil {
		t.Fatal(err)
	}
	m = nil
	if err := json.Unmarshal(min, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"secure", "httpOnly", "expires"} {
		if _, ok := m[k]; ok {
			t.Errorf("zero %q is written: %s", k, min)
		}
	}
}

func TestCookieRoundTrip(t *testing.T) {
	in := Cookie{Name: "a", Value: "b", Domain: "d", Path: "/p", Secure: true, Expires: 12}
	raw, _ := json.Marshal(in)
	var out Cookie
	if err := json.Unmarshal(raw, &out); err != nil || out != in {
		t.Fatalf("round trip = %+v, %v, want %+v", out, err, in)
	}
}

func TestFromExtensionFiltersByDomainAndCarriesFlags(t *testing.T) {
	raw := []interface{}{
		map[string]interface{}{"name": "sid", "value": "1", "domain": ".example.com", "path": "/", "sameSite": "lax"},
		map[string]interface{}{"name": "h", "value": "2", "domain": "www.example.com", "hostOnly": true, "sameSite": "no_restriction", "secure": true},
		map[string]interface{}{"name": "other", "value": "3", "domain": ".tracker.net"},
		map[string]interface{}{"name": "lookalike", "value": "4", "domain": "evilexample.com"},
	}
	got, err := FromExtension(raw, []string{"example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "sid" || got[1].Name != "h" {
		t.Fatalf("kept %+v", got)
	}
	if got[0].SameSite != "Lax" || got[1].SameSite != "None" || !got[1].HostOnly || got[0].HostOnly {
		t.Errorf("flags lost: %+v", got)
	}
	all, _ := FromExtension(raw, nil)
	if len(all) != 4 {
		t.Errorf("no filter keeps %d", len(all))
	}
}
