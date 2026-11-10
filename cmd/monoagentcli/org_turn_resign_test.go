package main

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/cli/orgenv"
	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/orgsign/orgsigntest"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The workflow-side entry points that rewrite a signed org (#1473): saving a
// workflow that raises a grant's tier, and `workflow delete --force`. A turn's
// shell without an agent marker must not get the org re-signed by them.

type countingSigner struct {
	t     *testing.T
	calls int
}

func (s *countingSigner) Sign(_ context.Context, root, org, hash string) error {
	s.calls++
	orgsigntest.SignHash(s.t, root, org, hash)
	return nil
}

// signedGrowthWithGrant is a growth org with a grant of the plain workflow,
// signed by the operator, with signing switched on and a counting signer.
func signedGrowthWithGrant(t *testing.T) (*orgCLIFixture, *countingSigner) {
	f := newOrgCLIFixture(t)
	t.Setenv("MONOMIND_ORGRT_OPERATOR_DIR", filepath.Join(t.TempDir(), "operator"))
	orgsigntest.AsOperator(t)
	f.mustRun(t, "automation", "add", "growth", "--workflow", f.plainWF, "--alias", "summarize")
	f.mustRun(t, "grant", "add", "growth", "--role", "writer", "--automation", "summarize")
	orgsigntest.Sign(t, f.root, "growth")
	s := &countingSigner{t: t}
	prevOn, prevSigner := orgenv.SigningOn, orgenv.Signer
	orgenv.SigningOn = func(context.Context, string) bool { return true }
	orgenv.Signer = s
	t.Cleanup(func() { orgenv.SigningOn, orgenv.Signer = prevOn, prevSigner })
	return f, s
}

func growthState(t *testing.T, f *orgCLIFixture) orgsign.Status {
	st, _, err := orgsign.VerifyFile(f.root, "growth")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// outboundize gives the plain workflow an outbound node through the CLI's own store,
// which re-derives the tiers of its grants.
func outboundize(t *testing.T, f *orgCLIFixture) {
	db, err := storage.NewDatabase(f.cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = openWorkflowStore(db).SaveWorkflowNodes(context.Background(), f.plainWF, []workflow.WorkflowNode{
		{ID: f.plainWF + "-t", WorkflowID: f.plainWF, Type: "trigger.manual", Name: "start"},
		{ID: f.plainWF + "-n", WorkflowID: f.plainWF, Type: "comm.slack", Name: "step"},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowSaveTierRaiseResigns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("on Windows a changed org is never re-signed for the operator: it asks for `monomind org sign` (a stated limit)")
	}
	for _, c := range []struct {
		name     string
		verdict  string
		wantSign bool
	}{
		{"operator terminal", "", true},
		{"unmarked turn", "a coder session's turn runs this command", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, s := signedGrowthWithGrant(t)
			opgate.SetForTest(t, func(bool) string { return c.verdict })
			outboundize(t, f)
			if got := s.calls > 0; got != c.wantSign || growthState(t, f).OK() != c.wantSign {
				t.Fatalf("sign calls %d, state %+v, want signed=%v", s.calls, growthState(t, f), c.wantSign)
			}
		})
	}
}

func TestWorkflowDeleteForceResigns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("on Windows a changed org is never re-signed for the operator: it asks for `monomind org sign` (a stated limit)")
	}
	for _, c := range []struct {
		name     string
		verdict  string
		wantSign bool
	}{
		{"operator terminal", "", true},
		{"unmarked turn", "a coder session's turn runs this command", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, s := signedGrowthWithGrant(t)
			opgate.SetForTest(t, func(bool) string { return c.verdict })
			if err := checkWorkflowOrgReferences(context.Background(), f.cfg, f.plainWF, true); err != nil {
				t.Fatal(err)
			}
			if got := s.calls > 0; got != c.wantSign || growthState(t, f).OK() != c.wantSign {
				t.Fatalf("sign calls %d, state %+v, want signed=%v", s.calls, growthState(t, f), c.wantSign)
			}
		})
	}
}
