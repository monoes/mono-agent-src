package chromecookies

import "testing"

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
