package agenttokens

import (
	"context"
	"errors"
	"testing"
	"time"
)

var missionB = MissionBinding{Session: 4, Task: 9, Role: RoleVerifier, Actor: "v4-t9", Owner: "run-a"}

func TestMissionTokenCarriesItsBindingAndStoresOnlyTheHash(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	tok, secret, err := s.CreateMission(ctx, "default", missionB, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Mission == nil || *tok.Mission != missionB || tok.ExpiresAt == nil || tok.Bound() {
		t.Fatalf("token: %+v", tok)
	}
	var stored string
	if err := s.db.QueryRow(`SELECT token_hash FROM agent_tokens WHERE id = ?`, tok.ID).Scan(&stored); err != nil || stored != Hash(secret) || stored == secret {
		t.Fatalf("stored %q", stored)
	}
	got, err := s.VerifyMission(ctx, "default", secret)
	if err != nil || got.Mission == nil || *got.Mission != missionB {
		t.Fatalf("verify: %+v %v", got, err)
	}
}

func TestMissionTokenIsNotAnAPIToken(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	_, secret, err := s.CreateMission(ctx, "default", missionB, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(ctx, secret); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the HTTP API's check accepted a mission token: %v", err)
	}
	_, plain, err := s.Create(ctx, "default", "plain", ScopeWrite, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyMission(ctx, "default", plain); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a plain token passed as a mission token: %v", err)
	}
}

func TestMissionTokenRefusals(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()
	for name, b := range map[string]MissionBinding{
		"no session": {Task: 1, Role: RoleWorker, Actor: "a", Owner: "o"},
		"no task":    {Session: 1, Role: RoleWorker, Actor: "a", Owner: "o"},
		"no role":    {Session: 1, Task: 1, Actor: "a", Owner: "o"},
		"odd role":   {Session: 1, Task: 1, Role: "admin", Actor: "a", Owner: "o"},
		"no actor":   {Session: 1, Task: 1, Role: RoleWorker, Actor: " ", Owner: "o"},
		"no owner":   {Session: 1, Task: 1, Role: RoleWorker, Actor: "a"},
	} {
		if _, _, err := s.CreateMission(ctx, "default", b, time.Hour); !errors.Is(err, ErrMission) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, _, err := s.CreateMission(ctx, "default", missionB, 0); !errors.Is(err, ErrMission) {
		t.Errorf("a mission token that never expires: %v", err)
	}

	// expired, revoked, another profile, forged: one error each
	past := NewStore(s.db)
	past.SetClock(func() time.Time { return now.Add(-2 * time.Hour) })
	_, expired, _ := past.CreateMission(ctx, "default", missionB, time.Hour)
	if _, err := s.VerifyMission(ctx, "default", expired); !errors.Is(err, ErrInvalid) {
		t.Errorf("expired: %v", err)
	}
	tok, secret, _ := s.CreateMission(ctx, "default", missionB, time.Hour)
	if _, err := s.VerifyMission(ctx, "other", secret); !errors.Is(err, ErrInvalid) {
		t.Errorf("another profile: %v", err)
	}
	if _, err := s.VerifyMission(ctx, "default", secret+"x"); !errors.Is(err, ErrInvalid) {
		t.Errorf("forged: %v", err)
	}
	if _, err := s.Revoke(ctx, "default", tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyMission(ctx, "default", secret); !errors.Is(err, ErrInvalid) {
		t.Errorf("revoked: %v", err)
	}
}

func TestRevokeOwnerEndsOnlyThatRunsTokens(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	b2 := missionB
	b2.Owner = "run-b"
	_, a1, _ := s.CreateMission(ctx, "default", missionB, time.Hour)
	_, a2, _ := s.CreateMission(ctx, "default", missionB, time.Hour)
	_, other, _ := s.CreateMission(ctx, "default", b2, time.Hour)
	n, err := s.RevokeOwner(ctx, "run-a")
	if err != nil || n != 2 {
		t.Fatalf("revoked %d: %v", n, err)
	}
	for _, sec := range []string{a1, a2} {
		if _, err := s.VerifyMission(ctx, "default", sec); !errors.Is(err, ErrInvalid) {
			t.Errorf("a token of the run survived: %v", err)
		}
	}
	if _, err := s.VerifyMission(ctx, "default", other); err != nil {
		t.Errorf("another run's token was revoked: %v", err)
	}
	if n, _ := s.RevokeOwner(ctx, ""); n != 0 {
		t.Errorf("an empty owner revoked %d", n)
	}
}
