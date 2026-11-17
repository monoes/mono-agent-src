package accountcli

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/account"
)

// A sign-in that cannot reach an OS key store says what to do about it, without
// turning the weaker file keyring on.
func TestSignInErrKeyStoreHint(t *testing.T) {
	t.Parallel()
	raw := fmt.Errorf("%w: %w", account.ErrKeyringUnavailable, errors.New("secrets: reading KEK from keychain: dbus: no secret service"))
	err := signInErr(raw)
	if !errors.Is(err, account.ErrKeyringUnavailable) {
		t.Fatalf("the sentinel must survive: %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"reading KEK from keychain", "MONOAGENT_ALLOW_FILE_KEYRING=1", "monoagentcli secret keyring", "weaker", "OS keychain"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}
}
