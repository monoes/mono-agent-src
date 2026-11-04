package doomsday

import (
	"strings"
	"testing"
)

func charterWith(limits string) string {
	return `{"goal":"ship it","limits":{"budget_micro":5000000` + limits + `},
"pins":{"boss":{"provider":"p","model":"m","version":"1"},"verifier":{"provider":"p","model":"v","version":"2"},"judging_policy":"j1"}}`
}

// The governor's limits are charter fields: they load, validate, pass to the store's charter (so they are hashed) and show in
// the render; unset they stay zero (the governor's defaults), so a charter without them is what it was.
func TestCharterCarriesTheGovernorLimitsIntoTheStoreCharter(t *testing.T) {
	c, err := LoadCharter(strings.NewReader(charterWith(`,"queue_max_rows":10,"queue_max_bytes":2048,"safe_min_concurrent":1`)))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	sc := toStore(c, "p", 1)
	if sc.Limits.QueueMaxRows != 10 || sc.Limits.QueueMaxBytes != 2048 || sc.Limits.SafeMinConcurrent != 1 {
		t.Fatalf("store limits: %+v", sc.Limits)
	}
	plain, _ := LoadCharter(strings.NewReader(charterWith(``)))
	if toStore(plain, "p", 1).Hash() == sc.Hash() {
		t.Fatal("the governor limits are not in the charter hash")
	}
	if l := toStore(plain, "p", 1).Limits; l.QueueMaxRows != 0 || l.QueueMaxBytes != 0 || l.SafeMinConcurrent != 0 {
		t.Fatalf("an unset limit must stay unset (the governor's default): %+v", l)
	}
	if out := Render(c, "abcdef0123456789"); !strings.Contains(out, "resource governor") {
		t.Fatalf("the render does not show the governor's limits:\n%s", out)
	}
}

func TestCharterRefusesGovernorLimitsOutsideTheirBounds(t *testing.T) {
	for name, limits := range map[string]string{
		"rows negative":  `,"queue_max_rows":-1`,
		"rows huge":      `,"queue_max_rows":99999999`,
		"bytes huge":     `,"queue_max_bytes":99999999999`,
		"safe above cap": `,"safe_min_concurrent":31`,
		"safe negative":  `,"safe_min_concurrent":-1`,
	} {
		c, err := LoadCharter(strings.NewReader(charterWith(limits)))
		if err == nil {
			err = c.Validate()
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
