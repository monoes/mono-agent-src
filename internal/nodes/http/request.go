package httpnodes

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/tracesig"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// RequestNode performs HTTP requests.
// Type: "http.request"
type RequestNode struct{}

func (n *RequestNode) Type() string { return "http.request" }

// requestPerItemFields are the config fields that commonly vary per item
// (e.g. a URL path or query param built from $json) and so must be resolved
// fresh for each item rather than once for the whole batch using only the
// first item's JSON. auth_* fields are deliberately excluded: they're almost
// always static across a batch, and leaving them out of this list keeps them
// on the normal top-level resolution path, where @secret:/@img- references
// already get resolved once — resolvePerItemConfig would otherwise need to
// duplicate that for no realistic benefit.
var requestPerItemFields = []string{"url", "query_params", "body", "headers"}

// paginationTemplateFields are held back from the engine's resolution
// too, but resolved per page (they read $response and $pageCount).
var paginationTemplateFields = []string{"pagination_next_url", "pagination_complete_expression", "pagination_parameters"}

// PerItemConfigFields declares requestPerItemFields and the pagination
// templates — see their doc comments.
func (n *RequestNode) PerItemConfigFields() []string {
	return append(append([]string{}, requestPerItemFields...), paginationTemplateFields...)
}

// requestRun is one execution of the node: settings parsed once, shared
// by every item.
type requestRun struct {
	method        string
	client        *http.Client
	copts         clientOptions
	ropts         responseOptions
	pag           paginationOptions
	arrayFormat   string
	keepCreds     bool
	batchSize     int
	batchInterval time.Duration
	engine        *workflow.ExpressionEngine
	input         workflow.NodeInput
}

func (n *RequestNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	if strings.TrimSpace(configString(config, "url")) == "" {
		return nil, fmt.Errorf("http.request: 'url' is required")
	}
	r, err := newRequestRun(config, input)
	if err != nil {
		return nil, err
	}
	var closeIdle func()
	r.client, closeIdle = newHTTPClient(r.copts)
	defer closeIdle()

	items := input.Items
	if len(items) == 0 {
		items = []workflow.Item{{JSON: map[string]interface{}{}}}
	}

	var mainItems, errorItems []workflow.Item
	for i, item := range items {
		if i > 0 && r.batchSize > 0 && r.batchInterval > 0 && i%r.batchSize == 0 {
			if err := sleepCtx(ctx, r.batchInterval); err != nil {
				return nil, err
			}
		}
		m, e := r.runItem(ctx, config, item)
		mainItems = append(mainItems, m...)
		errorItems = append(errorItems, e...)
	}

	var outputs []workflow.NodeOutput
	if len(mainItems) > 0 {
		outputs = append(outputs, workflow.NodeOutput{Handle: "main", Items: mainItems})
	}
	if len(errorItems) > 0 {
		outputs = append(outputs, workflow.NodeOutput{Handle: "error", Items: errorItems})
	}
	return outputs, nil
}

func newRequestRun(config map[string]interface{}, input workflow.NodeInput) (*requestRun, error) {
	r := &requestRun{engine: workflow.NewExpressionEngine(), input: input}
	r.method = strings.ToUpper(strings.TrimSpace(configString(config, "method")))
	if r.method == "" {
		r.method = http.MethodGet
	}
	if strings.ContainsAny(r.method, " \t\r\n/:()<>@,;\"[]?={}") {
		return nil, fmt.Errorf("http.request: method: %q is not an HTTP method", r.method)
	}
	var err error
	if r.copts, err = parseClientOptions(config, "http.request"); err != nil {
		return nil, err
	}
	if r.ropts, err = parseResponseOptions(config, "http.request"); err != nil {
		return nil, err
	}
	if r.pag, err = parsePaginationOptions(config, "http.request"); err != nil {
		return nil, err
	}
	if r.arrayFormat, err = oneOf(config, "query_array_format", "repeat", []string{"repeat", "brackets", "indices"}, nil); err != nil {
		return nil, fmt.Errorf("http.request: %w", err)
	}
	if r.keepCreds, err = configBool(config, "send_credentials_on_redirect", false); err != nil {
		return nil, fmt.Errorf("http.request: %w", err)
	}
	if n, ok, err := configNumber(config, "batch_size"); err != nil {
		return nil, fmt.Errorf("http.request: %w", err)
	} else if ok && n > 0 {
		r.batchSize = int(n)
	}
	if n, ok, err := configNumber(config, "batch_interval_ms"); err != nil {
		return nil, fmt.Errorf("http.request: %w", err)
	} else if ok && n > 0 {
		r.batchInterval = time.Duration(n) * time.Millisecond
	}
	return r, nil
}

// runItem makes the request (or the pages of it) for one input item.
func (r *requestRun) runItem(ctx context.Context, config map[string]interface{}, item workflow.Item) (mainItems, errItems []workflow.Item) {
	itemConfig, err := resolveItemFields(ctx, r.engine, config, requestPerItemFields, item, r.input, "http.request")
	if err != nil {
		return nil, []workflow.Item{errorItem(err)}
	}
	auth, err := buildAuthPlan(itemConfig, "http.request")
	if err != nil {
		return nil, []workflow.Item{errorItem(err)}
	}
	switch r.pag.mode {
	case "offset":
		return r.paginateOffset(ctx, itemConfig, item, auth)
	case "update_parameter", "next_url":
		return r.paginate(ctx, itemConfig, item, auth)
	}
	ob, err := r.prepare(ctx, itemConfig, item, auth, pageOverrides{}, -1)
	if err != nil {
		return nil, []workflow.Item{errorItem(err)}
	}
	f, err := send(ctx, r.client, ob, r.copts.maxBodyBytes, itemConfig, item.JSON)
	if err != nil {
		return nil, []workflow.Item{errorItem(err)}
	}
	items, failed := shapeResponse(f, r.ropts)
	if failed {
		return nil, items
	}
	return items, nil
}

func errorItem(err error) workflow.Item {
	return workflow.NewItem(map[string]interface{}{"error": err.Error()})
}

// pageOverrides are what a pagination step changes on the base request.
type pageOverrides struct {
	url, base string
	query     map[string]interface{}
	headers   map[string]interface{}
	body      map[string]interface{}
}

func (o *pageOverrides) set(in, name, value string) {
	target := &o.query
	switch in {
	case "header":
		target = &o.headers
	case "body":
		target = &o.body
	}
	if *target == nil {
		*target = map[string]interface{}{}
	}
	(*target)[name] = value
}

// prepare builds the request for one item (and page). legacyPage >= 0 adds
// the offset mode's page_field / per_page query parameters.
func (r *requestRun) prepare(ctx context.Context, itemConfig map[string]interface{}, item workflow.Item, auth authPlan, ov pageOverrides, legacyPage int) (*outbound, error) {
	u, err := resolveRequestURL(configString(itemConfig, "url"), itemConfig, "url")
	if err != nil {
		return nil, fmt.Errorf("http.request: %w", err)
	}
	skip := map[string]bool{}
	if ov.url != "" {
		base := u
		if ov.base != "" {
			if b, err := url.Parse(ov.base); err == nil {
				base = b
			}
		}
		next, err := base.Parse(ov.url)
		if err != nil || (next.Scheme != "http" && next.Scheme != "https") {
			return nil, fmt.Errorf("http.request: pagination_next_url: %q is not a valid URL", ov.url)
		}
		u = next
		for k := range u.Query() {
			skip[k] = true
		}
	}

	params, err := parseNameValues(itemConfig["query_params"], "query_params")
	if err != nil {
		return nil, fmt.Errorf("http.request: %w", err)
	}
	if len(params) > 0 || len(ov.query) > 0 || legacyPage >= 0 || auth.qName != "" {
		q := u.Query()
		applyQuery(q, params, r.arrayFormat, skip)
		for k, v := range ov.query {
			q.Set(k, stringifyValue(v))
		}
		if legacyPage >= 0 {
			q.Set(r.pag.pageField, fmt.Sprintf("%d", legacyPage))
			q.Set("per_page", fmt.Sprintf("%d", r.pag.pageSize))
		}
		auth.applyToQuery(q)
		u.RawQuery = q.Encode()
	}

	headers, err := parseNameValues(itemConfig["headers"], "headers")
	if err != nil {
		return nil, fmt.Errorf("http.request: %w", err)
	}
	extra, _ := parseNameValues(ov.headers, "pagination_parameters")
	headers = append(headers, extra...)

	body, err := buildBody(ctx, r.method, itemConfig, item, ov.body)
	if err != nil {
		return nil, fmt.Errorf("http.request: %w", err)
	}
	return &outbound{
		method: r.method, u: u, headers: headers,
		body: body.data, hasBody: body.present, contentType: body.contentType,
		auth: auth, keepCreds: r.keepCreds,
	}, nil
}

// resolveItemFields resolves fields against one item's own JSON and
// returns a copy of base with those fields overlaid by the result — base
// itself is never mutated, since it's shared across every item in the
// batch. Mirrors the resolution pipeline RunExecution normally applies
// once for the whole config (internal/workflow/execution.go): expression
// templates, then @img-NNN vault refs, then @secret:<name> refs. Every
// other field (including the auth_* ones) already went through that same
// pipeline once, batch-wide, in RunExecution before Execute was called.
func resolveItemFields(ctx context.Context, engine *workflow.ExpressionEngine, base map[string]interface{}, fields []string, item workflow.Item, input workflow.NodeInput, node string) (map[string]interface{}, error) {
	overlay := make(map[string]interface{}, len(fields))
	for _, k := range fields {
		if v, ok := base[k]; ok {
			overlay[k] = v
		}
	}

	resolved, err := engine.ResolveConfig(overlay, input.ItemContext(item.JSON))
	if err != nil {
		return nil, fmt.Errorf("%s: resolve per-item config: %w", node, err)
	}

	if vaultDB := vault.DBFromContext(ctx); vaultDB != nil {
		_ = vault.ResolveConfig(ctx, vaultDB, resolved)
		_ = secrets.ResolveConfig(ctx, vaultDB, vault.ProfileIDFromContext(ctx), resolved)
	}

	itemConfig := make(map[string]interface{}, len(base))
	for k, v := range base {
		itemConfig[k] = v
	}
	for k, v := range resolved {
		itemConfig[k] = v
	}
	return itemConfig, nil
}

// setTraceHeader adds the run's chain as a signed X-Monoagent-Trace header
// (internal/tracesig), when the run is on one and the request goes where a
// webhook of this machine can be: a loopback address, or the host of
// MONOAGENT_WEBHOOK_ADDR (any of this machine's addresses or its name when
// that is a wildcard bind; see ownHost). Other hosts get it only with
// propagate_trace (an outside system that returns the header to a webhook
// here): the token cannot lower a hop, but a third party holding one could
// push its chain to the limit. The hop signed is the deeper of the run's
// and the item's on the same chain (workflow.RunTrace). Without a key the
// request goes out without it, and any header of that name the workflow
// configured is removed either way.
func setTraceHeader(ctx context.Context, req *http.Request, config map[string]interface{}, item map[string]interface{}) {
	req.Header.Del(tracesig.Header)
	chain, hop, ok := workflow.RunTrace(ctx, item)
	if !ok {
		return
	}
	if propagate, _ := config["propagate_trace"].(bool); !propagate && !ownHost(req.URL.Hostname()) {
		return
	}
	key, err := tracesig.Key()
	if err != nil {
		return
	}
	tok, err := tracesig.Sign(key, chain, hop)
	if err != nil {
		return
	}
	req.Header.Set(tracesig.Header, tok)
}
