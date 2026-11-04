package jevconf

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/jev/jevtest/callers"
	"github.com/monoes/mono-agent/internal/secrets"
)

const storedKey = "ts-STORED-KEY-0123456789"

// All users of the TypeSafe key funnel into NewClient (nodes ai.choose,
// browser.jev, human_in_loop, applications.evaluate, action fallback, the CLI's
// capture/hil/retry/people surfaces, the org decider, the API's auto model) or
// into ResolveKey + ClientFor (jev models / key test, doctor).
func TestTheStoredKeyNeverReachesAHostTheCallerNamed(t *testing.T) {
	keyring.MockInit()
	for _, surface := range []Surface{NodeSurface("ai.choose"), NodeSurface("browser.jev"), HIL, ActionFallback, Capture, CLI} {
		for _, c := range callers.All() {
			t.Run(string(surface)+"/"+c.Name, func(t *testing.T) {
				db := newDB(t)
				seedKey(t, db, VaultScope, SecretName, storedKey, "2026-01-01T00:00:00Z")
				legit, envHost := jevtest.TwoHosts(t)
				c.Prepare(t)
				ctx := c.Ctx(t, db, context.Background(), "default", "ai.choose")
				client, err := NewClient(ctx, db, "default", "", "", surface)
				if c.Refused {
					if !errors.Is(err, secrets.ErrResolveRefused) || client != nil {
						t.Fatalf("an unapproved step with agent access off got the key: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				} else {
					_, _ = client.Ask(ctx, map[string]any{}, map[string]jev.Question{"q": {Type: jev.TypeChoice, Criteria: map[string]any{"a": "A", "b": "B"}}})
				}
				got := ""
				if err != nil {
					got = err.Error()
				}
				c.StoredKeyReach(t, legit, envHost, storedKey, "@secret:typesafe is not read from the vault", got)
				rows, _ := secrets.AgentAccessAudit(context.Background(), db, "default")
				switch {
				case c.Granted && (len(rows) != 1 || rows[0].Secret != SecretName || rows[0].NodeID != "n1"):
					t.Errorf("under agent access the key's use is audited by name: %+v", rows)
				case !c.Granted && len(rows) != 0:
					t.Errorf("audit rows without a grant: %+v", rows)
				}
			})
		}
	}
}

// A grant that names other secrets does not cover the implicit lookup, as it
// does not cover an explicit @secret:typesafe.
func TestAGrantNamingOtherSecretsDoesNotCoverTheKey(t *testing.T) {
	keyring.MockInit()
	db := newDB(t)
	seedKey(t, db, VaultScope, SecretName, storedKey, "2026-01-01T00:00:00Z")
	legit, envHost := jevtest.TwoHosts(t)
	if err := secrets.SetAgentAccessGrant(context.Background(), db, "default", secrets.AccessGrant{AnyHost: true, Secrets: []string{"github"}}); err != nil {
		t.Fatal(err)
	}
	ctx := secrets.AllowResolving(context.Background(), db, "default", secrets.AccessOrigin{})
	for _, explicit := range []string{"", "@secret:typesafe"} {
		c, err := NewClient(ctx, db, "default", explicit, "", NodeSurface("ai.choose"))
		if !errors.Is(err, secrets.ErrResolveRefused) || c != nil {
			t.Errorf("explicit %q: a grant covering only github gave out the key: %v", explicit, err)
		}
	}
	if legit.Calls()+envHost.Calls() != 0 {
		t.Error("something was sent")
	}
	rows, _ := secrets.AgentAccessAudit(context.Background(), db, "default")
	if len(rows) != 0 {
		t.Errorf("audit rows for a refused read: %+v", rows)
	}
}

// TYPESAFE_BASE_URL pairs only with TYPESAFE_API_KEY, from the operator's own
// terminal, as https (or http to this machine). Any other caller's env key goes
// to the TypeSafe host; a stored key or a step's own key always does.
func TestTheBaseURLOverridePairsOnlyWithTheOperatorsEnvKey(t *testing.T) {
	keyring.MockInit()
	const envK = "env-KEY-987654321"
	ask := func(t *testing.T, ctx context.Context, db *sql.DB, explicit string) error {
		c, err := NewClient(ctx, db, "default", explicit, "", NodeSurface("ai.choose"))
		if err != nil {
			return err
		}
		_, _ = c.Ask(ctx, map[string]any{}, map[string]jev.Question{"q": {Type: jev.TypeChoice, Criteria: map[string]any{"a": "A", "b": "B"}}})
		return nil
	}
	t.Run("operator's env key follows the override", func(t *testing.T) {
		db := newDB(t)
		legit, envHost := jevtest.TwoHosts(t)
		t.Setenv("TYPESAFE_API_KEY", envK)
		if err := ask(t, context.Background(), db, ""); err != nil {
			t.Fatal(err)
		}
		if !envHost.Saw(envK) || legit.Calls() != 0 {
			t.Errorf("env host saw=%v, typesafe host calls=%d", envHost.Saw(envK), legit.Calls())
		}
	})
	for _, c := range callers.All() {
		if c.Operator {
			continue
		}
		t.Run(c.Name+": env key goes to the TypeSafe host", func(t *testing.T) {
			db := newDB(t)
			legit, envHost := jevtest.TwoHosts(t)
			t.Setenv("TYPESAFE_API_KEY", envK)
			c.Prepare(t)
			if err := ask(t, c.Ctx(t, db, context.Background(), "default", "ai.choose"), db, ""); err != nil {
				t.Fatal(err)
			}
			if envHost.Calls() != 0 || !legit.Saw(envK) {
				t.Errorf("env host calls=%d, typesafe host saw=%v", envHost.Calls(), legit.Saw(envK))
			}
		})
	}
	t.Run("a step's own key never follows the override", func(t *testing.T) {
		db := newDB(t)
		legit, envHost := jevtest.TwoHosts(t)
		if err := ask(t, context.Background(), db, "cfg-KEY-5555555"); err != nil {
			t.Fatal(err)
		}
		if envHost.Calls() != 0 || !legit.Saw("cfg-KEY-5555555") {
			t.Errorf("env host calls=%d, typesafe host saw=%v", envHost.Calls(), legit.Saw("cfg-KEY-5555555"))
		}
	})
	for _, bad := range []string{"http://attacker.example", "ftp://127.0.0.1", "https://user:pw@h.example", "//x"} {
		t.Run("refused override "+bad, func(t *testing.T) {
			db := newDB(t)
			legit, envHost := jevtest.TwoHosts(t)
			t.Setenv("TYPESAFE_API_KEY", envK)
			t.Setenv("TYPESAFE_BASE_URL", bad)
			if err := ask(t, context.Background(), db, ""); !errors.Is(err, ErrBaseURL) {
				t.Fatalf("err = %v, want ErrBaseURL", err)
			}
			if legit.Calls()+envHost.Calls() != 0 {
				t.Error("something was sent")
			}
		})
	}
}
