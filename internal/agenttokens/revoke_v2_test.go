package agenttokens

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/procbind"
)

type fakeHandle struct{ closed atomic.Int32 }

func (h *fakeHandle) Alive() bool       { return h.closed.Load() == 0 }
func (h *fakeHandle) Pid() (int, error) { return 4242, nil }
func (h *fakeHandle) Close() error      { h.closed.Add(1); return nil }

// the times of every v2 token here: a registry entry for it carries the same expiry
var (
	v2Created = time.Now().UTC().Truncate(time.Millisecond)
	v2Expires = v2Created.Add(time.Hour)
)

// v2Mission puts a v2 mission token in the store.
func v2Mission(t *testing.T, s *Store, id, profile, owner string) {
	t.Helper()
	ctx := context.Background()
	key, err := procbind.RowKey(ctx, s.db, profile)
	if err != nil {
		t.Fatal(err)
	}
	row := procbind.MissionTokenRow{ID: id, ProfileID: profile, TokenHash: "h-" + id, Owner: owner, Session: 7, Task: 3, Role: "worker", Actor: "a",
		CreatedAt: v2Created, ExpiresAt: v2Expires}
	if err := procbind.InsertMissionToken(ctx, s.db, key, row); err != nil {
		t.Fatal(err)
	}
}

func v2Revoked(t *testing.T, s *Store, id string) bool {
	t.Helper()
	rv, err := procbind.IsRevoked(context.Background(), s.db, procbind.KindMission, id)
	if err != nil {
		t.Fatal(err)
	}
	return rv
}

// A legacy revocation revokes the v2 mission tokens of the same selection and no others, and ends their live entries.
func TestLegacyRevocationAlsoRevokesTheV2Tokens(t *testing.T) {
	ctx := context.Background()
	for name, run := range map[string]func(s *Store, legacyID string) error{
		"Revoke":        func(s *Store, id string) error { _, err := s.Revoke(ctx, "default", id); return err },
		"RevokeProfile": func(s *Store, _ string) error { _, err := s.RevokeProfile(ctx, "default"); return err },
		"RevokeOwner":   func(s *Store, _ string) error { _, err := s.RevokeOwner(ctx, "run-a"); return err },
	} {
		s := newStore(t)
		b := procbind.NewBinder(time.Now)
		s.SetBinder(b)
		tok, _, err := s.CreateMission(ctx, "default", missionB, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		v2Mission(t, s, tok.ID, "default", "run-a")      // selected by all three: same id, profile and owner
		v2Mission(t, s, "other-run", "default", "run-b") // another owner of the same profile
		v2Mission(t, s, "other-prof", "p2", "run-a")     // another profile, same owner
		h := &fakeHandle{}
		gen, err := b.Register(procbind.EntrySpec{TokenID: tok.ID, Kind: procbind.KindMission, Profile: "default", Scope: "mission:7", Owner: "run-a", Expires: v2Expires}, h)
		if err != nil {
			t.Fatal(err)
		}
		if err := b.Activate("default", tok.ID, gen); err != nil {
			t.Fatal(err)
		}
		if err := run(s, tok.ID); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !v2Revoked(t, s, tok.ID) {
			t.Errorf("%s: the v2 token of the selection is still live", name)
		}
		if h.closed.Load() != 1 {
			t.Errorf("%s: the live entry was not ended after the commit", name)
		}
		if name == "RevokeProfile" {
			if !v2Revoked(t, s, "other-run") {
				t.Errorf("%s: another owner of the profile survived", name)
			}
		} else if v2Revoked(t, s, "other-run") {
			t.Errorf("%s: another owner's v2 token was revoked", name)
		}
		if name != "RevokeOwner" && name != "RevokeProfile" && v2Revoked(t, s, "other-prof") {
			t.Errorf("%s: another profile's v2 token was revoked", name)
		}
		if name == "RevokeProfile" && v2Revoked(t, s, "other-prof") {
			t.Errorf("%s: another profile's v2 token was revoked", name)
		}
	}
}

// If the v2 side cannot be written the legacy change is rolled back and the call fails (decision K1).
func TestAFailingV2RevocationRollsTheLegacyChangeBack(t *testing.T) {
	ctx := context.Background()
	for name, run := range map[string]func(s *Store, legacyID string) error{
		"Revoke":        func(s *Store, id string) error { _, err := s.Revoke(ctx, "default", id); return err },
		"RevokeProfile": func(s *Store, _ string) error { _, err := s.RevokeProfile(ctx, "default"); return err },
		"RevokeOwner":   func(s *Store, _ string) error { _, err := s.RevokeOwner(ctx, "run-a"); return err },
	} {
		s := newStore(t)
		tok, secret, err := s.CreateMission(ctx, "default", missionB, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		v2Mission(t, s, tok.ID, "default", "run-a")
		if _, err := s.db.Exec(`CREATE TRIGGER zz_refuse BEFORE INSERT ON token_revocations_v2 BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
			t.Fatal(err)
		}
		if err := run(s, tok.ID); err == nil {
			t.Errorf("%s: the call succeeded although the v2 revocation failed", name)
		}
		if _, err := s.VerifyMission(ctx, "default", secret); err != nil {
			t.Errorf("%s: the legacy token was revoked although the v2 revocation failed: %v", name, err)
		}
		if v2Revoked(t, s, tok.ID) {
			t.Errorf("%s: a v2 revocation row exists", name)
		}
	}
}

// A v2 token whose id differs from every legacy id is reached by the owner selector, not by Revoke of a legacy id.
func TestAV2TokenWithItsOwnIdIsRevokedByOwnerNotByTheLegacyId(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	tok, _, err := s.CreateMission(ctx, "default", missionB, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	v2Mission(t, s, "v2-own-id", "default", "run-a")
	if _, err := s.Revoke(ctx, "default", tok.ID); err != nil {
		t.Fatal(err)
	}
	if v2Revoked(t, s, "v2-own-id") {
		t.Fatal("Revoke of a legacy id revoked a v2 token with another id")
	}
	if _, err := s.RevokeOwner(ctx, "run-a"); err != nil {
		t.Fatal(err)
	}
	if !v2Revoked(t, s, "v2-own-id") {
		t.Error("RevokeOwner did not revoke the v2 token")
	}
}

// The sweep finds a run that has only v2 tokens (a bound turn has no legacy token): the group's runs not reported as
// running are revoked, a run reported as running and the caller's own are left, another group's and a longer name that
// merely starts the same are untouched.
func TestSweepMissionsFindsRunsThatHaveOnlyV2Tokens(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for id, owner := range map[string]string{"g-own": "grp", "g-run1": "grp/run1", "g-run2": "grp/run2", "g-self": "grp/self", "g-other": "other/run1", "g-longer": "grp2/run1"} {
		v2Mission(t, s, id, "default", owner)
	}
	live := func(_ context.Context, owner string) bool { return owner == "grp/run2" }
	if _, err := s.SweepMissions(ctx, "grp", "grp/self", live); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"g-own": true, "g-run1": true, "g-run2": false, "g-self": false, "g-other": false, "g-longer": false} {
		if got := v2Revoked(t, s, id); got != want {
			t.Errorf("%s revoked=%v, want %v", id, got, want)
		}
	}
}
