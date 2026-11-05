// Package orgproposal is the store of the proposals a chat or an agent makes to change an org's grants or
// autonomy, and that only the user confirms (#1477, migration 161).
//
// A proposal is inert data: it carries typed arguments, the text the user is shown, and a digest of the live
// state it was made against. Nothing here applies anything or decides who may confirm; the CLI's confirm verb asks
// the gate and runs the real command. What this package guarantees is the row's side of that contract: a single
// use (an atomic claim), an expiry, a hash that binds what was shown to what runs, and refusals that tell the
// cases apart (not found, expired, already decided, hash mismatch, stale).
package orgproposal

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"
)

const (
	StatusPending  = "pending"
	StatusApplying = "applying" // claimed by a confirm; a crash here is never retried
	StatusApplied  = "applied"
	StatusFailed   = "failed"
	StatusDeclined = "declined"
	StatusStale    = "stale"
	StatusExpired  = "expired"

	VerbGrantAdd    = "grant.add"
	VerbAutonomySet = "autonomy.set"

	// TTL is how long a proposal can be confirmed.
	TTL = 30 * time.Minute
	// MaxPendingPerConversation caps what one conversation label can have waiting for one org; MaxPendingPerOrg is the
	// backstop over all labels of the org, so inventing labels cannot grow the table without bound.
	MaxPendingPerConversation = 5
	MaxPendingPerOrg          = 50
	// Retention is how long a decided row is kept as the audit trail.
	Retention = 30 * 24 * time.Hour
)

var (
	ErrNotFound     = errors.New("proposal_not_found")
	ErrTooMany      = errors.New("proposal_too_many: the org already has the most pending proposals allowed; decline one or wait for one to expire")
	ErrExpired      = errors.New("proposal_expired")
	ErrAlreadyDone  = errors.New("proposal_already_decided")
	ErrHashMismatch = errors.New("proposal_hash_mismatch")
	ErrStale        = errors.New("proposal_stale")
)

// Proposal is one org_proposals row. Status is the effective one: a pending row past its expiry reads as expired.
type Proposal struct {
	// ConversationID is a LABEL the proposer chose, never ownership: nothing may treat it as proof that a conversation
	// made or may see the proposal. A lookup trusts the stored row and its hash, not this id.
	ID, ProfileID, Org, ConversationID, Verb, Target string
	ArgsJSON, PreviewText, StateDigest, Hash         string
	CreatedBy, Status, ResultJSON                    string
	CreatedAt, ExpiresAt                             time.Time
	DecidedAt                                        *time.Time
}

// Input is what a proposal is made from. The id, the hash and the expiry are the store's.
type Input struct {
	ProfileID, Org, ConversationID, Verb, Target  string
	ArgsJSON, PreviewText, StateDigest, CreatedBy string
}

// Store reads and writes org_proposals.
type Store struct {
	DB  *sql.DB
	Now func() time.Time
	// afterGet runs in Claim between the checks and the claiming UPDATE; a test uses it to rewrite the row in that window.
	afterGet func()
}

func NewStore(db *sql.DB) *Store { return &Store{DB: db, Now: time.Now} }

const tsLayout = "2006-01-02T15:04:05Z"

func ts(t time.Time) string { return t.UTC().Format(tsLayout) }

// MaxLabelBytes bounds the labels a proposer names (conversation, created_by).
const MaxLabelBytes = 128

// cleanLabel drops control characters and cuts the label to MaxLabelBytes without splitting a character.
func cleanLabel(v string) string {
	out := make([]rune, 0, len(v))
	n := 0
	for _, r := range v {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			continue
		}
		if n += utf8.RuneLen(r); n > MaxLabelBytes {
			break
		}
		out = append(out, r)
	}
	return string(out)
}

// ComputeHash binds everything the user is shown and everything that runs: each field is length-prefixed, so a value
// cannot move from one field into the next.
func ComputeHash(id, profileID, org, verb, argsJSON, previewText, stateDigest string) string {
	h := sha256.New()
	h.Write([]byte("orgproposal/v1;"))
	for _, f := range []string{id, profileID, org, verb, argsJSON, previewText, stateDigest} {
		h.Write([]byte(strconv.Itoa(len(f)) + ":"))
		h.Write([]byte(f))
		h.Write([]byte(";"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// RecomputedHash is the hash of the row's content as it is now, not the stored column.
func (p *Proposal) RecomputedHash() string {
	return ComputeHash(p.ID, p.ProfileID, p.Org, p.Verb, p.ArgsJSON, p.PreviewText, p.StateDigest)
}

// Create stores a pending proposal. In one transaction it expires what is past its time, drops decided rows past
// their retention, supersedes the older pending proposal of the same conversation label for the same target, and inserts the new row only while
// the conversation label has fewer than MaxPendingPerConversation pending ones and the org fewer than MaxPendingPerOrg (the count is part of the INSERT, so concurrent creates
// cannot pass the cap).
func (s *Store) Create(ctx context.Context, in Input) (*Proposal, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, err
	}
	now := s.Now().UTC()
	in.ConversationID, in.CreatedBy = cleanLabel(in.ConversationID), cleanLabel(in.CreatedBy)
	p := &Proposal{ID: hex.EncodeToString(raw[:]), ProfileID: in.ProfileID, Org: in.Org, ConversationID: in.ConversationID,
		Verb: in.Verb, Target: in.Target, ArgsJSON: in.ArgsJSON, PreviewText: in.PreviewText, StateDigest: in.StateDigest,
		CreatedBy: in.CreatedBy, Status: StatusPending, CreatedAt: now, ExpiresAt: now.Add(TTL)}
	p.Hash = p.RecomputedHash()

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	n, cutoff := ts(now), ts(now.Add(-Retention))
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE org_proposals SET status = 'expired', decided_at = expires_at WHERE profile_id = ? AND status = 'pending' AND expires_at <= ?`, []any{in.ProfileID, n}},
		{`DELETE FROM org_proposals WHERE profile_id = ? AND status <> 'pending' AND COALESCE(decided_at, created_at) < ?`, []any{in.ProfileID, cutoff}},
		{`UPDATE org_proposals SET status = 'declined', result_json = '{"reason":"superseded"}', decided_at = ?
		   WHERE profile_id = ? AND org_name = ? AND conversation_id = ? AND verb = ? AND target = ? AND status = 'pending'`, []any{n, in.ProfileID, in.Org, in.ConversationID, in.Verb, in.Target}},
	} {
		if _, err := tx.ExecContext(ctx, q.sql, q.args...); err != nil {
			return nil, err
		}
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO org_proposals (id, profile_id, org_name, conversation_id, verb, target, args_json, preview_text,
		state_digest, hash, created_by, created_at, expires_at, status)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending'
		WHERE (SELECT COUNT(*) FROM org_proposals WHERE profile_id = ? AND org_name = ? AND conversation_id = ? AND status = 'pending') < ?
		  AND (SELECT COUNT(*) FROM org_proposals WHERE profile_id = ? AND org_name = ? AND status = 'pending') < ?`,
		p.ID, p.ProfileID, p.Org, p.ConversationID, p.Verb, p.Target, p.ArgsJSON, p.PreviewText, p.StateDigest, p.Hash,
		p.CreatedBy, ts(p.CreatedAt), ts(p.ExpiresAt), p.ProfileID, p.Org, p.ConversationID, MaxPendingPerConversation, p.ProfileID, p.Org, MaxPendingPerOrg)
	if err != nil {
		return nil, err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return nil, ErrTooMany // nothing above is committed: a refused create supersedes nothing
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return p, nil
}

const cols = `id, profile_id, org_name, conversation_id, verb, target, args_json, preview_text, state_digest, hash, created_by,
	created_at, expires_at, status, result_json, decided_at`

type scanner interface{ Scan(...any) error }

func (s *Store) scan(r scanner) (*Proposal, error) {
	var p Proposal
	var created, expires string
	var decided sql.NullString
	if err := r.Scan(&p.ID, &p.ProfileID, &p.Org, &p.ConversationID, &p.Verb, &p.Target, &p.ArgsJSON, &p.PreviewText, &p.StateDigest,
		&p.Hash, &p.CreatedBy, &created, &expires, &p.Status, &p.ResultJSON, &decided); err != nil {
		return nil, err
	}
	var err error
	if p.CreatedAt, err = time.Parse(tsLayout, created); err != nil {
		return nil, err
	}
	if p.ExpiresAt, err = time.Parse(tsLayout, expires); err != nil {
		return nil, err
	}
	if decided.Valid {
		if t, err := time.Parse(tsLayout, decided.String); err == nil {
			p.DecidedAt = &t
		}
	}
	if p.Status == StatusPending && !p.ExpiresAt.After(s.Now().UTC()) {
		p.Status = StatusExpired
	}
	return &p, nil
}

// Get returns the proposal of the profile, or ErrNotFound (another profile's id is not found).
func (s *Store) Get(ctx context.Context, profileID, id string) (*Proposal, error) {
	p, err := s.scan(s.DB.QueryRowContext(ctx, `SELECT `+cols+` FROM org_proposals WHERE profile_id = ? AND id = ?`, profileID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// List returns the profile's proposals, newest first; org and conversation narrow it when not empty.
func (s *Store) List(ctx context.Context, profileID, org, conversation string) ([]Proposal, error) {
	q, args := `SELECT `+cols+` FROM org_proposals WHERE profile_id = ?`, []any{profileID}
	if org != "" {
		q, args = q+` AND org_name = ?`, append(args, org)
	}
	if conversation != "" {
		q, args = q+` AND conversation_id = ?`, append(args, conversation)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY created_at DESC, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Proposal{}
	for rows.Next() {
		p, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// notPending is the refusal for a proposal that is not pending: expired (made permanent here) or decided.
func (s *Store) notPending(ctx context.Context, p *Proposal) error {
	if p.Status == StatusExpired {
		_, _ = s.DB.ExecContext(ctx, `UPDATE org_proposals SET status = 'expired', decided_at = expires_at WHERE id = ? AND status = 'pending' AND expires_at <= ?`, p.ID, ts(s.Now()))
		return ErrExpired
	}
	return fmt.Errorf("%w (%s)", ErrAlreadyDone, p.Status)
}

// Claim is the single use: it moves a pending, unexpired proposal to applying and returns it, or refuses with the
// reason. The refusals, in the order they are checked: not found; expired; already decided (a replay, a
// concurrent loser, a declined, stale or superseded row); hash mismatch (the hash given is not the hash of the row's
// content now, or the stored hash column is not either: the row was rewritten); stale (liveDigest, the digest of the
// org's state now computed by the caller, is not the one the proposal was made against). The last step is one UPDATE
// that repeats the state and the content it was judged on, so of any number of concurrent claims one wins.
func (s *Store) Claim(ctx context.Context, profileID, id, hash, liveDigest string) (*Proposal, error) {
	p, err := s.Get(ctx, profileID, id)
	if err != nil {
		return nil, err
	}
	if p.Status != StatusPending {
		return nil, s.notPending(ctx, p)
	}
	want := p.RecomputedHash()
	if subtle.ConstantTimeCompare([]byte(hash), []byte(want)) != 1 || subtle.ConstantTimeCompare([]byte(p.Hash), []byte(want)) != 1 {
		return nil, ErrHashMismatch
	}
	if s.afterGet != nil {
		s.afterGet()
	}
	now := ts(s.Now())
	if subtle.ConstantTimeCompare([]byte(liveDigest), []byte(p.StateDigest)) != 1 {
		_, _ = s.DB.ExecContext(ctx, `UPDATE org_proposals SET status = 'stale', decided_at = ? WHERE profile_id = ? AND id = ? AND status = 'pending'`, now, profileID, id)
		return nil, ErrStale
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE org_proposals SET status = 'applying', decided_at = ?
		WHERE profile_id = ? AND id = ? AND status = 'pending' AND expires_at > ? AND hash = ? AND args_json = ? AND preview_text = ? AND state_digest = ?`,
		now, profileID, id, now, p.Hash, p.ArgsJSON, p.PreviewText, p.StateDigest)
	if err != nil {
		return nil, err
	}
	if c, _ := res.RowsAffected(); c != 1 {
		again, err := s.Get(ctx, profileID, id)
		if err != nil {
			return nil, err
		}
		if again.Status != StatusPending {
			return nil, s.notPending(ctx, again)
		}
		return nil, ErrHashMismatch // rewritten between the check and the claim
	}
	p.Status = StatusApplying
	return p, nil
}

// Finish ends a claimed proposal: applied, or failed with the error as the result. Only an applying row ends.
func (s *Store) Finish(ctx context.Context, profileID, id, status, resultJSON string) error {
	if status != StatusApplied && status != StatusFailed {
		return fmt.Errorf("orgproposal: a claimed proposal ends applied or failed, not %q", status)
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE org_proposals SET status = ?, result_json = ?, decided_at = ? WHERE profile_id = ? AND id = ? AND status = 'applying'`,
		status, resultJSON, ts(s.Now()), profileID, id)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c != 1 {
		return fmt.Errorf("orgproposal: proposal %s is not being applied", id)
	}
	return nil
}

// Decline ends a pending proposal without applying it.
func (s *Store) Decline(ctx context.Context, profileID, id string) error {
	p, err := s.Get(ctx, profileID, id)
	if err != nil {
		return err
	}
	if p.Status != StatusPending {
		return s.notPending(ctx, p)
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE org_proposals SET status = 'declined', decided_at = ? WHERE profile_id = ? AND id = ? AND status = 'pending' AND expires_at > ?`,
		ts(s.Now()), profileID, id, ts(s.Now()))
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c != 1 {
		again, err := s.Get(ctx, profileID, id)
		if err != nil {
			return err
		}
		return s.notPending(ctx, again)
	}
	return nil
}
