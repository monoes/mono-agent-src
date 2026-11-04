//go:build doomsday

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
	defer c.mu.Unlock()
	if c.key != nil && now().Sub(c.at) < c.TTL {
		return c.key, nil
	}
	key, err := c.Derive(context.Background())
	if err != nil {
		c.key = nil
		return nil, err
	}
	c.key, c.at = store.NewHMACKeyer(key), now()
	return c.key, nil
}
