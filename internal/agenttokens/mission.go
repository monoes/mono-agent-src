package agenttokens

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/procbind"
)

// A mission token ties one process to what it may act on. A conductor mints one for each
// worker or verifier it starts and hands it to that process; the task commands and tools learn
// the session, the task, the role and the name of the process from the token the database
// verifies, and from no label in the process's own environment (which the process can change).
// The token is short-lived, ends with the run, and is refused by every other consumer: the HTTP
// API does not accept it (Verify), and a token of another kind is not one (VerifyMission).

// EnvMissionToken is the environment variable that carries a mission token to the process it
// was minted for. Its name holds none of the words (key, secret, token, password) by which an
// agent runtime's default shell policy drops variables from the environment of the shells and
// tool servers it starts: the one process this is minted for has to see it, so a name-based
// filter must not take it out. The runtimes' own policies live outside this repository.
const EnvMissionToken = "MONOAGENT_TASK_BINDING"

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
	secret, err := generate()
	if err != nil {
		return Token{}, "", err
	}
	now := s.now().UTC()
	// The name carries 8 random bytes, so two runs of one task practically never meet; a
	// collision is retried with a new suffix rather than failing the run.
	var id string
	for attempt := 0; ; attempt++ {
		var r [8]byte
		if _, err := rand.Read(r[:]); err != nil {
			return Token{}, "", fmt.Errorf("generating agent token: %w", err)
		}
		name := fmt.Sprintf("mission-s%d-t%d-%s-%s", b.Session, b.Task, b.Role, hex.EncodeToString(r[:]))
		if !validName(name) {
			return Token{}, "", ErrInvalidName
		}
		if id, err = newID(); err != nil {
			return Token{}, "", err
		}
		_, err = s.db.ExecContext(ctx,
			`INSERT INTO agent_tokens (id, profile_id, name, scope, prefix, token_hash, created_at, expires_at, mission_session, mission_task, mission_role, mission_actor, mission_owner)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, profileID, name, string(ScopeRead), secret[:prefixLen], Hash(secret), now.Format(timeFmt), now.Add(ttl).Format(timeFmt),
			b.Session, b.Task, b.Role, b.Actor, b.Owner)
		if err != nil && attempt < 2 && strings.Contains(err.Error(), "UNIQUE constraint failed: agent_tokens.profile_id, agent_tokens.name") {
			continue
		}
		if err != nil {
			return Token{}, "", fmt.Errorf("storing agent token: %w", err)
		}
		break
	}
	t, err := s.byID(ctx, id)
	return t, secret, err
}

// VerifyMission finds the mission token presented by a worker or verifier process. Unknown,
// expired, revoked and not-a-mission tokens are one error. It also refuses a token of another
// profile than profileID, and, for a token bound to a process (mission_process.go), a caller
// that is not that process or one of its descendants.
func (s *Store) VerifyMission(ctx context.Context, profileID, presented string) (Token, error) {
	if strings.HasPrefix(presented, procbind.TaskPrefixV2) {
		return s.verifyMissionV2(ctx, profileID, presented)
	}
	if strings.HasPrefix(presented, procbind.SessionPrefixV2) {
		return Token{}, ErrInvalid // a session token of the v2 family is no mission token: it never meets the legacy lookup
	}
	t, err := s.verify(ctx, presented)
	if err != nil {
		return Token{}, err
	}
	if t.Mission == nil || t.ProfileID != profileID {
		return Token{}, ErrInvalid
	}
	if err := s.checkMissionProcess(ctx, t); err != nil {
		return Token{}, err
	}
	return t, nil
}

// RevokeOwner revokes every unrevoked token the owner minted, for any profile: a conductor
// ending a run, or sweeping what an earlier run left.
func (s *Store) RevokeOwner(ctx context.Context, owner string) (int, error) {
	if owner == "" {
		return 0, nil
	}
	n := 0
	err := s.revokeBoth(ctx, procbind.Selector{Owner: owner}, func(conn *sql.Conn) error {
		res, err := conn.ExecContext(ctx, `UPDATE agent_tokens SET revoked_at = ? WHERE mission_owner = ? AND revoked_at IS NULL`,
			s.now().UTC().Format(timeFmt), owner)
		if err != nil {
			return fmt.Errorf("revoking mission tokens: %w", err)
		}
		c, _ := res.RowsAffected()
		n = int(c)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// SweepMissions is what a conductor does when it starts or resumes. group names the runs of one
// conversation: an owner that is group, or group followed by "/" and a run id. It revokes the
// unrevoked tokens of every other owner in the group that live does not report as running (a run
// that ended without revoking: a crash or a kill), leaves the tokens of a run that is still
// running (a second conductor of the conversation must not end the first one's workers) and
// those of self, and purges the mission tokens that have expired, so the list does not grow.
// A nil live reports no owner as running. It returns how many tokens it revoked.
func (s *Store) SweepMissions(ctx context.Context, group, self string, live func(ctx context.Context, owner string) bool) (int, error) {
	if group == "" {
		return 0, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT mission_owner FROM agent_tokens WHERE revoked_at IS NULL AND mission_session > 0
		   AND (mission_owner = ? OR mission_owner LIKE ?) AND mission_owner <> ?`, group, group+"/%", self)
	if err != nil {
		return 0, fmt.Errorf("listing mission token owners: %w", err)
	}
	var owners []string
	for rows.Next() {
		var o string
		if err := rows.Scan(&o); err != nil {
			rows.Close()
			return 0, err
		}
		owners = append(owners, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// The v2 mission tokens of the group's runs: a bound turn has no legacy token, so its run is found here (the
	// revocation of an owner revokes both forms).
	v2, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT mission_owner FROM agent_tokens_v2 t WHERE mission_session > 0
		   AND (mission_owner = ?1 OR substr(mission_owner, 1, length(?1) + 1) = ?1 || '/') AND mission_owner <> ?2
		   AND NOT EXISTS (SELECT 1 FROM token_revocations_v2 r WHERE r.token_kind = 'mission' AND r.token_id = t.id)`, group, self)
	if err != nil {
		return 0, fmt.Errorf("listing v2 mission token owners: %w", err)
	}
	have := map[string]bool{}
	for _, o := range owners {
		have[o] = true
	}
	for v2.Next() {
		var o string
		if err := v2.Scan(&o); err != nil {
			v2.Close()
			return 0, err
		}
		if !have[o] {
			have[o] = true
			owners = append(owners, o)
		}
	}
	v2.Close()
	if err := v2.Err(); err != nil {
		return 0, err
	}
	total := 0
	for _, o := range owners {
		if live != nil && live(ctx, o) {
			continue
		}
		n, err := s.RevokeOwner(ctx, o)
		total += n
		if err != nil {
			return total, err
		}
	}
	if _, err := s.db.ExecContext(ctx, purgeExpiredMissions, s.now().UTC().Format(timeFmt)); err != nil {
		return total, fmt.Errorf("purging expired mission tokens: %w", err)
	}
	s.purgeV2(ctx)
	return total, nil
}

// purgeInterval is how often a store runs the v2 retention sweep: a sweep of an idle database is cheap, but this
// runs on every conversation delete too.
const purgeInterval = time.Hour

// purgeV2 removes the v2 binding rows that are past retention (procbind.PurgeRetained, 3e). It never fails the
// sweep it runs in: an error is logged and the rows wait for the next one. This process's clock only limits how
// often it runs; what counts as old is decided by the database's clock.
func (s *Store) purgeV2(ctx context.Context) {
	s.mu.Lock()
	now := s.now()
	if !s.lastPurge.IsZero() && now.Sub(s.lastPurge) < purgeInterval {
		s.mu.Unlock()
		return
	}
	s.lastPurge = now
	s.mu.Unlock()
	if _, err := procbind.PurgeRetained(ctx, s.db); err != nil {
		log.Printf("agenttokens: v2 retention sweep: %v", err)
	}
}

// procbind:legacy-only an expired token is denied by its expiry; the v2 rows of expired tokens follow their own retention (3e).
const purgeExpiredMissions = `DELETE ` + `FROM agent_tokens WHERE mission_session > 0 AND expires_at IS NOT NULL AND expires_at < ?`
