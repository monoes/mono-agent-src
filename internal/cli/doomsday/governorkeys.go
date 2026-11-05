package doomsday

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/secrets"
)

// CachedKeyer derives the queue's MAC key at most once per TTL. The vault keyer reads the key store on every MAC, which a
// pump that verifies a page of rows would do dozens of times; the key does not change within a run, so a short cache is the
// same answer. A derivation error is never cached.
type CachedKeyer struct {
	Derive func(ctx context.Context) ([]byte, error)
	TTL    time.Duration
	// Timeout bounds one derivation (default 5 s).
	Timeout time.Duration
	Now     func() time.Time

	mu  sync.Mutex
	key store.Keyer
	at  time.Time
}

// NewCachedVaultKeyer caches the vault keyer's derivation for a short while.
func NewCachedVaultKeyer(k VaultKeyer) *CachedKeyer {
	return &CachedKeyer{TTL: 30 * time.Second, Derive: func(ctx context.Context) ([]byte, error) {
		key, err := secrets.DeriveKeyIfExists(ctx, k.DB, k.Profile, "doomsday/mac")
		if err != nil {
			return nil, fmt.Errorf("%w: %v", store.ErrUnverifiable, err)
		}
		return key, nil
	}}
}

// MAC implements the Keyer.
func (c *CachedKeyer) MAC(domain string, parts ...[]byte) ([]byte, error) {
	key, err := c.keyer()
	if err != nil {
		return nil, err
	}
	return key.MAC(domain, parts...)
}

func (c *CachedKeyer) keyer() (store.Keyer, error) {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	c.mu.Lock()
	if c.key != nil && now().Sub(c.at) < c.TTL {
		k := c.key
		c.mu.Unlock()
		return k, nil
	}
	c.mu.Unlock()
	// The derivation (a key store read) runs outside the lock and is bounded: a wedged store fails each caller after the
	// timeout instead of queueing them all behind one.
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultDeriveTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	type result struct {
		key []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- result{err: fmt.Errorf("%w: the key derivation panicked", store.ErrUnverifiable)}
			}
		}()
		k, err := c.Derive(ctx)
		ch <- result{k, err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			return nil, res.err
		}
		k := store.NewHMACKeyer(res.key)
		c.mu.Lock()
		c.key, c.at = k, now()
		c.mu.Unlock()
		return k, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: the key derivation timed out", store.ErrUnverifiable)
	}
}

const defaultDeriveTimeout = 5 * time.Second
