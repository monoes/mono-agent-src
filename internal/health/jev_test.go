package health

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestJevKeyCheck(t *testing.T) {
	ctx := context.Background()
	env := &Env{ProfileID: "p1"}
	if res := checkJevKey(ctx, env); res.Status != StatusSkip {
		t.Fatalf("nil hook: %+v", res)
	}
	env.JevKey = func(context.Context) (string, error) { return "vault", nil }
	if res := checkJevKey(ctx, env); res.Status != StatusOK || res.Summary != "vault entry `typesafe` present (not decrypted)" {
		t.Fatalf("vault key: %+v", res)
	}
	env.JevKey = func(context.Context) (string, error) { return "", errors.New("no key") }
	res := checkJevKey(ctx, env)
	if res.Status != StatusWarn || res.FixID != FixJevKey {
		t.Fatalf("missing key: %+v", res)
	}
	// A hook that answers nothing at all is "missing", not ok.
	env.JevKey = func(context.Context) (string, error) { return "", nil }
	if res := checkJevKey(ctx, env); res.Status != StatusWarn {
		t.Fatalf("empty source: %+v", res)
	}
}

func TestJevAPICheck(t *testing.T) {
	ctx := context.Background()
	env := &Env{ProfileID: "p1", JevKey: func(context.Context) (string, error) { return "env", nil }}
	if res := checkJevAPI(ctx, env); res.Status != StatusSkip {
		t.Fatalf("nil hook: %+v", res)
	}
	calls := 0
	env.JevModels = func(context.Context) error { calls++; return nil }
	if res := checkJevAPI(ctx, env); res.Status != StatusOK || calls != 1 {
		t.Fatalf("ok: %+v (calls %d)", res, calls)
	}
	env.JevModels = func(context.Context) error { return errors.New("jev: HTTP 401: bad key") }
	if res := checkJevAPI(ctx, env); res.Status != StatusWarn || !strings.Contains(res.Summary, "401") {
		t.Fatalf("api error: %+v", res)
	}
	// Without a key the API check is not applicable, and makes no call.
	env.JevKey = func(context.Context) (string, error) { return "", errors.New("no key") }
	calls = 0
	env.JevModels = func(context.Context) error { calls++; return nil }
	if res := checkJevAPI(ctx, env); res.Status != StatusSkip || calls != 0 {
		t.Fatalf("no key: %+v (calls %d)", res, calls)
	}
}

// With another default provider, a missing TypeSafe key is not applicable,
// and jev.api checks that provider (keyless ones too); TypeSafe as the
// default named explicitly behaves as with no hook (D4).
func TestJevChecksFollowTheDefaultProvider(t *testing.T) {
	ctx := context.Background()
	noKey := func(context.Context) (string, error) { return "", errors.New("no key") }
	calls := 0
	env := &Env{ProfileID: "p1", JevKey: noKey, JevModels: func(context.Context) error { calls++; return nil },
		JevProvider: func() string { return "local" }}
	if res := checkJevKey(ctx, env); res.Status != StatusSkip || !strings.Contains(res.Summary, `"local"`) {
		t.Fatalf("key with another provider: %+v", res)
	}
	if res := checkJevAPI(ctx, env); res.Status != StatusOK || calls != 1 || !strings.Contains(res.Summary, `"local"`) {
		t.Fatalf("api with another provider: %+v (calls %d)", res, calls)
	}
	env.JevModels = func(context.Context) error { return errors.New("connection refused") }
	if res := checkJevAPI(ctx, env); res.Status != StatusWarn || !strings.Contains(res.Summary, "connection refused") {
		t.Fatalf("api failing: %+v", res)
	}
	env.JevProvider = func() string { return "typesafe" }
	if res := checkJevKey(ctx, env); res.Status != StatusWarn || res.FixID != FixJevKey {
		t.Fatalf("typesafe default, no key: %+v", res)
	}
	if res := checkJevAPI(ctx, env); res.Status != StatusSkip {
		t.Fatalf("typesafe default, no key, api: %+v", res)
	}
}

func TestJevRegistered(t *testing.T) {
	reg := Default()
	var key, api *Check
	for i, c := range reg.Checks() {
		switch c.ID {
		case CheckJevKey:
			key = &reg.Checks()[i]
		case CheckJevAPI:
			api = &reg.Checks()[i]
		}
	}
	if key == nil || api == nil {
		t.Fatal("jev checks not registered")
	}
	if key.Group != GroupIntegrations || key.Required || key.Network || key.OnDemand || len(key.Features) != 1 || key.Features[0] != "jev" {
		t.Fatalf("jev.key = %+v", *key)
	}
	if !api.Network || !api.OnDemand || api.Required {
		t.Fatalf("jev.api = %+v", *api)
	}
	f, ok := reg.Fix(FixJevKey)
	if !ok || f.Safety != SafetyManual || f.Command != "monoagentcli jev key set" {
		t.Fatalf("fix = %+v %v", f, ok)
	}
}
