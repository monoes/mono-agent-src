package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/health"
	"github.com/monoes/mono-agent/internal/workflow"
)

// addWorkflowPublicationHooks feeds the doctor's workflows.publications row. Reading never
// changes anything and never creates the workflow folder; the fix runs the same recovery every
// command that opens the workflows runs.
func addWorkflowPublicationHooks(env *health.Env, requireOperator func(what string) error) {
	open := func() (*workflow.HybridWorkflowStore, error) {
		dir := filepath.Join(env.DataDir, "workflows")
		if _, err := os.Stat(dir); err != nil {
			return nil, nil
		}
		files, err := workflow.NewWorkflowFileStore(dir)
		if err != nil {
			return nil, err
		}
		return workflow.NewHybridWorkflowStore(files, workflow.NewSQLiteWorkflowStore(env.DB)), nil
	}
	env.WorkflowPublications = func(ctx context.Context) (*health.WorkflowPublicationsInfo, error) {
		info := &health.WorkflowPublicationsInfo{}
		store, err := open()
		if err != nil || store == nil || env.DB == nil {
			return info, err
		}
		pubs, err := store.OpenPublications(ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range pubs {
			info.Open = append(info.Open, health.OpenWorkflowPublication{WorkflowID: p.WorkflowID, Kind: p.Kind,
				Origin: p.Origin, State: p.State, Age: p.Age, OwnerGone: p.OwnerGone, Note: p.Note})
		}
		if info.Unreadable, info.Pending, err = store.FileDrift(ctx); err != nil {
			return info, err
		}
		if info.ImportedInactive, err = store.ImportedInactive(ctx); err != nil {
			return info, err
		}
		var shown []workflow.AdoptableFile
		shown, info.MoreAdoptable, err = store.AdoptableFiles(ctx)
		for _, a := range shown {
			info.Adoptable = append(info.Adoptable, health.AdoptableWorkflowFile{ID: a.ID, ProfileID: a.ProfileID, SHA: a.SHA, Path: a.Path, NodeTypes: a.NodeTypes, Triggers: a.Triggers, Preview: a.Preview})
		}
		return info, err
	}
	env.RecoverWorkflowPublications = func(ctx context.Context) (int, error) {
		store, err := open()
		if err != nil || store == nil || env.DB == nil {
			return 0, err
		}
		return store.RecoverPublications(ctx)
	}
	env.ResolveWorkflowPublication = func(ctx context.Context, id, keep string) error {
		// It rewrites or removes workflow files: the operator's act, not an agent's.
		if err := clicore.RequireOperator(requireOperator, "settle an unfinished workflow change"); err != nil {
			return err
		}
		store, err := open()
		if err != nil || store == nil || env.DB == nil {
			return err
		}
		return store.ResolveUnresolved(ctx, id, keep)
	}
	env.AdoptWorkflowFile = func(ctx context.Context, id, profile, sha string) error {
		// Creating a workflow from a file is the operator's act, in the profile it names.
		if err := clicore.RequireOperator(requireOperator, "adopt a workflow file"); err != nil {
			return err
		}
		if env.ProfileID == "" || env.ProfileID != profile {
			return fmt.Errorf("pass --profile %s: the file names that profile, and adopting is done in the profile it names", profile)
		}
		store, err := open()
		if err != nil {
			return err
		}
		if store == nil || env.DB == nil {
			return fmt.Errorf("there is no workflow folder or database to adopt into")
		}
		return store.AdoptFile(ctx, id, profile, sha)
	}
}
