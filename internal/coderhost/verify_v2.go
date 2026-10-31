package coderhost

import (
	"context"
	"errors"
	"strings"

	"github.com/monoes/mono-agent/internal/procbind"
)

// A session token with the v2 prefix is verified here and nowhere else: it is looked up only in
// coder_session_tokens_v2, needs a genuine row, a binding, no revocation and an authentic answer of the binding
// service, and is the session's current turn; it never falls back to the legacy table. The path is dark until the host
// wiring sets a verifier (SetV2Verifier): a Store without one denies every v2 secret, so nothing accepts a v2 token
// while issuance is off.

// SetV2Verifier turns on the verification of v2 session tokens. It is set by the code that wires the binding host, and
// only when issuance is on (procbind.IssuanceReady); a nil check leaves v2 secrets denied.
func (s *Store) SetV2Verifier(c *procbind.V2Check) { s.v2 = c }

// ErrSessionServiceDown is ErrSessionToken for a v2 token the binding service gave no authentic answer about.
var ErrSessionServiceDown = errors.Join(ErrSessionToken, procbind.ErrServiceDown)

// sessionScopeV2 is the scope the mint writes into a session token's binding row. procbind.SessionScope replaces it
// when the launcher's 3c-3b step exports one.
func sessionScopeV2(conversationID, turnID string) string {
	return "session:" + conversationID + "/" + turnID
}

// verifySessionV2 is VerifySessionToken for a token with procbind.SessionPrefixV2. Every refusal is ErrSessionToken
// (no hint which check failed), except a silent binding service, which is ErrSessionServiceDown.
func (s *Store) verifySessionV2(tok string) (Caller, error) {
	if s.v2 == nil || !strings.HasPrefix(tok, procbind.SessionPrefixV2) {
		return Caller{}, ErrSessionToken
	}
	c := *s.v2
	c.DB = s.db
	if c.Now == nil {
		c.Now = s.now
	}
	res, err := c.Verify(context.Background(), procbind.KindSession, tok)
	if err != nil {
		if errors.Is(err, procbind.ErrServiceDown) {
			return Caller{}, ErrSessionServiceDown
		}
		return Caller{}, ErrSessionToken
	}
	row := res.Session
	if res.Binding.Scope != sessionScopeV2(row.ConversationID, row.TurnID) {
		return Caller{}, ErrSessionToken
	}
	// the token is for the session's current turn, as a legacy one is
	var one int
	if err := s.db.QueryRow(`SELECT 1 FROM coder_sessions WHERE profile_id = ? AND conversation_id = ? AND turn_id = ?`,
		row.ProfileID, row.ConversationID, row.TurnID).Scan(&one); err != nil {
		return Caller{}, ErrSessionToken
	}
	return Caller{ProfileID: row.ProfileID, ConversationID: row.ConversationID, TurnID: row.TurnID}, nil
}
