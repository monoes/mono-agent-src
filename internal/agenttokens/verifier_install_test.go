package agenttokens

import (
	"testing"

	"github.com/monoes/mono-agent/internal/procbind"
	"github.com/monoes/mono-agent/internal/testdb"
)

// A store holds the v2 verifier only when issuance is on: with it off (the constant) every v2 secret is denied.
func TestAStoreInstallsTheV2VerifierOnlyWhenIssuanceIsOn(t *testing.T) {
	db := testdb.Open(t).DB
	if s := NewStore(db); !procbind.IssuanceReady && s.v2 != nil {
		t.Error("a store holds a verifier although issuance is off")
	}
	procbind.ForTestSetIssuance(t, true)
	if s := NewStore(db); s.v2 == nil {
		t.Error("a store has no verifier although issuance is on")
	}
}
