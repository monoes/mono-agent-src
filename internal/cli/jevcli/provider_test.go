package jevcli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestJevProviderListUseKey(t *testing.T) {
	cfg := jevTestCfg(t, true)
	out, _, err := runJev(t, cfg, "", "provider", "list")
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Selected  string         `json:"selected"`
		Providers []providerView `json:"providers"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if res.Selected != "typesafe" || len(res.Providers) < 10 {
		t.Fatalf("list = %s %d", res.Selected, len(res.Providers))
	}
	for _, p := range res.Providers {
		if p.ID == "openai" && p.Supported {
			t.Fatal("openai has no adapter yet")
		}
	}

	if _, _, err := runJev(t, cfg, "", "provider", "use", "openai"); err == nil {
		t.Fatal("selecting a provider without an adapter must fail")
	}
	if _, _, err := runJev(t, cfg, "", "provider", "key-set", "liquid"); err == nil {
		t.Fatal("an empty key must be refused")
	}
	if _, _, err := runJev(t, cfg, "lq-secret\n", "provider", "key-set", "liquid"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runJev(t, cfg, "", "provider", "set", "liquid", "--base-url", "https://liquid.example"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runJev(t, cfg, "", "provider", "use", "liquid"); err != nil {
		t.Fatal(err)
	}
	out, _, err = runJev(t, cfg, "", "provider", "list")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "lq-secret") {
		t.Fatal("list printed the key")
	}
	_ = json.Unmarshal([]byte(out), &res)
	if res.Selected != "liquid" {
		t.Fatalf("selected = %s", res.Selected)
	}
	for _, p := range res.Providers {
		if p.ID == "liquid" && (!p.HasKey || p.BaseURL != "https://liquid.example") {
			t.Fatalf("liquid = %+v", p)
		}
	}
}
