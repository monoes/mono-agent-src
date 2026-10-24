package aitaskkinds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/ai/generate"
	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/monoes/mono-agent/internal/workflowfix"
)

// RecordingExists says whether recording id is in profile. The CLI sets it; nil
// means it cannot be checked, which refuses the result rather than trusting it.
var RecordingExists func(ctx context.Context, profile, id string) (bool, error)

// CheckResult checks a result of kind against its schema and the caps on its
// strings and lists (aitask.Validate leaves maxLength and maxItems alone), and
// then the check of the kind, which looks at the profile's own data:
//
//   - workflow-generate: the workflow passes the validator `workflow validate`
//     uses (the verdict the agent wrote is ignored), is a draft (is_active
//     false) and holds no secret-looking value;
//   - failed-run-fix: the workflow exists in the profile and, for a
//     workflow-edit, the edits plan cleanly against it as it is now
//     (workflowfix.Plan): a refusal says why, so the agent can fix it;
//   - recording-analyze: the recording exists in the profile.
//
// It returns nil for a kind it has no checks for. Kind.Check calls it.
func CheckResult(ctx context.Context, profile, kind string, raw []byte) error {
	k, err := aitask.Lookup(kind)
	if err != nil {
		return err
	}
	if _, mine := Descriptions[kind]; !mine {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("the result is not valid JSON: %w", err)
	}
	if err := aitask.Validate(k.Schema, v); err != nil {
		return err
	}
	if err := checkCaps("$", k.Schema, v); err != nil {
		return err
	}
	obj, _ := v.(map[string]any)
	switch kind {
	case WorkflowGenerate:
		return checkGeneratedWorkflow(obj)
	case FailedRunFix:
		return checkFailedRunFix(ctx, profile, raw, obj)
	case RecordingAnalyze:
		id, _ := obj["recordingId"].(string)
		if RecordingExists == nil {
			return fmt.Errorf("cannot check that recording %q exists in this profile", id)
		}
		ok, err := RecordingExists(ctx, profile, id)
		if err != nil {
			return fmt.Errorf("checking recording %q: %w", id, err)
		}
		if !ok {
			return fmt.Errorf("$.recordingId: no recording %q in this profile: use the id from the goal", id)
		}
	}
	return nil
}

// checkGeneratedWorkflow recomputes the verdict with the same validator as
// `workflow validate` (generate.Validator over workflowfix.NodeTypeKnown).
func checkGeneratedWorkflow(obj map[string]any) error {
	raw, err := json.Marshal(obj["workflow"])
	if err != nil {
		return fmt.Errorf("workflow: %v", err)
	}
	wf, err := workflow.ParseWorkflowFileBytes(raw)
	if err != nil {
		return fmt.Errorf("workflow is not a workflow JSON document: %v", err)
	}
	if wf.IsActive {
		return errors.New("workflow.is_active must be false: the user saves a generated workflow after reviewing it")
	}
	for _, n := range wf.Nodes {
		if n.Type == "" {
			return fmt.Errorf("workflow: node %q has no type", n.ID)
		}
		if bad := secretValue(n.Config); bad != "" {
			return fmt.Errorf("workflow: node %q holds a secret-looking value at %q: use REPLACE_ME or a saved connection (credential_id)", n.ID, bad)
		}
	}
	errs, _ := generate.Validator(workflowfix.NodeTypeKnown)(&wf)
	if len(errs) > 0 {
		return fmt.Errorf("the workflow does not pass `workflow validate`:\n- %s", strings.Join(errs, "\n- "))
	}
	return nil
}

// secretValue returns the path of the first string in m that redaction changes.
func secretValue(m map[string]any) string {
	var walk func(path string, v any) string
	walk = func(path string, v any) string {
		switch t := v.(type) {
		case string:
			if aitask.RedactText(t) != t {
				return path
			}
		case map[string]any:
			for k, e := range t {
				if p := walk(path+"."+k, e); p != "" {
					return p
				}
			}
		case []any:
			for i, e := range t {
				if p := walk(fmt.Sprintf("%s[%d]", path, i), e); p != "" {
					return p
				}
			}
		}
		return ""
	}
	return walk("config", m)
}

func checkFailedRunFix(ctx context.Context, profile string, raw []byte, obj map[string]any) error {
	fix, _ := obj["fix"].(map[string]any)
	// The task, not the result, says which workflow and run this is about.
	wfID, _ := obj["workflowId"].(string)
	exID, _ := obj["executionId"].(string)
	if err := workflowfix.VerifyTarget(ctx, profile, wfID, exID); err != nil {
		return err
	}
	if kind, _ := fix["kind"].(string); kind == workflowfix.KindWorkflowEdit {
		if _, err := workflowfix.Plan(ctx, profile, raw); err != nil {
			return err
		}
		return nil
	}
	if edits, _ := fix["edits"].([]any); len(edits) > 0 {
		return errors.New("fix.edits is only for kind workflow-edit")
	}
	id, _ := obj["workflowId"].(string)
	ok, err := workflowfix.Exists(ctx, profile, id)
	if err != nil {
		return fmt.Errorf("checking workflow %q: %w", id, err)
	}
	if !ok {
		return fmt.Errorf("$.workflowId: no workflow %q in this profile: use the id from the goal", id)
	}
	return nil
}

func checkCaps(path string, schema map[string]any, v any) error {
	switch t := v.(type) {
	case string:
		if n, ok := schema["maxLength"].(float64); ok && float64(len([]rune(t))) > n {
			return fmt.Errorf("%s: must be at most %d characters", path, int(n))
		}
	case []any:
		if n, ok := schema["maxItems"].(float64); ok && float64(len(t)) > n {
			return fmt.Errorf("%s: must have at most %d items", path, int(n))
		}
		if items, ok := schema["items"].(map[string]any); ok {
			for i, e := range t {
				if err := checkCaps(fmt.Sprintf("%s[%d]", path, i), items, e); err != nil {
					return err
				}
			}
		}
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		for k, e := range t {
			if sub, ok := props[k].(map[string]any); ok {
				if err := checkCaps(path+"."+k, sub, e); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
