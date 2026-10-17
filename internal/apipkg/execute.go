package apipkg

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// httpClient sends every package request (tests swap it).
var httpClient = &http.Client{Timeout: 60 * time.Second}

// Response-size caps, as internal/nodes/service: no body is read unbounded,
// and a non-2xx body echoed into an error is capped tighter.
const (
	maxResponseBody  = 64 << 20 // 64 MiB
	maxErrorBodyEcho = 64 << 10 // 64 KiB
)

// Node is the workflow node of one package.
type Node struct {
	pkg *Package
}

// Type is the package's node type.
func (n *Node) Type() string { return n.pkg.NodeTypeOf() }

// PerItemConfigFields holds back every op field from the engine's
// first-item resolution: the node resolves them once per input item.
func (n *Node) PerItemConfigFields() []string { return n.pkg.fieldKeys() }

// Execute runs the configured operation once per input item.
func (n *Node) Execute(ctx context.Context, input workflow.NodeInput, config map[string]any) ([]workflow.NodeOutput, error) {
	return run(ctx, n.pkg, input, config, true)
}

// ExecuteExtension runs a package that extends nativeType (e.g.
// "service.github") for a native node whose operation switch did not handle
// the config. It errors when no package extends nativeType.
func ExecuteExtension(ctx context.Context, nativeType string, input workflow.NodeInput, config map[string]any) ([]workflow.NodeOutput, error) {
	pkg := extensionFor(nativeType)
	if pkg == nil {
		return nil, fmt.Errorf("apipkg: no package extends %s", nativeType)
	}
	return run(ctx, pkg, input, config, false)
}

// HasExtension reports whether a package extends nativeType with
// (resource, operation). An empty resource matches any package op with
// that operation name and no resource.
func HasExtension(nativeType, resource, operation string) bool {
	pkg := extensionFor(nativeType)
	if pkg == nil {
		return false
	}
	_, ok := pkg.Op(resource, operation)
	return ok
}

// fieldKeys is the union of every op's field keys, sorted.
func (p *Package) fieldKeys() []string {
	seen := map[string]bool{}
	var out []string
	for _, o := range p.Operations {
		for _, f := range o.Fields {
			if !seen[f.Key] {
				seen[f.Key] = true
				out = append(out, f.Key)
			}
		}
	}
	sort.Strings(out)
	return out
}

// opRun is one operation run for one input item.
type opRun struct {
	pkg     *Package
	op      *Operation
	params  map[string]any
	item    workflow.Item
	credDef *Credential
	scope   Scope
	// prop overrides the upload property (one of binary.properties).
	prop string
	// onPage, when set, sees each page's results while paginating;
	// returning true stops (output.walk stops at its cap).
	onPage func(results []any) bool
	// lastPage is the last decoded page of a paginated run.
	lastPage any
	// urlProps are the item's binary properties binary.upload_urls filled.
	urlProps []string
}

func (r *opRun) errf(format string, a ...any) error {
	return fmt.Errorf("apipkg %s %s: %w", r.pkg.ID, r.op.Key(), fmt.Errorf(format, a...))
}

// run executes pkg for every input item. perItem resolves held-back
// workflow expressions per item (a package node holds its fields back).
func run(ctx context.Context, pkg *Package, input workflow.NodeInput, config map[string]any, perItem bool) ([]workflow.NodeOutput, error) {
	cred, _ := config["credential"].(map[string]any)
	if pkg.needsCredential() && len(cred) == 0 {
		return nil, fmt.Errorf("apipkg %s: no credential: set credential_id to a %s connection (monoagentcli connect %s)",
			pkg.ID, pkg.PrimaryPlatform(), pkg.PrimaryPlatform())
	}
	scopes := scopeChecker{pkg: pkg, cred: cred, credDef: pkg.selectCredential(cred)}
	credDef, cred, err := pkg.prepareCredential(ctx, cred)
	if err != nil {
		return nil, err
	}
	items := input.Items
	if len(items) == 0 {
		items = []workflow.Item{{JSON: map[string]any{}}}
	}
	var held []string
	if perItem {
		held = pkg.fieldKeys()
	}
	engine := workflow.NewExpressionEngine()
	var out []workflow.Item
	for i, it := range items {
		if it.JSON == nil {
			it.JSON = map[string]any{}
		}
		params, err := itemParams(engine, config, held, it, input)
		if err != nil {
			return nil, fmt.Errorf("apipkg %s: item %d: %w", pkg.ID, i, err)
		}
		op, err := pkg.resolveOp(params)
		if err != nil {
			return nil, err
		}
		if err := scopes.check(op); err != nil {
			return nil, err
		}
		applyDefaults(op, params)
		if err := checkRequired(pkg, op, params); err != nil {
			return nil, err
		}
		if err := checkCollections(pkg, op, params); err != nil {
			return nil, err
		}
		r := &opRun{pkg: pkg, op: op, params: params, item: it, credDef: credDef,
			scope: Scope{P: params, Cred: cred, Item: it.JSON}}
		if op.Wait != nil {
			return r.dispatchAndWait(ctx, input)
		}
		if err := r.fetchUploadURL(ctx); err != nil {
			return nil, err
		}
		if err := r.fetchUploadURLs(ctx); err != nil {
			return nil, err
		}
		if op.Binary != nil && (op.Binary.UploadField != "" || !isEmpty(op.Binary.Properties) || len(r.urlProps) > 0) {
			// Filled for every step: a before step that requests an upload
			// slot needs the file's name and type (Pushbullet, S3 presigned).
			// binary.properties alone fills bin.files for a body that embeds
			// the files (Gmail MIME attachments) without uploading one.
			r.scope.Bin = r.binMeta()
		}
		if op.Batch {
			all := make([]any, 0, len(items))
			for _, x := range items {
				if x.JSON == nil {
					all = append(all, map[string]any{})
					continue
				}
				all = append(all, x.JSON)
			}
			r.scope.Items = all
		}
		if i == 0 && op.Bulk != nil {
			on, err := r.bulkOn()
			if err != nil {
				return nil, err
			}
			if on {
				res, err := r.runBulk(ctx, engine, config, held, items, input)
				if err != nil {
					return nil, err
				}
				return []workflow.NodeOutput{{Handle: "main", Items: res}}, nil
			}
		}
		res, err := r.execute(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, res...)
		if op.Batch {
			break // one request covered every item
		}
	}
	return []workflow.NodeOutput{{Handle: "main", Items: out}}, nil
}

// prepareCredential picks the credential definition for cred and adds what
// requests need before the first one: a client-credentials token, an
// assumed role's keys and the OAuth metadata values.
func (pkg *Package) prepareCredential(ctx context.Context, cred map[string]any) (*Credential, map[string]any, error) {
	credDef := pkg.selectCredential(cred)
	if credDef != nil && credDef.hasMethod("client_credentials") && isEmpty(cred["access_token"]) {
		tok, err := clientCredentialsToken(ctx, credDef, cred)
		if err != nil {
			return nil, nil, fmt.Errorf("apipkg %s: %w", pkg.ID, err)
		}
		withTok := make(map[string]any, len(cred)+1)
		for k, v := range cred {
			withTok[k] = v
		}
		withTok["access_token"] = tok
		cred = withTok
	}
	cred, err := withAssumedRole(ctx, credDef, cred)
	if err != nil {
		return nil, nil, fmt.Errorf("apipkg %s: %w", pkg.ID, err)
	}
	if cred, err = withMetadata(ctx, credDef, cred); err != nil {
		return nil, nil, fmt.Errorf("apipkg %s: %w", pkg.ID, err)
	}
	return credDef, cred, nil
}

// itemParams copies config, resolving the held-back keys against item.
func itemParams(engine *workflow.ExpressionEngine, config map[string]any, held []string, it workflow.Item, input workflow.NodeInput) (map[string]any, error) {
	params := make(map[string]any, len(config))
	for k, v := range config {
		params[k] = v
	}
	if len(held) == 0 {
		return params, nil
	}
	sub := map[string]any{}
	for _, k := range held {
		if v, ok := config[k]; ok {
			sub[k] = v
		}
	}
	resolved, err := engine.ResolveConfig(sub, workflow.ExpressionContext{
		JSON: it.JSON, Node: input.NodeOutputs, WorkflowID: input.WorkflowID, ExecutionID: input.ExecutionID,
	})
	if err != nil {
		return nil, err
	}
	for k, v := range resolved {
		params[k] = v
	}
	return params, nil
}

// resolveOp finds the op named by config resource/operation, defaulting to
// the first resource and its first operation (n8n leaves defaults unsaved).
func (p *Package) resolveOp(params map[string]any) (*Operation, error) {
	res, _ := params["resource"].(string)
	if res == "" && p.N8nResourceParam != "" {
		res, _ = params[p.N8nResourceParam].(string)
	}
	if res == "" {
		res = p.DefaultResource()
	}
	opName, _ := params["operation"].(string)
	if opName == "" && p.N8nOperationParam != "" {
		opName, _ = params[p.N8nOperationParam].(string)
	}
	if opName == "" {
		opName = p.DefaultOperation(res)
	}
	op, ok := p.Op(res, opName)
	if rp, _ := params["resource"].(string); !ok && rp == "" {
		// n8n hides resource for some modes (Discord webhook): an op name
		// only one resource has is unambiguous.
		var only *Operation
		n := 0
		for i := range p.Operations {
			if p.Operations[i].Operation == opName {
				only = &p.Operations[i]
				n++
			}
		}
		if n == 1 {
			op, ok = only, true
		}
	}
	if !ok {
		var have []string
		for _, o := range p.Operations {
			have = append(have, o.Key())
		}
		key := opName
		if res != "" {
			key = res + "." + opName
		}
		return nil, fmt.Errorf("apipkg %s: no operation %q (have: %s)", p.ID, key, strings.Join(have, ", "))
	}
	return op, nil
}

// applyDefaults fills unset params from the op's field defaults.
func applyDefaults(op *Operation, params map[string]any) {
	for _, f := range op.Fields {
		if _, ok := params[f.Key]; !ok && f.Default != nil {
			params[f.Key] = f.Default
		}
	}
}

func checkRequired(pkg *Package, op *Operation, params map[string]any) error {
	for _, f := range op.Fields {
		if !f.Required {
			continue
		}
		v := params[f.Key]
		if m, ok := v.(map[string]any); ok && m["__rl"] != nil {
			v = m["value"]
		}
		if isEmpty(v) {
			return fmt.Errorf("apipkg %s %s: field %q (%s) is required", pkg.ID, op.Key(), f.Key, f.Label)
		}
	}
	return nil
}
