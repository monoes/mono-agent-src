package apipkg

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
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
		r := &opRun{pkg: pkg, op: op, params: params, item: it, credDef: credDef,
			scope: Scope{P: params, Cred: cred, Item: it.JSON}}
		if err := r.fetchUploadURL(ctx); err != nil {
			return nil, err
		}
		if op.Binary != nil && (op.Binary.UploadField != "" || op.Binary.Properties != "") {
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

// execute sends the request (all pages) and turns the response into items.
func (r *opRun) execute(ctx context.Context) ([]workflow.Item, error) {
	for i, st := range r.op.Before {
		if st.ForEach != "" {
			lv, err := renderString(st.ForEach, r.scope)
			if err != nil {
				return nil, r.errf("before[%d].for_each: %w", i, err)
			}
			list, _ := lv.([]any)
			collected := make([]any, 0, len(list))
			for j, el := range list {
				sc := r.scope
				sc.El, sc.Idx = el, j
				step := *r
				step.scope = sc
				if s, ok := el.(string); ok && st.Upload {
					step.prop = s // one upload per binary property (Twist)
				}
				resp, err := step.sendStep(ctx, st, sc)
				if err != nil {
					return nil, fmt.Errorf("before[%d] element %d: %w", i, j, err)
				}
				var v any
				if resp != nil && len(strings.TrimSpace(string(resp.body))) > 0 {
					v = decodeBody(resp.body)
				}
				collected = append(collected, v)
			}
			r.scope.Pres = append(r.scope.Pres, collected)
			r.scope.Pre = collected
			continue
		}
		resp, err := r.sendStep(ctx, st, r.scope)
		if err != nil {
			return nil, fmt.Errorf("before[%d]: %w", i, err)
		}
		var v any
		if resp != nil && len(strings.TrimSpace(string(resp.body))) > 0 {
			v = decodeBody(resp.body)
		}
		r.scope.Pres = append(r.scope.Pres, v)
		r.scope.Pre = v
	}
	if r.op.Pagination != nil && r.pagingOn() {
		results, truncated, err := r.paginate(ctx)
		if err != nil {
			return nil, err
		}
		// also steps follow the last page (Elasticsearch closes its PIT
		// with the id the last page returned); main is the first page.
		if err := r.runAlso(ctx, r.lastPage, r.scope.Res); err != nil {
			return nil, err
		}
		var all any = results
		if o := r.op.Output; o != nil && o.Walk != nil {
			if all, err = r.walk(ctx, results, results); err != nil {
				return nil, err
			}
		}
		shaped, err := r.shapeOutput(ctx, all)
		if err != nil {
			return nil, err
		}
		items := toItems(shaped, true)
		if truncated {
			for i := range items {
				items[i].JSON["pagination_truncated"] = true
			}
		}
		return items, nil
	}
	resp, err := r.sendMain(ctx)
	if err != nil {
		return nil, err
	}
	if resp, err = r.poll(ctx, resp); err != nil {
		return nil, err
	}
	var res any
	if len(strings.TrimSpace(string(resp.body))) > 0 {
		res = decodeBody(resp.body)
	}
	if err := r.runAlso(ctx, res, res); err != nil {
		return nil, err
	}
	r.scope.Hdr = headerMap{}
	for k, vs := range resp.header {
		if len(vs) > 0 {
			r.scope.Hdr[k] = vs[0]
		}
	}
	if r.downloads() {
		if b := r.op.Binary; b.DownloadBase64 != "" {
			return r.downloadBase64Item(resp)
		}
		return []workflow.Item{r.downloadItem(resp)}, nil
	}
	if len(strings.TrimSpace(string(resp.body))) == 0 {
		// An empty body (201 + Location/Resource-ID headers) can still be
		// shaped from hdr by output.to / output.expand.
		if o := r.op.Output; o != nil && (o.To != nil || o.Expand != nil) {
			shaped, err := r.shapeOutput(ctx, map[string]any{})
			if err != nil {
				return nil, err
			}
			return toItems(shaped, true), nil
		}
		return []workflow.Item{{JSON: map[string]any{"success": true}}}, nil
	}
	v := decodeBody(resp.body)
	if o := r.op.Output; o != nil && o.From != "" {
		sc := r.scope
		sc.Res = v
		fv, err := renderString(o.From, sc)
		if err != nil {
			return nil, r.errf("output.from: %w", err)
		}
		v = fv
	}
	doc := v
	if path := r.outputPath(); path != "" {
		v = lookupPath(v, strings.Split(path, "."))
	}
	if o := r.op.Output; o != nil && o.SchemaRows != "" {
		fields, _ := lookupPath(doc, strings.Split(o.SchemaRows, ".")).([]any)
		nums := false
		if o.Numbers != "" {
			on, err := renderString(o.Numbers, r.scope)
			if err != nil {
				return nil, r.errf("output.numbers: %w", err)
			}
			b, _ := toBool(on)
			nums = b == true
		}
		v = schemaRows(v, fields, nums)
	}
	if r.op.Output != nil && r.op.Output.HeaderRow {
		v = rowsToObjects(v)
	}
	if o := r.op.Output; o != nil && o.Walk != nil {
		if v, err = r.walk(ctx, doc, v); err != nil {
			return nil, err
		}
	}
	r.scope.Res = doc // output.to/match templates may read the whole response
	if v, err = r.shapeOutput(ctx, v); err != nil {
		return nil, err
	}
	split := r.op.Output == nil || r.op.Output.Split == nil || *r.op.Output.Split
	return toItems(v, split), nil
}

// response is a 2xx response.
type response struct {
	status int
	header http.Header
	body   []byte
	url    string
}

// send builds and sends one request; non-2xx responses are errors.
func (r *opRun) send(ctx context.Context, pp pageParams) (*response, error) {
	if r.op.Stream != nil {
		return r.sendStream(ctx, pp)
	}
	b, err := r.buildRequest(ctx, pp)
	if err != nil {
		return nil, err
	}
	resp, err := r.client().Do(b.req)
	if err != nil {
		return nil, r.errf("%s %s: %w", b.req.Method, b.req.URL.Redacted(), err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return nil, r.errf("reading response: %w", err)
	}
	if len(body) > maxResponseBody {
		return nil, r.errf("response body exceeds %d MiB", maxResponseBody>>20)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, r.errf("%w", &statusError{code: resp.StatusCode, body: errorBodyEcho(body)})
	}
	if err := r.checkErrorWhen(body, resp.Header); err != nil {
		return nil, err
	}
	return &response{status: resp.StatusCode, header: resp.Header, body: body, url: b.req.URL.String()}, nil
}

// statusError is a non-2xx response; callers that treat a status as an
// answer (a webhook delete's 404) find it with errors.As.
type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.code, e.body) }

func errorBodyEcho(b []byte) string {
	if len(b) > maxErrorBodyEcho {
		return string(b[:maxErrorBodyEcho]) + fmt.Sprintf("…(%d bytes truncated)", len(b)-maxErrorBodyEcho)
	}
	return string(b)
}

// downloadItem stores a file response on the item's binary data.
func (r *opRun) downloadItem(resp *response) workflow.Item {
	prop := r.downloadProperty()
	ct := resp.header.Get("Content-Type")
	name := ""
	if _, params, err := mime.ParseMediaType(resp.header.Get("Content-Disposition")); err == nil {
		name = params["filename"]
	}
	// An op that uploads nothing may name the file it downloads
	// (binary.file_name / mime_type) for responses that don't.
	if b := r.op.Binary; b.UploadField == "" && b.Properties == "" {
		if name == "" {
			name, _ = renderText(b.FileName, r.scope)
		}
		if ct == "" {
			ct, _ = renderText(b.MimeType, r.scope)
		}
	}
	return workflow.Item{
		JSON: map[string]any{
			"mime_type": ct, "file_name": name, "size": len(resp.body), "binary_property": prop,
		},
		Binary: map[string][]byte{prop: resp.body},
	}
}

// decodeBody parses JSON, falling back to the text.
func decodeBody(b []byte) any {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		if t := bytes.TrimSpace(b); len(t) > 0 && t[0] == '<' {
			if x, err := decodeXML(t); err == nil {
				return x // WebDAV multistatus, SOAP-ish APIs
			}
		}
		return string(b)
	}
	return v
}

// toItems turns a value into items: an array splits into one item per
// element (when split), an object is one item, anything else is
// {"data": value}.
func toItems(v any, split bool) []workflow.Item {
	if arr, ok := v.([]any); ok && split {
		items := make([]workflow.Item, 0, len(arr))
		for _, el := range arr {
			it := workflow.Item{JSON: asObject(el)}
			takeFiles(&it)
			items = append(items, it)
		}
		return items
	}
	if v == nil {
		return []workflow.Item{}
	}
	it := workflow.Item{JSON: asObject(v)}
	takeFiles(&it)
	return []workflow.Item{it}
}

func asObject(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{"data": v}
}

// runAlso sends the operation's follow-up requests in order, each with res
// bound to the main response (the last page of a paginated op) and main to
// the first. A step whose "when" renders empty is skipped.
func (r *opRun) runAlso(ctx context.Context, res, main any) error {
	if len(r.op.Also) == 0 {
		return nil
	}
	for i, st := range r.op.Also {
		sc := r.scope
		sc.Res, sc.Main = res, main
		if st.ForEach != "" {
			lv, err := renderString(st.ForEach, sc)
			if err != nil {
				return r.errf("also[%d].for_each: %w", i, err)
			}
			list, _ := lv.([]any)
			for j, el := range list {
				esc := sc
				esc.El = el
				if _, err := r.sendStep(ctx, st, esc); err != nil {
					return fmt.Errorf("also[%d] element %d: %w", i, j, err)
				}
			}
			continue
		}
		if _, err := r.sendStep(ctx, st, sc); err != nil {
			return fmt.Errorf("also[%d]: %w", i, err)
		}
	}
	return nil
}

// ScopeCheck, when set, checks that a credential grants an umbrella scope
// group (see Credential.ScopeGroup). It is wired to
// connections.CheckCredentialScopes by the registry.
var ScopeCheck func(cred map[string]any, group string) error

// sendStep sends one extra request (before/also) of the operation with
// scope sc and the operation's auth.
func (r *opRun) sendStep(ctx context.Context, st Step, sc Scope) (*response, error) {
	if st.When != "" {
		w, err := renderString(st.When, sc)
		if err != nil {
			return nil, r.errf("when: %w", err)
		}
		if !truthy(w) {
			return nil, nil
		}
	}
	op := *r.op
	op.Method, op.Path, op.BaseURL = st.Method, st.Path, st.BaseURL
	op.Query, op.Body, op.BodyType, op.Headers = st.Query, st.Body, st.BodyType, st.Headers
	op.Pagination, op.Also, op.Before, op.Stream = nil, nil, nil, st.Stream
	if st.ErrorWhen != "" {
		op.ErrorWhen, op.ErrorMessage = st.ErrorWhen, st.ErrorMessage
	}
	if st.Upload && r.op.Binary != nil && r.op.Binary.UploadField != "" {
		b := *r.op.Binary
		b.UploadWhen, b.DownloadWhen, b.Download, b.DownloadBase64 = "", "", false, ""
		op.Binary = &b
		if st.BodyType == BodyMultipart {
			// S3-style presigned POST: the step's form fields plus the file.
			op.BodyType = BodyMultipart
		} else {
			op.Body, op.BodyType = nil, BodyRaw
		}
	} else if st.download {
		op.Binary = &Binary{Download: true}
	} else {
		op.Binary = nil
	}
	step := *r
	step.op, step.scope = &op, sc
	if st.Auth != nil && !*st.Auth {
		step.credDef = nil
	}
	resp, err := step.send(ctx, pageParams{})
	if err != nil || st.Repeat == nil {
		return resp, err
	}
	return r.repeatStep(ctx, st, sc, resp)
}

// shapeOutput applies output.each, output.expand and output.match to a
// result list.
func (r *opRun) shapeOutput(ctx context.Context, v any) (any, error) {
	o := r.op.Output
	if o == nil {
		return v, nil
	}
	list, isList := v.([]any)
	if !isList {
		// A single object is shaped as a one-element list (Coda getRow).
		if _, isObj := v.(map[string]any); !isObj {
			return v, nil
		}
		shaped, err := r.shapeOutput(ctx, []any{v})
		if err != nil {
			return nil, err
		}
		if l := shaped.([]any); len(l) == 1 {
			return l[0], nil
		} else if len(l) == 0 {
			return nil, nil
		}
		return shaped, nil
	}
	each, err := r.renderOutputKey(o.Each)
	if err != nil {
		return nil, r.errf("output.each: %w", err)
	}
	if each != "" {
		path := strings.Split(each, ".")
		out := make([]any, 0, len(list))
		for _, el := range list {
			if x := lookupPath(el, path); x != nil {
				out = append(out, x)
			}
		}
		list = out
	}
	if st := o.Expand; st != nil {
		for i, el := range list {
			sc := r.scope
			sc.El = el
			resp, err := r.sendStep(ctx, *st, sc)
			if err != nil {
				return nil, fmt.Errorf("output.expand element %d: %w", i, err)
			}
			if resp == nil || len(strings.TrimSpace(string(resp.body))) == 0 {
				continue
			}
			got := decodeBody(resp.body)
			if o.ExpandMergeKey == "" {
				list[i] = got
				continue
			}
			if o.ExpandMergePath != "" {
				got = lookupPath(got, strings.Split(o.ExpandMergePath, "."))
			}
			if m, ok := el.(map[string]any); ok {
				m[o.ExpandMergeKey] = got
			}
		}
	}
	if o.Download != nil {
		if err := r.downloadFiles(ctx, list); err != nil {
			return nil, err
		}
	}
	if o.Where != "" {
		if list, err = r.applyWhere(list); err != nil {
			return nil, err
		}
	}
	if !o.MatchAfterTo {
		if list, err = r.applyMatch(list); err != nil {
			return nil, err
		}
	}
	if o.Firestore != "" {
		on, err := renderString(o.Firestore, r.scope)
		if err != nil {
			return nil, r.errf("output.firestore: %w", err)
		}
		if b, _ := toBool(on); b == true {
			for i, el := range list {
				list[i] = firestoreDoc(el)
			}
		}
	}
	if o.Skip > 0 {
		if o.Skip >= len(list) {
			list = nil
		} else {
			list = list[o.Skip:]
		}
	}
	if o.Sort != "" {
		path := strings.Split(o.Sort, ".")
		sort.SliceStable(list, func(i, j int) bool {
			a, b := lookupPath(list[i], path), lookupPath(list[j], path)
			less := lessValue(a, b)
			if o.SortDesc {
				return lessValue(b, a)
			}
			return less
		})
	}
	if o.Limit {
		list = r.clientLimit(list)
	}
	if o.Merge {
		merged := map[string]any{}
		for _, el := range list {
			if m, ok := el.(map[string]any); ok {
				for k, x := range m {
					merged[k] = x
				}
			}
		}
		list = []any{merged}
	}
	if o.To != nil {
		apply := true
		if o.ToWhen != "" {
			w, err := renderString(o.ToWhen, r.scope)
			if err != nil {
				return nil, r.errf("output.to_when: %w", err)
			}
			apply = truthy(w)
		}
		if apply {
			for i, el := range list {
				sc := r.scope
				sc.El = el
				x, err := renderValue(o.To, sc)
				if err != nil {
					return nil, r.errf("output.to element %d: %w", i, err)
				}
				carryFiles(el, x)
				list[i] = x
			}
		}
	}
	if o.MatchAfterTo {
		if list, err = r.applyMatch(list); err != nil {
			return nil, err
		}
	}
	return list, nil
}

// renderOutputKey renders an output path/each value, which may be a
// template over p (n8n rawData/simple toggles); empty means none.
func (r *opRun) renderOutputKey(s string) (string, error) {
	if !hasTemplate(s) {
		return s, nil
	}
	return renderText(s, r.scope)
}

// outputPath is the rendered output.path ("" when unset or empty).
func (r *opRun) outputPath() string {
	if r.op.Output == nil {
		return ""
	}
	p, err := r.renderOutputKey(r.op.Output.Path)
	if err != nil {
		return r.op.Output.Path
	}
	return p
}

// downloads reports whether this run's response is a file.
func (r *opRun) downloads() bool {
	b := r.op.Binary
	if b == nil {
		return false
	}
	if b.DownloadWhen == "" {
		return b.Download
	}
	w, err := renderString(b.DownloadWhen, r.scope)
	return err == nil && truthy(w)
}

// poll repeats op.Poll after the main response until its until template
// (res = the latest response) is true, waiting interval_ms between tries
// and failing after max_wait_ms.
func (r *opRun) poll(ctx context.Context, resp *response) (*response, error) {
	pl := r.op.Poll
	if pl == nil {
		return resp, nil
	}
	interval := time.Duration(pl.IntervalMs) * time.Millisecond
	if interval <= 0 {
		interval = time.Second
	}
	maxWait, err := r.waitFor(pl.MaxWaitMs, pl.MaxWaitSeconds, r.scope, "poll.max_wait_seconds")
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(maxWait)
	first := decodeBody(resp.body)
	r.scope.Main = first // output templates read the first reply as main
	for {
		sc := r.scope
		sc.Main = first
		sc.Res = decodeBody(resp.body)
		done, err := renderString(pl.Until, sc)
		if err != nil {
			return nil, r.errf("poll.until: %w", err)
		}
		if b, _ := toBool(done); b == true {
			return resp, nil
		}
		if time.Now().After(deadline) {
			return nil, r.errf("poll: not done after %s", maxWait)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		next, err := r.sendStep(ctx, pl.Step, sc)
		if err != nil {
			return nil, fmt.Errorf("poll: %w", err)
		}
		if next != nil {
			resp = next
		}
	}
}

// schemaRows turns BigQuery rows ({f: [{v}]}) into objects keyed by the
// schema's field names; RECORD fields recurse, REPEATED ones are lists.
func schemaRows(v any, fields []any, nums bool) any {
	rows, ok := v.([]any)
	if !ok || len(fields) == 0 {
		return v
	}
	out := make([]any, len(rows))
	for i, row := range rows {
		out[i] = schemaRow(row, fields, nums)
	}
	return out
}

func schemaRow(row any, fields []any, nums bool) any {
	cells, _ := lookupPath(row, []string{"f"}).([]any)
	obj := map[string]any{}
	for i, f := range fields {
		fm, _ := f.(map[string]any)
		name := stringify(fm["name"])
		if name == "" || i >= len(cells) {
			continue
		}
		obj[name] = schemaCell(lookupPath(cells[i], []string{"v"}), fm, nums)
	}
	return obj
}

func schemaCell(v any, f map[string]any, nums bool) any {
	if v == nil {
		return nil
	}
	if stringify(f["mode"]) == "REPEATED" {
		list, _ := v.([]any)
		out := make([]any, len(list))
		single := map[string]any{}
		for k, x := range f {
			if k != "mode" {
				single[k] = x
			}
		}
		for i, el := range list {
			out[i] = schemaCell(lookupPath(el, []string{"v"}), single, nums)
		}
		return out
	}
	switch stringify(f["type"]) {
	case "RECORD", "STRUCT":
		sub, _ := f["fields"].([]any)
		return schemaRow(v, sub, nums)
	case "INTEGER", "INT64", "NUMERIC", "BIGNUMERIC", "FLOAT", "FLOAT64":
		if nums {
			if n, err := strconv.ParseFloat(stringify(v), 64); err == nil {
				return n
			}
		}
	case "BOOLEAN", "BOOL":
		if b, err := strconv.ParseBool(stringify(v)); err == nil {
			return b
		}
	}
	return v
}

// clientLimit honours the op's returnAll/limit params on a list the API
// returned whole (n8n slices client-side).
func (r *opRun) clientLimit(list []any) []any {
	if b, _ := toBool(r.params["returnAll"]); b == true {
		return list
	}
	n, _ := toInt(r.params["limit"])
	if l, ok := n.(int64); ok && l > 0 && int(l) < len(list) {
		return list[:l]
	}
	return list
}

// lessValue orders numbers numerically and anything else as text.
func lessValue(a, b any) bool {
	fa, ea := toFloat(a)
	fb, eb := toFloat(b)
	if ea == nil && eb == nil && fa != nil && fb != nil {
		return fa.(float64) < fb.(float64)
	}
	return stringify(a) < stringify(b)
}

// pagingOn reports whether pagination applies to this run.
func (r *opRun) pagingOn() bool {
	w := r.op.Pagination.When
	if w == "" {
		return true
	}
	v, err := renderString(w, r.scope)
	return err == nil && truthy(v)
}

// client is the HTTP client for this run: a connection whose
// allow_unauthorized_certs field is true (self-hosted Venafi, TheHive with
// self-signed certificates) skips TLS verification, as n8n's credential
// option does.
func (r *opRun) client() *http.Client {
	if !truthy(r.scope.Cred["allow_unauthorized_certs"]) {
		return httpClient
	}
	insecureOnce.Do(func() {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // user opted in per connection
		insecureClient = &http.Client{Timeout: httpClient.Timeout, Transport: t}
	})
	if httpClient.Transport != nil && httpClient.Transport != http.DefaultTransport {
		return httpClient // tests reroute traffic through their own transport
	}
	return insecureClient
}

var (
	insecureOnce   sync.Once
	insecureClient *http.Client
)
