// Package orgsvc is the org side of `monoagentcli daemon`: trigger.org, the
// automation-role endpoint receiver, the waker, report-up, the decision
// service, and one org-file watcher per profile that reconciles the files
// with their rows.
package orgsvc

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monoes/mono-agent/internal/cli/orgenv"
	"github.com/monoes/mono-agent/internal/orgbridge"
	"github.com/monoes/mono-agent/internal/orgdecide"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/orggroup"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// ProfileRoots lists every profile's org folder (the daemon spans profiles).
func ProfileRoots(db *sql.DB) ([]orgdecide.ProfileRoot, error) {
	rows, err := db.Query(`SELECT id FROM profiles`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	sort.Strings(ids)
	var out []orgdecide.ProfileRoot
	for _, id := range ids {
		if root := profiledir.Root(db, id); root != "" {
			out = append(out, orgdecide.ProfileRoot{ProfileID: id, Root: root})
		}
	}
	return out, nil
}

// Services are the org-side services `monoagentcli daemon` hosts next
// to the workflow engine.
type Services struct {
	db                *storage.Database
	openWorkflowStore func(*storage.Database) *workflow.HybridWorkflowStore
	mux               *orgbridge.Mux
	trigger           *orgbridge.TriggerSource
	receiver          *orgbridge.Receiver
	logf              func(format string, args ...interface{})

	// resyncInterval and watchInterval are 0 (the defaults) outside tests.
	resyncInterval time.Duration
	watchInterval  time.Duration

	mu       sync.Mutex
	watchers map[string]*orgWatch // by profile id; see watch.go

	active atomic.Int32 // goroutines Start launched and not yet ended; see Wait
}

// goTracked runs fn on a goroutine that Wait can see. (A sync.WaitGroup would
// do, but the supervisor waits, may give up, and starts again; reusing a
// WaitGroup while an earlier Wait is still pending is a panic.)
func (s *Services) goTracked(fn func()) {
	s.active.Add(1)
	go func() {
		defer s.active.Add(-1)
		fn()
	}()
}

// Wait reports whether every goroutine Start launched has ended, waiting up to
// timeout for them to. They end when the context Start was given ends.
func (s *Services) Wait(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for s.active.Load() > 0 {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}

// New registers trigger.org with the engine; call before engine.Start so
// restored workflows activate it. open opens the CLI's workflow store.
func New(db *storage.Database, engine *workflow.WorkflowEngine, open func(*storage.Database) *workflow.HybridWorkflowStore) *Services {
	mux := orgbridge.NewMux(nil)
	s := &Services{
		db: db, openWorkflowStore: open, mux: mux, trigger: orgbridge.NewTriggerSource(mux, db.DB),
		logf: func(format string, args ...interface{}) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	}
	s.trigger.Logf = s.logf
	engine.RegisterTriggerSource(orgbridge.TriggerNodeType, s.trigger)
	engine.SetTraceAdmitter(s.admitWebhookTrace)
	s.receiver = &orgbridge.Receiver{
		DB: db.DB, Store: open(db), Mux: mux, Resume: engine.ResumeExecution, Logf: s.logf,
		RootOf: func(profileID string) string { return profiledir.Root(db.DB, profileID) },
	}
	return s
}

// Logf logs a line the way the services do (stderr in the daemon).
func (s *Services) Logf(format string, args ...interface{}) { s.logf(format, args...) }

// RegisterRoutes mounts the automation-role endpoint receiver on the
// daemon's HTTP API.
func (s *Services) RegisterRoutes(mux *http.ServeMux) { s.receiver.Register(mux) }

// Start runs reconcile and the org file watchers (watch.go), the waker, and the decision
// service until ctx ends.
func (s *Services) Start(ctx context.Context, engine *workflow.WorkflowEngine) {
	s.watchOrgFiles(ctx)

	waker := &orgbridge.Waker{
		DB: s.db.DB, Mux: s.mux, Resume: engine.ResumeExecution,
		RootOf: func(profileID string) string { return profiledir.Root(s.db.DB, profileID) },
		Logf:   s.logf,
	}
	s.goTracked(func() { waker.Run(ctx) })
	s.goTracked(func() { s.receiver.Run(ctx) })

	reportUp := &orggroup.ReportUpWatcher{
		DB: s.db.DB, Mux: s.mux, Logf: s.logf,
		Roots: func(context.Context) ([]orggroup.ProfileRoot, error) {
			roots, err := ProfileRoots(s.db.DB)
			out := make([]orggroup.ProfileRoot, 0, len(roots))
			for _, r := range roots {
				out = append(out, orggroup.ProfileRoot{ProfileID: r.ProfileID, Root: r.Root})
			}
			return out, err
		},
	}
	s.goTracked(func() { reportUp.Run(ctx) })

	svc := orgdecide.NewService(s.db.DB, func(context.Context) ([]orgdecide.ProfileRoot, error) {
		return ProfileRoots(s.db.DB)
	})
	svc.Logf = s.logf
	wfs := orgenv.GrantWorkflows(s.db, s.openWorkflowStore)
	svc.LoadWorkflow, svc.ListWorkflows = wfs.GetWorkflow, wfs.ListWorkflows
	svc.WorkflowFacts = func(ctx context.Context, profileID, workflowID string) string {
		return s.workflowFacts(ctx, s.db, workflowID)
	}
	s.goTracked(func() { svc.Run(ctx) })
}

// reconciler reconciles org files for the daemon (orgenv.Reconciler): its
// own saves are marked so the watcher does not reconcile them again.
func (s *Services) reconciler() *orgenv.Reconciler {
	return &orgenv.Reconciler{DB: s.db, OpenWorkflowStore: s.openWorkflowStore, Logf: s.logf, Saved: s.markSelfWrite, Unsigned: true}
}

// reconcileProfile reconciles every org of a profile (orgenv.Reconciler.Profile).
func (s *Services) reconcileProfile(ctx context.Context, pr orgdecide.ProfileRoot) []orgenv.ReconcileOutcome {
	return s.reconciler().Profile(ctx, pr)
}

// reconcileDoc reconciles one org file (orgenv.Reconciler.Doc).
func (s *Services) reconcileDoc(ctx context.Context, pr orgdecide.ProfileRoot, d *orgdesign.Doc, force bool) orgenv.ReconcileOutcome {
	return s.reconciler().Doc(ctx, pr, d, force)
}

// workflowFacts describes a workflow for the decider prompt from the DB.
func (s *Services) workflowFacts(ctx context.Context, db *storage.Database, workflowID string) string {
	wf, err := s.openWorkflowStore(db).GetWorkflow(ctx, workflowID)
	if err != nil || wf == nil {
		return "workflow " + workflowID + " (not found)"
	}
	types := make([]string, 0, len(wf.Nodes))
	for _, n := range wf.Nodes {
		types = append(types, n.Type)
	}
	out := orggrant.OutboundNodes(wf)
	outbound := "none"
	if len(out) > 0 {
		outbound = strings.Join(out, ", ")
	}
	return fmt.Sprintf("workflow %q (%s); nodes: %s; outbound nodes: %s", wf.Name, wf.ID, strings.Join(types, ", "), outbound)
}

// admitWebhookTrace records a webhook request that carried a verified
// signed trace (internal/tracesig) as a crossing on its chain, so a loop
// that leaves through an HTTP request and comes back through a webhook
// climbs like any other and stops at the hop limit. The target is a
// workflow, not an org, so the hop limit is the run_config.max_hops of the
// org the chain started in (orgbridge.Ledger.ChainOrigin), and the defaults
// for a chain no org started (a webhook run's own). The repeat limit is
// orgbridge.TriggerRepeats whatever the org says.
func (s *Services) admitWebhookTrace(ctx context.Context, workflowID, chain string, hop int) (int, string, error) {
	wf, err := s.openWorkflowStore(s.db).GetWorkflow(ctx, workflowID)
	if err != nil {
		return 0, "", fmt.Errorf("workflow %s: %w", workflowID, err)
	}
	if wf == nil || wf.ProfileID == "" {
		return 0, "", fmt.Errorf("workflow %s has no profile, so its trace cannot be attributed", workflowID)
	}
	profile := wf.ProfileID
	ledger := orgbridge.NewLedger(s.db.DB)
	origin, err := ledger.ChainOrigin(ctx, profile, chain)
	if err != nil {
		return 0, "", err
	}
	var lim orgbridge.Limits
	if origin != "" {
		lim = orgbridge.LimitsFor(profiledir.Root(s.db.DB, profile), origin)
	}
	adm, err := ledger.Admit(ctx, orgbridge.Call{
		ProfileID: profile, Trace: orgbridge.Trace{ChainID: chain, Hop: hop}, OriginOrg: origin,
		Direction: orgbridge.DirWebhookIn, WorkflowID: workflowID,
	}, lim)
	if err != nil {
		return 0, "", err
	}
	if !adm.OK() {
		return 0, adm.Reason, nil
	}
	return adm.Trace.Hop, "", nil
}
