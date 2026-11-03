package stop

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// VaultKeyer derives the MAC keys of the store and the witness from the profile's vault key. A failure to
// obtain the key makes every MAC (and so every verify) fail closed. Domain separation: each domain gets its own
// subkey, and each part is length-prefixed so a boundary cannot be shifted.
type VaultKeyer struct {
	// Key returns the vault key bytes; wiring it to the real vault is the caller's job.
	Key func() ([]byte, error)
}

var _ store.Keyer = VaultKeyer{}

// ErrNoVaultKey is returned when the vault key cannot be obtained.
var ErrNoVaultKey = errors.New("stop: the vault key is unavailable, so nothing can be authenticated")

func (k VaultKeyer) MAC(domain string, parts ...[]byte) ([]byte, error) {
	if k.Key == nil {
		return nil, ErrNoVaultKey
	}
	key, err := k.Key()
	if err != nil || len(key) == 0 {
		return nil, fmt.Errorf("%w: %v", ErrNoVaultKey, err)
	}
	sub := hmac.New(sha256.New, key)
	sub.Write([]byte("monoagent-doomsday-key-v1\x00" + domain))
	h := hmac.New(sha256.New, sub.Sum(nil))
	for _, p := range parts {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(p)))
		h.Write(n[:])
		h.Write(p)
	}
	return h.Sum(nil), nil
}

// StoreRuns adapts the real store to the RunStore the Stop order uses. The store moves one state at a time and
// refuses quiescent while a live member is registered; Advance here walks the intermediate states.
type StoreRuns struct {
	Store   store.Store
	Profile string
	By      string
	// Tokens revokes the run's tokens and cancels queued commands (outside the store package).
	Tokens interface {
		RevokeTokens(ctx context.Context, gen uint64) error
		CancelQueue(ctx context.Context, gen uint64) error
	}
	// Runs lists the open runs; the store has no listing method (reported to the integrator).
	Runs func(ctx context.Context) ([]RunInfo, error)
}

var order = []RunState{StateRunning, StateStopRequested, StateAuthorityRevoked, StateCleanupPending, StateQuiescent}

func rank(s RunState) int {
	for i, o := range order {
		if o == s {
			return i
		}
	}
	return -1
}

func (s StoreRuns) State(ctx context.Context, gen uint64) (RunState, error) {
	r, err := s.Store.GetRun(ctx, s.Profile, int64(gen))
	return RunState(r.State), err
}

func (s StoreRuns) Advance(ctx context.Context, gen uint64, to RunState, cause string) error {
	for {
		cur, err := s.State(ctx, gen)
		if err != nil {
			return err
		}
		if rank(cur) < 0 || rank(to) < 0 {
			return store.ErrBadTransition
		}
		if rank(cur) >= rank(to) {
			return nil
		}
		if _, err := s.Store.Advance(ctx, s.Profile, int64(gen), store.RunState(order[rank(cur)+1]), s.By, cause); err != nil {
			return err
		}
	}
}

func (s StoreRuns) RevokeTokens(ctx context.Context, gen uint64) error {
	return s.Tokens.RevokeTokens(ctx, gen)
}
func (s StoreRuns) CancelQueue(ctx context.Context, gen uint64) error {
	return s.Tokens.CancelQueue(ctx, gen)
}

func (s StoreRuns) LiveMembers(ctx context.Context, gen uint64) ([]string, error) {
	ms, err := s.Store.LiveMembers(ctx, s.Profile, int64(gen))
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.Kind+":"+m.ID)
	}
	return ids, err
}

func (s StoreRuns) OpenRuns(ctx context.Context) ([]RunInfo, error) { return s.Runs(ctx) }
