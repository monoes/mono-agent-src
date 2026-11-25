package jevconf

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/secrets"
)

// asOperator clears every agent marker, so a test run inside an agent saves
// records as the operator's own terminal does.
func asOperator(t *testing.T) {
	t.Helper()
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
}

func specKind(t *testing.T, spec KindSpec) string {
	t.Helper()
	k := fmt.Sprintf("spec-%d", kindSeq.Add(1))
	t.Cleanup(RegisterKind(k, func(Provider, string) (jev.Backend, error) { return jevtest.NewFakeBackend(nil), nil }, spec))
	return k
}

// A kind with a default host: a record added with none gets it, is saved with
// it, and its key is bound to it.
func TestAKindsDefaultHostCompletesTheRecordAndBindsTheKey(t *testing.T) {
	asOperator(t)
	db := newDB(t)
	ctx := context.Background()
	kind := specKind(t, KindSpec{DefaultBaseURL: "https://vendor.example"})
	p := Provider{Name: "v", Kind: kind, SecretName: "decision-v"}
	if err := SaveProvider(ctx, db, p); err != nil {
		t.Fatal(err)
	}
	saved, ok, err := FindProvider(db, "v")
	if err != nil || !ok || saved.BaseURL != "https://vendor.example" {
		t.Fatalf("saved = %+v %v", saved, err)
	}
	if _, err := PutProviderKey(ctx, db, p, "fake-key-1", "n"); err != nil {
		t.Fatal(err)
	}
	id, found, err := machineEntry(ctx, db, "decision-v")
	if err != nil || !found {
		t.Fatalf("no entry: %v", err)
	}
	fields, _, err := secrets.DecryptFields(ctx, db, VaultScope, id)
	if err != nil || fields[originField] != "https://vendor.example" {
		t.Fatalf("bound to %q %v", fields[originField], err)
	}
}

// A kind without a default host still needs one.
func TestAKindWithoutADefaultHostNeedsOne(t *testing.T) {
	asOperator(t)
	kind := specKind(t, KindSpec{})
	err := SaveProvider(context.Background(), newDB(t), Provider{Name: "v", Kind: kind, SecretName: "decision-v"})
	if !errors.Is(err, ErrProviderURL) {
		t.Fatalf("err = %v", err)
	}
}

// A kind that needs a key is refused without one even on this computer, when
// saved and when a record written without one is used.
func TestAKindThatNeedsAKeyIsNeverKeyless(t *testing.T) {
	asOperator(t)
	db := newDB(t)
	ctx := context.Background()
	kind := specKind(t, KindSpec{NeedsKey: true})
	p := Provider{Name: "v", Kind: kind, BaseURL: "http://localhost:9"}
	if err := SaveProvider(ctx, db, p); !errors.Is(err, ErrKeyNeeded) {
		t.Fatalf("save = %v", err)
	}
	raw := fmt.Sprintf(`[{"name":"v","kind":%q,"base_url":"http://localhost:9"}]`, kind)
	if err := setSetting(db, providersKey, raw); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveProviderClient(ctx, db, "default", "v", "", ""); !errors.Is(err, ErrKeyNeeded) {
		t.Fatalf("use = %v", err)
	}
}

// A record stored with no base_url (written straight into the database) is
// not completed on use: its key goes to no host.
func TestAStoredRecordWithoutAHostIsNotCompletedOnUse(t *testing.T) {
	asOperator(t)
	db := newDB(t)
	kind := specKind(t, KindSpec{DefaultBaseURL: "https://vendor.example"})
	if err := setSetting(db, providersKey, fmt.Sprintf(`[{"name":"v","kind":%q,"secret_name":"decision-v"}]`, kind)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveProviderClient(context.Background(), db, "default", "v", "", ""); !errors.Is(err, ErrProviderURL) {
		t.Fatalf("use = %v", err)
	}
}
