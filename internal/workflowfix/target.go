package workflowfix

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/monoes/mono-agent/internal/aitask"
)

// Target is the workflow and run a failed-run-fix task is about. It comes from the
// context the app submitted with the task (structured fields, in an immutable,
// hash-verified snapshot), never from the agent's result: the agent cannot pick the
// workflow its fix is applied to.
type Target struct{ WorkflowID, ExecutionID string }

// TargetFor returns the target of task taskID in profile. The CLI sets it (it finds
// the task's verified context); unset, every plan is refused (no_target).
var TargetFor func(ctx context.Context, profile, taskID string) (Target, error)

var (
	idPattern      = `[A-Za-z0-9][A-Za-z0-9_-]{0,63}`
	workflowLine   = regexp.MustCompile(`^workflow: (` + idPattern + `)$`)
	executionLine  = regexp.MustCompile(`^execution: (` + idPattern + `)$`)
	anyTargetLine  = regexp.MustCompile(`^(workflow|execution):`)
	maxTargetBytes = int64(64 << 10)
)

// ParseTarget reads the first two lines the app writes (`workflow: <id>`,
// `execution: <id>`) of one context file. Any other line that looks like either one
// makes the file ambiguous, so it is refused.
func ParseTarget(text string) (Target, bool) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) < 2 {
		return Target{}, false
	}
	w := workflowLine.FindStringSubmatch(lines[0])
	e := executionLine.FindStringSubmatch(lines[1])
	if w == nil || e == nil {
		return Target{}, false
	}
	for _, l := range lines[2:] {
		if anyTargetLine.MatchString(l) {
			return Target{}, false
		}
	}
	return Target{WorkflowID: w[1], ExecutionID: e[1]}, true
}

// TargetFromDir finds the target in a verified context folder: exactly one file
// starts with a `workflow:` line, and it parses.
func TargetFromDir(dir string) (Target, error) {
	var found []Target
	bad := false
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return err
		}
		fi, err := d.Info()
		if err != nil || fi.Size() > maxTargetBytes {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil || !strings.HasPrefix(string(b), "workflow:") {
			return nil
		}
		if t, ok := ParseTarget(string(b)); ok {
			found = append(found, t)
		} else {
			bad = true
		}
		return nil
	})
	if err != nil {
		return Target{}, fail(CodeNoTarget, "the task's context cannot be read: %v", err)
	}
	if bad || len(found) != 1 {
		return Target{}, fail(CodeNoTarget, "the task's context does not name exactly one workflow and one run")
	}
	return found[0], nil
}

// TaskTarget is the target of the task ctx works for, or no_target.
func TaskTarget(ctx context.Context, profile string) (Target, error) {
	id := aitask.TaskIDFrom(ctx)
	if id == "" || TargetFor == nil {
		return Target{}, fail(CodeNoTarget, "the task this fix belongs to cannot be determined")
	}
	t, err := TargetFor(ctx, profile, id)
	if err != nil {
		if IsCode(err, CodeNoTarget) {
			return Target{}, err
		}
		return Target{}, fail(CodeNoTarget, "the task's target cannot be read: %v", err)
	}
	return t, nil
}

// VerifyTarget refuses a result that names another workflow or run than the task's
// target, or a run that does not belong to the workflow in this profile.
func VerifyTarget(ctx context.Context, profile, workflowID, executionID string) error {
	t, err := TaskTarget(ctx, profile)
	if err != nil {
		return err
	}
	if workflowID != t.WorkflowID || executionID != t.ExecutionID {
		return fail(CodeWrongTarget, "the result names workflow %q and run %q, but this task is about workflow %q and run %q: use the ids from the goal", workflowID, executionID, t.WorkflowID, t.ExecutionID)
	}
	if Store == nil {
		return fail(CodeNoBackend, "workflows cannot be read here")
	}
	owner, err := Store.ExecutionWorkflow(ctx, profile, t.ExecutionID)
	if err != nil {
		return err
	}
	if owner != t.WorkflowID {
		return fail(CodeWrongTarget, "run %q is not a run of workflow %q in this profile", t.ExecutionID, t.WorkflowID)
	}
	return nil
}
