//go:build doomsday

package doomsday

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/store"
)

func TestCachedKeyerDerivesOncePerTTLAndAnswersLikeTheKeyer(t *testing.T) {
	now := time.Unix(1000, 0)
	derives := 0
	c := &CachedKeyer{TTL: time.Minute, Now: func() time.Time { return now }, Derive: func(_ context.Context) ([]byte, error) { derives++; return []byte("k"), nil }}
	want, _ := store.NewHMACKeyer([]byte("k")).MAC("d", []byte("a"), []byte("b"))
	for i := 0; i < 50; i++ {
		got, err := c.MAC("d", []byte("a"), []byte("b"))
		if err != nil || string(got) != string(want) {
			t.Fatalf("mac %d: %v", i, err)
		}
	}
	if derives != 1 {
		t.Fatalf("%d derivations for 50 MACs inside the TTL", derives)
	}
	now = now.Add(2 * time.Minute)
	if _, err := c.MAC("d", []byte("a")); err != nil || derives != 2 {
		t.Fatalf("after the TTL: %d derivations (%v)", derives, err)
	}
}

func TestCachedKeyerNeverCachesAFailure(t *testing.T) {
	fail := true
	derives := 0
	c := &CachedKeyer{TTL: time.Minute, Now: time.Now, Derive: func(_ context.Context) ([]byte, error) {
		derives++
		if fail {
			return nil, errors.New("keychain locked")
		}
		return []byte("k"), nil
	}}
	if _, err := c.MAC("d", []byte("a")); err == nil {
		t.Fatal("a failed derivation answered")
	}
	fail = false
	if _, err := c.MAC("d", []byte("a")); err != nil || derives != 2 {
		t.Fatalf("the failure was cached: %v (%d derivations)", err, derives)
	}
}
