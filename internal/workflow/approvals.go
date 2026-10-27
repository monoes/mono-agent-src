package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/attachments"
	"github.com/monoes/mono-agent/internal/fsconfine"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/vault"
)

// Operator approval of workflow definitions (#831 review C). A run of a
// definition the operator never approved — one the chat, an agent or a
// workflow made or changed since the operator last saved or activated the
// workflow from the app or their own terminal — is an AI run, whatever
// triggered it: RunExecution marks its context (AIAuthoredFrom), and what
// refuses AI runs (internal/operator.AIRun) refuses it. The approvals live in
// workflow_approvals (migration 106), outside the definition, so no import
// or settings key can claim one; the operator's commands record them
// (a publication made with WithApproval, internal/operator.ApprovingContext).

type aiAuthoredKey struct{}

// WithAIAuthored marks ctx as a run of a definition the operator never
// approved. No secret resolves in it (secrets.RefuseResolving): a step it
// runs could send the value anywhere, or show it in an error or an output.
func WithAIAuthored(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, aiAuthoredKey{}, true)
	return secrets.RefuseResolving(ctx, unapprovedSecrets(ctx, ApproveHint("", "")))
}

// unapprovedSecrets is why a run of a definition the operator never
// approved reads no secret: approve says how the user approves it, or,
// when whether they did could not be checked (ApprovalCheckError), it says
// that, as RefuseUnapprovedProgram does.
func unapprovedSecrets(ctx context.Context, approve string) string {
	if err := ApprovalCheckError(ctx); err != nil {
		return fmt.Sprintf("whether you approved this workflow could not be checked (%v): run it again", err)
	}
	return "the chat, an agent or another workflow wrote or changed this workflow since you last saved or activated it: " + approve
}

// AIAuthoredFrom reports whether ctx is a run of a definition the operator
// never approved.
func AIAuthoredFrom(ctx context.Context) bool {
	v, _ := ctx.Value(aiAuthoredKey{}).(bool)
	return v
}

// WithUnapproved marks ctx as a run of a definition the operator never
// approved (WithAIAuthored) and keeps its file steps to FileGuard: out of
// the hidden files of the home outside project, the profile's project
// folder, and out of what runs code later (fsconfine.GuardHiddenHome).
// approve says how the user approves it, for the refusals.
func WithUnapproved(ctx context.Context, project, approve string) context.Context {
	ctx = WithAIAuthored(ctx)
	ctx = secrets.RefuseResolving(ctx, unapprovedSecrets(ctx, approve))
	return fsconfine.GuardHiddenHome(ctx, FileGuard(project, approve))
}

// WithUnapprovedStep is WithUnapproved for one step an AI configured and
// runs on its own, outside any workflow (MCP node_run, `node run` from an
// agent's shell, the chat or a workflow): there is no workflow the user
// could approve. The profile's agent-access grant can allow secret use;
// the step keeps the remaining unapproved-workflow restrictions, scope and
// audit. tool names the command.
func WithUnapprovedStep(ctx context.Context, project, tool string) context.Context {
	ctx = WithUnapproved(ctx, project, tool+" never does that for an AI: put the step in a workflow and have the user save or activate it")
	profile, _ := vault.ProfileIDFromContext(ctx)
	db := vault.DBFromContext(ctx)
	if grant, err := secrets.GetAgentAccess(ctx, db, profile); err == nil && grant != nil && ApprovalCheckError(ctx) == nil {
		return secrets.AllowResolving(ctx, db, profile, secrets.AccessOrigin{})
	}
	return secrets.RefuseResolving(ctx, tool+" runs a step an AI wrote, not one the user approved: put the step in a workflow and have the user save or activate it")
}

// FileGuard is the fsconfine.Guard of a workflow the user hasn't approved,
// or one it called: project is the profile's project folder; the synced
// message attachments may be read and the CLI's export folder
// (~/.monoagent/output) read and written, though both lie in ~/.monoagent.
func FileGuard(project, approve string) fsconfine.Guard {
	g := fsconfine.Guard{Project: project, ReadOK: []string{attachments.Dir()}, Approve: approve}
	if home, err := os.UserHomeDir(); err == nil {
		g.WriteOK = []string{filepath.Join(home, ".monoagent", "output")}
	}
	return g
}

// ApproveHint says how the user approves workflow id of profile, for a
// refusal: "" fields leave the generic advice.
func ApproveHint(profile, id string) string {
	if profile == "" || id == "" {
		return "save or activate the workflow in the app or your terminal to let it"
	}
	return fmt.Sprintf("if the change is yours, run `monoagentcli --profile %s workflow activate %s` (or save it in the app) to let it", profile, id)
}

// ActivateHint tells the operator how to switch on a definition they have not approved: look at it
// first, then name what was looked at, so what gets approved is what was reviewed.
func ActivateHint(profile, id string) string {
	if profile == "" || id == "" {
		return "review it in the app or your terminal, then activate it from there"
	}
	return fmt.Sprintf("review it with `monoagentcli --profile %s workflow get %s --json`, then run `monoagentcli --profile %s workflow activate %s --expect <its review_hash>` to approve exactly that", profile, id, profile, id)
}

type approvalCheckErrorKey struct{}

// withApprovalCheckError records on ctx the store error that kept the run
// from telling whether the operator approved its definition (nil: none).
func withApprovalCheckError(ctx context.Context, err error) context.Context {
	if err == nil {
		return ctx
	}
	return context.WithValue(ctx, approvalCheckErrorKey{}, err)
}

// ApprovalCheckError is the store error that kept the run in ctx from
// telling whether the operator approved its definition, so it runs as
// unapproved (AIAuthoredFrom); nil when the check worked. A refusal names it.
func ApprovalCheckError(ctx context.Context) error {
	err, _ := ctx.Value(approvalCheckErrorKey{}).(error)
	return err
}

// ErrUnapprovedProgram is wrapped by RefuseUnapprovedProgram's refusal.
var ErrUnapprovedProgram = errors.New("a workflow the user has not approved runs no programs on this computer")

// RefuseUnapprovedProgram refuses step what, one that runs a program on this
// computer (a command, a Python script, a local MCP server, ssh to this
// machine), in a run of a definition the operator never approved
// (AIAuthoredFrom). Such a step can do anything the user can, the approvals
// only the user gives included, so what the chat, an agent or another
// workflow wrote runs none until the user approves the workflow. A workflow
// the user approved runs these steps as before.
func RefuseUnapprovedProgram(ctx context.Context, what, workflowID string) error {
	if !AIAuthoredFrom(ctx) {
		return nil
	}
	if err := ApprovalCheckError(ctx); err != nil {
		return fmt.Errorf("%s runs a program on this computer, and whether you approved this workflow could not be checked (%v): run it again: %w",
			what, err, ErrUnapprovedProgram)
	}
	profile, _ := vault.ProfileIDFromContext(ctx)
	return fmt.Errorf("%s runs a program on this computer, and the chat, an agent or another workflow wrote or changed this workflow since you last saved or activated it: "+
		"if the change is yours, run `monoagentcli --profile %s workflow activate %s` (or save it in the app): %w", what, profile, workflowID, ErrUnapprovedProgram)
}

// definitionApprover is a store that keeps the operator's approvals.
type definitionApprover interface {
	DefinitionApproved(ctx context.Context, workflowID, hash string) (bool, error)
}

// operatorApproved reports whether wf's definition is one the operator
// approved. A store that keeps no approvals approves everything. A store
// error is tried once more, since a transient one would refuse a legitimate
// run's steps; one that persists approves nothing and is returned.
func operatorApproved(ctx context.Context, store WorkflowStore, wf *Workflow) (bool, error) {
	a, ok := store.(definitionApprover)
	if !ok {
		return true, nil
	}
	hash, err := definitionHash(wf)
	if err != nil {
		return false, err
	}
	approved, err := a.DefinitionApproved(ctx, wf.ID, hash)
	if err != nil {
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
		approved, err = a.DefinitionApproved(ctx, wf.ID, hash)
	}
	return err == nil && approved, err
}

// IsApproved reports whether the operator approved wf's current definition: the
// check a run makes, for a command that wants to say whether the user still has
// to approve a change. A store that keeps no approvals approves everything.
func IsApproved(ctx context.Context, store WorkflowStore, wf *Workflow) (bool, error) {
	return operatorApproved(ctx, store, wf)
}

// DefinitionApproved reports whether the operator approved definition hash
// of the workflow. A database from before approvals (no table: a test's
// schema) approves everything.
func (s *SQLiteWorkflowStore) DefinitionApproved(ctx context.Context, workflowID, hash string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_approvals WHERE workflow_id = ? AND definition_hash = ?`,
		workflowID, hash).Scan(&n)
	if err != nil && strings.Contains(err.Error(), "no such table") {
		return true, nil
	}
	return n > 0, err
}

// ApproveDefinition records the workflow's current definition as the
// operator's. Only the operator's own commands call it.
func (s *SQLiteWorkflowStore) ApproveDefinition(ctx context.Context, workflowID string) error {
	wf, err := s.GetWorkflow(ctx, workflowID)
	if err != nil || wf == nil {
		return err
	}
	return s.approve(ctx, wf)
}

func (s *SQLiteWorkflowStore) approve(ctx context.Context, wf *Workflow) error {
	hash, err := definitionHash(wf)
	if err != nil {
		return err
	}
	// A workflow with no SQLite row (a file nothing synced yet) can't run,
	// and its approval would have nothing to go with on delete.
	_, err = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO workflow_approvals (workflow_id, definition_hash)
		SELECT ?, ? WHERE EXISTS (SELECT 1 FROM workflows WHERE id = ?)`, wf.ID, hash, wf.ID)
	return err
}

// DefinitionApproved is the SQLite store's: approvals live there.
func (h *HybridWorkflowStore) DefinitionApproved(ctx context.Context, workflowID, hash string) (bool, error) {
	return h.sql.DefinitionApproved(ctx, workflowID, hash)
}

// ApproveDefinition records the definition the engine would run (the file's,
// else SQLite's) as the operator's.
func (h *HybridWorkflowStore) ApproveDefinition(ctx context.Context, workflowID string) error {
	wf, err := h.GetWorkflow(ctx, workflowID)
	if err != nil || wf == nil {
		return err
	}
	return h.sql.approve(ctx, wf)
}

// ApproveDefinition records the workflow's current definition as the
// operator's, through the engine's store (when it keeps approvals).
func (e *WorkflowEngine) ApproveDefinition(ctx context.Context, workflowID string) error {
	if a, ok := e.store.(interface {
		ApproveDefinition(ctx context.Context, workflowID string) error
	}); ok {
		return a.ApproveDefinition(ctx, workflowID)
	}
	return nil
}
