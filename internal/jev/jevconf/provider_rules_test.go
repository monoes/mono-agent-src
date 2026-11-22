package jevconf

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/jev/jevtest/callers"
	"github.com/monoes/mono-agent/internal/secrets"
)

// senderKind registers a keySender kind and counts its builds.
func senderKind(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	var builds atomic.Int64
	kind := fmt.Sprintf("sender-%d", kindSeq.Add(1))
	t.Cleanup(RegisterKind(kind, func(p Provider, key string) (jev.Backend, error) {
		builds.Add(1)
		return keySender{p.BaseURL, key}, nil
	}))
	return kind, &builds
}

// tamper replaces jev.providers as a process writing the database directly would.
func tamper(t *testing.T, db *sql.DB, recs []Provider) {
	t.Helper()
	raw, err := json.Marshal(recs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE settings SET value = ? WHERE key = 'jev.providers'`, string(raw)); err != nil {
		t.Fatal(err)
	}
}

// S5: a keyless provider may only be on this machine. SaveProvider refuses
// one on another host, and a record injected into the settings table is
// refused when its client is built, for every caller, before any builder
// runs (so nothing is sent).
func TestAKeylessProviderIsOnlyOnThisMachine(t *testing.T) {
	keyring.MockInit()
	ctx := context.Background()
	for _, c := range callers.All() {
		t.Run(c.Name, func(t *testing.T) {
			db := newDB(t)
			kind, builds := senderKind(t)
			remote := Provider{Name: "free", Kind: kind, BaseURL: "https://exfil.invalid/v1"}
			if err := SaveProvider(ctx, db, remote); !errors.Is(err, ErrKeylessRemote) || !strings.Contains(err.Error(), "--key-stdin") {
				t.Fatalf("a keyless remote provider was saved: %v", err)
			}
			if err := SaveProvider(ctx, db, Provider{Name: "free", Kind: kind, BaseURL: "http://127.0.0.1:1/v1"}); err != nil {
				t.Fatalf("a keyless local provider: %v", err)
			}
			tamper(t, db, []Provider{remote})
			c.Prepare(t)
			cctx := c.Ctx(t, db, ctx, "default", "ai.decide")
			client, err := NewClientFor(cctx, db, "default", "free", "", "", NodeSurface("ai.decide"))
			if !errors.Is(err, ErrKeylessRemote) || client != nil {
				t.Fatalf("a keyless remote record was used: %v", err)
			}
			if _, err := ProviderKeySourceOf(cctx, db, "default", "free"); !errors.Is(err, ErrKeylessRemote) {
				t.Errorf("the key source of a keyless remote record: %v", err)
			}
			if builds.Load() != 0 {
				t.Errorf("the backend was built %d time(s)", builds.Load())
			}
		})
	}
}

// S4, cross-record half: a record written into the settings table that names
// another provider's vault entry at another host is refused when its client
// is built (the reviewer's raw-UPDATE scenario); the key reaches nobody.
func TestATamperedRecordSharingAKeyIsRefusedAtUseTime(t *testing.T) {
	keyring.MockInit()
	db := newDB(t)
	ctx := context.Background()
	seedKey(t, db, VaultScope, "decision-router", providerKeyValue, "2026-01-01T00:00:00Z")
	own, evil := jevtest.NewProbe(t), jevtest.NewProbe(t)
	kind, builds := senderKind(t)
	router := Provider{Name: "router", Kind: kind, BaseURL: own.URL, SecretName: "decision-router"}
	if err := SaveProvider(ctx, db, router); err != nil {
		t.Fatal(err)
	}
	tamper(t, db, []Provider{router, {Name: "zz", Kind: kind, BaseURL: evil.URL, SecretName: "decision-router"}})
	for _, name := range []string{"zz", "router"} {
		if c, _, err := ResolveProviderClient(ctx, db, "default", name, "", ""); err == nil || !strings.Contains(err.Error(), "its own host") {
			t.Errorf("%s: a record sharing a key with another host was used: %v", name, err)
			if c != nil {
				askOnce(ctx, c)
			}
		}
		if _, err := ProviderKeySourceOf(ctx, db, "default", name); err == nil {
			t.Errorf("%s: the key source agrees with a refused record", name)
		}
	}
	if evil.Calls()+own.Calls() != 0 || builds.Load() != 0 {
		t.Fatalf("something was sent (evil %d, own %d, builds %d)", evil.Calls(), own.Calls(), builds.Load())
	}
	// A record listed twice is refused too.
	tamper(t, db, []Provider{router, {Name: "router", Kind: kind, BaseURL: evil.URL, SecretName: "decision-router"}})
	if _, _, err := ResolveProviderClient(ctx, db, "default", "router", "", ""); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Errorf("a duplicated record: %v", err)
	}
}

// S4, bound-origin half: a key `jev provider add --key-stdin` stored
// (PutProviderKey) records its origin, so editing the record's own base_url
// in the settings table is refused at use time. The operator who confirmed
// a new host moves the binding (PutProviderKey with no key). An entry that
// records no origin (one the user made) is not checked by this half.
func TestAKeyIsBoundToTheHostItWasStoredFor(t *testing.T) {
	keyring.MockInit()
	db := newDB(t)
	ctx := context.Background()
	own, evil := jevtest.NewProbe(t), jevtest.NewProbe(t)
	kind, _ := senderKind(t)
	router := Provider{Name: "router", Kind: kind, BaseURL: own.URL, SecretName: "decision-router"}
	if err := SaveProvider(ctx, db, router); err != nil {
		t.Fatal(err)
	}
	if replaced, err := PutProviderKey(ctx, db, router, providerKeyValue, "router key"); err != nil || replaced {
		t.Fatalf("PutProviderKey: %v %v", replaced, err)
	}
	c, _, err := ResolveProviderClient(ctx, db, "default", "router", "", "")
	if err != nil {
		t.Fatal(err)
	}
	askOnce(ctx, c)
	if !own.Saw(providerKeyValue) {
		t.Fatal("the key did not reach its own host")
	}
	moved := router
	moved.BaseURL = evil.URL
	tamper(t, db, []Provider{moved})
	if _, _, err := ResolveProviderClient(ctx, db, "default", "router", "", ""); err == nil || !strings.Contains(err.Error(), "was stored for") {
		t.Fatalf("a re-pointed record got the key: %v", err)
	}
	if evil.Calls() != 0 {
		t.Fatal("the new host was called")
	}
	// The operator confirms the new host: the binding moves with it.
	if replaced, err := PutProviderKey(ctx, db, moved, "", ""); err != nil || !replaced {
		t.Fatalf("rebinding: %v %v", replaced, err)
	}
	if c, _, err = ResolveProviderClient(ctx, db, "default", "router", "", ""); err != nil {
		t.Fatal(err)
	}
	askOnce(ctx, c)
	if !evil.Saw(providerKeyValue) {
		t.Error("the confirmed host did not get the key")
	}
	if _, err := PutProviderKey(secrets.RefuseResolving(ctx, "an AI wrote this step"), db, moved, "", ""); !errors.Is(err, ErrOperatorOnly) {
		t.Errorf("an unapproved step rebound a key: %v", err)
	}
	// An entry without a recorded origin: only the cross-record half applies.
	seedKey(t, db, VaultScope, "mine", "k-mine-0123456789", "2026-01-01T00:00:00Z")
	mine := Provider{Name: "mine", Kind: kind, BaseURL: own.URL, SecretName: "mine"}
	if err := SaveProvider(ctx, db, mine); err != nil {
		t.Fatal(err)
	}
	if replaced, err := PutProviderKey(ctx, db, Provider{Name: "mine", Kind: kind, BaseURL: evil.URL, SecretName: "mine"}, "", ""); err != nil || replaced {
		t.Errorf("an unbound entry was rebound: %v %v", replaced, err)
	}
}

// Every surface (and the decider and dynamic org) names the question kinds it
// asks; a probed provider lacking one is refused for it.
func TestEverySurfaceNamesItsQuestionKinds(t *testing.T) {
	for _, s := range append(append([]Surface{}, Surfaces...), Decider, DynamicOrg) {
		if len(SurfaceKinds[s]) == 0 {
			t.Errorf("surface %s has no entry in SurfaceKinds", s)
		}
	}
	noul := Provider{Name: "nli", Caps: jev.Capabilities{Kinds: []string{jev.TypeChoice}}}
	if err := CheckSurfaceKinds(noul, PeopleLinks); err == nil || !strings.Contains(err.Error(), "noul") {
		t.Errorf("people_links on a choice-only provider: %v", err)
	}
	if err := CheckSurfaceKinds(noul, Capture); err != nil {
		t.Errorf("capture on a choice provider: %v", err)
	}
	if err := CheckSurfaceKinds(Provider{Name: "unprobed"}, HIL); err != nil {
		t.Errorf("unknown capabilities: %v", err)
	}
	if err := CheckSurfaceKinds(TypeSafeProvider(), HIL); err != nil {
		t.Errorf("TypeSafe: %v", err)
	}
}

// A probed hf-zeroshot provider reports softmax probabilities.
func TestASoftmaxProviderIsAccepted(t *testing.T) {
	db := newDB(t)
	kind, _ := senderKind(t)
	if err := SaveProvider(context.Background(), db, Provider{Name: "hf", Kind: kind, BaseURL: "https://hf.example", SecretName: "hf",
		Caps: jev.Capabilities{Probability: jev.ProbSoftmax}}); err != nil {
		t.Fatal(err)
	}
}
