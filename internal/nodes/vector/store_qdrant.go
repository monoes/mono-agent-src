package vector

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// qdrantBackend talks to Qdrant's REST API.
type qdrantBackend struct {
	base, apiKey, collection string
	distance                 string
	create                   bool
	contentKey, metaKey      string
	exists                   bool // collection known to exist (cached per instance)
}

var qdrantDistances = map[string]string{
	"cosine": "Cosine", "dot": "Dot", "euclid": "Euclid", "euclidean": "Euclid", "manhattan": "Manhattan",
}

func newQdrant(t storeTarget) (backend, error) {
	base := trimSlash(strOr(t.cfg, "", "url", "qdrant_url"))
	if base == "" {
		return nil, fmt.Errorf("url is required for provider qdrant (e.g. http://localhost:6333)")
	}
	dist := strOr(t.cfg, "cosine", "distance")
	d, ok := qdrantDistances[strings.ToLower(dist)]
	if !ok {
		return nil, fmt.Errorf("distance %q is not supported for qdrant (want Cosine, Dot, Euclid or Manhattan)", dist)
	}
	return &qdrantBackend{
		base:       base,
		apiKey:     str(t.cfg, "api_key"),
		collection: t.collection,
		distance:   d,
		create:     boolField(t.cfg, "create_collection", true),
		contentKey: strOr(t.cfg, "content", "content_payload_key"),
		metaKey:    strOr(t.cfg, "metadata", "metadata_payload_key"),
	}, nil
}

func (q *qdrantBackend) Close() error { return nil }

func (q *qdrantBackend) headers() map[string]string { return map[string]string{"api-key": q.apiKey} }

func (q *qdrantBackend) collURL(suffix string) string {
	return q.base + "/collections/" + url.PathEscape(q.collection) + suffix
}

// storageID maps a document id onto one Qdrant/Weaviate accept: a UUID (or,
// for Qdrant, an unsigned integer). Other ids get a deterministic
// name-based UUID; mapped reports whether the original must be kept
// alongside as "doc_id".
func storageID(id string, allowUint bool) (sid interface{}, mapped bool) {
	if u, err := uuid.Parse(id); err == nil {
		return u.String(), false
	}
	if allowUint {
		if n, err := strconv.ParseUint(id, 10, 64); err == nil {
			return n, false
		}
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(id)).String(), true
}

func (q *qdrantBackend) ensureCollection(ctx context.Context, size int) error {
	if q.exists {
		return nil
	}
	err := doJSON(ctx, "GET", q.collURL(""), q.headers(), nil, nil)
	if err == nil {
		q.exists = true
		return nil
	}
	if !isNotFound(err) {
		return fmt.Errorf("check collection %q: %w", q.collection, err)
	}
	if !q.create {
		return fmt.Errorf("collection %q does not exist (create_collection is off)", q.collection)
	}
	body := map[string]interface{}{"vectors": map[string]interface{}{"size": size, "distance": q.distance}}
	if err := doJSON(ctx, "PUT", q.collURL(""), q.headers(), body, nil); err != nil {
		return fmt.Errorf("create collection %q: %w", q.collection, err)
	}
	q.exists = true
	return nil
}

func (q *qdrantBackend) Upsert(ctx context.Context, docs []Document) error {
	if len(docs) == 0 {
		return nil
	}
	if err := q.ensureCollection(ctx, len(docs[0].Vector)); err != nil {
		return err
	}
	points := make([]map[string]interface{}, len(docs))
	for i, d := range docs {
		sid, mapped := storageID(d.ID, true)
		payload := map[string]interface{}{q.contentKey: d.Content, q.metaKey: nonNilMap(d.Metadata)}
		if mapped {
			payload["doc_id"] = d.ID
		}
		points[i] = map[string]interface{}{"id": sid, "vector": d.Vector, "payload": payload}
	}
	return doJSON(ctx, "PUT", q.collURL("/points?wait=true"), q.headers(), map[string]interface{}{"points": points}, nil)
}

func (q *qdrantBackend) Search(ctx context.Context, vector []float64, topK int, filter interface{}) ([]Document, error) {
	body := map[string]interface{}{"vector": vector, "limit": topK, "with_payload": true}
	if filter != nil {
		body["filter"] = filter
	}
	var resp struct {
		Result []struct {
			ID      interface{}            `json:"id"`
			Score   float64                `json:"score"`
			Payload map[string]interface{} `json:"payload"`
		} `json:"result"`
	}
	if err := doJSON(ctx, "POST", q.collURL("/points/search"), q.headers(), body, &resp); err != nil {
		return nil, err
	}
	out := make([]Document, 0, len(resp.Result))
	for _, r := range resp.Result {
		id := valueText(r.ID)
		if s, ok := r.Payload["doc_id"].(string); ok && s != "" {
			id = s
		}
		content, _ := r.Payload[q.contentKey].(string)
		out = append(out, Document{ID: id, Content: content, Metadata: copyMap(asMap(r.Payload[q.metaKey])), Score: r.Score})
	}
	return out, nil
}

func (q *qdrantBackend) Delete(ctx context.Context, ids []string, filter interface{}, all bool) (int, error) {
	if all {
		if err := doJSON(ctx, "DELETE", q.collURL(""), q.headers(), nil, nil); err != nil && !isNotFound(err) {
			return 0, err
		}
		q.exists = false
		return -1, nil
	}
	if len(ids) > 0 {
		pts := make([]interface{}, len(ids))
		for i, id := range ids {
			pts[i], _ = storageID(id, true)
		}
		if err := doJSON(ctx, "POST", q.collURL("/points/delete?wait=true"), q.headers(), map[string]interface{}{"points": pts}, nil); err != nil {
			return 0, err
		}
	}
	if filter != nil {
		if err := doJSON(ctx, "POST", q.collURL("/points/delete?wait=true"), q.headers(), map[string]interface{}{"filter": filter}, nil); err != nil {
			return 0, err
		}
	}
	return -1, nil
}
