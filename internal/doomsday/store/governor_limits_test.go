package store

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

// goldenV2Hash is the hash the charter below had BEFORE the governor's limits existed (canon version 2), computed with
// the unmodified code of origin/master: a charter written then must keep its identity.
const goldenV2Hash = "41e2e3431a5543413d1e4a36aa69ddb2aa1d8c4f61c6037159e2930ad7147f95"

func TestLegacyCanonVersionKeepsItsHash(t *testing.T) {
	c := charter("p", 3)
	c.legacy = true
	if got := fmt.Sprintf("%x", c.Hash()); got != goldenV2Hash {
		t.Fatalf("a version 2 charter hashes %s, want %s", got, goldenV2Hash)
	}
	c.legacy = false
	if got := fmt.Sprintf("%x", c.Hash()); got == goldenV2Hash {
		t.Fatal("the current canon version must not hash like version 2")
	}
}

// A row written under version 2 loads, verifies and reads the governor limits as unset; a row of the current version
// carries them, and each limit changes the hash.
func TestOldCharterRowsVerifyAndGovernorLimitsAreHashed(t *testing.T) {
	s, _ := newStore(t)
	old := charter("p", 1)
	old.legacy = true
	if _, err := s.Enter(ctx, old, old.Hash(), "operator"); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadCharter(ctx, "p", 1)
	if err != nil {
		t.Fatalf("a version 2 row must keep verifying: %v", err)
	}
	if !got.legacy || got.Limits.QueueMaxRows != 0 || got.Limits.QueueMaxBytes != 0 || got.Limits.SafeMinConcurrent != 0 {
		t.Fatalf("an old charter reads the governor limits as unset: %+v", got.Limits)
	}
	if h := got.Hash(); !bytes.Equal(h[:], func() []byte { x := old.Hash(); return x[:] }()) {
		t.Fatal("the loaded old charter re-hashes differently")
	}

	base := charter("q", 1)
	h0 := base.Hash()
	for name, mut := range map[string]func(*Charter){
		"rows":  func(c *Charter) { c.Limits.QueueMaxRows = 10 },
		"bytes": func(c *Charter) { c.Limits.QueueMaxBytes = 1 << 20 },
		"safe":  func(c *Charter) { c.Limits.SafeMinConcurrent = 1 },
	} {
		c := charter("q", 1)
		mut(&c)
		if c.Hash() == h0 {
			t.Fatalf("%s does not change the charter hash", name)
		}
	}
	nc := charter("n", 1)
	nc.Limits.QueueMaxRows, nc.Limits.QueueMaxBytes, nc.Limits.SafeMinConcurrent = 7, 4096, 1
	if _, err := s.Enter(ctx, nc, nc.Hash(), "operator"); err != nil {
		t.Fatal(err)
	}
	back, err := s.LoadCharter(ctx, "n", 1)
	if err != nil || back.Limits.QueueMaxRows != 7 || back.Limits.QueueMaxBytes != 4096 || back.Limits.SafeMinConcurrent != 1 {
		t.Fatalf("limits did not round trip: %+v %v", back.Limits, err)
	}
}

// A version 2 row whose body names a governor limit was edited after hashing: it does not verify (no silent default raise).
func TestOldCharterRowWithGovernorLimitsInItsBodyDoesNotVerify(t *testing.T) {
	s, db := newStore(t)
	old := charter("p", 1)
	old.legacy = true
	if _, err := s.Enter(ctx, old, old.Hash(), "operator"); err != nil {
		t.Fatal(err)
	}
	// the charter row is insert-only by trigger; edit it through a copy of the row's body read back
	if _, err := db.Exec(`DROP TRIGGER IF EXISTS doomsday_charters_no_update`); err != nil {
		t.Skip("the charter row cannot be edited in this schema: ", err)
	}
	if _, err := db.Exec(`UPDATE doomsday_charters SET body = json_set(body, '$.limits.queue_max_rows', 999999) WHERE profile = 'p'`); err != nil {
		t.Skip(err)
	}
	if _, err := s.LoadCharter(ctx, "p", 1); !errors.Is(err, ErrUnverifiable) {
		t.Fatalf("an edited old row loaded: %v", err)
	}
}

func TestValidateBoundsTheGovernorLimits(t *testing.T) {
	for name, mut := range map[string]func(*Charter){
		"rows negative":  func(c *Charter) { c.Limits.QueueMaxRows = -1 },
		"rows huge":      func(c *Charter) { c.Limits.QueueMaxRows = MaxQueueRows + 1 },
		"bytes huge":     func(c *Charter) { c.Limits.QueueMaxBytes = MaxQueueBytes + 1 },
		"safe above cap": func(c *Charter) { c.Limits.SafeMinConcurrent = c.Limits.MaxConcurrent + 1 },
		"safe negative":  func(c *Charter) { c.Limits.SafeMinConcurrent = -1 },
	} {
		c := charter("p", 1)
		mut(&c)
		if err := c.Validate(); !errors.Is(err, ErrInvalidCharter) {
			t.Errorf("%s: %v", name, err)
		}
	}
	c := charter("p", 1)
	c.Limits.QueueMaxRows, c.Limits.QueueMaxBytes, c.Limits.SafeMinConcurrent = 10, 1024, 1
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
