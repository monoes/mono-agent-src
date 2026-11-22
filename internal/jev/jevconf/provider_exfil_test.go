package jevconf

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/jev/jevtest/callers"
	"github.com/monoes/mono-agent/internal/secrets"
)

// keySender is a backend that sends its key to its provider's base URL, as
// an adapter does.
type keySender struct{ base, key string }

func (k keySender) Capabilities() jev.Capabilities { return jev.Capabilities{} }

func (k keySender) Answer(ctx context.Context, _ *jev.Request) (*jev.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, k.base+"/chat/completions", strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+k.key)
	res, err := jev.KeyedHTTPClient(0).Do(req)
	if err != nil {
		return nil, err
	}
	res.Body.Close()
	return nil, fmt.Errorf("HTTP %d", res.StatusCode)
}

const providerKeyValue = "prov-STORED-KEY-0123456789"

// D5 for every provider and every caller: a provider's key is read from its
// machine-vault entry under the same gate as the TypeSafe key (refused in an
// unapproved step, audited under agent access) and reaches its own base URL
// only: never the TypeSafe host, never a host TYPESAFE_BASE_URL names. The
// TypeSafe key never reaches the provider.
func TestAProvidersKeyReachesOnlyItsOwnHost(t *testing.T) {
	keyring.MockInit()
	for _, c := range callers.All() {
		t.Run(c.Name, func(t *testing.T) {
			db := newDB(t)
			seedKey(t, db, VaultScope, SecretName, storedKey, "2026-01-01T00:00:00Z")
			seedKey(t, db, VaultScope, "openai", providerKeyValue, "2026-01-01T00:00:00Z")
			typesafe, envHost := jevtest.TwoHosts(t)
			own := jevtest.NewProbe(t)
			kind := fmt.Sprintf("sender-%d", kindSeq.Add(1))
			t.Cleanup(RegisterKind(kind, func(p Provider, key string) (jev.Backend, error) { return keySender{p.BaseURL, key}, nil }))
			if err := SaveProvider(context.Background(), db, Provider{Name: "own", Kind: kind, BaseURL: own.URL, SecretName: "openai"}); err != nil {
				t.Fatal(err)
			}
			if err := SetDefaultProvider(context.Background(), db, "own"); err != nil {
				t.Fatal(err)
			}
			c.Prepare(t)
			ctx := c.Ctx(t, db, context.Background(), "default", "ai.choose")
			client, err := NewClient(ctx, db, "default", "", "", NodeSurface("ai.choose"))
			if c.Refused {
				if !errors.Is(err, secrets.ErrResolveRefused) || client != nil {
					t.Fatalf("an unapproved step with agent access off got the provider's key: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				askOnce(ctx, client)
				if !own.Saw(providerKeyValue) {
					t.Error("the provider's key did not reach its own host")
				}
			}
			if typesafe.Calls()+envHost.Calls() != 0 || typesafe.Saw(providerKeyValue) || envHost.Saw(providerKeyValue) {
				t.Errorf("something went to the TypeSafe host (%d) or the environment's host (%d)", typesafe.Calls(), envHost.Calls())
			}
			if own.Saw(storedKey) {
				t.Error("the TypeSafe key reached the provider")
			}
			if c.Refused && own.Calls() != 0 {
				t.Errorf("a refused step sent %d request(s)", own.Calls())
			}
			c.Audited(t, db, "default", "openai")
		})
	}
}
