package agenttokens

import (
	"context"
	"errors"

	"github.com/monoes/mono-agent/internal/procbind"
)

// A mission token with the v2 prefix is verified here and nowhere else: it is looked up only in agent_tokens_v2, needs
// a genuine row, a binding, no revocation and an authentic answer of the binding service, and never falls back to the
// legacy table. The verifier is dark until the host wiring sets it (SetV2Verifier): a Store without one denies every v2
// secret, so nothing accepts a v2 token while issuance is off.

// SetV2Verifier turns on the verification of v2 mission tokens. It is set by the code that wires the binding host, and
// only when issuance is on (procbind.IssuanceReady); a nil check leaves v2 secrets denied.
func (s *Store) SetV2Verifier(c *procbind.V2Check) { s.v2 = c }

// verifyMissionV2 is VerifyMission for a secret with procbind.TaskPrefixV2. Every refusal is ErrInvalid, except a
// binding service that gave no answer, which is ErrBindingServiceDown (also ErrInvalid) so the operator can tell.
func (s *Store) verifyMissionV2(ctx context.Context, profileID, presented string) (Token, error) {
	if s.v2 == nil {
		return Token{}, ErrInvalid
	}
	c := *s.v2
	c.DB = s.db
	if c.Now == nil {
		c.Now = s.now
	}
	res, err := c.Verify(ctx, procbind.KindMission, presented)
	if err != nil {
		if errors.Is(err, procbind.ErrServiceDown) {
			return Token{}, ErrBindingServiceDown
		}
		return Token{}, ErrInvalid
	}
	m := res.Mission
	if m.ProfileID != profileID {
		return Token{}, ErrInvalid
	}
	exp := m.ExpiresAt
	created := m.CreatedAt
	return Token{
		ID: m.ID, ProfileID: m.ProfileID, Name: "mission-v2-" + m.ID, Scope: ScopeRead, Prefix: presented[:prefixLen],
		CreatedAt: created, ExpiresAt: &exp,
		Mission: &MissionBinding{Session: m.Session, Task: m.Task, Role: m.Role, Actor: m.Actor, Owner: m.Owner},
	}, nil
}

// ErrBindingServiceDown is ErrInvalid for a v2 token the binding service gave no authentic answer about.
var ErrBindingServiceDown = errors.Join(ErrInvalid, procbind.ErrServiceDown)
