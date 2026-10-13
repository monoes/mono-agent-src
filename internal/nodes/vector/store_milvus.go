package vector

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// milvusBackend talks to Milvus (or Zilliz Cloud) over its REST API v2.
// The collection is created on first insert with Milvus' quick-setup schema:
// a VarChar primary key "id", a float vector "vector" and dynamic fields, so
// the text and metadata columns need no declaration.
type milvusBackend struct {
	baseURL    string
	auth       string
	dbName     string
	collection string
	textField  string
	metric     string
	create     bool
	ensured    bool
}

func newMilvus(t storeTarget) (backend, error) {
	b := &milvusBackend{
		baseURL:    trimSlash(strOr(t.cfg, "http://localhost:19530", "url", "baseUrl", "base_url")),
		dbName:     str(t.cfg, "db_name"),
		collection: t.collection,
		textField:  strOr(t.cfg, "text", "text_field"),
		metric:     strings.ToUpper(strOr(t.cfg, "COSINE", "metric_type")),
		create:     boolField(t.cfg, "create_collection", true),
	}
	if tok := str(t.cfg, "token"); tok != "" {
		b.auth = "Bearer " + tok
	} else if u := str(t.cfg, "username"); u != "" {
		b.auth = "Bearer " + u + ":" + str(t.cfg, "password")
	}
	return b, nil
}

func (b *milvusBackend) Close() error { return nil }

// milvusResponse is the envelope every v2 endpoint returns with HTTP 200;
// a non-zero code is the real error.
type milvusResponse struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (b *milvusBackend) call(ctx context.Context, path string, body map[string]interface{}) (json.RawMessage, error) {
	if b.dbName != "" {
		body["dbName"] = b.dbName
	}
	var resp milvusResponse
	if err := doJSON(ctx, "POST", b.baseURL+path, map[string]string{"Authorization": b.auth}, body, &resp); err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("milvus %s: code %d: %s", path, resp.Code, resp.Message)
	}
	return resp.Data, nil
}

func (b *milvusBackend) ensureCollection(ctx context.Context, dim int) error {
	if b.ensured || !b.create {
		return nil
	}
	data, err := b.call(ctx, "/v2/vectordb/collections/has", map[string]interface{}{"collectionName": b.collection})
	if err != nil {
		return err
	}
	var has struct {
		Has bool `json:"has"`
	}
	_ = json.Unmarshal(data, &has)
	if !has.Has {
		_, err = b.call(ctx, "/v2/vectordb/collections/create", map[string]interface{}{
			"collectionName":   b.collection,
			"dimension":        dim,
			"metricType":       b.metric,
			"idType":           "VarChar",
			"autoID":           false,
			"primaryFieldName": "id",
			"vectorFieldName":  "vector",
			"params":           map[string]interface{}{"max_length": "512"},
		})
		if err != nil {
			return fmt.Errorf("create collection %q: %w", b.collection, err)
		}
	}
	b.ensured = true
	return nil
}

func (b *milvusBackend) Upsert(ctx context.Context, docs []Document) error {
	if len(docs) == 0 {
		return nil
	}
	if err := b.ensureCollection(ctx, len(docs[0].Vector)); err != nil {
		return err
	}
	rows := make([]map[string]interface{}, len(docs))
	for i, d := range docs {
		rows[i] = map[string]interface{}{
			"id":        d.ID,
			"vector":    d.Vector,
			b.textField: d.Content,
			"metadata":  nonNilMap(d.Metadata),
		}
	}
	_, err := b.call(ctx, "/v2/vectordb/entities/upsert", map[string]interface{}{"collectionName": b.collection, "data": rows})
	return err
}

func milvusFilter(filter interface{}) (string, error) {
	switch f := filter.(type) {
	case nil:
		return "", nil
	case string:
		return f, nil
	}
	return "", fmt.Errorf("milvus filter must be a boolean expression string such as metadata[\"source\"] == \"a\", got %T", filter)
}

func (b *milvusBackend) Search(ctx context.Context, vector []float64, topK int, filter interface{}) ([]Document, error) {
	expr, err := milvusFilter(filter)
	if err != nil {
		return nil, err
	}
	body := map[string]interface{}{
		"collectionName": b.collection,
		"data":           [][]float64{vector},
		"annsField":      "vector",
		"limit":          topK,
		"outputFields":   []string{b.textField, "metadata"},
	}
	if expr != "" {
		body["filter"] = expr
	}
	data, err := b.call(ctx, "/v2/vectordb/entities/search", body)
	if err != nil {
		return nil, err
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("milvus search: decode results: %w", err)
	}
	out := make([]Document, 0, len(rows))
	for _, r := range rows {
		d := Document{ID: valueText(r["id"]), Metadata: asMap(r["metadata"])}
		d.Content, _ = r[b.textField].(string)
		if s, ok := r["distance"].(float64); ok {
			d.Score = s
		}
		out = append(out, d)
	}
	return out, nil
}

// milvusIDList renders ids as a Milvus "in" list of double-quoted strings.
func milvusIDList(ids []string) string {
	q := make([]string, len(ids))
	for i, id := range ids {
		b, _ := json.Marshal(id) // JSON string escaping matches Milvus string literals
		q[i] = string(b)
	}
	return "id in [" + strings.Join(q, ",") + "]"
}

func (b *milvusBackend) Delete(ctx context.Context, ids []string, filter interface{}, all bool) (int, error) {
	if all {
		_, err := b.call(ctx, "/v2/vectordb/collections/drop", map[string]interface{}{"collectionName": b.collection})
		b.ensured = false
		return -1, err
	}
	expr, err := milvusFilter(filter)
	if err != nil {
		return 0, err
	}
	var parts []string
	if len(ids) > 0 {
		parts = append(parts, milvusIDList(ids))
	}
	if expr != "" {
		parts = append(parts, "("+expr+")")
	}
	if len(parts) == 0 {
		return 0, fmt.Errorf("milvus delete needs ids or a filter")
	}
	data, err := b.call(ctx, "/v2/vectordb/entities/delete", map[string]interface{}{
		"collectionName": b.collection,
		"filter":         strings.Join(parts, " and "),
	})
	if err != nil {
		return 0, err
	}
	var res struct {
		DeleteCount *int `json:"deleteCount"`
	}
	if json.Unmarshal(data, &res) == nil && res.DeleteCount != nil {
		return *res.DeleteCount, nil
	}
	return -1, nil
}
