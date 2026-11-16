package org

import (
	"context"
	"os"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/orgenv"
	"github.com/monoes/mono-agent/internal/orgdecide"
	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/orgsign/orgsigntest"
	"github.com/monoes/mono-agent/internal/storage"
)

// The daemon has no agent marker and no turn verdict, so it cannot tell who wrote the rows it
// reconciles a verified org file from (a coder turn can revoke a grant). Its save must therefore
// leave the org for the operator to review, never re-sign it (#1475).
func TestDaemonReconcileFromRowsDoesNotResign(t *testing.T) {
	for _, daemon := range []bool{true, false} {
		f, s, _ := newSigningFixture(t, "2.21.0")
		f.mustRun(t, "automation", "add", "growth", "--workflow", f.outboundWF, "--alias", "publish_post")
		f.mustRun(t, "grant", "add", "growth", "--role", "writer", "--automation", "publish_post")
		orgsigntest.Sign(t, f.root, "growth")
		s.calls = nil

		db, err := storage.NewDatabase(f.cfg.DBPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		ctx := context.Background()
		// A row written outside the file: the file's display copy of the grant is now stale.
		if err := orggrant.NewStore(db.DB).RevokeGrant(ctx, "default", "growth", "writer", "publish_post"); err != nil {
			t.Fatal(err)
		}
		if !f.signature(t).OK() {
			t.Fatalf("daemon=%v: a row write alone must leave the file verified", daemon)
		}
		r := &orgenv.Reconciler{DB: db, OpenWorkflowStore: openTestWorkflowStore, Logf: t.Logf, Unsigned: daemon}
		d := f.load(t)
		res := r.Doc(ctx, orgdecide.ProfileRoot{ProfileID: "default", Root: f.root}, d, false)
		if !res.Saved {
			t.Fatalf("daemon=%v: reconcile did not rewrite the file: %+v", daemon, res)
		}
		if daemon {
			if len(s.calls) != 0 || f.signature(t).OK() || res.Signature == nil || res.Signature.Notice == "" {
				t.Fatalf("daemon reconcile re-signed or gave no notice: calls %v sig %+v", s.calls, res.Signature)
			}
		} else if len(s.calls) != 1 || !f.signature(t).OK() {
			t.Fatalf("the operator's own reconcile should still re-sign: calls %v", s.calls)
		}
	}
}

// Replacing an existing verified org from a whole document drops its signature outright; it does not
// rely on the loaded-hash check, so no process can re-sign the replacement unreviewed.
func TestCreateJSONReplaceDropsTheSignature(t *testing.T) {
	f, s, _ := newSigningFixture(t, "2.21.0")
	orgsigntest.Sign(t, f.root, "growth")
	sigPath, err := orgsign.SignaturePath(f.root, "growth")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sigPath); err != nil {
		t.Fatalf("fixture did not sign: %v", err)
	}
	doc := `{"name":"growth","goal":"g2","status":"stopped","schedule":null,"roles":[{"id":"lead","title":"Lead","type":"lead","reports_to":null,"responsibilities":["lead"],"policy":{"git":"push"}}]}`
	captureStderr(t, func() { f.mustRun(t, "create-json", "growth", "--json", doc) })
	if _, err := os.Stat(sigPath); !os.IsNotExist(err) {
		t.Fatalf("signature of the replaced org still on disk: %v", err)
	}
	if len(s.calls) != 0 {
		t.Fatalf("replacement was signed: %v", s.calls)
	}
	if st := f.signature(t); st.OK() {
		t.Fatalf("replaced org verifies: %+v", st)
	}
}
