package coderhost

import (
	"errors"
)

// TokensLiveError marks a failure after which a token of a turn may still be live: the revoke at its end, or at its refusal,
// did not succeed. The host does not settle the session on it (no new turn starts next to a live token); it holds the session
// and retries the revocation at every pass until it succeeds.
type TokensLiveError struct {
	Err error
	// Ended, when set, is closed once the turn this failure is about has finished, its exit cleanup included. While it is open the
	// old turn may still be running, so the host does not settle the session or start another turn, whatever the retry says.
	Ended <-chan struct{}
}

func (e *TokensLiveError) Error() string {
	if e.Err == nil {
		return "a token of the turn may still be live"
	}
	return e.Err.Error()
}

func (e *TokensLiveError) Unwrap() error { return e.Err }

// TokensMayBeLive reports that the failure leaves a token possibly live.
func (e *TokensLiveError) TokensMayBeLive() bool { return true }

// StartedError wraps a launch failure that came AFTER the turn's process was created: the turn started (and may have spent) even
// though the start failed. A failure without it means no process ever existed.
type StartedError struct{ Err error }

func (e *StartedError) Error() string { return e.Err.Error() }
func (e *StartedError) Unwrap() error { return e.Err }

// TurnStarted says whether err (or anything it wraps) is a failure after the turn's process was created.
func TurnStarted(err error) bool {
	var s *StartedError
	return err != nil && errors.As(err, &s)
}

// TokensMayBeLive says whether err (or anything it wraps) says a token of the turn may still be live.
func TokensMayBeLive(err error) bool {
	var t interface{ TokensMayBeLive() bool }
	return err != nil && errors.As(err, &t) && t.TokensMayBeLive()
}

// liveFailures lists every TokensLiveError anywhere in err's tree (joined errors included), not only the first one errors.As finds.
func liveFailures(err error) []*TokensLiveError {
	var out []*TokensLiveError
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		if t, ok := e.(*TokensLiveError); ok {
			out = append(out, t)
		}
		switch u := e.(type) {
		case interface{ Unwrap() []error }:
			for _, c := range u.Unwrap() {
				walk(c)
			}
		case interface{ Unwrap() error }:
			walk(u.Unwrap())
		}
	}
	walk(err)
	return out
}

// TurnsPending returns the channel of every turn that err names as possibly still running (its cleanup unfinished), in the order the
// failures appear; a failure without a channel does not hide one that has it. It starts no goroutine: the host looks at the channels.
func TurnsPending(err error) []<-chan struct{} {
	var chans []<-chan struct{}
	for _, f := range liveFailures(err) {
		if f.Ended != nil {
			chans = append(chans, f.Ended)
		}
	}
	return chans
}

// allEnded says whether every channel is closed.
func allEnded(chans []<-chan struct{}) bool {
	for _, c := range chans {
		select {
		case <-c:
		default:
			return false
		}
	}
	return true
}

// TurnPending returns ONE channel that closes when every pending turn has finished, or nil when err names none. It starts a
// goroutine when there are several (for a caller that has to block on them); the host uses TurnsPending instead.
func TurnPending(err error) <-chan struct{} {
	chans := TurnsPending(err)
	switch len(chans) {
	case 0:
		return nil
	case 1:
		return chans[0]
	}
	all := make(chan struct{})
	go func() {
		for _, c := range chans {
			<-c
		}
		close(all)
	}()
	return all
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
