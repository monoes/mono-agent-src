package agenttokens

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// A mission token ties one process to what it may act on. A conductor mints one for each
// worker or verifier it starts and hands it to that process; the task commands and tools learn
// the session, the task, the role and the name of the process from the token the database
// verifies, and from no label in the process's own environment (which the process can change).
// The token is short-lived, ends with the run, and is refused by every other consumer: the HTTP
// API does not accept it (Verify), and a token of another kind is not one (VerifyMission).

// EnvMissionToken is the environment variable that carries a mission token to the process it
// was minted for.
const EnvMissionToken = "MONOAGENT_TASK_TOKEN"

// Mission roles.
const (
	RoleWorker   = "worker"
	RoleVerifier = "verifier"
)

// MissionBinding is the role a mission token carries.
type MissionBinding struct {
	Session int64  `json:"session"`
	Task    int64  `json:"task"`
	Role    string `json:"role"`
	// Actor is the name the process acts under (claims, comments and verdicts carry it).
	Actor string `json:"actor"`
	// Owner is the run that minted the token: the conductor revokes by it.
	Owner string `json:"owner"`
}

// Verifier reports whether the role is the verifier's.
func (m MissionBinding) Verifier() bool { return m.Role == RoleVerifier }

// ErrMission means a mission binding that is not complete.
var ErrMission = errors.New("a mission token needs a session, a task, a role (worker or verifier), an actor name and an owner")

// CreateMission issues a mission token for the profile, valid for ttl (which must be positive:
// a mission token always ends). The returned secret is the only time the token exists in the clear.
func (s *Store) CreateMission(ctx context.Context, profileID string, b MissionBinding, ttl time.Duration) (Token, string, error) {
	if profileID == "" {
		return Token{}, "", errors.New("agent token needs a profile")
	}
	if b.Session <= 0 || b.Task <= 0 || (b.Role != RoleWorker && b.Role != RoleVerifier) ||
		strings.TrimSpace(b.Actor) == "" || b.Owner == "" || ttl <= 0 {
		return Token{}, "", ErrMission
	}
	var r [4]byte
	if _, err := rand.Read(r[:]); err != nil {
		return Token{}, "", fmt.Errorf("generating agent token: %w", err)
	}
	name := fmt.Sprintf("mission-s%d-t%d-%s-%s", b.Session, b.Task, b.Role, hex.EncodeToString(r[:]))
	if !validName(name) {
		return Token{}, "", ErrInvalidName
	}
	secret, err := generate()
	if err != nil {
		return Token{}, "", err
	}
	id, err := newID()
	if err != nil {
		return Token{}, "", err
	}
	now := s.now().UTC()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO agent_tokens (id, profile_id, name, scope, prefix, token_hash, created_at, expires_at, mission_session, mission_task, mission_role, mission_actor, mission_owner)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, profileID, name, string(ScopeRead), secret[:prefixLen], Hash(secret), now.Format(timeFmt), now.Add(ttl).Format(timeFmt),
		b.Session, b.Task, b.Role, b.Actor, b.Owner)
	if err != nil {
		return Token{}, "", fmt.Errorf("storing agent token: %w", err)
	}
	t, err := s.byID(ctx, id)
	return t, secret, err
}

// VerifyMission finds the mission token presented by a worker or verifier process. Unknown,
// expired, revoked and not-a-mission tokens are one error. It also refuses a token of another
// profile than profileID.
func (s *Store) VerifyMission(ctx context.Context, profileID, presented string) (Token, error) {
	t, err := s.verify(ctx, presented)
	if err != nil {
		return Token{}, err
	}
	if t.Mission == nil || t.ProfileID != profileID {
		return Token{}, ErrInvalid
	}
	return t, nil
}

// RevokeOwner revokes every unrevoked token the owner minted, for any profile: a conductor
// ending a run, or sweeping what an earlier run left.
func (s *Store) RevokeOwner(ctx context.Context, owner string) (int, error) {
	if owner == "" {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx, `UPDATE agent_tokens SET revoked_at = ? WHERE mission_owner = ? AND revoked_at IS NULL`,
		s.now().UTC().Format(timeFmt), owner)
	if err != nil {
		return 0, fmt.Errorf("revoking mission tokens: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
