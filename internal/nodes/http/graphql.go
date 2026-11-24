package httpnodes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// GraphQLNode sends a GraphQL query or mutation.
// Type: "http.graphql"
type GraphQLNode struct{}

func (n *GraphQLNode) Type() string { return "http.graphql" }

// graphqlPerItemFields are resolved against each item's JSON, like
// http.request's requestPerItemFields.
var graphqlPerItemFields = []string{"endpoint", "query", "variables", "operation_name", "headers"}

// PerItemConfigFields declares graphqlPerItemFields.
// CredentialDestinationKeys: see RequestNode.CredentialDestinationKeys.
func (n *GraphQLNode) CredentialDestinationKeys() [][]string {
	return [][]string{{"endpoint", "base_url", "instance_url"}, {"proxy"}}
}

func (n *GraphQLNode) PerItemConfigFields() []string { return graphqlPerItemFields }

type graphqlRun struct {
	method       string
	format       string // json | graphql (request body)
	respFormat   string // json | text
	dataProperty string
	copts        clientOptions
	client       *http.Client
	engine       *workflow.ExpressionEngine
	input        workflow.NodeInput
}

func (n *GraphQLNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	if strings.TrimSpace(configString(config, "endpoint")) == "" {
		return nil, fmt.Errorf("http.graphql: 'endpoint' is required")
	}
	if strings.TrimSpace(configString(config, "query")) == "" {
		return nil, fmt.Errorf("http.graphql: 'query' is required")
	}
	r := &graphqlRun{engine: workflow.NewExpressionEngine(), input: input, dataProperty: "data"}
	var err error
	if r.method, err = oneOf(config, "request_method", "post", []string{"post", "get"}, nil); err != nil {
		return nil, fmt.Errorf("http.graphql: %w", err)
	}
	r.method = strings.ToUpper(r.method)
	if r.format, err = oneOf(config, "request_format", "json", []string{"json", "graphql"}, nil); err != nil {
		return nil, fmt.Errorf("http.graphql: %w", err)
	}
	if r.respFormat, err = oneOf(config, "response_format", "json", []string{"json", "text"}, responseFormatAliases); err != nil {
		return nil, fmt.Errorf("http.graphql: %w", err)
	}
	if p := strings.TrimSpace(configString(config, "data_property_name")); p != "" {
		r.dataProperty = p
	}
	if r.copts, err = parseClientOptions(config, "http.graphql"); err != nil {
		return nil, err
	}
	r.copts.refuseInternal = workflow.AIAuthoredFrom(ctx)
	var closeIdle func()
	r.client, closeIdle = newHTTPClient(r.copts)
	defer closeIdle()

	items := input.Items
	if len(items) == 0 {
		items = []workflow.Item{{JSON: map[string]interface{}{}}}
	}
	var mainItems, errItems []workflow.Item
	for _, item := range items {
		m, e := r.runItem(ctx, config, item)
		mainItems = append(mainItems, m...)
		errItems = append(errItems, e...)
	}
	var outputs []workflow.NodeOutput
	if len(mainItems) > 0 {
		outputs = append(outputs, workflow.NodeOutput{Handle: "main", Items: mainItems})
	}
	if len(errItems) > 0 {
		outputs = append(outputs, workflow.NodeOutput{Handle: "error", Items: errItems})
	}
	return outputs, nil
}

func (r *graphqlRun) runItem(ctx context.Context, config map[string]interface{}, item workflow.Item) (mainItems, errItems []workflow.Item) {
	fail := func(err error) ([]workflow.Item, []workflow.Item) { return nil, []workflow.Item{errorItem(err)} }
	cfg, err := resolveItemFields(ctx, r.engine, config, graphqlPerItemFields, item, r.input, "http.graphql")
	if err != nil {
		return fail(err)
	}
	ob, err := r.prepare(cfg)
	if err != nil {
		return fail(fmt.Errorf("http.graphql: %w", err))
	}
	f, err := send(ctx, r.client, ob, r.copts.maxBodyBytes, cfg, item.JSON)
	if err != nil {
		return fail(err)
	}
	return r.shape(f)
}

// prepare builds the GraphQL request: GET puts query, variables and
// operationName in the query string; POST sends them as a JSON document,
// or the bare query as application/graphql with request_format graphql.
func (r *graphqlRun) prepare(cfg map[string]interface{}) (*outbound, error) {
	auth, err := buildAuthPlan(cfg, "http.graphql")
	if err != nil {
		return nil, err
	}
	u, err := resolveRequestURL(configString(cfg, "endpoint"), cfg, "endpoint")
	if err != nil {
		return nil, err
	}
	query := configString(cfg, "query")
	vars, err := graphqlVariables(cfg["variables"])
	if err != nil {
		return nil, err
	}
	opName := strings.TrimSpace(configString(cfg, "operation_name"))
	headers, err := parseNameValues(cfg["headers"], "headers")
	if err != nil {
		return nil, err
	}

	ob := &outbound{method: r.method, u: u, headers: headers, auth: auth}
	q := u.Query()
	if r.method == http.MethodGet {
		q.Set("query", query)
		if len(vars) > 0 {
			b, _ := json.Marshal(vars)
			q.Set("variables", string(b))
		}
		if opName != "" {
			q.Set("operationName", opName)
		}
	} else if r.format == "graphql" {
		ob.body, ob.hasBody, ob.contentType = []byte(query), true, "application/graphql"
	} else {
		doc := map[string]interface{}{"query": query, "variables": vars, "operationName": nil}
		if vars == nil {
			doc["variables"] = map[string]interface{}{}
		}
		if opName != "" {
			doc["operationName"] = opName
		}
		b, err := json.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("variables: %v", err)
		}
		ob.body, ob.hasBody, ob.contentType = b, true, "application/json"
	}
	auth.applyToQuery(q)
	if r.method == http.MethodGet || auth.qName != "" {
		u.RawQuery = q.Encode()
	}
	return ob, nil
}

// graphqlVariables reads variables: an object, or a JSON object string.
func graphqlVariables(v interface{}) (map[string]interface{}, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case map[string]interface{}:
		return t, nil
	case string:
		if strings.TrimSpace(t) == "" {
			return nil, nil
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(t), &m); err != nil {
			return nil, fmt.Errorf("variables: not a JSON object: %v", err)
		}
		return m, nil
	}
	return nil, fmt.Errorf("variables: want a JSON object, got %T", v)
}

// shape turns the response into items. A GraphQL "errors" list sends the
// item to the error handle even with a 200, as does a non-2xx status.
func (r *graphqlRun) shape(f *fetched) (mainItems, errItems []workflow.Item) {
	if r.respFormat == "text" {
		it := workflow.NewItem(map[string]interface{}{r.dataProperty: string(f.body)})
		if !isSuccess(f.status) {
			it.JSON["status"] = f.status
			return nil, []workflow.Item{it}
		}
		return []workflow.Item{it}, nil
	}
	var parsed interface{}
	if err := json.Unmarshal(f.body, &parsed); err != nil {
		return nil, []workflow.Item{workflow.NewItem(map[string]interface{}{
			"error":  "http.graphql: response body is not valid JSON (set response_format to text to keep it as text)",
			"status": f.status, "body": string(f.body),
		})}
	}
	if obj, ok := parsed.(map[string]interface{}); ok {
		if msg := graphqlErrorMessage(obj["errors"]); msg != "" {
			out := map[string]interface{}{"error": msg, "errors": obj["errors"], "status": f.status}
			if d, ok := obj["data"]; ok {
				out["data"] = d
			}
			return nil, []workflow.Item{workflow.NewItem(out)}
		}
	}
	if !isSuccess(f.status) {
		return nil, []workflow.Item{workflow.NewItem(map[string]interface{}{"status": f.status, "headers": headerMap(f.header, false), "body": parsed})}
	}
	return bodyItems(parsed, r.dataProperty), nil
}

// graphqlErrorMessage joins the messages of a GraphQL errors value; "" when
// there are none.
func graphqlErrorMessage(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case []interface{}:
		var msgs []string
		for _, e := range t {
			if m, ok := e.(map[string]interface{}); ok {
				if s, ok := m["message"].(string); ok {
					msgs = append(msgs, s)
					continue
				}
			}
			msgs = append(msgs, stringifyValue(e))
		}
		return strings.Join(msgs, ", ")
	}
	return ""
}
