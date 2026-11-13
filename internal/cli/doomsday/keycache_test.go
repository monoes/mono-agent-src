package doomsday

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// A MAC derives the vault key once, not once per call: the witness checks every record of a generation on each
// admission, and each derivation reads the OS keyring (about 25 ms on macOS), which made a turn start outlast the
// bound child's wait for its credentials.
func TestVaultKeyerDerivesTheKeyOnceAndNeverKeepsAFailure(t *testing.T) {
	calls := 0
	fail := false
	old := deriveKey
	deriveKey = func(context.Context, *sql.DB, string, string) ([]byte, error) {
		calls++
		if fail {
			return nil, errors.New("no key")
		}
		return []byte("0123456789abcdef0123456789abcdef"), nil
	}
	defer func() { deriveKey = old }()
	keyCacheMu.Lock()
	keyCache = map[string]cachedKey{}
	keyCacheMu.Unlock()

	k := VaultKeyer{Profile: "cache-test"}
	var first []byte
	for i := 0; i < 5; i++ {
		m, err := k.MAC("audit", []byte("x"))
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = m
		} else if string(m) != string(first) {
			t.Fatal("the same input gave different MACs")
		}
	}
	if calls != 1 {
		t.Fatalf("derived %d times for 5 MACs, want 1", calls)
	}
	other := VaultKeyer{Profile: "cache-test-2"}
	fail = true
	if _, err := other.MAC("audit", []byte("x")); err == nil {
		t.Fatal("a profile without a key must fail closed")
	}
	fail = false
	if _, err := other.MAC("audit", []byte("x")); err != nil || calls != 3 {
		t.Fatalf("a failure was kept: err=%v calls=%d", err, calls)
	}
}
