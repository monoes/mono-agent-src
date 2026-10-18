package org

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/apikeys"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// Deleting a profile must not leave API keys that still authenticate as it:
// `org teardown-profile` revokes them with the org grants and endpoints.
func TestOrgTeardownProfileRevokesAPIKeys(t *testing.T) {
	f := newOrgCLIFixture(t)
	db, profileID, _, err := testEnv(f.cfg).Profile()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store := apikeys.NewStore(db.DB)
	_, secret, err := store.Create(ctx, profileID, "svc", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Create(ctx, profileID, "svc-2", false); err != nil {
		t.Fatal(err)
	}
	_, otherSecret, err := store.Create(ctx, "another-profile", "svc", false)
	if err != nil {
		t.Fatal(err)
	}

	preview := f.mustRun(t, "teardown-profile", "--dry-run")
	if preview["api_keys"] != float64(2) {
		t.Fatalf("dry-run api_keys = %v, want 2", preview["api_keys"])
	}
	if _, err := store.Authenticate(ctx, secret); err != nil {
		t.Fatalf("a dry run revoked a key: %v", err)
	}

	out := f.mustRun(t, "teardown-profile")
	if out["api_keys"] != float64(2) {
		t.Fatalf("api_keys = %v, want 2", out["api_keys"])
	}
	if _, err := store.Authenticate(ctx, secret); !errors.Is(err, apikeys.ErrInvalidKey) {
		t.Fatalf("a key of the torn-down profile still authenticates: %v", err)
	}
	if _, err := store.Authenticate(ctx, otherSecret); err != nil {
		t.Fatalf("another profile's key must survive: %v", err)
	}
}

// Revoking a profile's API keys, grants and endpoints cuts off whatever uses
// them: an agent, the chat or a workflow is refused (exit 3, operator_only)
// and nothing is revoked, while a dry run stays open (#845 review, item 5).
func TestOrgTeardownProfileIsTheOperatorsOnly(t *testing.T) {
	f := newOrgCLIFixture(t)
	db, profileID, _, err := testEnv(f.cfg).Profile()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store := apikeys.NewStore(db.DB)
	_, secret, err := store.Create(ctx, profileID, "svc", false)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLAUDECODE", "1")
	_, err = f.run(t, "teardown-profile")
	var fields interface{ JSONErrorFields() map[string]any }
	if clicore.ExitCodeFor(err) != 3 || !errors.As(err, &fields) || fields.JSONErrorFields()["code"] != "operator_only" || !strings.Contains(err.Error(), "CLAUDECODE") {
		t.Fatalf("an agent's teardown-profile: %v", err)
	}
	if _, err := store.Authenticate(ctx, secret); err != nil {
		t.Fatalf("a refused teardown revoked a key: %v", err)
	}
	if preview := f.mustRun(t, "teardown-profile", "--dry-run"); preview["api_keys"] != float64(1) {
		t.Errorf("an agent's dry run: api_keys = %v, want 1", preview["api_keys"])
	}
}
