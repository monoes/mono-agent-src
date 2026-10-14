package vector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// databricksBackend talks to Databricks Vector Search's REST API. The index
// (catalog.schema.index) must exist: its description says which column is
// the primary key and whether Databricks embeds the text itself (a Delta
// Sync index with an embedding source column, searched by query_text) or
// stores the caller's vectors (searched by query_vector). Only Direct
// Access indexes accept writes.
type databricksBackend struct {
	host, token      string
	clientID, secret string
	index            string
	contentCol       string
	metaCols         []string
	queryType        string // ANN | HYBRID
	info             *databricksIndex
}

type databricksIndex struct {
	primaryKey, indexType string
	sourceCol, vectorCol  string   // sourceCol set: Databricks embeds the text
	columns               []string // schema columns, when the index declares them
}

func newDatabricks(t storeTarget) (backend, error) {
	host := trimSlash(strOr(t.cfg, "", "host", "url"))
	if host == "" {
		return nil, fmt.Errorf("host is required for provider databricks (the workspace URL, e.g. https://adb-123.4.azuredatabricks.net)")
	}
	b := &databricksBackend{
		host: host, token: str(t.cfg, "access_token"),
		clientID: str(t.cfg, "client_id"), secret: str(t.cfg, "client_secret"),
		index: t.collection, contentCol: str(t.cfg, "content_column"), metaCols: stringList(t.cfg, "metadata_columns"),
		queryType: strings.ToUpper(strOr(t.cfg, "ANN", "search_mode")),
	}
	if b.clientID != "" && b.secret != "" {
		b.token = "" // a service principal always fetches a fresh token
	}
	if b.token == "" && (b.clientID == "" || b.secret == "") {
		return nil, fmt.Errorf("databricks needs access_token, or client_id and client_secret of a service principal")
	}
	if b.queryType != "ANN" && b.queryType != "HYBRID" {
		return nil, fmt.Errorf("search_mode %q is not supported for databricks (want ANN or HYBRID)", b.queryType)
	}
	return b, nil
}

func (b *databricksBackend) Close() error { return nil }

// authorize fetches a service-principal token (OAuth client credentials)
// when no access token is configured.
func (b *databricksBackend) authorize(ctx context.Context) error {
	if b.token != "" {
		return nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "scope": {"all-apis"}}
	req, err := http.NewRequestWithContext(ctx, "POST", b.host+"/oidc/v1/token", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(b.clientID, b.secret)
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("databricks token: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("databricks token: HTTP %d: %s", resp.StatusCode, errorBodyEcho(data))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(data, &tok); err != nil || tok.AccessToken == "" {
		return fmt.Errorf("databricks token: no access_token in the response")
	}
	b.token = tok.AccessToken
	return nil
}

func (b *databricksBackend) headers() map[string]string {
	return map[string]string{"Authorization": "Bearer " + b.token}
}

func (b *databricksBackend) indexURL(suffix string) string {
	return b.host + "/api/2.0/vector-search/indexes/" + url.PathEscape(b.index) + suffix
}

// describe loads and caches the index description.
func (b *databricksBackend) describe(ctx context.Context) (*databricksIndex, error) {
	if b.info != nil {
		return b.info, nil
	}
	if err := b.authorize(ctx); err != nil {
		return nil, err
	}
	type cols []struct {
		Name string `json:"name"`
	}
	var raw struct {
		PrimaryKey string `json:"primary_key"`
		IndexType  string `json:"index_type"`
		Direct     *struct {
			Vectors    cols   `json:"embedding_vector_columns"`
			SchemaJSON string `json:"schema_json"`
		} `json:"direct_access_index_spec"`
		Delta *struct {
			Sources cols     `json:"embedding_source_columns"`
			Vectors cols     `json:"embedding_vector_columns"`
			Sync    []string `json:"columns_to_sync"`
		} `json:"delta_sync_index_spec"`
	}
	if err := doJSON(ctx, "GET", b.indexURL(""), b.headers(), nil, &raw); err != nil {
		return nil, fmt.Errorf("describe index %s: %w", b.index, err)
	}
	if raw.PrimaryKey == "" {
		return nil, fmt.Errorf("describe index %s: the response names no primary_key", b.index)
	}
	info := &databricksIndex{primaryKey: raw.PrimaryKey, indexType: raw.IndexType}
	if d := raw.Direct; d != nil {
		if len(d.Vectors) > 0 {
			info.vectorCol = d.Vectors[0].Name
		}
		var schema map[string]interface{}
		if json.Unmarshal([]byte(d.SchemaJSON), &schema) == nil {
			for k := range schema {
				info.columns = append(info.columns, k)
			}
			sort.Strings(info.columns)
		}
	}
	if d := raw.Delta; d != nil {
		if len(d.Sources) > 0 {
			info.sourceCol = d.Sources[0].Name
		}
		if len(d.Vectors) > 0 {
			info.vectorCol = d.Vectors[0].Name
		}
		info.columns = d.Sync
	}
	b.info = info
	return info, nil
}

func (b *databricksBackend) content(info *databricksIndex) (string, error) {
	if b.contentCol != "" {
		return b.contentCol, nil
	}
	if info.sourceCol != "" {
		return info.sourceCol, nil
	}
	return "", fmt.Errorf("content_column is required: index %s does not embed a source column itself", b.index)
}

func (b *databricksBackend) Upsert(ctx context.Context, docs []Document) error {
	info, err := b.describe(ctx)
	if err != nil {
		return err
	}
	if info.indexType != "DIRECT_ACCESS" || info.vectorCol == "" || info.sourceCol != "" {
		return fmt.Errorf("index %s syncs from its source table; only a Direct Access index accepts inserts", b.index)
	}
	content, err := b.content(info)
	if err != nil {
		return err
	}
	if content == info.primaryKey || content == info.vectorCol {
		return fmt.Errorf("content_column %q is the primary key or vector column of %s; choose another column", content, b.index)
	}
	known := map[string]bool{}
	for _, c := range info.columns {
		known[c] = true
	}
	rows := make([]map[string]interface{}, len(docs))
	for i, d := range docs {
		row := map[string]interface{}{}
		// Metadata keys outside the index schema would fail the whole batch.
		for k, v := range d.Metadata {
			if known[k] {
				row[k] = v
			}
		}
		row[info.primaryKey], row[content], row[info.vectorCol] = d.ID, d.Content, d.Vector
		rows[i] = row
	}
	inputs, err := json.Marshal(rows)
	if err != nil {
		return fmt.Errorf("encode rows: %w", err)
	}
	return b.checkStatus(ctx, "POST", b.indexURL("/upsert-data"), map[string]interface{}{"inputs_json": string(inputs)}, "upsert into")
}

// checkStatus sends a write and fails unless Databricks reports SUCCESS.
func (b *databricksBackend) checkStatus(ctx context.Context, method, u string, body interface{}, what string) error {
	var resp struct {
		Status string `json:"status"`
		Result struct {
			Failed []string `json:"failed_primary_keys"`
		} `json:"result"`
	}
	if err := doJSON(ctx, method, u, b.headers(), body, &resp); err != nil {
		return err
	}
	if resp.Status != "SUCCESS" {
		return fmt.Errorf("databricks rejected the %s %s (status %q); failed primary keys: %s",
			what, b.index, resp.Status, strings.Join(resp.Result.Failed, ", "))
	}
	return nil
}

func (b *databricksBackend) Search(ctx context.Context, vector []float64, topK int, filter interface{}) ([]Document, error) {
	return b.query(ctx, "", vector, topK, filter)
}

// SearchText sends the text as query_text: an index that embeds its own
// source column needs it, and HYBRID uses it for keyword matching.
func (b *databricksBackend) SearchText(ctx context.Context, text string, vector []float64, topK int, filter interface{}) ([]Document, error) {
	return b.query(ctx, text, vector, topK, filter)
}

func (b *databricksBackend) query(ctx context.Context, text string, vector []float64, topK int, filter interface{}) ([]Document, error) {
	info, err := b.describe(ctx)
	if err != nil {
		return nil, err
	}
	content, err := b.content(info)
	if err != nil {
		return nil, err
	}
	body := map[string]interface{}{"num_results": topK, "query_type": b.queryType}
	switch {
	case info.sourceCol != "":
		if text == "" {
			return nil, fmt.Errorf("index %s embeds its own text: search with a text query", b.index)
		}
		body["query_text"] = text
	case vector == nil:
		return nil, fmt.Errorf("index %s stores your vectors: set embedding.provider to embed the query", b.index)
	default:
		body["query_vector"] = vector
		if b.queryType == "HYBRID" && text != "" {
			body["query_text"] = text
		}
	}
	meta := b.metaCols
	if len(meta) == 0 {
		for _, c := range info.columns {
			if c != info.primaryKey && c != content && c != info.vectorCol {
				meta = append(meta, c)
			}
		}
	}
	columns := uniqueStrings(append([]string{info.primaryKey, content}, meta...))
	body["columns"] = columns
	if m := asMap(filter); len(m) > 0 {
		f, err := json.Marshal(m)
		if err != nil {
			return nil, fmt.Errorf("filter: %w", err)
		}
		body["filters_json"] = string(f)
	} else if filter != nil && m == nil {
		return nil, fmt.Errorf("databricks filter must be a JSON object such as {\"source\": \"handbook\"}, got %T", filter)
	}
	var resp struct {
		Manifest struct {
			Columns []struct {
				Name string `json:"name"`
			} `json:"columns"`
		} `json:"manifest"`
		Result struct {
			Rows [][]interface{} `json:"data_array"`
		} `json:"result"`
	}
	if err := doJSON(ctx, "POST", b.indexURL("/query"), b.headers(), body, &resp); err != nil {
		return nil, err
	}
	names := make([]string, len(resp.Manifest.Columns))
	for i, c := range resp.Manifest.Columns {
		names[i] = c.Name
	}
	out := make([]Document, 0, len(resp.Result.Rows))
	for _, row := range resp.Result.Rows {
		d := Document{Metadata: map[string]interface{}{}}
		for i, v := range row {
			if i >= len(names) {
				break
			}
			switch {
			// Databricks appends score after the requested columns.
			case i == len(row)-1 && names[i] == "score" && i >= len(columns):
				d.Score, _ = v.(float64)
			case names[i] == info.primaryKey:
				d.ID = valueText(v)
			case names[i] == content:
				d.Content = valueText(v)
			default:
				d.Metadata[names[i]] = v
			}
		}
		out = append(out, d)
	}
	return out, nil
}

func (b *databricksBackend) Delete(ctx context.Context, ids []string, filter interface{}, all bool) (int, error) {
	if all || filter != nil {
		return 0, fmt.Errorf("databricks deletes by primary key only: pass ids (clearing the index or deleting by filter is not supported)")
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if err := b.authorize(ctx); err != nil {
		return 0, err
	}
	q := url.Values{"primary_keys": ids}
	if err := b.checkStatus(ctx, "DELETE", b.indexURL("/delete-data")+"?"+q.Encode(), nil, "delete from"); err != nil {
		return 0, err
	}
	return len(ids), nil
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
