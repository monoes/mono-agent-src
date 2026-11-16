package orgenv

import (
	"context"
	"sort"

	"github.com/monoes/mono-agent/internal/orgdecide"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/orggroup"
	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Reconciler reconciles a profile's org files with their grant, endpoint
// and autonomy rows: the daemon at startup and when a file changes, and
// `org reconcile` and `org teardown-profile`.
type Reconciler struct {
	DB *storage.Database
	// OpenWorkflowStore opens the store granted workflows load from.
	OpenWorkflowStore func(*storage.Database) *workflow.HybridWorkflowStore
	Logf              func(format string, args ...interface{})
	// Saved, when set, is told about each reconciled file saved: the
	// daemon's watcher then skips its own write.
	Saved func(profileID, name, sha string)
	// Unsigned makes every save leave the org's signature for the operator to review. The daemon sets
	// it: it has no marker or turn verdict, and the rows it reconciles from (a grant revoked, an
	// enforcement row) can have been written by a coder turn. Changes the operator made through the
	// CLI are saved signed by that command itself, under the rows lock, before the daemon looks.
	Unsigned bool
}

// Profile reconciles every org of a profile at startup, so grants,
// providers, approvalTools, and autonomy agree with the enforcement rows
// even after the org files were edited while no daemon ran (C-3, C-54).
// `org reconcile` runs the same pass after a profile folder moves (C-24).
func (r *Reconciler) Profile(ctx context.Context, pr orgdecide.ProfileRoot) []ReconcileOutcome {
	docs, bad, err := orgdesign.LoadAll(pr.Root)
	if err != nil {
		return nil
	}
	var out []ReconcileOutcome
	for name, e := range bad {
		r.Logf("org services: %s/%s: unreadable org file: %v", pr.ProfileID, name, e)
		out = append(out, ReconcileOutcome{Org: name, Error: "unreadable org file: " + e.Error()})
	}
	forced := map[string]bool{}
	for _, d := range orggroup.ApplyReportUp(docs) {
		forced[d.Name] = true
	}
	for _, d := range docs {
		out = append(out, r.Doc(ctx, pr, d, forced[d.Name]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Org < out[j].Org })
	return out
}

// Doc reconciles one org file; force saves it even when nothing changed.
func (r *Reconciler) Doc(ctx context.Context, pr orgdecide.ProfileRoot, d *orgdesign.Doc, force bool) ReconcileOutcome {
	res := ReconcileOutcome{Org: d.Name, Findings: []orggrant.Finding{}}
	// A command writing a row and then this file holds the rows lock until both are written. d was
	// read before the lock, so it can be the file without that command's display copy; read it again.
	release, err := LockRows(pr.Root)
	if err != nil {
		r.Logf("org services: reconcile %s: %v", d.Name, err)
		res.Error = err.Error()
		return res
	}
	defer release()
	fresh, err := orgdesign.Load(pr.Root, d.Name)
	if err != nil {
		// Gone or unreadable since it was read: reconciling the stale copy could save it back over a
		// deleted or half-edited file.
		r.Logf("org services: reconcile %s: reload under the rows lock: %v", d.Name, err)
		res.Error = "reload under the rows lock: " + err.Error()
		return res
	}
	d = fresh
	fail := func(format string, err error) ReconcileOutcome {
		r.Logf(format, d.Name, err)
		res.Error = err.Error()
		return res
	}
	rep, err := orggrant.Reconcile(ctx, orggrant.NewStore(r.DB.DB), d, orggrant.GenOptions{
		ProfileID: pr.ProfileID, CLIPath: SelfExecutable(), APIAddr: APIAddr(r.DB),
		Workflow: GrantWorkflowLoader(r.DB, r.OpenWorkflowStore),
	})
	if err != nil {
		return fail("org services: reconcile %s: %v", err)
	}
	res.Findings = append(res.Findings, rep.Findings...)
	auto, err := orgdecide.ReconcileAutonomy(ctx, orgdecide.NewStore(r.DB.DB), pr.ProfileID, d)
	if err != nil {
		return fail("org services: autonomy reconcile %s: %v", err)
	}
	if auto.Ignored != "" {
		r.Logf("org services: %s: %s", d.Name, auto.Ignored)
		res.Findings = append(res.Findings, orggrant.Finding{Kind: "autonomy_raise_ignored", Detail: auto.Ignored})
	}
	if !force && !rep.Changed && !auto.DocChanged {
		return res
	}
	for _, f := range rep.Findings {
		r.Logf("org services: %s: %s %s", d.Name, f.Kind, f.Detail)
	}
	// An org edited outside mono-agent fails the signature check first, so
	// this write never signs that edit (orgsign.Before).
	var (
		sha string
		sig *orgsign.Outcome
	)
	if r.Unsigned {
		sha, sig, err = SaveUnsigned(ctx, pr.Root, d, "the daemon reconciled it from enforcement rows whose writer it cannot tell from a coder turn")
	} else {
		sha, sig, err = SaveSigned(ctx, pr.Root, d.Name, d)
	}
	if err != nil {
		return fail("org services: saving reconciled %s: %v", err)
	}
	if sig != nil && sig.Notice != "" {
		r.Logf("org services: %s", sig.Notice)
	}
	res.Signature = sig
	res.Saved = true
	if r.Saved != nil {
		r.Saved(pr.ProfileID, d.Name, sha)
	}
	return res
}
