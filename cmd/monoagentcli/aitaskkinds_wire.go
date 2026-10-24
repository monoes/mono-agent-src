package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/aitaskkinds"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/recording"
	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/monoes/mono-agent/internal/workflowfix"
)

// cliWorkflowBackend reads and edits the profile's workflows the way `workflow get` and
// `workflow node set` do (workflowio.OwnedWorkflow's ownership rule). Its SaveNodes records no
// approval: an edit made through it leaves the workflow unapproved.
type cliWorkflowBackend struct{ cfg *globalConfig }

func (b cliWorkflowBackend) Load(ctx context.Context, profile, id string) (*workflow.Workflow, error) {
	db, err := initDB(b.cfg)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	return ownedWorkflow(ctx, openWorkflowStore(db), db.DB, profile, id), nil
}

func (b cliWorkflowBackend) ExecutionWorkflow(ctx context.Context, profile, executionID string) (string, error) {
	db, err := initDB(b.cfg)
	if err != nil {
		return "", fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	store := openWorkflowStore(db)
	ex, err := store.GetExecution(ctx, executionID)
	if err != nil || ex == nil {
		return "", nil
	}
	if ownedWorkflow(ctx, store, db.DB, profile, ex.WorkflowID) == nil {
		return "", nil
	}
	return ex.WorkflowID, nil
}

func (b cliWorkflowBackend) SaveNodesGuarded(ctx context.Context, profile, id string, nodes []workflow.WorkflowNode, guard workflow.SaveGuard) error {
	db, err := initDB(b.cfg)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	store := openWorkflowStore(db)
	if ownedWorkflow(ctx, store, db.DB, profile, id) == nil {
		return fmt.Errorf("workflow %q not found", id)
	}
	return store.SaveNodesGuarded(ctx, id, nodes, guard)
}

var nodeTypesOnce struct {
	sync.Once
	reg *workflow.NodeTypeRegistry
}

// wireAitaskKinds gives the AI task kinds what their completion checks look at: the profile's
// workflows, the node types `workflow validate` knows, and the profile's recordings. Anything
// that cannot be read refuses the result.
func wireAitaskKinds(cfg *globalConfig) {
	workflowfix.Store = cliWorkflowBackend{cfg: cfg}
	workflowfix.NodeTypeKnown = func(t string) bool {
		nodeTypesOnce.Do(func() { nodeTypesOnce.reg = buildNodeRegistry(false, nil) })
		return nodeTypesOnce.reg.Has(t)
	}
	workflowfix.TargetFor = func(ctx context.Context, profile, taskID string) (workflowfix.Target, error) {
		db, err := initDB(cfg)
		if err != nil {
			return workflowfix.Target{}, fmt.Errorf("open database: %w", err)
		}
		defer db.Close()
		t, err := (&aitask.Store{DB: db.DB, Profile: profile}).Get(taskID)
		if err != nil {
			return workflowfix.Target{}, err
		}
		dir, err := aitask.VerifiedContext(filepath.Dir(clicore.ExpandPath(cfg.DBPath)), t)
		if err != nil {
			return workflowfix.Target{}, err
		}
		return workflowfix.TargetFromDir(dir)
	}
	aitaskkinds.RecordingExists = func(_ context.Context, profile, id string) (bool, error) {
		if err := recording.SetProfile(profile); err != nil {
			return false, err
		}
		_, err := recording.Find(id)
		if err == recording.ErrNotFound {
			return false, nil
		}
		return err == nil, err
	}
}
