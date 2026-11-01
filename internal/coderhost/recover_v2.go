package coderhost

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/monoes/mono-agent/internal/procbind"
	"github.com/monoes/mono-agent/internal/procchain"
)

// A turn launched with peer-credential binding (a v2 turn: its token row is in coder_session_tokens_v2, which is
// durable) is never adopted by a later host, whatever the setting is now: adopting one under the legacy path would
// resurrect it without the binding. On every host start the previous host's v2 turn is STOPPED, by its leader's own
// pidfd and nothing else (no process group is ever signalled), and its session settled only once the leader is
// confirmed gone. A check that cannot be completed (no descriptor, no permission, an unreadable stamp, a leader that
// will not exit) is not evidence of absence: the session is HELD, no new turn starts for it, and the check is retried
// at every pass, bounded only by the operator's attention (the hold is logged with its cause).

// LeaderStopper stops the leader of a turn (procbind.Stopper; tests substitute a fake).
type LeaderStopper interface {
	StopLeader(pid int, stamp string) procbind.StopResult
}

type heldSession struct {
	x     Session
	cause string
	// retry, when set, is how a hold for tokens that may be live ends (holdForTokens); a bound turn's hold has none and
	// is re-checked by recovery.
	retry func() error
	// fails and next pace the retries of a hold for tokens: each failure doubles the wait (holdBackoff, at most holdBackoffMax).
	fails int
	next  time.Time
}

// V2Turn says whether the session's turn is a v2 turn, and the leader identity its binding row recorded before the
// frame was delivered (pid and start stamp; zero when the row cannot be read, e.g. the profile has no data key, and the
// caller falls back to what the session recorded).
func (s *Store) V2Turn(profileID, conversationID, turnID string) (isV2 bool, pid int, stamp string, err error) {
	if turnID == "" {
		return false, 0, "", nil
	}
	var id string
	err = s.db.QueryRow(`SELECT id FROM coder_session_tokens_v2 WHERE profile_id = ? AND conversation_id = ? AND turn_id = ? ORDER BY created_at DESC LIMIT 1`,
		profileID, conversationID, turnID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, "", nil
	}
	if err != nil {
		return false, 0, "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key, kerr := procbind.RowKeyIfExists(ctx, s.db, profileID)
	if kerr != nil {
		return true, 0, "", nil
	}
	b, berr := procbind.GetBinding(ctx, s.db, key, procbind.KindSession, profileID, id)
	if berr != nil {
		return true, 0, "", nil
	}
	return true, b.WorkerPID, b.WorkerStamp, nil
}

func (h *Host) leaderStopper() LeaderStopper {
	if h.Stopper != nil {
		return h.Stopper
	}
	return procbind.Stopper{StampOf: BoundStamp, Same: procchain.SameStamp}
}

// holdBackoff is the first wait between retries of a hold for tokens that may be live; it doubles up to holdBackoffMax.
var (
	holdBackoff    = 2 * time.Second
	holdBackoffMax = time.Minute
)

// stopping is the cause shown for a session whose leader is being stopped right now (the host's lock is released for
// the stop, which can take two grace periods); it holds the chat and keeps the retry pass away from the session.
const stopping = "its turn is being stopped"

// recoverV2 stops a v2 turn's leader and settles the session if the leader is gone; it holds the session otherwise.
// Callers hold mu. mu is released while the leader is stopped (up to two grace periods per session), so the host keeps
// ticking for every other session; the session is held, with a cause that says why, for that time.
//
// The turn's tokens are revoked BEFORE the leader is stopped: a leader that survives the stop must have no usable
// credential.
func (h *Host) recoverV2(x Session, pid int, stamp string) {
	k := key(x.ProfileID, x.ConversationID)
	if pid == 0 { // the binding row could not be read: what the host saved of the turn, if it saved it
		pid, stamp = x.TurnPID, x.TurnStamp
	}
	hold := func(cause string) {
		if h.held == nil {
			h.held = map[string]heldSession{}
		}
		if prev, ok := h.held[k]; cause != stopping && (!ok || prev.cause != cause) {
			h.logf("coder host: the turn of %s is held: %s", x.ConversationID, cause)
		}
		h.held[k] = heldSession{x: x, cause: cause}
	}
	revokeErr := h.Store.RevokeSessionTokensErr(x.ProfileID, x.ConversationID)
	hold(stopping)
	h.mu.Unlock()
	r := h.leaderStopper().StopLeader(pid, stamp)
	h.mu.Lock()
	if r.Outcome == procbind.StopHeld {
		hold(r.Cause)
		return
	}
	if revokeErr != nil {
		hold("the turn's tokens could not be revoked")
		h.logf("coder host: the tokens of the turn of %s could not be revoked: %v", x.ConversationID, revokeErr)
		return
	}
	if x.Role == "child" { // the run is over: no task token of it outlives it, and a token that cannot be ended holds the session
		if err := h.revokeChildTokens(x.ProfileID, x.ConversationID); err != nil {
			hold("the child's task tokens could not be revoked")
			h.logf("coder host: the task tokens of the child %s could not be revoked: %v", x.ConversationID, err)
			return
		}
	}
	delete(h.held, k)
	if x.TurnID != "" {
		h.L.Finish(x.ProfileID, x.ConversationID, x.TurnID, "the coder host restarted and the bound turn was stopped")
	}
	h.settle(x)
}

// retryHeld re-checks every held session once. Callers hold mu.
func (h *Host) retryHeld() {
	snapshot := make(map[string]heldSession, len(h.held))
	for k, hs := range h.held { // recoverV2 releases mu while it stops a leader, so the map is not ranged over directly
		snapshot[k] = hs
	}
	for k, hs := range snapshot {
		if hs.cause == stopping {
			continue
		}
		if hs.retry != nil { // a hold for tokens that may be live: end them, then settle
			if time.Now().Before(hs.next) {
				continue
			}
			if err := hs.retry(); err != nil {
				hs.fails++
				wait := holdBackoff << min(hs.fails-1, 10)
				if wait > holdBackoffMax {
					wait = holdBackoffMax
				}
				hs.next = time.Now().Add(wait)
				h.held[k] = hs
				if hs.fails&(hs.fails-1) == 0 { // logged at the 1st, 2nd, 4th, 8th ... failure, not at every pass
					h.logf("coder host: the tokens of %s still cannot be revoked (%d attempts): %v", hs.x.ConversationID, hs.fails, err)
				}
				continue
			}
			delete(h.held, k)
			h.settle(hs.x)
			continue
		}
		x := hs.x
		if cur, ok, err := h.Store.GetSession(x.ProfileID, x.ConversationID); err == nil && ok {
			x = cur
		}
		isV2, pid, stamp, err := h.Store.V2Turn(x.ProfileID, x.ConversationID, x.TurnID)
		if err != nil {
			continue
		}
		if !isV2 { // the turn is no longer one (settled elsewhere): nothing to hold
			delete(h.held, k)
			continue
		}
		h.recoverV2(x, pid, stamp)
	}
}

// Held lists the held sessions and why, for `doctor` and the status of the host.
func (h *Host) Held() map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]string{}
	for k, hs := range h.held {
		out[k] = hs.cause
	}
	return out
}
