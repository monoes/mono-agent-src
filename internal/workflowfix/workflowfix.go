// Package workflowfix turns the result of a failed-run-fix task into the exact
// edits to a workflow, for the user to review (Plan) and then apply (Apply).
// The agent that wrote the result only proposed; nothing here runs unless the
// operator asks, and an applied edit leaves the workflow UNAPPROVED: the
// definition changes, so the stored approval (keyed by the definition's hash,
// internal/workflow/approvals.go) no longer matches, and nothing here records a
// new one. The user approves by saving or activating the workflow themselves.
package workflowfix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/monoes/mono-agent/internal/ai/generate"
	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Fix kinds in a failed-run-fix result.
const (
	KindWorkflowEdit = "workflow-edit"
	KindAdvice       = "advice"
	KindNone         = "none"
)

// Limits that keep a proposal fully displayable: the app shows every changed leaf.
const (
	// MaxEdits is how many node edits a result may carry.
	MaxEdits = 10
	// MaxLeafChanges is how many changed config values a proposal may hold in all.
	MaxLeafChanges = 50
	// MaxValueChars is the longest changed value, before or after.
	MaxValueChars = 2000
	// MaxKeyChars, MaxPathChars and MaxDepth bound the names the app has to display: one key,
	// the whole dotted path, and how deeply a changed value is nested under the node's config.
	MaxKeyChars  = 128
	MaxPathChars = 512
	MaxDepth     = 8
)

// Warning codes.
const (
	WarnWorkflowActive = "workflow_active"
	WarnNewOutbound    = "new_outbound"
	WarnCredentialKey  = "credential_field"
	WarnExpressionAdd  = "expression_added"
)

// Error codes (Error.Code).
const (
	CodeBadResult      = "bad_result"
	CodeNoEdit         = "no_edit"
	CodeNoChange       = "no_change"
	CodeNotFound       = "workflow_not_found"
	CodeUnknownNode    = "unknown_node"
	CodeSecretKey      = "secret_key"
	CodeInvalid        = "invalid_workflow"
	CodeWorkflowActive = "workflow_active_needs_confirm"
	CodeStale          = "stale_proposal"
	CodeChanged        = "changed_since_preview"
	CodeNoTarget       = "no_target"
	CodeWrongTarget    = "wrong_target"
	CodeTooLarge       = "too_large"
	CodeExpectHash     = "expect_hash_required"
	CodeFileSync       = "file_sync_failed"
	CodeInvalidKey     = "invalid_key"
	CodeNoBackend      = "no_backend"
)

// Error is a refusal with a code the CLI and the GUI show.
type Error struct {
	Code, Message string
	// Fields are extra machine-readable details for --json (for example file_inconsistent).
	Fields map[string]any
}

func (e *Error) Error() string { return e.Message }

func fail(code, format string, a ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

// Backend reads and writes the profile's workflows. The CLI sets it; with none
// every Plan and Apply is refused (never trusted).
type Backend interface {
	// Load returns workflow id when profile owns it, else (nil, nil).
	Load(ctx context.Context, profile, id string) (*workflow.Workflow, error)
	// SaveNodesGuarded stores the nodes of an existing workflow in one transaction that
	// first compares its ReviewHash with guard.ExpectHash (workflow.ErrChangedSincePreview
	// when it differs), and, with guard.RevokeApprovals, removes its stored approvals.
	// It records no approval.
	SaveNodesGuarded(ctx context.Context, profile, id string, nodes []workflow.WorkflowNode, guard workflow.SaveGuard) error
	// ExecutionWorkflow is the id of the workflow run executionID belongs to, "" when there is
	// no such run in this profile.
	ExecutionWorkflow(ctx context.Context, profile, executionID string) (string, error)
}

// Store is the Backend in force, and NodeTypeKnown the node-type hook of the
// validator (what `workflow validate` uses); both are set by the CLI at start.
var (
	Store         Backend
	NodeTypeKnown func(string) bool
)

// Edit is one change of one config path of one node.
type Edit struct {
	Node     string `json:"node"`
	NodeName string `json:"nodeName"`
	Path     string `json:"path"`
	Before   any    `json:"before"`
	After    any    `json:"after"`
}

// Warning is something the user should look at before applying.
type Warning struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

// Preview is what applying a result would do to the workflow as it is now.
type Preview struct {
	WorkflowID   string `json:"workflowId"`
	WorkflowName string `json:"workflowName"`
	Active       bool   `json:"active"`
	// DefinitionHash identifies the reviewed workflow (its definition and whether it is
	// active); a real apply must present it (workflow.ReviewHash).
	DefinitionHash string    `json:"definitionHash"`
	Edits          []Edit    `json:"edits"`
	Warnings       []Warning `json:"warnings"`

	nodes []workflow.WorkflowNode // the whole node list after the edits
}

type proposal struct {
	WorkflowID  string `json:"workflowId"`
	ExecutionID string `json:"executionId"`
	Fix         struct {
		Kind  string `json:"kind"`
		Edits []struct {
			Node   string         `json:"node"`
			Config map[string]any `json:"config"`
		} `json:"edits"`
	} `json:"fix"`
}

var secretKey = regexp.MustCompile(`(?i)(pass(word|wd|phrase)?|secret|token|api[_-]?key|apikey|authorization|bearer|private[_-]?key|credential|cookie|session[_-]?id)`)

// credentialRef keys name a saved connection, not a secret.
var credentialRef = map[string]bool{"credential_id": true, "credentialid": true, "connection_id": true}

// Plan computes the edits of result (a failed-run-fix result) against the
// workflow as it is now, in profile. It changes nothing.
func Plan(ctx context.Context, profile string, result []byte) (Preview, error) {
	return PlanSince(ctx, profile, result, time.Time{})
}

// modified is when wf, or any of its nodes, last changed.
func modified(wf *workflow.Workflow) time.Time {
	t := wf.UpdatedAt
	for _, n := range wf.Nodes {
		if n.UpdatedAt.After(t) {
			t = n.UpdatedAt
		}
	}
	return t
}

// PlanSince is Plan for a proposal that was made at since (when the task
// completed; zero skips the check): a workflow changed after that is stale_proposal,
// because the person who looked at the proposal did not look at this workflow.
func PlanSince(ctx context.Context, profile string, result []byte, since time.Time) (Preview, error) {
	return planFor(ctx, profile, result, since, "")
}

// PlanExpect is PlanSince for an apply of the definition whose Preview.DefinitionHash is
// expect: another definition (edited or activated since) is changed_since_preview.
func PlanExpect(ctx context.Context, profile string, result []byte, since time.Time, expect string) (Preview, error) {
	return planFor(ctx, profile, result, since, expect)
}

func planFor(ctx context.Context, profile string, result []byte, since time.Time, expect string) (Preview, error) {
	var p proposal
	if err := json.Unmarshal(result, &p); err != nil {
		return Preview{}, fail(CodeBadResult, "the result is not a failed-run-fix result: %v", err)
	}
	if p.Fix.Kind != KindWorkflowEdit {
		return Preview{}, fail(CodeNoEdit, "this fix is %q, not a workflow edit: there is nothing to apply", p.Fix.Kind)
	}
	if len(p.Fix.Edits) == 0 || len(p.Fix.Edits) > MaxEdits {
		return Preview{}, fail(CodeBadResult, "a workflow-edit fix needs 1 to %d edits", MaxEdits)
	}
	if Store == nil {
		return Preview{}, fail(CodeNoBackend, "workflows cannot be read here")
	}
	if err := VerifyTarget(ctx, profile, p.WorkflowID, p.ExecutionID); err != nil {
		return Preview{}, err
	}
	wf, err := Store.Load(ctx, profile, p.WorkflowID)
	if err != nil {
		return Preview{}, err
	}
	if wf == nil {
		return Preview{}, fail(CodeNotFound, "no workflow %q in this profile", p.WorkflowID)
	}
	hash, err := workflow.ReviewHash(wf)
	if err != nil {
		return Preview{}, err
	}
	baseline := validate(wf)

	nodes := append([]workflow.WorkflowNode(nil), wf.Nodes...)
	plan := Preview{WorkflowID: wf.ID, WorkflowName: wf.Name, Active: wf.IsActive, Edits: []Edit{}, Warnings: []Warning{}}
	var changed []workflow.WorkflowNode
	for _, e := range p.Fix.Edits {
		i := indexOf(nodes, e.Node)
		if i < 0 {
			return Preview{}, fail(CodeUnknownNode, "the workflow has no node %q", e.Node)
		}
		before := nodes[i].Config
		after := deepMerge(before, e.Config)
		if err := checkKeys(before, e.Config, nil); err != nil {
			return Preview{}, err
		}
		diffs := diff(nil, 0, 0, before, after)
		for _, d := range diffs {
			if err := checkBounds(d); err != nil {
				return Preview{}, err
			}
			if err := checkNew(d); err != nil {
				return Preview{}, err
			}
			plan.Edits = append(plan.Edits, Edit{Node: nodes[i].ID, NodeName: nodes[i].Name, Path: d.path, Before: d.before, After: d.after})
			plan.Warnings = append(plan.Warnings, warningsFor(nodes[i], d)...)
		}
		if len(diffs) == 0 {
			continue
		}
		nodes[i].Config = after
		if err := nodes[i].MarshalConfig(); err != nil {
			return Preview{}, fail(CodeBadResult, "node %q: %v", e.Node, err)
		}
		changed = append(changed, nodes[i])
	}
	if err := checkDisplayable(plan.Edits); err != nil {
		return Preview{}, err
	}
	if len(plan.Edits) == 0 {
		return Preview{}, fail(CodeNoChange, "the edits change nothing in the workflow as it is now (already applied?)")
	}
	if outbound := orggrant.OutboundNodes(&workflow.Workflow{Nodes: changed}); len(outbound) > 0 {
		plan.Warnings = append(plan.Warnings, Warning{WarnNewOutbound, "the edit changes a step that acts outside this computer: " + strings.Join(outbound, ", ")})
	}
	if wf.IsActive {
		plan.Warnings = append(plan.Warnings, Warning{WarnWorkflowActive, "this workflow is active: applying the edit changes what its triggers run"})
	}
	after := *wf
	after.Nodes = nodes
	if fresh := newErrors(baseline, validate(&after)); len(fresh) > 0 {
		return Preview{}, fail(CodeInvalid, "the edited workflow does not pass `workflow validate`:\n- %s", strings.Join(fresh, "\n- "))
	}
	// Not the definition the person reviewed: ask them again (before staleness, which an edit
	// made since the preview would also trip).
	if expect != "" && hash != expect {
		return Preview{}, fail(CodeChanged, "the workflow is not the one you reviewed: preview it again")
	}
	if !since.IsZero() && modified(wf).After(since) {
		return Preview{}, fail(CodeStale, "the workflow changed since this fix was proposed: ask again")
	}
	plan.nodes = nodes
	plan.DefinitionHash = hash
	plan.Warnings = dedupe(plan.Warnings)
	return plan, nil
}

// Options say how Apply runs.
type Options struct {
	// AllowActive lets the edit touch an active workflow (the operator confirmed).
	AllowActive bool
	// Since is when the proposal was made; a workflow changed after it is stale.
	Since time.Time
	// ExpectHash is the Preview.DefinitionHash the person reviewed. Required: the edit
	// is applied only to that definition, in the same transaction that checks it.
	ExpectHash string
}

// Apply plans result again against the workflow as it is now and stores the
// edits. An active workflow is refused unless opts.AllowActive. The workflow stays
// unapproved: no approval is recorded. It returns the number of edits applied.
func Apply(ctx context.Context, profile string, result []byte, opts Options) (int, error) {
	if opts.ExpectHash == "" {
		return 0, fail(CodeExpectHash, "an apply needs the definitionHash the dry-run showed (--expect-hash)")
	}
	plan, err := planFor(ctx, profile, result, opts.Since, opts.ExpectHash)
	if err != nil {
		return 0, err
	}
	if plan.Active && !opts.AllowActive {
		return 0, fail(CodeWorkflowActive, "workflow %q is active: review the edit, then confirm to apply it", plan.WorkflowName)
	}
	// The comparison is made again inside the transaction that writes, and every stored
	// approval goes with the write, so an older approved definition cannot be restored by it.
	err = Store.SaveNodesGuarded(ctx, profile, plan.WorkflowID, plan.nodes, workflow.SaveGuard{ExpectHash: opts.ExpectHash, RevokeApprovals: true})
	if errors.Is(err, workflow.ErrChangedSincePreview) {
		return 0, fail(CodeChanged, "the workflow changed while the edit was being applied: preview it again")
	}
	var he *workflow.TxHookError
	if errors.As(err, &he) {
		return 0, he.Err // the caller's own refusal from inside the transaction (the apply-once claim)
	}
	if errors.Is(err, workflow.ErrFileInconsistent) {
		return 0, &Error{Code: CodeFileSync, Message: fmt.Sprintf("the workflow file could not be written and could not be put back: the file and the database may disagree about workflow %s: %v", plan.WorkflowID, err),
			Fields: map[string]any{"file_inconsistent": plan.WorkflowID}}
	}
	if errors.Is(err, workflow.ErrFileSyncFailed) {
		return 0, fail(CodeFileSync, "the workflow file could not be written, so nothing was changed: %v", err)
	}
	if err != nil {
		return 0, fmt.Errorf("save nodes: %w", err)
	}
	return len(plan.Edits), nil
}

func validate(wf *workflow.Workflow) []string {
	cp := *wf
	cp.Nodes = append([]workflow.WorkflowNode(nil), wf.Nodes...)
	cp.Connections = append([]workflow.WorkflowConnection(nil), wf.Connections...)
	errs, _ := generate.Validator(NodeTypeKnown)(&cp)
	return errs
}

func newErrors(baseline, now []string) []string {
	seen := map[string]bool{}
	for _, b := range baseline {
		seen[b] = true
	}
	var out []string
	for _, n := range now {
		if !seen[n] {
			out = append(out, n)
		}
	}
	return out
}

func indexOf(nodes []workflow.WorkflowNode, id string) int {
	for i := range nodes {
		if nodes[i].ID == id {
			return i
		}
	}
	return -1
}

type change struct {
	segs          []string // the structured path: one entry per key, never split on a dot
	path          string   // the display label of segs (escapeKey joined by ".")
	before, after any
	depth, maxKey int // path segments, and the longest key on the path
}

// escapeKey makes a key safe to join into a label: a literal "\" or "." becomes "\\" or "\.", so
// two different paths never render alike (the keys "a.b" and "a" > "b" are "a\.b" and "a.b").
func escapeKey(k string) string {
	return strings.NewReplacer(`\`, `\\`, ".", `\.`).Replace(k)
}

func label(segs []string) string {
	out := make([]string, len(segs))
	for i, k := range segs {
		out[i] = escapeKey(k)
	}
	return strings.Join(out, ".")
}

// deepMerge returns base with patch merged in: objects merge key by key,
// anything else is replaced. Neither argument is modified.
func deepMerge(base, patch map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(patch))
	for k, v := range base {
		out[k] = v
	}
	for k, pv := range patch {
		if pm, ok := pv.(map[string]any); ok {
			if bm, ok := out[k].(map[string]any); ok {
				out[k] = deepMerge(bm, pm)
				continue
			}
		}
		out[k] = pv
	}
	return out
}

// diff lists the paths whose value differs between a and b: leaves, and also every
// newly added key whose value is an empty object (before: absent, after: {}), so that
// nothing the edit adds is invisible. Recursion stops one level past MaxDepth; the
// change it leaves there is refused by checkBounds.
func diff(prefix []string, depth, maxKey int, a, b map[string]any) []change {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	var out []change
	for _, k := range sorted {
		segs := append(append([]string(nil), prefix...), k)
		mk := maxKey
		if n := len([]rune(k)); n > mk {
			mk = n
		}
		av, aok := a[k]
		bv, bok := b[k]
		am, aIsMap := av.(map[string]any)
		bm, bIsMap := bv.(map[string]any)
		switch {
		case bIsMap && (aIsMap || !aok) && depth+1 <= MaxDepth && !(!aok && len(bm) == 0):
			out = append(out, diff(segs, depth+1, mk, am, bm)...)
		case aok && bok && reflect.DeepEqual(av, bv):
		default:
			out = append(out, change{segs: segs, path: label(segs), before: av, after: bv, depth: depth + 1, maxKey: mk})
		}
	}
	return out
}

// checkBounds refuses a change whose names the app could not display in full: a key over
// MaxKeyChars (also inside the new value), a dotted path over MaxPathChars, or a value nested
// deeper than MaxDepth counting the path.
func checkBounds(c change) error {
	inKey, inDepth := shape(c.after)
	if c.maxKey > MaxKeyChars || inKey > MaxKeyChars {
		return fail(CodeTooLarge, "a key of the change at %q is longer than %d characters: propose a smaller change", clip(c.path), MaxKeyChars)
	}
	if len([]rune(c.path)) > MaxPathChars {
		return fail(CodeTooLarge, "the path %q is longer than %d characters: propose a smaller change", clip(c.path), MaxPathChars)
	}
	if c.depth+inDepth > MaxDepth {
		return fail(CodeTooLarge, "the change at %q is nested deeper than %d levels: propose a smaller change", clip(c.path), MaxDepth)
	}
	return nil
}

func clip(s string) string {
	if r := []rune(s); len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return s
}

// shape returns the longest key inside v and how many levels of objects and lists v nests.
func shape(v any) (maxKey, depth int) {
	switch t := v.(type) {
	case map[string]any:
		depth = 1
		for k, e := range t {
			if n := len([]rune(k)); n > maxKey {
				maxKey = n
			}
			ek, ed := shape(e)
			if ek > maxKey {
				maxKey = ek
			}
			if ed+1 > depth {
				depth = ed + 1
			}
		}
	case []any:
		depth = 1
		for _, e := range t {
			ek, ed := shape(e)
			if ek > maxKey {
				maxKey = ek
			}
			if ed+1 > depth {
				depth = ed + 1
			}
		}
	}
	return maxKey, depth
}

// checkNew refuses an edit that adds a key which looks like a secret, or a
// value that redacts to something else (it holds a secret).
func checkNew(c change) error {
	if c.before == nil {
		for _, part := range c.segs {
			if looksSecretKey(part) {
				return fail(CodeSecretKey, "the edit adds %q, which looks like a secret: a fix never carries one (use a saved connection)", clip(c.path))
			}
		}
	}
	return checkAdded(c.path, c.after)
}

// checkAdded walks a new value, objects and lists to any depth: a credential-looking key or a
// secret-looking string anywhere inside is refused.
func checkAdded(path string, v any) error {
	switch t := v.(type) {
	case string:
		if aitask.RedactText(t) != t {
			return fail(CodeSecretKey, "the edit puts a secret-looking value at %q", clip(path))
		}
	case map[string]any:
		for k, e := range t {
			if looksSecretKey(k) {
				return fail(CodeSecretKey, "the edit adds %q under %q, which looks like a secret: a fix never carries one (use a saved connection)", clip(k), clip(path))
			}
			if err := checkAdded(path+"."+escapeKey(k), e); err != nil {
				return err
			}
		}
	case []any:
		for i, e := range t {
			if err := checkAdded(fmt.Sprintf("%s[%d]", path, i), e); err != nil {
				return err
			}
		}
	}
	return nil
}

// looksSecretKey is the secret-key rule over a key with format and control characters
// stripped and case folded, so a zero-width character cannot hide "api_key". A reference to a
// saved connection (credential_id) is not a secret.
func looksSecretKey(k string) bool {
	n := strings.ToLower(stripInvisible(k))
	return secretKey.MatchString(n) && !credentialRef[n]
}

// stripInvisible removes control and format characters (zero-width, bidi controls).
func stripInvisible(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

// foldKey is what two keys must differ in to be different keys: case and surrounding space do not count.
func foldKey(k string) string { return strings.ToLower(strings.TrimSpace(k)) }

// checkKeys refuses keys the app could not show unambiguously: control, format or bidi
// characters, text that is not in NFC, or a key that collides with a sibling (an existing one, or
// another in the same patch) after case folding and trimming. It walks objects and lists to any
// depth; base is the existing object at this level (nil for a new one).
func checkKeys(base, patch map[string]any, at []string) error {
	if len(at) > MaxDepth {
		return fail(CodeTooLarge, "the change is nested deeper than %d levels: propose a smaller change", MaxDepth)
	}
	seen := map[string]string{}
	for k := range base {
		seen[foldKey(k)] = k
	}
	for k, v := range patch {
		where := label(append(append([]string(nil), at...), k))
		if stripInvisible(k) != k {
			return fail(CodeInvalidKey, "the key %q contains a control, format or direction character: propose a plain key", clip(where))
		}
		if !norm.NFC.IsNormalString(k) {
			return fail(CodeInvalidKey, "the key %q is not in normalized (NFC) form", clip(where))
		}
		if other, ok := seen[foldKey(k)]; ok && other != k {
			return fail(CodeInvalidKey, "the key %q collides with the existing key %q (they differ only in case or spacing)", clip(where), clip(other))
		}
		seen[foldKey(k)] = k
		var next map[string]any
		if b, ok := base[k].(map[string]any); ok {
			next = b
		}
		if err := checkKeysIn(next, v, append(append([]string(nil), at...), k)); err != nil {
			return err
		}
	}
	return nil
}

func checkKeysIn(base map[string]any, v any, at []string) error {
	if len(at) > MaxDepth+1 {
		return fail(CodeTooLarge, "the change is nested deeper than %d levels: propose a smaller change", MaxDepth)
	}
	switch t := v.(type) {
	case map[string]any:
		return checkKeys(base, t, at)
	case []any:
		for i, e := range t {
			if err := checkKeysIn(nil, e, append(append([]string(nil), at...), fmt.Sprintf("[%d]", i))); err != nil {
				return err
			}
		}
	}
	return nil
}

func warningsFor(n workflow.WorkflowNode, c change) []Warning {
	var out []Warning
	for _, part := range c.segs {
		if secretKey.MatchString(strings.ToLower(stripInvisible(part))) {
			out = append(out, Warning{WarnCredentialKey, fmt.Sprintf("node %q: the edit touches %q, a credential field", n.ID, c.path)})
			break
		}
	}
	if s, ok := c.after.(string); ok && strings.Contains(s, "{{") {
		if b, _ := c.before.(string); !strings.Contains(b, "{{") {
			out = append(out, Warning{WarnExpressionAdd, fmt.Sprintf("node %q: the edit adds an expression at %q", n.ID, c.path)})
		}
	}
	return out
}

func dedupe(ws []Warning) []Warning {
	seen := map[Warning]bool{}
	out := ws[:0:0]
	for _, w := range ws {
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// IsCode reports whether err is a refusal with this code.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// Exists reports whether profile owns workflow id.
func Exists(ctx context.Context, profile, id string) (bool, error) {
	if Store == nil {
		return false, fail(CodeNoBackend, "workflows cannot be read here")
	}
	wf, err := Store.Load(ctx, profile, id)
	return wf != nil, err
}

// checkDisplayable refuses a proposal the app could not show in full.
func checkDisplayable(edits []Edit) error {
	if len(edits) > MaxLeafChanges {
		return fail(CodeTooLarge, "the fix changes %d values; at most %d are allowed: propose a smaller change", len(edits), MaxLeafChanges)
	}
	for _, e := range edits {
		for _, v := range []any{e.Before, e.After} {
			if valueChars(v) > MaxValueChars {
				return fail(CodeTooLarge, "the value at %q is longer than %d characters: propose a smaller change", e.Path, MaxValueChars)
			}
		}
	}
	return nil
}

func valueChars(v any) int {
	if s, ok := v.(string); ok {
		return len([]rune(s))
	}
	b, _ := json.Marshal(v)
	return len([]rune(string(b)))
}
