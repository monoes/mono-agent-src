package doomsday

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A profile with no vault data key is told so in plain words and what to do, by the engine's containment step (which start,
// render, probe and Enter all ask first), before the witness is built. Mutants tried: drop the VaultKey step in Containment
// (the witness's technical refusal comes instead: red); a wording that leaves the secrets package's sentence in (red).
func TestAProfileWithNoVaultKeyIsToldInPlainWords(t *testing.T) {
	r, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w := r.w
	w.VaultKey = VaultKeyCheck(db.DB, "no-such-profile")
	pins := countingPins(t)
	err := w.Engine().Containment()
	if !errors.Is(err, ErrNoVaultKey) {
		t.Fatalf("no key: %v", err)
	}
	for _, want := range []string{"no vault key yet", "secret add", "start again"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q is missing from %q", want, err)
		}
	}
	for _, tech := range []string{"secrets:", "data key", "witness", "authority cannot be verified"} {
		if strings.Contains(err.Error(), tech) {
			t.Errorf("the refusal still speaks technically (%q): %q", tech, err)
		}
	}
	if pins.Load() != 0 {
		t.Fatalf("the witness was built (%d pin(s)) for a profile that cannot sign", pins.Load())
	}
}

// The probe reports it before the form is filled in: not available, with the same reason, and nothing written.
func TestProbeReportsAMissingVaultKey(t *testing.T) {
	r, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w := r.w
	w.VaultKey = VaultKeyCheck(db.DB, "no-such-profile")
	got := Availability(context.Background(), w.Engine())
	if got.ModeAvailable || !strings.Contains(got.Reason, "no vault key yet") {
		t.Fatalf("probe: %+v", got)
	}
}

// Enter itself refuses too (no starter can skip the step), with nothing recorded.
func TestEnterRefusesAMissingVaultKeyInPlainWords(t *testing.T) {
	r, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w := r.w
	w.VaultKey = VaultKeyCheck(db.DB, "no-such-profile")
	e := w.Engine()
	canon, err := e.Canonical(context.Background(), mustCharter(t, r.charter))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Enter(context.Background(), canon, canon.Hash, Ack{Kind: "script"}); !errors.Is(err, ErrNoVaultKey) {
		t.Fatalf("Enter: %v", err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM doomsday_runs`); n != 0 {
		t.Fatalf("%d run row(s) written", n)
	}
}
