package store

import (
	"errors"
	"reflect"
	"testing"
)

// typedCharter lifts every line the typed sections need and fills every section.
func typedCharter() Charter {
	c := charter("p", 1)
	c.Lines = []string{LineSpending, LineThirdParty, LineVault, LineDestructive, LineHostSettings}
	c.Typed = Typed{
		Send:         &SendLine{Platform: "x", Account: "acct", Destinations: []string{"b", "a"}, MaxPerRun: 3, ContentClass: "post"},
		Vault:        &VaultLine{Secrets: []string{"api"}, Hosts: []string{"example.com"}, MaxAttempts: 5, BucketSize: 1024, BucketCount: 2, MaxCallMS: 5000, SlotMS: 500, Predicate: "ok"},
		Local:        &LocalLine{Roots: []string{"/srv/w"}, Verbs: []string{"delete"}, MaxCount: 4, MaxBytes: 100},
		Settings:     &SettingsLine{Keys: map[string][]string{"theme": {"dark", "light"}}},
		GrantTargets: []string{"lead"},
	}
	return c
}

func TestTypedFieldsAreInTheHash(t *testing.T) {
	base := typedCharter().Hash()
	muts := map[string]func(c *Charter){
		"send platform":    func(c *Charter) { c.Typed.Send.Platform = "y" },
		"send account":     func(c *Charter) { c.Typed.Send.Account = "z" },
		"send destination": func(c *Charter) { c.Typed.Send.Destinations = append(c.Typed.Send.Destinations, "c") },
		"send max":         func(c *Charter) { c.Typed.Send.MaxPerRun++ },
		"send class":       func(c *Charter) { c.Typed.Send.ContentClass = "dm" },
		"send off":         func(c *Charter) { c.Typed.Send = nil },
		"vault secret":     func(c *Charter) { c.Typed.Vault.Secrets = []string{"other"} },
		"vault host":       func(c *Charter) { c.Typed.Vault.Hosts = []string{"example.org"} },
		"vault attempts":   func(c *Charter) { c.Typed.Vault.MaxAttempts++ },
		"vault bucket":     func(c *Charter) { c.Typed.Vault.BucketSize++ },
		"vault count":      func(c *Charter) { c.Typed.Vault.BucketCount++ },
		"vault call":       func(c *Charter) { c.Typed.Vault.MaxCallMS++ },
		"vault slot":       func(c *Charter) { c.Typed.Vault.SlotMS++ },
		"vault predicate":  func(c *Charter) { c.Typed.Vault.Predicate = "no" },
		"vault off":        func(c *Charter) { c.Typed.Vault = nil },
		"local root":       func(c *Charter) { c.Typed.Local.Roots = []string{"/srv/x"} },
		"local verb":       func(c *Charter) { c.Typed.Local.Verbs = []string{"delete", "overwrite"} },
		"local count":      func(c *Charter) { c.Typed.Local.MaxCount++ },
		"local bytes":      func(c *Charter) { c.Typed.Local.MaxBytes++ },
		"local off":        func(c *Charter) { c.Typed.Local = nil },
		"setting key":      func(c *Charter) { c.Typed.Settings.Keys["font"] = []string{"*"} },
		"setting value":    func(c *Charter) { c.Typed.Settings.Keys["theme"] = []string{"dark"} },
		"settings off":     func(c *Charter) { c.Typed.Settings = nil },
		"grant target":     func(c *Charter) { c.Typed.GrantTargets = []string{"lead", "child"} },
	}
	for name, m := range muts {
		c := typedCharter()
		m(&c)
		if c.Hash() == base {
			t.Errorf("changing %s did not change the hash", name)
		}
	}
	// Sets are sets: order and duplicates do not change the hash.
	c := typedCharter()
	c.Typed.Send.Destinations = []string{"a", "b", "a"}
	c.Typed.Settings.Keys["theme"] = []string{"light", "dark", "dark"}
	if c.Hash() != base {
		t.Error("set order or duplicates changed the hash")
	}
	// A default charter without the section hashes differently from one with an empty-looking section.
	if charter("p", 1).Hash() == typedCharter().Hash() {
		t.Error("typed section ignored")
	}
}

func TestTypedValidationIsDefaultDeny(t *testing.T) {
	if err := typedCharter().Validate(); err != nil {
		t.Fatalf("the full charter: %v", err)
	}
	if err := charter("p", 1).Validate(); err != nil {
		t.Fatalf("no typed section is valid and enables nothing: %v", err)
	}
	cases := map[string]func(c *Charter){
		"send without its line":     func(c *Charter) { c.Lines = []string{LineSpending, LineVault, LineDestructive, LineHostSettings} },
		"vault without its line":    func(c *Charter) { c.Lines = []string{LineSpending, LineThirdParty, LineDestructive, LineHostSettings} },
		"local without its line":    func(c *Charter) { c.Lines = []string{LineSpending, LineThirdParty, LineVault, LineHostSettings} },
		"settings without its line": func(c *Charter) { c.Lines = []string{LineSpending, LineThirdParty, LineVault, LineDestructive} },
		"grant without spending":    func(c *Charter) { c.Lines = []string{LineThirdParty, LineVault, LineDestructive, LineHostSettings} },
		"send no destinations":      func(c *Charter) { c.Typed.Send.Destinations = nil },
		"send blank destination":    func(c *Charter) { c.Typed.Send.Destinations = []string{" "} },
		"send max zero":             func(c *Charter) { c.Typed.Send.MaxPerRun = 0 },
		"send no account":           func(c *Charter) { c.Typed.Send.Account = "" },
		"vault no secret":           func(c *Charter) { c.Typed.Vault.Secrets = nil },
		"vault blank secret":        func(c *Charter) { c.Typed.Vault.Secrets = []string{""} },
		"vault no host":             func(c *Charter) { c.Typed.Vault.Hosts = nil },
		"vault bucket over":         func(c *Charter) { c.Typed.Vault.BucketSize = maxBucketSize + 1 },
		"vault count over":          func(c *Charter) { c.Typed.Vault.BucketCount = maxBucketCount + 1 },
		"vault call under":          func(c *Charter) { c.Typed.Vault.MaxCallMS = minCallMS - 1 },
		"vault slot under":          func(c *Charter) { c.Typed.Vault.SlotMS = minSlotMS - 1 },
		"vault slot over call":      func(c *Charter) { c.Typed.Vault.SlotMS = c.Typed.Vault.MaxCallMS + 1 },
		"vault attempts over":       func(c *Charter) { c.Typed.Vault.MaxAttempts = maxAttempts + 1 },
		"vault too many slots":      func(c *Charter) { c.Typed.Vault.MaxCallMS, c.Typed.Vault.SlotMS = maxCallMS, minSlotMS },
		"local relative root":       func(c *Charter) { c.Typed.Local.Roots = []string{"srv"} },
		"local root slash":          func(c *Charter) { c.Typed.Local.Roots = []string{"/"} },
		"local unknown verb":        func(c *Charter) { c.Typed.Local.Verbs = []string{"chmod"} },
		"local count zero":          func(c *Charter) { c.Typed.Local.MaxCount = 0 },
		"local negative bytes":      func(c *Charter) { c.Typed.Local.MaxBytes = -1 },
		"settings empty":            func(c *Charter) { c.Typed.Settings.Keys = map[string][]string{} },
		"setting without values":    func(c *Charter) { c.Typed.Settings.Keys["theme"] = nil },
		"unknown grant target":      func(c *Charter) { c.Typed.GrantTargets = []string{"root"} },
	}
	for name, m := range cases {
		c := typedCharter()
		m(&c)
		if err := c.Validate(); !errors.Is(err, ErrInvalidCharter) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTypedCharterRoundTripsThroughTheStore(t *testing.T) {
	s, _ := newStore(t)
	c := typedCharter()
	if _, err := s.Enter(ctx, c, c.Hash(), "op"); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadCharter(ctx, "p", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hash() != c.Hash() || !reflect.DeepEqual(got.Typed, c.normalized().Typed) {
		t.Fatalf("round trip changed the typed section: %+v", got.Typed)
	}
}

func TestEntryCheckRefusesBeforeAnythingIsRecorded(t *testing.T) {
	s, _ := newStore(t)
	s = s.WithEntryCheck(func(Charter) error { return errors.New("never-liftable key") })
	c := typedCharter()
	if _, err := s.Enter(ctx, c, c.Hash(), "op"); !errors.Is(err, ErrInvalidCharter) {
		t.Fatalf("got %v", err)
	}
	if n, _ := s.NextGeneration(ctx, "p"); n != 1 || auditCount(t, s) != 0 {
		t.Fatal("a refused charter left rows")
	}
}
