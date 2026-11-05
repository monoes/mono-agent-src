// Package orgproposal is the store of the proposals a chat or an agent makes to change an org's grants or
// autonomy, and that only the user confirms (#1477). STUB: the tests of the first commit run against it.
package orgproposal

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const (
	StatusPending  = "pending"
	StatusApplying = "applying"
	StatusApplied  = "applied"
	StatusFailed   = "failed"
	StatusDeclined = "declined"
	StatusStale    = "stale"
	StatusExpired  = "expired"

	VerbGrantAdd    = "grant.add"
	VerbAutonomySet = "autonomy.set"

	TTL              = 30 * time.Minute
	MaxPendingPerOrg = 5
	Retention        = 30 * 24 * time.Hour
)

var (
	ErrNotFound     = errors.New("orgproposal: not found")
	ErrTooMany      = errors.New("orgproposal: too many pending proposals for the org")
	ErrExpired      = errors.New("orgproposal: expired")
	ErrAlreadyDone  = errors.New("orgproposal: already decided")
	ErrHashMismatch = errors.New("orgproposal: hash does not match the stored proposal")
	ErrStale        = errors.New("orgproposal: the org changed since the proposal was made")
	errStub         = errors.New("orgproposal: not implemented")
)

// Proposal is one org_proposals row.
type Proposal struct {
	ID, ProfileID, Org, ConversationID, Verb, Target string
	ArgsJSON, PreviewText, StateDigest, Hash         string
	CreatedBy, Status, ResultJSON                    string
	CreatedAt, ExpiresAt                             time.Time
	DecidedAt                                        *time.Time
}

// Input is what a proposal is made from.
type Input struct {
	ProfileID, Org, ConversationID, Verb, Target  string
	ArgsJSON, PreviewText, StateDigest, CreatedBy string
}

// Store reads and writes org_proposals.
type Store struct {
	DB  *sql.DB
	Now func() time.Time
}

func NewStore(db *sql.DB) *Store { return &Store{DB: db, Now: time.Now} }

func ComputeHash(id, profileID, org, verb, argsJSON, previewText, stateDigest string) string {
	return ""
}

func (p *Proposal) RecomputedHash() string { return "" }

func (s *Store) Create(ctx context.Context, in Input) (*Proposal, error) { return nil, errStub }
func (s *Store) Get(ctx context.Context, profileID, id string) (*Proposal, error) {
	return nil, errStub
}
func (s *Store) List(ctx context.Context, profileID, org, conversation string) ([]Proposal, error) {
	return nil, errStub
}
func (s *Store) Claim(ctx context.Context, profileID, id, hash, liveDigest string) (*Proposal, error) {
	return nil, errStub
}
func (s *Store) Finish(ctx context.Context, profileID, id, status, resultJSON string) error {
	return errStub
}
func (s *Store) Decline(ctx context.Context, profileID, id string) error { return errStub }
