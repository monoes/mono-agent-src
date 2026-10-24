// Package workflowaitask is `workflow apply-fix` and `workflow save-draft`: the
// user's side of the AI task kinds failed-run-fix and workflow-generate. The
// agent of those kinds only proposes (its result is data); these commands turn
// a finished proposal into a change, only for the operator, after the user has
// looked at it (apply-fix --dry-run shows the exact edits).
//
// They take only a task id. The result is read from the task row, checked again
// against the workflow as it is NOW, and refused when the workflow changed since
// the task completed (stale_proposal) or is active (workflow_active_needs_confirm,
// unless --allow-active, which the app passes only after a separate confirm).
// Each proposal is applied once: ai_task_applies (migration 123) records who,
// which task, which workflow and how many edits, and its primary key refuses a
// second apply. Neither command approves what it writes: the workflow stays
// unapproved until the user saves or activates it themselves.
package workflowaitask

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/aitaskkinds"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/monoes/mono-agent/internal/workflowfix"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	JSONOutput func() bool
	ProfileID  func() string
	OpenDB     func() (*storage.Database, error)
	// RequireOperator refuses an agent, the chat's runs and workflow nodes.
	RequireOperator func(what string) error
	// WorkflowStore is the profile's workflows, files and SQLite alike.
	WorkflowStore func(db *storage.Database) *workflow.HybridWorkflowStore
	// Wire runs before a command reads workflows (it sets up workflowfix).
	Wire func()
}

// Typed refusals, in --json as "code". The codes of workflowfix (unknown_node,
// secret_key, invalid_workflow, stale_proposal, workflow_active_needs_confirm, no_edit, no_change)
// pass through unchanged.
const (
	CodeNotFound    = "not_found"
	CodeWrongKind   = "wrong_kind"
	CodeNotComplete = "not_complete"
	CodeNoResult    = "no_result"
	CodeBadResult   = "bad_result"
	CodeBadTaskID   = "invalid_input"
	CodeApplied     = "already_applied"
)

// taskIDPattern is what a task id looks like; anything else is refused before the database is opened.
var taskIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// hashPattern is what --expect-hash may be: it starts alphanumeric, so a value that looks
// like a flag (the next flag swallowed as this one's value) is refused, not obeyed.
var hashPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_-]{7,127}$`)

// checkInputs validates the positional task id and --expect-hash before anything else runs.
func checkInputs(cmd *cobra.Command, taskID string) error {
	if !taskIDPattern.MatchString(taskID) {
		return refuse(CodeBadTaskID, "%q is not an AI task id", taskID)
	}
	if f := cmd.Flags().Lookup("expect-hash"); f != nil && f.Changed && !hashPattern.MatchString(f.Value.String()) {
		return refuse(CodeBadTaskID, "--expect-hash %q is not a definitionHash (8 to 128 characters, starting with a letter or digit)", f.Value.String())
	}
	return nil
}

type coded struct {
	exit   int
	msg    string
	fields map[string]any
}

func (e *coded) Error() string                   { return e.msg }
func (e *coded) JSONErrorFields() map[string]any { return e.fields }
func (e *coded) Unwrap() error                   { return &clicore.Error{Code: e.exit, Msg: e.msg} }

func refuse(code, format string, a ...any) error {
	exit := 3
	if code == CodeNotFound {
		exit = 2
	}
	return &coded{exit: exit, msg: fmt.Sprintf(format, a...), fields: map[string]any{"code": code}}
}

// cliErr gives a workflowfix refusal its exit code and --json code.
func cliErr(err error) error {
	var fe *workflowfix.Error
	if errors.As(err, &fe) {
		e := refuse(fe.Code, "%s", fe.Message).(*coded)
		for k, v := range fe.Fields {
			e.fields[k] = v
		}
		return e
	}
	return err
}

func (d Deps) print(cmd *cobra.Command, v any, human string) error {
	if d.JSONOutput() {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(v)
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), human)
	return err
}

// task returns this profile's finished task id of the given kind, with its
// stored result. The result is read from the task row only, never from a path.
func (d Deps) task(db *storage.Database, id, kind string) (aitask.Task, error) {
	if !taskIDPattern.MatchString(id) {
		return aitask.Task{}, refuse(CodeBadTaskID, "%q is not an AI task id", id)
	}
	t, err := (&aitask.Store{DB: db.DB, Profile: d.ProfileID()}).Get(id)
	if err != nil {
		var ae *aitask.Error
		if errors.As(err, &ae) && ae.Code == aitask.CodeNotFound {
			return t, refuse(CodeNotFound, "no AI task %s in this profile", id)
		}
		return t, err
	}
	switch {
	case t.Kind != kind:
		return t, refuse(CodeWrongKind, "task %s is a %s task, not %s", id, t.Kind, kind)
	case t.Status != aitask.StatusComplete:
		return t, refuse(CodeNotComplete, "task %s is %s: only a complete task has a result to use", id, t.Status)
	case len(t.Result) == 0:
		return t, refuse(CodeNoResult, "task %s has no result", id)
	}
	if done, err := applied(db, t.ID); err != nil {
		return t, err
	} else if done {
		return t, refuse(CodeApplied, "the proposal of task %s was already applied: ask again for a new one", t.ID)
	}
	return t, nil
}

// applied says whether the proposal of task id was already applied or saved.
func applied(db *storage.Database, id string) (bool, error) {
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM ai_task_applies WHERE task_id = ?`, id).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// claimHook is the apply-once claim as a function for workflow.WithTxHook: it runs inside the
// transaction of the write it guards, so the row commits with that write or not at all. The
// primary key makes a second apply of the same proposal (even a concurrent one) fail. The row is
// the audit line: who (operator.Caller, "operator" for the user's own process), task, workflow and
// count, no value.
func (d Deps) claimHook(t aitask.Task, workflowID string, edits int) func(context.Context, *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		actor := operator.Caller()
		if actor == "" {
			actor = "operator"
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO ai_task_applies (task_id, profile_id, kind, workflow_id, edit_count, actor, applied_at) VALUES (?,?,?,?,?,?,?)`,
			t.ID, d.ProfileID(), t.Kind, workflowID, edits, actor, time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			var n int
			if qerr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_task_applies WHERE task_id = ?`, t.ID).Scan(&n); qerr == nil && n > 0 {
				return refuse(CodeApplied, "the proposal of task %s was already applied: ask again for a new one", t.ID)
			}
			return err
		}
		return nil
	}
}

// auditInconsistent records that the file of workflow id may disagree with the database after a
// failed save that could not put the previous file back. It returns err joined with a failure to
// write the record, so nothing is lost silently.
func (d Deps) auditInconsistent(db *storage.Database, t aitask.Task, workflowID string, err error) error {
	_, werr := db.DB.Exec(`INSERT INTO ai_task_apply_audit (task_id, profile_id, workflow_id, event, at) VALUES (?,?,?,?,?)`,
		t.ID, d.ProfileID(), workflowID, "file_inconsistent", time.Now().UTC().Format(time.RFC3339Nano))
	if werr != nil {
		return errors.Join(err, fmt.Errorf("recording the inconsistency: %w", werr))
	}
	return err
}

// completedAt is when the task's result was handed back.
func completedAt(t aitask.Task) time.Time {
	ts, err := time.Parse(time.RFC3339Nano, t.UpdatedAt)
	if err != nil {
		return time.Now().Add(time.Hour) // unreadable: the check can only fail, never pass by accident
	}
	return ts
}

// New returns the two commands, for `workflow`.
func New(d Deps) []*cobra.Command {
	cmds := []*cobra.Command{d.applyFix(), d.saveDraft()}
	for _, c := range cmds {
		clicore.WithJSONErrors(d.JSONOutput, c)
	}
	return cmds
}

func (d Deps) start(what string) error {
	if err := clicore.RequireOperator(d.RequireOperator, what); err != nil {
		return err
	}
	if d.Wire != nil {
		d.Wire()
	}
	return nil
}

func (d Deps) applyFix() *cobra.Command {
	var dryRun, allowActive bool
	var expectHash string
	cmd := &cobra.Command{
		Use:   "apply-fix <task-id>",
		Short: "Show or apply the workflow edits a finished failed-run-fix task proposed (the user's own)",
		Long: "Reads the result of a finished failed-run-fix task of this profile and plans its edits against the workflow as it is now. " +
			"--dry-run prints them (node, path, before, after) and warnings (workflow_active, new_outbound, credential_field, expression_added) and changes nothing. " +
			"Without it the edits are stored once; the workflow is then UNAPPROVED (a person saves or activates it to approve it again). " +
			"Refused: a workflow that changed since the task completed (stale_proposal), an active workflow unless --allow-active " +
			"(workflow_active_needs_confirm), a proposal applied before (already_applied), a proposal whose result names another workflow or run than the task (wrong_target) or is too big to display (too_large). " +
			"The apply is bound to the definition you reviewed (--expect-hash, changed_since_preview) and removes every stored approval of the workflow. " + clicore.OperatorOnlyHelp,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkInputs(cmd, args[0]); err != nil {
				return err
			}
			if err := d.start("apply an AI task's workflow fix"); err != nil {
				return err
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()
			t, err := d.task(db, args[0], aitaskkinds.FailedRunFix)
			if err != nil {
				return err
			}
			// The task, not the result, names the workflow: workflowfix reads its target from the task's context.
			ctx := aitask.WithTaskID(context.Background(), t.ID)
			since := completedAt(t)
			expect := expectHash
			if dryRun {
				expect = ""
			} else if expect == "" {
				return refuse(workflowfix.CodeExpectHash, "an apply needs --expect-hash with the definitionHash the dry-run showed")
			}
			plan, err := workflowfix.PlanExpect(ctx, d.ProfileID(), t.Result, since, expect)
			if err != nil {
				return cliErr(err)
			}
			if dryRun {
				return d.print(cmd, map[string]any{"ok": true, "workflowId": plan.WorkflowID, "workflowName": plan.WorkflowName,
					"edits": plan.Edits, "warnings": plan.Warnings, "definitionHash": plan.DefinitionHash},
					fmt.Sprintf("%d edit(s) to %s (%s); %d warning(s)", len(plan.Edits), plan.WorkflowName, plan.WorkflowID, len(plan.Warnings)))
			}
			if plan.Active && !allowActive {
				return refuse(workflowfix.CodeWorkflowActive, "workflow %q is active: confirm to apply the edit to it", plan.WorkflowName)
			}
			// The claim commits with the write, inside its transaction: nothing to clean up on failure.
			applyCtx := workflow.WithTxHook(ctx, d.claimHook(t, plan.WorkflowID, len(plan.Edits)))
			n, err := workflowfix.Apply(applyCtx, d.ProfileID(), t.Result, workflowfix.Options{AllowActive: allowActive, Since: since, ExpectHash: expectHash})
			if err != nil {
				var fe *workflowfix.Error
				if errors.As(err, &fe) && fe.Fields["file_inconsistent"] != nil {
					err = d.auditInconsistent(db, t, plan.WorkflowID, err)
				}
				return cliErr(err)
			}
			return d.print(cmd, map[string]any{"ok": true, "workflowId": plan.WorkflowID, "applied": n, "needsApproval": true},
				fmt.Sprintf("applied %d edit(s) to %s; save or activate it to approve it again", n, plan.WorkflowName))
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the edits and warnings; change nothing")
	cmd.Flags().StringVar(&expectHash, "expect-hash", "", "The definitionHash the dry-run showed (required to apply): the edit is applied only to that definition")
	cmd.Flags().BoolVar(&allowActive, "allow-active", false, "Apply to a workflow that is active (the user confirmed)")
	return cmd
}

func (d Deps) saveDraft() *cobra.Command {
	return &cobra.Command{
		Use:   "save-draft <task-id>",
		Short: "Save the workflow a finished workflow-generate task proposed, as an inactive, unapproved draft (the user's own)",
		Long: "Reads the result of a finished workflow-generate task of this profile, checks the workflow again with the validator `workflow validate` uses, " +
			"and saves it once as a NEW inactive workflow (it never overwrites one). The draft is unapproved: a person saves or activates it to approve it. " + clicore.OperatorOnlyHelp,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkInputs(cmd, args[0]); err != nil {
				return err
			}
			if err := d.start("save an AI task's generated workflow"); err != nil {
				return err
			}
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()
			t, err := d.task(db, args[0], aitaskkinds.WorkflowGenerate)
			if err != nil {
				return err
			}
			var res struct {
				Workflow json.RawMessage `json:"workflow"`
			}
			if err := json.Unmarshal(t.Result, &res); err != nil || len(res.Workflow) == 0 {
				return refuse(CodeBadResult, "task %s has no workflow in its result", t.ID)
			}
			if err := aitaskkinds.CheckResult(context.Background(), d.ProfileID(), aitaskkinds.WorkflowGenerate, t.Result); err != nil {
				return refuse(CodeBadResult, "%v", err)
			}
			doc, err := workflow.ParseWorkflowFileBytes(res.Workflow)
			if err != nil {
				return refuse(CodeBadResult, "the workflow does not parse: %v", err)
			}
			wf, err := d.save(context.Background(), db, t, doc)
			if err != nil {
				return err
			}
			// A new workflow has no approvals; revoking is the rule for everything this command writes.
			_ = d.WorkflowStore(db).RevokeApprovals(context.Background(), wf.ID)
			return d.print(cmd, map[string]any{"ok": true, "workflowId": wf.ID, "name": wf.Name, "nodeCount": len(wf.Nodes), "needsApproval": true},
				fmt.Sprintf("saved draft %s (%s), inactive; approve it by saving or activating it", wf.Name, wf.ID))
		},
	}
}

// save stores doc as a new inactive workflow of the profile under a fresh id,
// whatever id or active flag the document carries, and records no approval.
func (d Deps) save(ctx context.Context, db *storage.Database, t aitask.Task, doc workflow.Workflow) (*workflow.Workflow, error) {
	wf := &workflow.Workflow{
		ID:          uuid.New().String(),
		Name:        doc.Name,
		Description: doc.Description,
		IsActive:    false,
		Version:     1,
		ProfileID:   d.ProfileID(),
		Settings:    doc.Settings,
	}
	for _, n := range doc.Nodes {
		if n.Schema == nil {
			n.Schema, _ = workflow.LoadDefaultSchema(n.Type)
		}
		n.WorkflowID = ""
		wf.Nodes = append(wf.Nodes, n)
	}
	for _, c := range doc.Connections {
		c.WorkflowID = ""
		wf.Connections = append(wf.Connections, c)
	}
	// The apply-once claim is written in the save's own transaction.
	ctx = workflow.WithTxHook(ctx, d.claimHook(t, wf.ID, len(wf.Nodes)))
	if err := d.WorkflowStore(db).SaveWorkflow(ctx, wf); err != nil {
		var he *workflow.TxHookError
		if errors.As(err, &he) {
			return nil, he.Err
		}
		return nil, fmt.Errorf("save workflow: %w", err)
	}
	return wf, nil
}
