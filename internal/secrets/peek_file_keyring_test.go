package secrets

import (
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"testing"

	"github.com/zalando/go-keyring"
)

// A keychain that works and has no entry answers not-found (macOS does, even for a home with no
// keychain file). With the file keyring opted in the KEK lives in the file, so peekKEK must find it
// there: secret rebind and the lockless DEK path depend on it.
func TestPeekKEK_FileKeyringWhenKeychainSaysNotFound(t *testing.T) {
	resetKEKState(t)
	keyring.MockInit()
	testhome.SetHome(t, t.TempDir())
	t.Setenv(fileKeyringEnv, "1")
	captureFileKeyringWarns(t)
	stubFilePassphrase(t, "correct horse battery staple")

	db := newSecretsTestDB(t)
	if _, err := Add(context.Background(), db.DB, "default", "secret", "k", map[string]string{"secret": "v"}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	kek, found, err := peekKEK("default")
	if err != nil || !found || len(kek) != 32 {
		t.Fatalf("peekKEK = %d bytes, found=%v, err=%v; want the file KEK", len(kek), found, err)
	}
}
