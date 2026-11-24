package agenttokens

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/procchain"
)

func TestMissionTokenTTLIsCappedAt24Hours(t *testing.T) {
	s := newStore(t)
	tok, _, err := s.CreateMission(context.Background(), "default", missionB, 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got := tok.ExpiresAt.Sub(tok.CreatedAt); got > MaxMissionTTL+time.Minute {
		t.Fatalf("a 90 day ttl lives %v", got)
	}
}

// The process signature covers the scope: widening it in the row invalidates the record.
func TestMissionProcessSignatureCoversTheScope(t *testing.T) {
	needAncestry(t)
	s := newStore(t)
	ctx := context.Background()
	tok, secret := mintMission(t, s)
	self := os.Getpid()
	if err := s.BindMissionProcess(ctx, "default", tok.ID, self, procchain.Stamp(self)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyMission(ctx, "default", secret); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE agent_tokens SET scope = 'write' WHERE id = ?`, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyMission(ctx, "default", secret); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a widened scope was accepted: %v", err)
	}
}

// A record signed before the scope was covered still verifies.
func TestMissionProcessLegacySignatureStillVerifies(t *testing.T) {
	needAncestry(t)
	s := newStore(t)
	ctx := context.Background()
	tok, secret := mintMission(t, s)
	self := os.Getpid()
	stamp := procchain.Stamp(self)
	r, err := s.missionRowByID(ctx, tok.ID)
	if err != nil {
		t.Fatal(err)
	}
	key, err := missionKey(ctx, s.db, "default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE agent_tokens SET mission_pid = ?, mission_stamp = ?, mission_mac = ? WHERE id = ?`,
		self, stamp, r.signV1(key, self, stamp), tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyMission(ctx, "default", secret); err != nil {
		t.Fatalf("a legacy signature was refused: %v", err)
	}
}
