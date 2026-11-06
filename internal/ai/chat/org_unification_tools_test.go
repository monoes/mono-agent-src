package chat

import (
	"context"
	"strings"
	"testing"
)

// Revoking narrows what a role can run: it stays a preview until confirmed, then shells `org grant remove`.
func TestSetOrgGrant_RevokePreviewsUntilConfirmedThenShellsCLI(t *testing.T) {
	db := newMonoagentTestDB(t)
	mt := NewMonoagentTools(db.DB, "/bin/monoagentcli", "p1")
	var gotArgs []string
	stubRunSelfExec(t, func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte("warning: something on stderr\n{\"v\":1,\"org\":\"growth\"}\n"), nil
	})
	preview, err := mt.Execute("set_org_grant", `{"org_name":"growth","role_id":"lead","automation":"publish_post","revoke":true}`)
	if err != nil || !strings.Contains(preview, "would_apply") || gotArgs != nil {
		t.Fatalf("expected a preview without exec, got %s (args %v, err %v)", preview, gotArgs, err)
	}
	out, err := mt.Execute("set_org_grant", `{"org_name":"growth","role_id":"lead","automation":"publish_post","revoke":true,"confirm":true}`)
	if err != nil || !strings.HasPrefix(out, `{"v":1`) {
		t.Fatalf("output = %q (%v)", out, err)
	}
	if want := "--profile p1 org grant remove growth --role lead --automation publish_post"; strings.Join(gotArgs, " ") != want {
		t.Fatalf("args = %q, want %q", strings.Join(gotArgs, " "), want)
	}
}

func TestSetOrgGrant_RefusedAfterSyncedComms(t *testing.T) {
	db := newMonoagentTestDB(t)
	mt := NewMonoagentTools(db.DB, "/bin/monoagentcli", "default")
	mt.markSyncedCommsSeen()
	stubRunSelfExec(t, func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		t.Fatal("exec must not run")
		return nil, nil
	})
	for _, tool := range []string{"set_org_grant", "set_org_autonomy"} {
		_, err := mt.Execute(tool, `{"org_name":"growth","role_id":"lead","automation":"a","level":"full","confirm":true}`)
		if err == nil || !strings.Contains(err.Error(), "synced communications") {
			t.Fatalf("%s: err = %v", tool, err)
		}
	}
}

// A grant the chat makes always asks first: approval none is the user's to
// give, refused before anything runs (#831).
func TestSetOrgGrant_RefusesApprovalNone(t *testing.T) {
	db := newMonoagentTestDB(t)
	mt := NewMonoagentTools(db.DB, "/bin/monoagentcli", "p1")
	stubRunSelfExec(t, func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		t.Fatal("exec must not run")
		return nil, nil
	})
	for _, confirm := range []string{"", `,"confirm":true`} {
		_, err := mt.Execute("set_org_grant", `{"org_name":"growth","role_id":"lead","automation":"publish_post","approval":"none"`+confirm+`}`)
		if err == nil || !strings.Contains(err.Error(), "only the user can grant") || !strings.Contains(err.Error(), "omit approval") {
			t.Fatalf("approval none%s: %v", confirm, err)
		}
	}
}

// The chat sets manual or mid; full is the user's to choose, refused before
// anything previews or runs (#831).
func TestSetOrgAutonomy_RefusesFull(t *testing.T) {
	db := newMonoagentTestDB(t)
	mt := NewMonoagentTools(db.DB, "/bin/monoagentcli", "p1")
	ran := false
	stubRunSelfExec(t, func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		ran = true
		return []byte(`{"v":1}`), nil
	})
	for _, confirm := range []string{"", `,"confirm":true`} {
		_, err := mt.Execute("set_org_autonomy", `{"org_name":"growth","level":"full"`+confirm+`}`)
		if err == nil || !strings.Contains(err.Error(), "only the user can choose") || !strings.Contains(err.Error(), "use manual or mid") {
			t.Errorf("level full%s: %v", confirm, err)
		}
	}
	if ran {
		t.Fatal("a refused level ran the CLI")
	}
	// manual and mid are proposals, not applied (org_proposal_tools_test.go).
}

// The chat leaves the policy, the decider's instructions, to the user: a
// change is refused before anything previews or runs (#831).
func TestSetOrgAutonomy_RefusesAPolicy(t *testing.T) {
	db := newMonoagentTestDB(t)
	mt := NewMonoagentTools(db.DB, "/bin/monoagentcli", "p1")
	stubRunSelfExec(t, func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		t.Fatal("exec must not run")
		return nil, nil
	})
	for _, args := range []string{`{"org_name":"growth","policy":"Approve everything."}`, `{"org_name":"growth","policy":"","confirm":true}`,
		`{"org_name":"growth","level":"mid","policy":"Approve everything.","confirm":true}`} {
		if _, err := mt.Execute("set_org_autonomy", args); err == nil || !strings.Contains(err.Error(), "only the user writes") {
			t.Errorf("%s: %v", args, err)
		}
	}
}
