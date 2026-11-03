package store

import (
	"bytes"
	"context"
	"crypto/hmac"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SQLStore is the SQLite implementation of Store.
type SQLStore struct {
	db   *sql.DB
	keys Keyer
	now  func() time.Time
	// entry, when set, is an extra check of a charter at Enter (the ops package supplies one that validates the
	// constraints it enforces); a charter it refuses is never recorded.
	entry func(Charter) error
}

// WithEntryCheck returns the store with an additional charter check run by Enter after Validate.
func (s *SQLStore) WithEntryCheck(fn func(Charter) error) *SQLStore {
	c := *s
	c.entry = fn
	return &c
}

var _ Store = (*SQLStore)(nil)

// New returns a store over a database that has migration 153 applied.
func New(db *sql.DB, keys Keyer) *SQLStore { return &SQLStore{db: db, keys: keys, now: time.Now} }

func b(s string) []byte  { return []byte(s) }
func i64(n int64) []byte { return []byte(strconv.FormatInt(n, 10)) }
func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// NextGeneration is one more than the highest generation ever recorded for the profile; a number is never reused.
func (s *SQLStore) NextGeneration(ctx context.Context, profile string) (int64, error) {
	return nextGeneration(ctx, s.db, profile)
}

func nextGeneration(ctx context.Context, q DBTX, profile string) (int64, error) {
	var g int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(generation),0) FROM doomsday_charters WHERE profile = ?`, profile).Scan(&g)
	return g + 1, err
}

func (s *SQLStore) charterMAC(c Charter, hash [32]byte) ([]byte, error) {
	return s.keys.MAC("charter", hash[:], b(c.Profile), i64(c.Generation), b(c.ID()), i64(CanonVersion))
}

func (s *SQLStore) runMAC(r Run) ([]byte, error) {
	return s.keys.MAC("run", b(r.Profile), i64(r.Generation), b(r.CharterID), b(string(r.State)), i64(r.IntentAt), b(r.IntentBy), b(r.Cause))
}

// Enter records a charter and its running run. The charter is re-read and re-hashed inside the transaction and must
// equal the hash the operator confirmed; a generation that is not the next one, an active run, or a live member of an
// earlier run refuses.
func (s *SQLStore) Enter(ctx context.Context, c Charter, confirmed [32]byte, by string) (Run, error) {
	if err := c.Validate(); err != nil {
		return Run{}, err
	}
	if s.entry != nil {
		if err := s.entry(c); err != nil {
			return Run{}, fmt.Errorf("%w: %v", ErrInvalidCharter, err)
		}
	}
	if c.Hash() != confirmed {
		return Run{}, ErrHashMismatch
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	next, err := nextGeneration(ctx, tx, c.Profile)
	if err != nil {
		return Run{}, err
	}
	if c.Generation != next {
		return Run{}, ErrGenerationMoved
	}
	var live int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM doomsday_members WHERE profile = ? AND live = 1`, c.Profile).Scan(&live); err != nil {
		return Run{}, err
	}
	if live > 0 {
		return Run{}, ErrLiveMembers
	}
	hash := c.Hash()
	mac, err := s.charterMAC(c, hash)
	if err != nil {
		return Run{}, err
	}
	body, _ := json.Marshal(c.normalized())
	if _, err := tx.ExecContext(ctx, `INSERT INTO doomsday_charters(profile, generation, charter_id, canon_version, charter_hash, body, mac, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		c.Profile, c.Generation, c.ID(), CanonVersion, hash[:], string(body), mac, s.now().Unix()); err != nil {
		return Run{}, err
	}
	// Re-hash what was actually stored, in this transaction.
	stored, err := loadCharterRow(ctx, tx, s, c.Profile, c.Generation)
	if err != nil {
		return Run{}, err
	}
	if stored.Hash() != confirmed {
		return Run{}, ErrHashMismatch
	}
	r := Run{Profile: c.Profile, Generation: c.Generation, CharterID: c.ID(), State: StateRunning}
	if mac, err = s.runMAC(r); err != nil {
		return Run{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO doomsday_runs(profile, generation, charter_id, state, mac) VALUES (?,?,?,?,?)`,
		r.Profile, r.Generation, r.CharterID, string(r.State), mac); err != nil {
		if isUnique(err) {
			return Run{}, ErrActiveRun
		}
		return Run{}, err
	}
	if _, err := s.appendAuditTx(ctx, tx, c.Profile, c.Generation, "run.enter", by+" "+r.CharterID); err != nil {
		return Run{}, err
	}
	return r, tx.Commit()
}

// LoadCharter returns the stored charter after re-hashing it and verifying its MAC.
func (s *SQLStore) LoadCharter(ctx context.Context, profile string, generation int64) (Charter, error) {
	return loadCharterRow(ctx, s.db, s, profile, generation)
}

func loadCharterRow(ctx context.Context, q DBTX, s *SQLStore, profile string, generation int64) (Charter, error) {
	var (
		id, body string
		ver      int
		hash, mc []byte
	)
	err := q.QueryRowContext(ctx, `SELECT charter_id, canon_version, charter_hash, body, mac FROM doomsday_charters WHERE profile = ? AND generation = ?`,
		profile, generation).Scan(&id, &ver, &hash, &body, &mc)
	if err != nil {
		return Charter{}, err
	}
	if ver != CanonVersion {
		return Charter{}, ErrUnknownSchema
	}
	var c Charter
	if json.Unmarshal([]byte(body), &c) != nil || c.Profile != profile || c.Generation != generation {
		return Charter{}, ErrUnverifiable
	}
	h := c.Hash()
	if !bytes.Equal(h[:], hash) || c.ID() != id {
		return Charter{}, ErrUnverifiable
	}
	want, err := s.charterMAC(c, h)
	if err != nil || !hmac.Equal(want, mc) {
		return Charter{}, ErrUnverifiable
	}
	return c, nil
}

// GetRun returns the run row after verifying its MAC.
func (s *SQLStore) GetRun(ctx context.Context, profile string, generation int64) (Run, error) {
	return getRun(ctx, s.db, s, profile, generation)
}

func getRun(ctx context.Context, q DBTX, s *SQLStore, profile string, generation int64) (Run, error) {
	r := Run{Profile: profile, Generation: generation}
	var st string
	var mc []byte
	err := q.QueryRowContext(ctx, `SELECT charter_id, state, intent_at, intent_by, cause, mac FROM doomsday_runs WHERE profile = ? AND generation = ?`,
		profile, generation).Scan(&r.CharterID, &st, &r.IntentAt, &r.IntentBy, &r.Cause, &mc)
	if err != nil {
		return Run{}, err
	}
	r.State = RunState(st)
	want, err := s.runMAC(r)
	if err != nil || !hmac.Equal(want, mc) {
		return Run{}, ErrUnverifiable
	}
	return r, nil
}

var nextState = map[RunState]RunState{
	StateRunning: StateStopRequested, StateStopRequested: StateAuthorityRevoked,
	StateAuthorityRevoked: StateCleanupPending, StateCleanupPending: StateQuiescent,
}

// Advance moves the run one step forward and appends the audit row in the same transaction. Quiescent is refused
// while any member is live, whatever any repair state says.
func (s *SQLStore) Advance(ctx context.Context, profile string, generation int64, to RunState, by, cause string) (Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	r, err := getRun(ctx, tx, s, profile, generation)
	if err != nil {
		return Run{}, err
	}
	if nextState[r.State] != to {
		return Run{}, ErrBadTransition
	}
	if to == StateQuiescent {
		var live int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM doomsday_members WHERE profile = ? AND generation = ? AND live = 1`, profile, generation).Scan(&live); err != nil {
			return Run{}, err
		}
		if live > 0 {
			return Run{}, ErrLiveMembers
		}
	}
	from := r.State
	r.State = to
	if from == StateRunning {
		r.IntentAt, r.IntentBy, r.Cause = s.now().Unix(), by, cause
	}
	mac, err := s.runMAC(r)
	if err != nil {
		return Run{}, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE doomsday_runs SET state = ?, intent_at = ?, intent_by = ?, cause = ?, mac = ? WHERE profile = ? AND generation = ? AND state = ?`,
		string(to), r.IntentAt, r.IntentBy, r.Cause, mac, profile, generation, string(from))
	if err != nil {
		return Run{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return Run{}, ErrBadTransition
	}
	if _, err := s.appendAuditTx(ctx, tx, profile, generation, "run.advance", string(from)+">"+string(to)+" "+by); err != nil {
		return Run{}, err
	}
	return r, tx.Commit()
}

// Admit refuses unless the run row verifies, is the profile's highest generation and is running.
func (s *SQLStore) Admit(ctx context.Context, q DBTX, profile string, generation int64) error {
	r, err := getRun(ctx, q, s, profile, generation)
	if err != nil {
		if errors.Is(err, ErrUnverifiable) {
			return &Refusal{Reason: "run row cannot be verified"}
		}
		return &Refusal{Reason: "no such run"}
	}
	var top int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(generation),0) FROM doomsday_runs WHERE profile = ?`, profile).Scan(&top); err != nil || top != generation {
		return &Refusal{Reason: "not the current generation"}
	}
	if r.State != StateRunning {
		return &Refusal{Reason: "run is " + string(r.State)}
	}
	return nil
}

// Register adds a live member; the insert itself carries the running-state predicate.
func (s *SQLStore) Register(ctx context.Context, m Member) error {
	if err := s.Admit(ctx, s.db, m.Profile, m.Generation); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO doomsday_members(profile, generation, member_id, kind, pid, stamp, live)
SELECT ?,?,?,?,?,?,1 WHERE EXISTS (SELECT 1 FROM doomsday_runs WHERE profile = ? AND generation = ? AND state = 'running')`,
		m.Profile, m.Generation, m.ID, m.Kind, m.Pid, m.Stamp, m.Profile, m.Generation)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return &Refusal{Reason: "run is not running"}
	}
	return nil
}

// Release marks a member gone. A released member never becomes live again.
func (s *SQLStore) Release(ctx context.Context, profile string, generation int64, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE doomsday_members SET live = 0 WHERE profile = ? AND generation = ? AND member_id = ? AND live = 1`, profile, generation, id)
	return err
}

// LiveMembers lists the members still live.
func (s *SQLStore) LiveMembers(ctx context.Context, profile string, generation int64) ([]Member, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT member_id, kind, pid, stamp FROM doomsday_members WHERE profile = ? AND generation = ? AND live = 1 ORDER BY member_id`, profile, generation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		m := Member{Profile: profile, Generation: generation, Live: true}
		if err := rows.Scan(&m.ID, &m.Kind, &m.Pid, &m.Stamp); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ConsumeCumulative adds n to the run's counter kind in one conditional insert: the statement itself checks the run is
// running and that the new total stays within limit; approvalID can be consumed once. The audit row is in the same
// transaction.
func (s *SQLStore) ConsumeCumulative(ctx context.Context, profile string, generation int64, kind, approvalID string, n, limit int64) error {
	if n <= 0 || limit < 0 {
		return ErrOverLimit
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.chargeTx(ctx, tx, profile, generation, kind, approvalID, n, limit); err != nil {
		return err
	}
	if _, err := s.appendAuditTx(ctx, tx, profile, generation, "counter.consume", kind+" "+approvalID+" "+strconv.FormatInt(n, 10)); err != nil {
		return err
	}
	return tx.Commit()
}

// chargeTx is the conditional insert of one counter delta in the caller's transaction: the statement itself checks the
// run is running and that the new total stays within limit; a repeated approval id is ErrAlreadyConsumed.
func (s *SQLStore) chargeTx(ctx context.Context, tx *sql.Tx, profile string, generation int64, kind, approvalID string, n, limit int64) error {
	return s.chargeTxIf(ctx, tx, profile, generation, kind, approvalID, n, limit, "")
}

// chargeTxIf is chargeTx with one more condition in the same statement (extra is a SQL boolean; its args follow).
// When the statement writes nothing for a reason that is not the run state or the cap, the caller classifies it.
func (s *SQLStore) chargeTxIf(ctx context.Context, tx *sql.Tx, profile string, generation int64, kind, approvalID string, n, limit int64, extra string, extraArgs ...any) error {
	if n <= 0 || limit < 0 {
		return ErrOverLimit
	}
	if extra == "" {
		extra = "1"
	}
	args := append([]any{profile, generation, kind, approvalID, n, profile, generation, profile, generation, kind, limit, n}, extraArgs...)
	res, err := tx.ExecContext(ctx, `INSERT INTO doomsday_counter_deltas(profile, generation, kind, approval_id, n)
SELECT ?,?,?,?,? WHERE EXISTS (SELECT 1 FROM doomsday_runs WHERE profile = ? AND generation = ? AND state = 'running')
AND (SELECT COALESCE(SUM(n),0) FROM doomsday_counter_deltas WHERE profile = ? AND generation = ? AND kind = ?) <= ? - ?
AND `+extra, args...)
	if err != nil {
		if isUnique(err) {
			return ErrAlreadyConsumed
		}
		return err
	}
	if c, _ := res.RowsAffected(); c != 1 {
		if err := s.Admit(ctx, tx, profile, generation); err != nil {
			return err
		}
		return ErrOverLimit
	}
	return nil
}
