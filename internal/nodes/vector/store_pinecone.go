package vector

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// pineconeBackend talks to a Pinecone index's data plane. The index host
// comes from index_host or, when unset, from describing the index on the
// control plane.
type pineconeBackend struct {
	host, apiKey, namespace, textKey string
}

const pineconeAPIVersion = "2025-04"

func newPinecone(ctx context.Context, t storeTarget) (backend, error) {
	key := str(t.cfg, "api_key")
	if key == "" {
		return nil, fmt.Errorf("api_key is required for provider pinecone (set credential_id to a pinecone connection)")
	}
	p := &pineconeBackend{apiKey: key, namespace: t.namespace, textKey: strOr(t.cfg, "text", "text_key")}
	host := str(t.cfg, "index_host")
	if host == "" {
		control := trimSlash(strOr(t.cfg, "https://api.pinecone.io", "control_url"))
		var desc struct {
			Host string `json:"host"`
		}
		if err := doJSON(ctx, "GET", control+"/indexes/"+url.PathEscape(t.collection), p.headers(), nil, &desc); err != nil {
			return nil, fmt.Errorf("describe index %q (set index_host to skip this): %w", t.collection, err)
		}
		if desc.Host == "" {
			return nil, fmt.Errorf("describe index %q returned no host; set index_host", t.collection)
		}
		host = desc.Host
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	p.host = trimSlash(host)
	return p, nil
}

func (p *pineconeBackend) Close() error { return nil }

func (p *pineconeBackend) headers() map[string]string {
	return map[string]string{"Api-Key": p.apiKey, "X-Pinecone-API-Version": pineconeAPIVersion}
}

func (p *pineconeBackend) Upsert(ctx context.Context, docs []Document) error {
	vecs := make([]map[string]interface{}, len(docs))
	for i, d := range docs {
		meta := copyMap(d.Metadata)
		meta[p.textKey] = d.Content
		vecs[i] = map[string]interface{}{"id": d.ID, "values": d.Vector, "metadata": meta}
	}
	body := map[string]interface{}{"vectors": vecs, "namespace": p.namespace}
	return doJSON(ctx, "POST", p.host+"/vectors/upsert", p.headers(), body, nil)
}

func (p *pineconeBackend) Search(ctx context.Context, vector []float64, topK int, filter interface{}) ([]Document, error) {
	body := map[string]interface{}{
		"vector": vector, "topK": topK, "includeMetadata": true, "includeValues": false, "namespace": p.namespace,
	}
	if filter != nil {
		body["filter"] = filter
	}
	var resp struct {
		Matches []struct {
			ID       string                 `json:"id"`
			Score    float64                `json:"score"`
			Metadata map[string]interface{} `json:"metadata"`
		} `json:"matches"`
	}
	if err := doJSON(ctx, "POST", p.host+"/query", p.headers(), body, &resp); err != nil {
		return nil, err
	}
	out := make([]Document, 0, len(resp.Matches))
	for _, m := range resp.Matches {
		meta := copyMap(m.Metadata)
		content, _ := meta[p.textKey].(string)
		delete(meta, p.textKey)
		out = append(out, Document{ID: m.ID, Content: content, Metadata: meta, Score: m.Score})
	}
	return out, nil
}

func (p *pineconeBackend) Delete(ctx context.Context, ids []string, filter interface{}, all bool) (int, error) {
	send := func(body map[string]interface{}) error {
		body["namespace"] = p.namespace
		return doJSON(ctx, "POST", p.host+"/vectors/delete", p.headers(), body, nil)
	}
	if all {
		return -1, send(map[string]interface{}{"deleteAll": true})
	}
	if len(ids) > 0 {
		if err := send(map[string]interface{}{"ids": ids}); err != nil {
			return 0, err
		}
	}
	if filter != nil {
		if err := send(map[string]interface{}{"filter": filter}); err != nil {
			return 0, err
		}
	}
	return -1, nil
}
