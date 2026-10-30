package procbind

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Storage for the peer-credential binding (migration 148): insert and read of the five v2 tables, every row
// signed under the profile's row key, and the revocation that legacy code paths write inside their own
// transaction. Nothing calls it yet.
//
// A row's signature covers every column except itself, under a domain that names the kind of row, so a
// signature of one kind never validates another and a changed or moved field does not verify. Reads verify the
// signature and refuse a row that does not carry a genuine one (the database alone does not make a row).

// DBTX is what the store needs of a database or a transaction (*sql.DB and *sql.Tx both satisfy it).
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

var (
	ErrRowNotFound = errors.New("procbind: no such row")
	ErrRowTampered = errors.New("procbind: the row does not carry a genuine signature")
	ErrBadSelector = errors.New("procbind: the revocation selector is not allowed")
)

const (
	rowVersion = 2
	rowDomain  = "mono/procbind/v2"
	// the stored form of every timestamp: text that compares as text (the migration's CHECK holds every column to it)
	tsLayout = "2006-01-02T15:04:05.000Z"
)

func ts(t time.Time) string { return t.UTC().Format(tsLayout) }

func parseTS(s string) (time.Time, error) { return time.Parse(tsLayout, s) }

// rowMAC signs a row: tag names the kind of row, fields are its columns in a fixed order.
func rowMAC(key []byte, tag string, fields ...string) (string, error) {
	if len(key) < MinKeyLen {
		return "", fmt.Errorf("procbind: row key shorter than %d bytes", MinKeyLen)
	}
	m := hmac.New(sha256.New, key)
	m.Write(Encode(rowDomain, rowVersion, append([]string{tag}, fields...)...))
	return hex.EncodeToString(m.Sum(nil)), nil
}

func verifyMAC(key []byte, got, tag string, fields ...string) error {
	want, err := rowMAC(key, tag, fields...)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(want), []byte(got)) {
		return ErrRowTampered
	}
	return nil
}

func need(vals ...string) error {
	for _, v := range vals {
		if v == "" {
			return errors.New("procbind: a row needs its identifying fields")
		}
	}
	return nil
}

// ---- mission tokens (agent_tokens_v2)

// MissionTokenRow is a v2 mission token. The secret is never stored, only its hash.
type MissionTokenRow struct {
	ID, ProfileID, TokenHash string
	Owner                    string
	Session, Task            int64
	Role, Actor              string
	CreatedAt, ExpiresAt     time.Time
}

func (r MissionTokenRow) fields() []string {
	return []string{r.ID, r.ProfileID, r.TokenHash, r.Owner, strconv.FormatInt(r.Session, 10), strconv.FormatInt(r.Task, 10), r.Role, r.Actor, ts(r.CreatedAt), ts(r.ExpiresAt)}
}

func InsertMissionToken(ctx context.Context, x DBTX, key []byte, r MissionTokenRow) error {
	if err := need(r.ID, r.ProfileID, r.TokenHash); err != nil || r.CreatedAt.IsZero() || r.ExpiresAt.IsZero() {
		return errors.New("procbind: a mission token row needs an id, a profile, a hash and its times")
	}
	mac, err := rowMAC(key, "mission-token", r.fields()...)
	if err != nil {
		return err
	}
	_, err = x.ExecContext(ctx, `INSERT INTO agent_tokens_v2 (id, profile_id, token_hash, mission_owner, mission_session, mission_task, mission_role, mission_actor, created_at, expires_at, version, mac)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.ID, r.ProfileID, r.TokenHash, r.Owner, r.Session, r.Task, r.Role, r.Actor, ts(r.CreatedAt), ts(r.ExpiresAt), rowVersion, mac)
	return err
}

func GetMissionToken(ctx context.Context, q DBTX, key []byte, profileID, id string) (MissionTokenRow, error) {
	var r MissionTokenRow
	var created, expires, mac string
	var version int
	err := q.QueryRowContext(ctx, `SELECT id, profile_id, token_hash, mission_owner, mission_session, mission_task, mission_role, mission_actor, created_at, expires_at, version, mac
		FROM agent_tokens_v2 WHERE profile_id = ? AND id = ?`, profileID, id).
		Scan(&r.ID, &r.ProfileID, &r.TokenHash, &r.Owner, &r.Session, &r.Task, &r.Role, &r.Actor, &created, &expires, &version, &mac)
	if errors.Is(err, sql.ErrNoRows) {
		return MissionTokenRow{}, ErrRowNotFound
	}
	if err != nil {
		return MissionTokenRow{}, err
	}
	if version != rowVersion {
		return MissionTokenRow{}, ErrRowTampered
	}
	if r.CreatedAt, err = parseTS(created); err != nil {
		return MissionTokenRow{}, ErrRowTampered
	}
	if r.ExpiresAt, err = parseTS(expires); err != nil {
		return MissionTokenRow{}, ErrRowTampered
	}
	if err := verifyMAC(key, mac, "mission-token", r.fields()...); err != nil {
		return MissionTokenRow{}, err
	}
	return r, nil
}

// ---- session tokens (coder_session_tokens_v2)

// SessionTokenRow is a v2 coder session token (one turn of one conversation).
type SessionTokenRow struct {
	ID, ProfileID, TokenHash string
	ConversationID, TurnID   string
	CreatedAt, ExpiresAt     time.Time
}

func (r SessionTokenRow) fields() []string {
	return []string{r.ID, r.ProfileID, r.TokenHash, r.ConversationID, r.TurnID, ts(r.CreatedAt), ts(r.ExpiresAt)}
}

func InsertSessionToken(ctx context.Context, x DBTX, key []byte, r SessionTokenRow) error {
	if err := need(r.ID, r.ProfileID, r.TokenHash, r.ConversationID, r.TurnID); err != nil || r.CreatedAt.IsZero() || r.ExpiresAt.IsZero() {
		return errors.New("procbind: a session token row needs an id, a profile, a hash, a conversation, a turn and its times")
	}
	mac, err := rowMAC(key, "session-token", r.fields()...)
	if err != nil {
		return err
	}
	_, err = x.ExecContext(ctx, `INSERT INTO coder_session_tokens_v2 (id, profile_id, token_hash, conversation_id, turn_id, created_at, expires_at, version, mac)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.ID, r.ProfileID, r.TokenHash, r.ConversationID, r.TurnID, ts(r.CreatedAt), ts(r.ExpiresAt), rowVersion, mac)
	return err
}

func GetSessionToken(ctx context.Context, q DBTX, key []byte, profileID, id string) (SessionTokenRow, error) {
	var r SessionTokenRow
	var created, expires, mac string
	var version int
	err := q.QueryRowContext(ctx, `SELECT id, profile_id, token_hash, conversation_id, turn_id, created_at, expires_at, version, mac
		FROM coder_session_tokens_v2 WHERE profile_id = ? AND id = ?`, profileID, id).
		Scan(&r.ID, &r.ProfileID, &r.TokenHash, &r.ConversationID, &r.TurnID, &created, &expires, &version, &mac)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionTokenRow{}, ErrRowNotFound
	}
	if err != nil {
		return SessionTokenRow{}, err
	}
	if version != rowVersion {
		return SessionTokenRow{}, ErrRowTampered
	}
	if r.CreatedAt, err = parseTS(created); err != nil {
		return SessionTokenRow{}, ErrRowTampered
	}
	if r.ExpiresAt, err = parseTS(expires); err != nil {
		return SessionTokenRow{}, ErrRowTampered
	}
	if err := verifyMAC(key, mac, "session-token", r.fields()...); err != nil {
		return SessionTokenRow{}, err
	}
	return r, nil
}

// ---- bindings (process_bindings)

// BindingRow ties a token to the worker it was issued to. Its mode is always peercred. The table has no kind
// column (a mission token and a session token may share an id), so the kind is part of the signed fields and not
// stored: a binding verifies only when it is read as the kind it was written as.
type BindingRow struct {
	Kind                      Kind
	TokenID, ProfileID, Scope string
	WorkerPID                 int
	WorkerStamp               string
	HostGeneration            string
	CreatedAt, ExpiresAt      time.Time
}

func (r BindingRow) fields() []string {
	return []string{string(r.Kind), r.TokenID, r.ProfileID, r.Scope, "peercred", strconv.Itoa(r.WorkerPID), r.WorkerStamp, r.HostGeneration, ts(r.CreatedAt), ts(r.ExpiresAt)}
}

func InsertBinding(ctx context.Context, x DBTX, key []byte, r BindingRow) error {
	if err := need(r.TokenID, r.ProfileID, r.Scope, r.WorkerStamp, r.HostGeneration); err != nil || (r.Kind != KindMission && r.Kind != KindSession) || r.WorkerPID <= 1 || r.CreatedAt.IsZero() || r.ExpiresAt.IsZero() {
		return errors.New("procbind: a binding row needs a token, a profile, a scope, a worker pid and stamp, a host generation and its times")
	}
	mac, err := rowMAC(key, "binding", r.fields()...)
	if err != nil {
		return err
	}
	_, err = x.ExecContext(ctx, `INSERT INTO process_bindings (token_id, profile_id, scope, mode, worker_pid, worker_stamp, host_generation, version, created_at, expires_at, mac)
		VALUES (?, ?, ?, 'peercred', ?, ?, ?, ?, ?, ?, ?)`, r.TokenID, r.ProfileID, r.Scope, r.WorkerPID, r.WorkerStamp, r.HostGeneration, rowVersion, ts(r.CreatedAt), ts(r.ExpiresAt), mac)
	return err
}

func GetBinding(ctx context.Context, q DBTX, key []byte, kind Kind, profileID, tokenID string) (BindingRow, error) {
	r := BindingRow{Kind: kind}
	var created, expires, mac string
	var version int
	err := q.QueryRowContext(ctx, `SELECT token_id, profile_id, scope, worker_pid, worker_stamp, host_generation, version, created_at, expires_at, mac
		FROM process_bindings WHERE profile_id = ? AND token_id = ?`, profileID, tokenID).
		Scan(&r.TokenID, &r.ProfileID, &r.Scope, &r.WorkerPID, &r.WorkerStamp, &r.HostGeneration, &version, &created, &expires, &mac)
	if errors.Is(err, sql.ErrNoRows) {
		return BindingRow{}, ErrRowNotFound
	}
	if err != nil {
		return BindingRow{}, err
	}
	if version != rowVersion { // the mode is fixed by the table's CHECK and is part of the signed fields
		return BindingRow{}, ErrRowTampered
	}
	if r.CreatedAt, err = parseTS(created); err != nil {
		return BindingRow{}, ErrRowTampered
	}
	if r.ExpiresAt, err = parseTS(expires); err != nil {
		return BindingRow{}, ErrRowTampered
	}
	if err := verifyMAC(key, mac, "binding", r.fields()...); err != nil {
		return BindingRow{}, err
	}
	return r, nil
}

// ---- revocations (token_revocations_v2)

// RevocationRow records that a token was revoked, and why.
type RevocationRow struct {
	Kind      Kind
	TokenID   string
	ProfileID string
	Reason    string
	CreatedAt time.Time
}

func (r RevocationRow) fields() []string {
	return []string{string(r.Kind), r.TokenID, r.ProfileID, r.Reason, ts(r.CreatedAt)}
}

func InsertRevocation(ctx context.Context, x DBTX, key []byte, r RevocationRow) error {
	if (r.Kind != KindMission && r.Kind != KindSession) || r.TokenID == "" || r.ProfileID == "" || r.CreatedAt.IsZero() {
		return errors.New("procbind: a revocation row needs a kind, a token, a profile and a time")
	}
	mac, err := rowMAC(key, "revocation", r.fields()...)
	if err != nil {
		return err
	}
	_, err = x.ExecContext(ctx, `INSERT INTO token_revocations_v2 (token_kind, token_id, profile_id, reason, created_at, mac) VALUES (?, ?, ?, ?, ?, ?)`,
		string(r.Kind), r.TokenID, r.ProfileID, r.Reason, ts(r.CreatedAt), mac)
	return err
}

func GetRevocation(ctx context.Context, q DBTX, key []byte, kind Kind, profileID, tokenID string) (RevocationRow, error) {
	var r RevocationRow
	var created, mac, k string
	err := q.QueryRowContext(ctx, `SELECT token_kind, token_id, profile_id, reason, created_at, mac FROM token_revocations_v2 WHERE token_kind = ? AND profile_id = ? AND token_id = ?`,
		string(kind), profileID, tokenID).Scan(&k, &r.TokenID, &r.ProfileID, &r.Reason, &created, &mac)
	if errors.Is(err, sql.ErrNoRows) {
		return RevocationRow{}, ErrRowNotFound
	}
	if err != nil {
		return RevocationRow{}, err
	}
	r.Kind = Kind(k)
	if r.CreatedAt, err = parseTS(created); err != nil {
		return RevocationRow{}, ErrRowTampered
	}
	if err := verifyMAC(key, mac, "revocation", r.fields()...); err != nil {
		return RevocationRow{}, err
	}
	return r, nil
}

// IsRevoked says whether a revocation row exists for the token. A token's identity is its kind and id (the
// table's primary key; the token tables key their ids globally), so a revocation row counts whatever profile
// it names. It reads existence only; a verifier that must trust the row reads it with GetRevocation. Existence
// is enough to deny: a forged or misplaced revocation can only make a token deny, never allow.
func IsRevoked(ctx context.Context, q DBTX, kind Kind, tokenID string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM token_revocations_v2 WHERE token_kind = ? AND token_id = ?`, string(kind), tokenID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ---- host starts (procbind_host)

// HostRow is one start of the host: the highest Seq of a profile is the current generation.
type HostRow struct {
	ProfileID  string
	Seq        int64
	Generation string
	StartedAt  time.Time
}

func (r HostRow) fields() []string {
	return []string{r.ProfileID, strconv.FormatInt(r.Seq, 10), r.Generation, ts(r.StartedAt)}
}

// InsertHost records a start of the host with the next sequence number of the profile. Call it inside a write
// transaction: two concurrent starts of one profile collide on the key and the second fails, which is the
// single-host rule.
func InsertHost(ctx context.Context, x DBTX, key []byte, profileID, generation string, startedAt time.Time) (HostRow, error) {
	if profileID == "" || generation == "" || startedAt.IsZero() {
		return HostRow{}, errors.New("procbind: a host row needs a profile, a generation and a time")
	}
	var maxSeq int64
	if err := x.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM procbind_host WHERE profile_id = ?`, profileID).Scan(&maxSeq); err != nil {
		return HostRow{}, err
	}
	stored, err := parseTS(ts(startedAt)) // the stored precision, so the row returned equals the row read back
	if err != nil {
		return HostRow{}, err
	}
	r := HostRow{ProfileID: profileID, Seq: maxSeq + 1, Generation: generation, StartedAt: stored}
	mac, err := rowMAC(key, "host", r.fields()...)
	if err != nil {
		return HostRow{}, err
	}
	if _, err := x.ExecContext(ctx, `INSERT INTO procbind_host (profile_id, seq, generation, started_at, mac) VALUES (?, ?, ?, ?, ?)`,
		r.ProfileID, r.Seq, r.Generation, ts(r.StartedAt), mac); err != nil {
		return HostRow{}, err
	}
	return r, nil
}

// CurrentHost is the newest host row of the profile. If it does not carry a genuine signature it is refused: an
// older genuine row is never used in its place.
func CurrentHost(ctx context.Context, q DBTX, key []byte, profileID string) (HostRow, error) {
	var r HostRow
	var started, mac string
	err := q.QueryRowContext(ctx, `SELECT profile_id, seq, generation, started_at, mac FROM procbind_host WHERE profile_id = ? ORDER BY seq DESC LIMIT 1`, profileID).
		Scan(&r.ProfileID, &r.Seq, &r.Generation, &started, &mac)
	if errors.Is(err, sql.ErrNoRows) {
		return HostRow{}, ErrRowNotFound
	}
	if err != nil {
		return HostRow{}, err
	}
	if r.StartedAt, err = parseTS(started); err != nil {
		return HostRow{}, ErrRowTampered
	}
	if err := verifyMAC(key, mac, "host", r.fields()...); err != nil {
		return HostRow{}, err
	}
	return r, nil
}

// ---- revocation

// checkSelector refuses a selector that is too broad or mixes the two token families. A profile is required,
// with one exception: a bare owner (the legacy RevokeOwner is profile-less), which selects only mission
// tokens of that owner and its runs ("owner/...").
func checkSelector(s Selector) error {
	if s.TokenID == "" && s.Owner == "" && s.Session == "" && !s.All {
		return fmt.Errorf("%w: it selects by nothing", ErrBadSelector)
	}
	if s.Kind != "" && s.Kind != KindMission && s.Kind != KindSession {
		return fmt.Errorf("%w: unknown kind %q", ErrBadSelector, s.Kind)
	}
	if !s.Generation.IsZero() {
		return fmt.Errorf("%w: a generation names a registry entry, not a stored row", ErrBadSelector)
	}
	if (s.Session != "" || s.Turn != "") && s.Kind == KindMission {
		return fmt.Errorf("%w: a session or a turn selects session tokens, not mission tokens", ErrBadSelector)
	}
	if s.Turn != "" && s.Session == "" {
		return fmt.Errorf("%w: a turn needs its session", ErrBadSelector)
	}
	if s.Owner != "" && (s.Session != "" || s.Turn != "" || s.Kind == KindSession) {
		return fmt.Errorf("%w: an owner selects mission tokens only", ErrBadSelector)
	}
	if s.Profile == "" {
		if s.Owner == "" || s.TokenID != "" || s.All || s.Session != "" || s.Turn != "" || (s.Kind != "" && s.Kind != KindMission) {
			return fmt.Errorf("%w: without a profile only an owner may be selected", ErrBadSelector)
		}
	}
	return nil
}

type target struct {
	kind    Kind
	id      string
	profile string
}

func collect(ctx context.Context, q DBTX, kind Kind, query string, args ...any) ([]target, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []target
	for rows.Next() {
		t := target{kind: kind}
		if err := rows.Scan(&t.id, &t.profile); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeTx writes a revocation row for every unrevoked v2 token the selector matches, using the caller's
// transaction (or database), and returns the tokens it revoked. It touches no legacy table. The caller commits
// it with the legacy change it belongs to, so both happen or neither does: a failure here is returned and the
// caller rolls its own change back (decision K1).
//
// keyFor gives the row key of a profile; it is called only for a profile that has something to revoke, and once
// for each, before any row is written. It must not write to this database or wait on this transaction's connection.
// The caller uses a write transaction that takes the write lock at its start (BEGIN IMMEDIATE): the revocation is
// then serialised with every other writer, a second revoke of the same token finds the first one's row and skips
// it, and a deferred transaction cannot lose its snapshot between the read here and the insert. Outside a
// transaction a failure after the first row leaves the rows written so far. After the commit, Drop ends the
// live registry entries of the returned refs.
func RevokeTx(ctx context.Context, x DBTX, keyFor func(profile string) ([]byte, error), sel Selector, reason string, now time.Time) ([]TokenRef, error) {
	if err := checkSelector(sel); err != nil {
		return nil, err
	}
	if keyFor == nil {
		return nil, errors.New("procbind: a revocation needs a key function")
	}
	var targets []target
	if sel.Session == "" && sel.Turn == "" && (sel.Kind == "" || sel.Kind == KindMission) {
		found, err := collect(ctx, x, KindMission, `SELECT id, profile_id FROM agent_tokens_v2 t
			WHERE (?1 = '' OR profile_id = ?1) AND (?2 = '' OR id = ?2)
			  AND (?3 = '' OR mission_owner = ?3 OR substr(mission_owner, 1, length(?3) + 1) = ?3 || '/')
			  AND NOT EXISTS (SELECT 1 FROM token_revocations_v2 r WHERE r.token_kind = 'mission' AND r.token_id = t.id)
			ORDER BY id`,
			sel.Profile, sel.TokenID, sel.Owner)
		if err != nil {
			return nil, err
		}
		targets = append(targets, found...)
	}
	if sel.Owner == "" && (sel.Kind == "" || sel.Kind == KindSession) {
		found, err := collect(ctx, x, KindSession, `SELECT id, profile_id FROM coder_session_tokens_v2 t
			WHERE (?1 = '' OR profile_id = ?1) AND (?2 = '' OR id = ?2) AND (?3 = '' OR conversation_id = ?3) AND (?4 = '' OR turn_id = ?4)
			  AND NOT EXISTS (SELECT 1 FROM token_revocations_v2 r WHERE r.token_kind = 'session' AND r.token_id = t.id)
			ORDER BY id`,
			sel.Profile, sel.TokenID, sel.Session, sel.Turn)
		if err != nil {
			return nil, err
		}
		targets = append(targets, found...)
	}
	// every profile's key is resolved before the first row is written, so a key that cannot be had fails the call
	// with nothing written (outside a transaction too)
	keys := map[string][]byte{}
	for _, t := range targets {
		if _, ok := keys[t.profile]; ok {
			continue
		}
		key, err := keyFor(t.profile)
		if err != nil {
			return nil, fmt.Errorf("procbind: the row key of profile %q: %w", t.profile, err)
		}
		if len(key) < MinKeyLen {
			return nil, fmt.Errorf("procbind: the row key of profile %q is too short", t.profile)
		}
		keys[t.profile] = key
	}
	var refs []TokenRef
	for _, t := range targets {
		if err := InsertRevocation(ctx, x, keys[t.profile], RevocationRow{Kind: t.kind, TokenID: t.id, ProfileID: t.profile, Reason: reason, CreatedAt: now}); err != nil {
			return nil, err
		}
		refs = append(refs, TokenRef{Kind: t.kind, TokenID: t.id, Profile: t.profile})
	}
	return refs, nil
}

// Drop ends the live registry entries of tokens whose revocation has been committed. It returns how many
// entries it revoked; a nil binder or no refs do nothing. A token that is revoked in the database already
// denies at the next verification (the verifier reads the revocation row), so a failure to reach the binder
// only delays the release of its handle.
func Drop(b *Binder, refs []TokenRef) int {
	if b == nil {
		return 0
	}
	n := 0
	for _, r := range refs {
		n += len(b.Revoke(Selector{Profile: r.Profile, Kind: r.Kind, TokenID: r.TokenID}))
	}
	return n
}
