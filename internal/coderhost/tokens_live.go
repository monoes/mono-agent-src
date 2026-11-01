package coderhost

import (
	"errors"
)

// TokensLiveError marks a failure after which a token of a turn may still be live: the revoke at its end, or at its refusal,
// did not succeed. The host does not settle the session on it (no new turn starts next to a live token); it holds the session
// and retries the revocation at every pass until it succeeds.
type TokensLiveError struct{ Err error }

func (e *TokensLiveError) Error() string {
	if e.Err == nil {
		return "a token of the turn may still be live"
	}
	return e.Err.Error()
}

func (e *TokensLiveError) Unwrap() error { return e.Err }

// TokensMayBeLive reports that the failure leaves a token possibly live.
func (e *TokensLiveError) TokensMayBeLive() bool { return true }

// TokensMayBeLive says whether err (or anything it wraps) says a token of the turn may still be live.
func TokensMayBeLive(err error) bool {
	var t interface{ TokensMayBeLive() bool }
	return err != nil && errors.As(err, &t) && t.TokensMayBeLive()
}

// holdForTokens keeps the session unsettled, with retry as the way to end its tokens; retryHeld calls it at every pass and
// settles the session once it succeeds. Callers hold mu.
func (h *Host) holdForTokens(x Session, cause string, retry func() error) {
	k := key(x.ProfileID, x.ConversationID)
	if h.held == nil {
		h.held = map[string]heldSession{}
	}
	if prev, ok := h.held[k]; !ok || prev.cause != cause {
		h.logf("coder host: the turn of %s is held: %s", x.ConversationID, cause)
	}
	h.held[k] = heldSession{x: x, cause: cause, retry: retry}
}

// endTurnTokens is the retry of a hold for tokens: the turn's own tokens, and for a child its task tokens.
func (h *Host) endTurnTokens(x Session) func() error {
	return func() error {
		err := h.Store.RevokeTurnSessionTokensErr(x.ProfileID, x.ConversationID, x.TurnID)
		if x.Role == "child" {
			err = errors.Join(err, h.revokeChildTokens(x.ProfileID, x.ConversationID))
		}
		return err
	}
}
