package jevconf

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/jev/jevtest/callers"
	"github.com/monoes/mono-agent/internal/secrets"
)

const storedKey = "ts-STORED-KEY-0123456789"

func askOnce(ctx context.Context, c *jev.Client) {
	_, _ = c.Ask(ctx, map[string]any{}, map[string]jev.Question{"q": {Type: jev.TypeChoice, Criteria: map[string]any{"a": "A", "b": "B"}}})
}

// All users of the TypeSafe key funnel into NewClient (nodes ai.choose,
// browser.jev, human_in_loop, applications.evaluate, action fallback, the CLI's
// capture/hil/retry/people surfaces, the org decider, the API's auto model) or
// into ResolveClient (jev models / key test, doctor). A stored key goes to the
// TypeSafe host; a step the user did not approve reads it only under agent
// access, audited.
func TestTheStoredKeyReachesTheTypeSafeHostAndRefusalsApply(t *testing.T) {
	keyring.MockInit()
	for _, surface := range []Surface{NodeSurface("ai.choose"), NodeSurface("browser.jev"), HIL, ActionFallback, Capture, CLI} {
		for _, c := range callers.All() {
			t.Run(string(surface)+"/"+c.Name, func(t *testing.T) {
				db := newDB(t)
				seedKey(t, db, VaultScope, SecretName, storedKey, "2026-01-01T00:00:00Z")
				legit := jevtest.TypeSafeHost(t)
				c.Prepare(t)
				ctx := c.Ctx(t, db, context.Background(), "default", "ai.choose")
				client, err := NewClient(ctx, db, "default", "", "", surface)
				got := ""
				if err != nil {
					got = err.Error()
				} else {
					askOnce(ctx, client)
				}
				if c.Refused && (!errors.Is(err, secrets.ErrResolveRefused) || client != nil) {
					t.Fatalf("an unapproved step with agent access off got the key: %v", err)
				}
				if !c.Refused && err != nil {
					t.Fatal(err)
				}
				c.StoredKeyReach(t, legit, storedKey, "@secret:typesafe is not read from the vault", got)
				c.Audited(t, db, "default", SecretName)
			})
		}
	}
}

// A stored key with a TYPESAFE_BASE_URL set is refused, for every caller, and
// told so: it is not sent to the host the environment names, and not silently
// to the TypeSafe host either.
func TestAStoredKeyWithAnOverrideIsRefusedNotRedirected(t *testing.T) {
	keyring.MockInit()
	for _, c := range callers.All() {
		t.Run(c.Name, func(t *testing.T) {
			db := newDB(t)
			seedKey(t, db, VaultScope, SecretName, storedKey, "2026-01-01T00:00:00Z")
			legit, envHost := jevtest.TwoHosts(t)
			c.Prepare(t)
			ctx := c.Ctx(t, db, context.Background(), "default", "ai.choose")
			client, err := NewClient(ctx, db, "default", "", "", NodeSurface("ai.choose"))
			if client != nil || err == nil {
				t.Fatal("a client was built")
			}
			if c.Refused != errors.Is(err, secrets.ErrResolveRefused) || (!c.Refused && !errors.Is(err, ErrBaseURLIgnored)) {
				t.Fatalf("err = %v", err)
			}
			c.StoredKeyWithAnOverride(t, legit, envHost, storedKey, "@secret:typesafe is not read from the vault", err.Error())
			// A step's own api_key is no different, however it was written.
			if _, err := NewClient(ctx, db, "default", "cfg-KEY-5555555", "", NodeSurface("ai.choose")); !errors.Is(err, ErrBaseURLIgnored) {
				t.Fatalf("a step's own key with an override: %v", err)
			}
			if legit.Calls()+envHost.Calls() != 0 {
				t.Error("something was sent")
			}
		})
	}
}

// A grant naming other secrets does not cover the implicit lookup, as it does
// not cover an explicit @secret:typesafe.
func TestAGrantNamingOtherSecretsDoesNotCoverTheKey(t *testing.T) {
	keyring.MockInit()
	db := newDB(t)
	seedKey(t, db, VaultScope, SecretName, storedKey, "2026-01-01T00:00:00Z")
	legit := jevtest.TypeSafeHost(t)
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
	if legit.Calls() != 0 {
		t.Error("something was sent")
	}
	if rows, _ := secrets.AgentAccessAudit(context.Background(), db, "default"); len(rows) != 0 {
		t.Errorf("audit rows for a refused read: %+v", rows)
	}
}

// A grant that cannot be read fails closed: the key is not read, nothing is
// sent (secrets' permit refuses when agent access could not be checked).
func TestAnUnreadableGrantFailsClosed(t *testing.T) {
	keyring.MockInit()
	db := newDB(t)
	seedKey(t, db, VaultScope, SecretName, storedKey, "2026-01-01T00:00:00Z")
	legit := jevtest.TypeSafeHost(t)
	// The grant is read through a handle that cannot be read (closed).
	broken, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	broken.Close()
	ctx := secrets.AllowResolving(context.Background(), broken, "default", secrets.AccessOrigin{})
	c, err := NewClient(ctx, db, "default", "", "", NodeSurface("ai.choose"))
	if !errors.Is(err, secrets.ErrResolveRefused) || c != nil || !strings.Contains(err.Error(), "agent access could not be checked") {
		t.Fatalf("an unreadable grant gave out the key: %v", err)
	}
	if legit.Calls() != 0 {
		t.Error("something was sent")
	}
}

// TYPESAFE_BASE_URL pairs with TYPESAFE_API_KEY alone, from the operator's own
// terminal.
func TestTheBaseURLOverridePairsOnlyWithTheOperatorsEnvKey(t *testing.T) {
	keyring.MockInit()
	const envK = "env-KEY-987654321"
	ask := func(ctx context.Context, db *sql.DB, explicit string) error {
		c, err := NewClient(ctx, db, "default", explicit, "", NodeSurface("ai.choose"))
		if err != nil {
			return err
		}
		askOnce(ctx, c)
		return nil
	}
	t.Run("operator's env key follows the override", func(t *testing.T) {
		db := newDB(t)
		legit, envHost := jevtest.TwoHosts(t)
		t.Setenv("TYPESAFE_API_KEY", envK)
		if err := ask(context.Background(), db, ""); err != nil {
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
		t.Run(c.Name+": the env key is refused its override, not redirected", func(t *testing.T) {
			db := newDB(t)
			legit, envHost := jevtest.TwoHosts(t)
			t.Setenv("TYPESAFE_API_KEY", envK)
			c.Prepare(t)
			err := ask(c.Ctx(t, db, context.Background(), "default", "ai.choose"), db, "")
			if !errors.Is(err, ErrBaseURLIgnored) || !strings.Contains(err.Error(), "TYPESAFE_BASE_URL ignored: it pairs only with TYPESAFE_API_KEY from your own terminal") {
				t.Fatalf("err = %v", err)
			}
			if envHost.Calls() != 0 || legit.Calls() != 0 {
				t.Errorf("env host calls=%d, typesafe host calls=%d: the env key was sent", envHost.Calls(), legit.Calls())
			}
		})
	}
}

// What a TYPESAFE_BASE_URL may be, for the operator's env key: https, or http to
// this machine, by net/url and net.ParseIP only.
func TestTheOverrideIsHttpsOrHttpToThisMachine(t *testing.T) {
	ctx := context.Background()
	for _, ok := range []string{"https://ts.example", "https://ts.example:8443/api/", "http://127.0.0.1:8080", "http://[::1]:9", "http://localhost:3", "http://127.5.5.5"} {
		t.Setenv("TYPESAFE_BASE_URL", ok)
		if b, err := BaseURL(ctx, SourceEnv); err != nil || b != strings.TrimRight(ok, "/") {
			t.Errorf("%q = %q, %v; want it honoured", ok, b, err)
		}
	}
	for _, bad := range []string{
		"http://ts.example", "http://192.168.1.10:8080", "http://10.0.0.1",
		"http://localhost.evil.com", "http://127.0.0.1.evil.com", "http://127.0.0.1@evil.com", "http://LOCALHOST",
		"http://127.1", "http://0.0.0.0", "http://2130706433", "http://0x7f000001", "http://017700000001",
		"http://[::1%25lo]", "http://[::1%lo]", "http://[fe80::1%25eth0]", "http://[::]",
		"http://xn--localhost-fja.example", "http://ｌｏｃａｌｈｏｓｔ", "http://localhоst", "http://localhost.",
		"ftp://127.0.0.1", "ws://localhost", "//127.0.0.1", "localhost:80", "https://user:pw@ts.example", "https://ts.example?x=1", "https://ts.example#f", "https:///x", "",
	} {
		if bad == "" {
			continue
		}
		t.Setenv("TYPESAFE_BASE_URL", bad)
		b, err := BaseURL(ctx, SourceEnv)
		if !errors.Is(err, ErrBaseURL) || b != "" {
			t.Errorf("%q = %q, %v; want ErrBaseURL", bad, b, err)
			continue
		}
		if !strings.Contains(err.Error(), "http to a LAN or any other non-loopback host") || strings.Contains(err.Error(), "pw@") {
			t.Errorf("%q: the refusal text does not say what is refused, or shows a password: %v", bad, err)
		}
	}
}
