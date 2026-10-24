// Package agenttokens issues and verifies the scoped bearer tokens a sandboxed coding
// agent presents to the profile-scoped HTTP API (`monoagentcli agent-mode token`).
//
// A token belongs to one profile, has a scope (what the API lets it call), may expire
// and can be revoked. Only its SHA-256 is stored for verification; the audit table
// holds the token's name, never the token.
package agenttokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Prefix starts every token, so a secret scanner and a person can recognise one.
const Prefix = "mat-"

const (
	tokenBytes = 32
	prefixLen  = len(Prefix) + 4 // characters kept for display
	timeFmt    = "2006-01-02T15:04:05.000Z"
	idPrefix   = "atk_"
	// lastUsedEvery bounds how often last_used_at is written per token.
	lastUsedEvery = time.Minute
)

// Scope is what a token may do. Each scope includes the ones before it.
type Scope string

const (
	// ScopeRead: GET only (list, get, status).
	ScopeRead Scope = "read"
	// ScopeWrite: also mutations that are not sends (validate, tagging a document).
	ScopeWrite Scope = "write"
	// ScopeRun: also running and (de)activating workflows.
	ScopeRun Scope = "run"
	// ScopeFull: everything the profile's own bearer reaches (approvals, ...). Only
	// the profile's bearer has it; `token create` never issues it.
	ScopeFull Scope = "full"
)

// Issuable lists the scopes `token create` may issue, narrowest first.
var Issuable = []Scope{ScopeRead, ScopeWrite, ScopeRun}

func (s Scope) rank() int {
	switch s {
	case ScopeRead:
		return 1
	case ScopeWrite:
		return 2
	case ScopeRun:
		return 3
	case ScopeFull:
		return 4
	}
	return 0
}

// Valid reports whether s is one of the four scopes.
func (s Scope) Valid() bool { return s.rank() > 0 }

// Allows reports whether a holder of scope s may call something that needs `need`.
// An unknown scope allows nothing.
func (s Scope) Allows(need Scope) bool {
	return s.rank() > 0 && need.rank() > 0 && s.rank() >= need.rank()
}

// ParseIssuable parses a scope `token create` may issue.
func ParseIssuable(v string) (Scope, error) {
	for _, s := range Issuable {
		if string(s) == v {
			return s, nil
		}
	}
	return "", fmt.Errorf("unknown scope %q (want read, write or run)", v)
}

var (
	// ErrNotFound means no such token in the profile.
	ErrNotFound = errors.New("agent token not found")
	// ErrInvalid is every authentication failure: unknown, expired or revoked.
	ErrInvalid = errors.New("invalid agent token")
	// ErrNameTaken means an unrevoked token of the profile has the name.
	ErrNameTaken = errors.New("an active agent token with that name already exists in this profile")
	// ErrInvalidName means the name breaks the naming rule.
	ErrInvalidName = errors.New("token name must be 1-64 characters: letters, digits, '.', '_' or '-', starting with a letter or digit, and not holding mat-")
)

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func validName(n string) bool {
	return nameRE.MatchString(n) && !strings.Contains(strings.ToLower(n), Prefix)
}

// Token is a token's metadata. The token itself is never part of it.
type Token struct {
	ID         string     `json:"id"`
	ProfileID  string     `json:"profile_id"`
	Name       string     `json:"name"`
	Scope      Scope      `json:"scope"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

// Use is one audited request.
type Use struct {
	At        time.Time `json:"at"`
	TokenName string    `json:"token"`
	ProfileID string    `json:"profile_id"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Status    int       `json:"status"`
}

// Store reads and writes the agent_tokens and agent_token_audit tables.
type Store struct {
	db  *sql.DB
	now func() time.Time

	mu      sync.Mutex
	touched map[string]time.Time
	inserts atomic.Uint64
}

// NewStore returns a Store over a migrated database.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db, now: time.Now, touched: map[string]time.Time{}}
}

// SetClock replaces the clock; for tests.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// Hash is the stored form of a token: the hex SHA-256 of its text.
func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func generate() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating agent token: %w", err)
	}
	return Prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return idPrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))[:12], nil
}

// Create issues a token for the profile. ttl <= 0 means it never expires. The
// returned secret is the only time the token exists in the clear here.
func (s *Store) Create(ctx context.Context, profileID, name string, scope Scope, ttl time.Duration) (Token, string, error) {
	if profileID == "" {
		return Token{}, "", errors.New("agent token needs a profile")
	}
	if !validName(name) {
		return Token{}, "", ErrInvalidName
	}
	if _, err := ParseIssuable(string(scope)); err != nil {
		return Token{}, "", err
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
	var exp any
	if ttl > 0 {
		exp = now.Add(ttl).Format(timeFmt)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO agent_tokens (id, profile_id, name, scope, prefix, token_hash, created_at, expires_at) VALUES (?,?,?,?,?,?,?,?)`,
		id, profileID, name, string(scope), secret[:prefixLen], Hash(secret), now.Format(timeFmt), exp)
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: agent_tokens.profile_id, agent_tokens.name") {
		return Token{}, "", ErrNameTaken
	}
	if err != nil {
		return Token{}, "", fmt.Errorf("storing agent token: %w", err)
	}
	t, err := s.byID(ctx, id)
	return t, secret, err
}

const selectToken = `SELECT id, profile_id, name, scope, prefix, created_at, expires_at, last_used_at, revoked_at FROM agent_tokens`

type scanner interface{ Scan(dest ...any) error }

func parseT(v sql.NullString) *time.Time {
	if !v.Valid {
		return nil
	}
	t, err := time.Parse(timeFmt, v.String)
	if err != nil {
		return nil
	}
	return &t
}

func scanToken(row scanner) (Token, error) {
	var (
		t                       Token
		scope, created          string
		expires, lastUsed, revk sql.NullString
	)
	if err := row.Scan(&t.ID, &t.ProfileID, &t.Name, &scope, &t.Prefix, &created, &expires, &lastUsed, &revk); err != nil {
		return Token{}, err
	}
	t.Scope = Scope(scope)
	t.CreatedAt, _ = time.Parse(timeFmt, created)
	t.ExpiresAt, t.LastUsedAt, t.RevokedAt = parseT(expires), parseT(lastUsed), parseT(revk)
	return t, nil
}

func (s *Store) byID(ctx context.Context, id string) (Token, error) {
	t, err := scanToken(s.db.QueryRowContext(ctx, selectToken+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, ErrNotFound
	}
	return t, err
}

// Active reports whether the token may be used at now.
func (t Token) Active(now time.Time) bool {
	if t.RevokedAt != nil {
		return false
	}
	return t.ExpiresAt == nil || now.Before(*t.ExpiresAt)
}

// Verify finds the token presented as a bearer. Unknown, malformed, expired and
// revoked tokens are one error, so the caller cannot tell which.
func (s *Store) Verify(ctx context.Context, presented string) (Token, error) {
	hash := Hash(presented)
	var stored string
	row := s.db.QueryRowContext(ctx, `SELECT token_hash, id FROM agent_tokens WHERE token_hash = ?`, hash)
	var id string
	if err := row.Scan(&stored, &id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Token{}, ErrInvalid
		}
		return Token{}, fmt.Errorf("looking up agent token: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(stored), []byte(hash)) != 1 {
		return Token{}, ErrInvalid
	}
	t, err := s.byID(ctx, id)
	if err != nil {
		return Token{}, ErrInvalid
	}
	if !t.Active(s.now().UTC()) {
		return Token{}, ErrInvalid
	}
	return t, nil
}

// Get finds a token of the profile by id, or by the name of an unrevoked one.
func (s *Store) Get(ctx context.Context, profileID, ref string) (Token, error) {
	t, err := scanToken(s.db.QueryRowContext(ctx, selectToken+` WHERE profile_id = ? AND id = ?`, profileID, ref))
	if err == nil {
		return t, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Token{}, err
	}
	t, err = scanToken(s.db.QueryRowContext(ctx,
		selectToken+` WHERE profile_id = ? AND name = ? AND revoked_at IS NULL`, profileID, ref))
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, ErrNotFound
	}
	return t, err
}

// List returns the profile's tokens, oldest first; revoked ones only with includeRevoked.
func (s *Store) List(ctx context.Context, profileID string, includeRevoked bool) ([]Token, error) {
	q := selectToken + ` WHERE profile_id = ?`
	if !includeRevoked {
		q += ` AND revoked_at IS NULL`
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY created_at, id`, profileID)
	if err != nil {
		return nil, fmt.Errorf("listing agent tokens: %w", err)
	}
	defer rows.Close()
	out := []Token{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Revoke ends a token now. Revoking a revoked token leaves it as it was.
func (s *Store) Revoke(ctx context.Context, profileID, ref string) (Token, error) {
	t, err := s.Get(ctx, profileID, ref)
	if err != nil {
		return Token{}, err
	}
	if t.RevokedAt != nil {
		return t, nil
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE agent_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		s.now().UTC().Format(timeFmt), t.ID); err != nil {
		return Token{}, fmt.Errorf("revoking agent token: %w", err)
	}
	return s.byID(ctx, t.ID)
}

// RevokeProfile revokes every unrevoked token of the profile.
func (s *Store) RevokeProfile(ctx context.Context, profileID string) (int, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE agent_tokens SET revoked_at = ? WHERE profile_id = ? AND revoked_at IS NULL`,
		s.now().UTC().Format(timeFmt), profileID)
	if err != nil {
		return 0, fmt.Errorf("revoking agent tokens of the profile: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// The audit table is bounded by count, never by an id gap, so that one token's traffic cannot
// push another's history out:
//
//   - auditKeepPerToken is how many rows of served requests one token keeps (status other than
//     401 and 403, and a request still running);
//   - auditKeepRefusedPerToken is how many refused ones (401 and 403: a scope it lacked, another
//     profile's path) it keeps, in a set of their own that served requests cannot push out, so a
//     token cannot wash its earlier probing away with ordinary calls;
//   - auditKeep is the backstop for the whole table: over it, the oldest rows of the token
//     holding the most rows go first (served before refused).
var (
	auditKeep                = 50000
	auditKeepPerToken        = 2000
	auditKeepRefusedPerToken = 500
)

// refusedClause is the rows that are in a token's refused set.
const refusedClause = `status IN (401, 403)`

// maxAuditPath bounds the stored path.
const maxAuditPath = 200

// cleanPath escapes control characters and cuts the path to maxAuditPath bytes, so a
// crafted request path cannot put raw escape sequences in the log.
func cleanPath(p string) string {
	var b strings.Builder
	for _, r := range p {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			fmt.Fprintf(&b, "\\x%02x", r)
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if len(out) > maxAuditPath {
		out = strings.ToValidUTF8(out[:maxAuditPath], "")
	}
	return out
}

// BeginUse audits a request made with t BEFORE it runs, with status 0 until FinishUse
// fills it in; the caller refuses the request when this fails. It never takes the token
// itself. It also prunes the table: auditKeepPerToken rows per token, auditKeep overall.
func (s *Store) BeginUse(ctx context.Context, t Token, method, path string) (int64, error) {
	now := s.now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_token_audit (at, token_id, token_name, profile_id, method, path, status) VALUES (?,?,?,?,?,?,0)`,
		now.Format(timeFmt), t.ID, t.Name, t.ProfileID, method, cleanPath(path))
	if err != nil {
		return 0, fmt.Errorf("auditing agent token use: %w", err)
	}
	id, _ := res.LastInsertId()
	// The whole-table backstop is checked on every insert for a small table and every
	// auditKeep/400th for a large one: counting the table is not free.
	if s.inserts.Add(1)%uint64(max(1, auditKeep/400)) == 0 {
		s.enforceTotal(ctx)
	}
	s.mu.Lock()
	last, seen := s.touched[t.ID]
	due := !seen || now.Sub(last) >= lastUsedEvery
	if due {
		s.touched[t.ID] = now
	}
	s.mu.Unlock()
	if due {
		_, _ = s.db.ExecContext(ctx, `UPDATE agent_tokens SET last_used_at = ? WHERE id = ?`, now.Format(timeFmt), t.ID)
	}
	return id, nil
}

// FinishUse records the status the request ended with, and trims the set it now belongs to
// (served or refused) to its per-token cap.
func (s *Store) FinishUse(ctx context.Context, id int64, status int) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE agent_token_audit SET status = ? WHERE id = ?`, status, id); err != nil {
		return fmt.Errorf("auditing agent token use: %w", err)
	}
	class, keep := `NOT (`+refusedClause+`)`, auditKeepPerToken
	if status == 401 || status == 403 {
		class, keep = refusedClause, auditKeepRefusedPerToken
	}
	// Both statements walk the token's own rows through idx_agent_token_audit_token.
	_, _ = s.db.ExecContext(ctx,
		`DELETE FROM agent_token_audit WHERE token_id = (SELECT token_id FROM agent_token_audit WHERE id = ?) AND `+class+`
		   AND id NOT IN (SELECT id FROM agent_token_audit WHERE token_id = (SELECT token_id FROM agent_token_audit WHERE id = ?) AND `+class+` ORDER BY id DESC LIMIT ?)`,
		id, id, keep)
	return nil
}

// enforceTotal brings the table under auditKeep by deleting the oldest rows of the token that
// holds the most, served rows before refused ones. Best effort: a failure only leaves the
// table a little over until the next check.
func (s *Store) enforceTotal(ctx context.Context) {
	for i := 0; i < 8; i++ {
		var total int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_token_audit`).Scan(&total); err != nil || total <= auditKeep {
			return
		}
		var heaviest string
		if err := s.db.QueryRowContext(ctx,
			`SELECT token_id FROM agent_token_audit GROUP BY token_id ORDER BY COUNT(*) DESC LIMIT 1`).Scan(&heaviest); err != nil {
			return
		}
		res, err := s.db.ExecContext(ctx,
			`DELETE FROM agent_token_audit WHERE id IN (SELECT id FROM agent_token_audit WHERE token_id = ?
			   ORDER BY (`+refusedClause+`) ASC, id ASC LIMIT ?)`, heaviest, total-auditKeep)
		if err != nil {
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return
		}
	}
}

// Audit returns the most recent uses of the token (newest first), at most limit.
func (s *Store) Audit(ctx context.Context, t Token, limit int) ([]Use, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT at, token_name, profile_id, method, path, status FROM agent_token_audit WHERE token_id = ? ORDER BY id DESC LIMIT ?`,
		t.ID, limit)
	if err != nil {
		return nil, fmt.Errorf("reading audit: %w", err)
	}
	defer rows.Close()
	out := []Use{}
	for rows.Next() {
		var u Use
		var at string
		if err := rows.Scan(&at, &u.TokenName, &u.ProfileID, &u.Method, &u.Path, &u.Status); err != nil {
			return nil, err
		}
		u.At, _ = time.Parse(timeFmt, at)
		u.Path = cleanPath(u.Path)
		out = append(out, u)
	}
	return out, rows.Err()
}
