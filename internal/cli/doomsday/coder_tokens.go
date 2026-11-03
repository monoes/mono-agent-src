package doomsday

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
)

// CoderHost is the part of the coder host's store Stop uses; *coderhost.Store satisfies it. Nothing here edits the
// host: revocation and queue cancel are its existing exported calls.
type CoderHost interface {
	RevokeSessionTokensErr(profileID, conversationID string) error
	CancelPendingSends(profileID, conversationID string, beforeID int64, reason string) (int, error)
	CancelChildMasterSends(profileID, master, reason string) error
}

// CoderTokens is the real token revocation and queue cancel of the Stop order (stop.RunStore's Tokens). The
// sessions of a run are those stamped with its generation (migration 153) plus the registry members of kind
// "session" (member id = conversation id).
type CoderTokens struct {
	DB      *sql.DB
	Profile string
	Host    CoderHost
}

const cancelReason = "the open-ended run was stopped"

func (c CoderTokens) conversations(ctx context.Context, gen uint64) ([]string, error) {
	rows, err := c.DB.QueryContext(ctx, `SELECT conversation_id FROM coder_sessions WHERE profile_id = ? AND doomsday_generation = ?
UNION SELECT member_id FROM doomsday_members WHERE profile = ? AND generation = ? AND kind = 'session'`, c.Profile, int64(gen), c.Profile, int64(gen))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// RevokeTokens revokes the session tokens of every session of the generation. Every session is tried; a failure
// is returned after the loop, so one bad row never leaves the others' tokens alive.
func (c CoderTokens) RevokeTokens(ctx context.Context, gen uint64) error {
	convs, err := c.conversations(ctx, gen)
	if err != nil {
		return fmt.Errorf("the sessions of generation %d cannot be listed: %w", gen, err)
	}
	var errs []error
	for _, conv := range convs {
		if err := c.Host.RevokeSessionTokensErr(c.Profile, conv); err != nil {
			errs = append(errs, fmt.Errorf("session %s: %w", conv, err))
		}
	}
	return errors.Join(errs...)
}

// CancelQueue fails the pending sends of every session of the generation, and the pending sends those sessions
// issued as masters.
func (c CoderTokens) CancelQueue(ctx context.Context, gen uint64) error {
	convs, err := c.conversations(ctx, gen)
	if err != nil {
		return fmt.Errorf("the sessions of generation %d cannot be listed: %w", gen, err)
	}
	var errs []error
	for _, conv := range convs {
		if _, err := c.Host.CancelPendingSends(c.Profile, conv, math.MaxInt64, cancelReason); err != nil {
			errs = append(errs, fmt.Errorf("session %s: %w", conv, err))
		}
		if err := c.Host.CancelChildMasterSends(c.Profile, conv, cancelReason); err != nil {
			errs = append(errs, fmt.Errorf("session %s: %w", conv, err))
		}
	}
	return errors.Join(errs...)
}
