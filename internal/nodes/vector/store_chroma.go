package vector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// chromaBackend talks to Chroma's HTTP API v2 (self-hosted or Chroma
// Cloud). The collection is looked up — and created when missing — with
// get_or_create, using cosine space; its id is cached for the run.
type chromaBackend struct {
	baseURL  string
	token    string
	tenant   string
	database string
	name     string
	id       string
}

func newChroma(t storeTarget) (backend, error) {
	db := t.namespace
	if db == "" {
		db = strOr(t.cfg, "default_database", "database")
	}
	return &chromaBackend{
		baseURL:  trimSlash(strOr(t.cfg, "http://localhost:8000", "url", "baseUrl", "base_url")),
		token:    strOr(t.cfg, "", "api_key", "token"),
		tenant:   strOr(t.cfg, "default_tenant", "tenant"),
		database: db,
		name:     t.collection,
	}, nil
}

func (b *chromaBackend) Close() error { return nil }

func (b *chromaBackend) headers() map[string]string {
	return map[string]string{"x-chroma-token": b.token}
}

func (b *chromaBackend) collectionsURL() string {
	return fmt.Sprintf("%s/api/v2/tenants/%s/databases/%s/collections", b.baseURL, url.PathEscape(b.tenant), url.PathEscape(b.database))
}

func (b *chromaBackend) collectionID(ctx context.Context) (string, error) {
	if b.id != "" {
		return b.id, nil
	}
	var resp struct {
		ID string `json:"id"`
	}
	body := map[string]interface{}{
		"name":          b.name,
		"get_or_create": true,
		"metadata":      map[string]interface{}{"hnsw:space": "cosine"},
	}
	if err := doJSON(ctx, "POST", b.collectionsURL(), b.headers(), body, &resp); err != nil {
		return "", fmt.Errorf("chroma collection %q: %w", b.name, err)
	}
	if resp.ID == "" {
		return "", fmt.Errorf("chroma collection %q: response carried no id", b.name)
	}
	b.id = resp.ID
	return b.id, nil
}

func (b *chromaBackend) post(ctx context.Context, op string, body interface{}, out interface{}) error {
	id, err := b.collectionID(ctx)
	if err != nil {
		return err
	}
	return doJSON(ctx, "POST", b.collectionsURL()+"/"+url.PathEscape(id)+"/"+op, b.headers(), body, out)
}

// chromaMetadata flattens metadata to the scalar values Chroma accepts;
// nested objects and arrays are stored as JSON text.
func chromaMetadata(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		switch v.(type) {
		case nil:
		case string, bool, float64, float32, int, int64:
			out[k] = v
		default:
			j, _ := json.Marshal(v)
			out[k] = string(j)
		}
	}
	return out
}

func (b *chromaBackend) Upsert(ctx context.Context, docs []Document) error {
	if len(docs) == 0 {
		return nil
	}
	ids := make([]string, len(docs))
	embs := make([][]float64, len(docs))
	texts := make([]string, len(docs))
	metas := make([]map[string]interface{}, len(docs))
	for i, d := range docs {
		ids[i], embs[i], texts[i], metas[i] = d.ID, d.Vector, d.Content, chromaMetadata(d.Metadata)
	}
	return b.post(ctx, "upsert", map[string]interface{}{
		"ids": ids, "embeddings": embs, "documents": texts, "metadatas": metas,
	}, nil)
}

func (b *chromaBackend) Search(ctx context.Context, vector []float64, topK int, filter interface{}) ([]Document, error) {
	body := map[string]interface{}{
		"query_embeddings": [][]float64{vector},
		"n_results":        topK,
		"include":          []string{"documents", "metadatas", "distances"},
	}
	if filter != nil {
		if _, ok := filter.(map[string]interface{}); !ok {
			return nil, fmt.Errorf("chroma filter must be a JSON where object, got %T", filter)
		}
		body["where"] = filter
	}
	var resp struct {
		IDs       [][]string                 `json:"ids"`
		Documents [][]*string                `json:"documents"`
		Metadatas [][]map[string]interface{} `json:"metadatas"`
		Distances [][]*float64               `json:"distances"`
	}
	if err := b.post(ctx, "query", body, &resp); err != nil {
		return nil, err
	}
	if len(resp.IDs) == 0 {
		return nil, nil
	}
	out := make([]Document, len(resp.IDs[0]))
	for i, id := range resp.IDs[0] {
		d := Document{ID: id}
		if len(resp.Documents) > 0 && i < len(resp.Documents[0]) && resp.Documents[0][i] != nil {
			d.Content = *resp.Documents[0][i]
		}
		if len(resp.Metadatas) > 0 && i < len(resp.Metadatas[0]) {
			d.Metadata = resp.Metadatas[0][i]
		}
		if len(resp.Distances) > 0 && i < len(resp.Distances[0]) && resp.Distances[0][i] != nil {
			d.Score = 1 - *resp.Distances[0][i]
		}
		out[i] = d
	}
	return out, nil
}

func (b *chromaBackend) Delete(ctx context.Context, ids []string, filter interface{}, all bool) (int, error) {
	if all {
		err := doJSON(ctx, "DELETE", b.collectionsURL()+"/"+url.PathEscape(b.name), b.headers(), nil, nil)
		b.id = ""
		if err != nil && !isNotFound(err) {
			return 0, err
		}
		return -1, nil
	}
	body := map[string]interface{}{}
	if len(ids) > 0 {
		body["ids"] = ids
	}
	if filter != nil {
		if _, ok := filter.(map[string]interface{}); !ok {
			return 0, fmt.Errorf("chroma filter must be a JSON where object, got %T", filter)
		}
		body["where"] = filter
	}
	return -1, b.post(ctx, "delete", body, nil)
}
