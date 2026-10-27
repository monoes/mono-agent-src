package agenttokens

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/procchain"
	"github.com/monoes/mono-agent/internal/secrets"
)

// A mission token can name the process its worker or verifier runs as, so that a process which
// is not that worker (a sibling worker, any other process of the same user) and merely read the
// token from the process table or an inherited environment is refused: the caller must be the
// named process or one of its descendants. A worker's own shell and tool servers are its
// descendants and keep working. This is the ancestry binding of the coder session tokens
// (internal/coderhost/sessiontoken.go), applied to mission tokens.
//
// Binding (BindMissionProcess, called by the conductor right after it starts the run):
//   - the pid and its start stamp are recorded with a signature (HMAC-SHA-256 under a key
//     derived from the profile's vault data key, purpose missionProcessPurpose) over the token's
//     identity, binding, expiry, pid and stamp; a row with a process and no valid signature is
//     refused. The signature stops an edit of the process columns, or a move of them to another
//     token, by a writer that lacks the vault key. It does not stop a writer who clears all three
//     columns (the row then reads as an unbound token) or who inserts a token row of its own;
//     like the session token this is a courtesy layer between agents of one user, not a sandbox.
//   - revoke, expiry and the per-turn owners the sweep works by are unchanged.
//
// Once a process is recorded and ancestry is supported, the caller must show that process in its
// parent chain (with the same start stamp: pid reuse is refused). It fails closed: a caller whose
// chain cannot be read (its own /proc, a race) is refused like one that is outside the tree. Kept
// as before (no process check) when:
//   - no process was recorded (a token minted before this existed, a runtime whose tool servers
//     may live outside the worker's process tree, so the conductor binds none: see the dynorg
//     runtime list, a process whose start stamp could not be read, a bind that failed, or the
//     moment between the start of the run and the record), or
//   - ancestry is not read at all (off Linux: procchain.Supported is false).
//
// Between the start of the run and BindMissionProcess the token names no process and behaves
// as an unbound token: only the process it was handed to holds it then.

const missionProcessPurpose = "mission-token-process"

// ErrNotInWorkerTree is ErrInvalid for a token that is genuine but presented by a process outside
// the tree of the process it was bound to.
var ErrNotInWorkerTree = fmt.Errorf("%w (the caller is not inside this worker's process tree: a detached or reparented tool, another namespace, or an unreadable ancestry; run the task command from the worker's own shell or tool server)", ErrInvalid)

func missionMAC(key []byte, f ...string) string {
	m := hmac.New(sha256.New, key)
	for _, s := range f {
		fmt.Fprintf(m, "%d:%s;", len(s), s)
	}
	return hex.EncodeToString(m.Sum(nil))
}

// missionRow is what the process signature covers.
type missionRow struct {
	id, profile, hash, expires, role, actor, owner string
	session, task                                  int64
	pid                                            int
	stamp, mac                                     string
}

func (r missionRow) sign(key []byte, pid int, stamp string) string {
	return missionMAC(key, r.id, r.profile, r.hash, r.expires, r.role, r.actor, r.owner,
		fmt.Sprint(r.session), fmt.Sprint(r.task), fmt.Sprint(pid), stamp)
}

func (s *Store) missionRowByID(ctx context.Context, id string) (missionRow, error) {
	r := missionRow{id: id}
	var exp sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT profile_id, token_hash, expires_at, mission_role, mission_actor, mission_owner, mission_session, mission_task, mission_pid, mission_stamp, mission_mac
		FROM agent_tokens WHERE id = ? AND mission_session > 0`, id).
		Scan(&r.profile, &r.hash, &exp, &r.role, &r.actor, &r.owner, &r.session, &r.task, &r.pid, &r.stamp, &r.mac)
	r.expires = exp.String
	return r, err
}

func missionKey(ctx context.Context, db *sql.DB, profileID string) ([]byte, error) {
	return secrets.DeriveKey(ctx, db, profileID, missionProcessPurpose)
}

// MissionProcessSupported says whether process ancestry can be read here, so that a binding is
// recorded and checked at all.
func MissionProcessSupported() bool { return procchain.Supported }

// ProcessStamp is the start stamp of the process pid ("" when unreadable or unsupported).
func ProcessStamp(pid int) string { return procchain.Stamp(pid) }

// BindMissionProcess names the process (pid and start stamp) the mission token id runs as and
// signs the row. It refuses a pid that is not a process, and, where ancestry is supported, an
// empty stamp (callers then leave the token unbound).
func (s *Store) BindMissionProcess(ctx context.Context, profileID, id string, pid int, stamp string) error {
	if pid <= 1 {
		return fmt.Errorf("%d is not a worker's pid", pid)
	}
	if procchain.Supported && stamp == "" {
		return errors.New("a worker's process needs a start stamp to bind a token to it")
	}
	r, err := s.missionRowByID(ctx, id)
	if err != nil || r.profile != profileID {
		return ErrNotFound
	}
	key, err := missionKey(ctx, s.db, profileID)
	if err != nil {
		return fmt.Errorf("the mission token could not be signed: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE agent_tokens SET mission_pid = ?, mission_stamp = ?, mission_mac = ? WHERE id = ? AND revoked_at IS NULL`,
		pid, stamp, r.sign(key, pid, stamp), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound // revoked (or gone) before the process could be recorded
	}
	return nil
}

// checkMissionProcess applies the binding to the caller; see the rules above.
func (s *Store) checkMissionProcess(ctx context.Context, t Token) error {
	r, err := s.missionRowByID(ctx, t.ID)
	if err != nil {
		return ErrInvalid
	}
	if r.pid == 0 && r.stamp == "" && r.mac == "" {
		return nil // names no process
	}
	key, err := missionKey(ctx, s.db, r.profile)
	if err != nil || !hmac.Equal([]byte(r.sign(key, r.pid, r.stamp)), []byte(r.mac)) {
		return ErrInvalid // a process was recorded and the record is not genuine (or cannot be checked)
	}
	if !procchain.Supported {
		return nil
	}
	if !procchain.DescendsFrom(r.pid, r.stamp) {
		return ErrNotInWorkerTree
	}
	return nil
}
