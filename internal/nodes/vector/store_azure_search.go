package vector

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// azureSearchBackend talks to Azure AI Search's REST API with an admin
// API key. Indexes use the LangChain layout n8n creates, so an index
// written by n8n can be read here and the other way round:
//
//	id             Edm.String (key)
//	content        Edm.String (searchable)
//	content_vector Collection(Edm.Single) (vector field)
//	metadata       {source: Edm.String, attributes: [{key, value}]}
type azureSearchBackend struct {
	endpoint, apiKey, apiVersion, index string
	queryType, semanticConfig           string
	metadataKeys                        []string
	exists                              bool
}

const azureSearchVectorField = "content_vector"

// azureKey is what Azure AI Search accepts as a document key.
var azureKey = regexp.MustCompile(`^[A-Za-z0-9_\-=]+$`)

var azureQueryTypes = map[string]string{
	"vector": "vector", "hybrid": "hybrid", "semantichybrid": "semantic_hybrid", "semantic_hybrid": "semantic_hybrid",
}

func newAzureSearch(t storeTarget) (backend, error) {
	endpoint := trimSlash(strOr(t.cfg, "", "endpoint", "url"))
	if endpoint == "" {
		return nil, fmt.Errorf("endpoint is required for provider azure_ai_search (e.g. https://<service>.search.windows.net)")
	}
	key := str(t.cfg, "api_key")
	if key == "" {
		return nil, fmt.Errorf("api_key is required for provider azure_ai_search (an admin key of the search service)")
	}
	qt := strOr(t.cfg, "hybrid", "query_type")
	norm, ok := azureQueryTypes[strings.ToLower(qt)]
	if !ok {
		return nil, fmt.Errorf("query_type %q is not supported for azure_ai_search (want vector, hybrid or semantic_hybrid)", qt)
	}
	return &azureSearchBackend{
		endpoint:       endpoint,
		apiKey:         key,
		apiVersion:     strOr(t.cfg, "2024-07-01", "api_version"),
		index:          t.collection,
		queryType:      norm,
		semanticConfig: str(t.cfg, "semantic_configuration"),
		metadataKeys:   stringList(t.cfg, "metadata_keys"),
	}, nil
}

func (a *azureSearchBackend) Close() error { return nil }

func (a *azureSearchBackend) headers() map[string]string {
	return map[string]string{"api-key": a.apiKey}
}

func (a *azureSearchBackend) indexURL(suffix string) string {
	return a.endpoint + "/indexes/" + url.PathEscape(a.index) + suffix + "?api-version=" + url.QueryEscape(a.apiVersion)
}

func (a *azureSearchBackend) ensureIndex(ctx context.Context, dims int) error {
	if a.exists {
		return nil
	}
	err := doJSON(ctx, "GET", a.indexURL(""), a.headers(), nil, nil)
	if err == nil {
		a.exists = true
		return nil
	}
	if !isNotFound(err) {
		return fmt.Errorf("check index %q: %w", a.index, err)
	}
	attr := []map[string]interface{}{
		{"name": "key", "type": "Edm.String", "filterable": true},
		{"name": "value", "type": "Edm.String", "filterable": true},
	}
	body := map[string]interface{}{
		"name": a.index,
		"fields": []map[string]interface{}{
			{"name": "id", "type": "Edm.String", "key": true, "filterable": true},
			{"name": "content", "type": "Edm.String", "searchable": true},
			{"name": azureSearchVectorField, "type": "Collection(Edm.Single)", "searchable": true,
				"dimensions": dims, "vectorSearchProfile": "vector-search-profile"},
			{"name": "metadata", "type": "Edm.ComplexType", "fields": []map[string]interface{}{
				{"name": "source", "type": "Edm.String", "filterable": true},
				{"name": "attributes", "type": "Collection(Edm.ComplexType)", "fields": attr},
			}},
		},
		"vectorSearch": map[string]interface{}{
			"algorithms": []map[string]interface{}{{"name": "vector-search-algorithm", "kind": "hnsw",
				"hnswParameters": map[string]interface{}{"metric": "cosine"}}},
			"profiles": []map[string]interface{}{{"name": "vector-search-profile", "algorithm": "vector-search-algorithm"}},
		},
	}
	if err := doJSON(ctx, "PUT", a.indexURL(""), a.headers(), body, nil); err != nil {
		return fmt.Errorf("create index %q: %w", a.index, err)
	}
	a.exists = true
	return nil
}

// azureMetadata converts metadata to the index's {source, attributes}
// shape; keys limits the attributes when set. Values become strings.
func azureMetadata(meta map[string]interface{}, keys []string) map[string]interface{} {
	if len(keys) == 0 {
		for k := range meta {
			keys = append(keys, k)
		}
		sort.Strings(keys)
	}
	attrs := []map[string]interface{}{}
	for _, k := range keys {
		if v, ok := meta[k]; ok && v != nil {
			attrs = append(attrs, map[string]interface{}{"key": k, "value": valueText(v)})
		}
	}
	src, _ := meta["source"].(string)
	return map[string]interface{}{"source": src, "attributes": attrs}
}

func (a *azureSearchBackend) Upsert(ctx context.Context, docs []Document) error {
	if len(docs) == 0 {
		return nil
	}
	if err := a.ensureIndex(ctx, len(docs[0].Vector)); err != nil {
		return err
	}
	value := make([]map[string]interface{}, len(docs))
	for i, d := range docs {
		if !azureKey.MatchString(d.ID) {
			return fmt.Errorf("document id %q is not a valid Azure AI Search key (letters, digits, _ - = only)", d.ID)
		}
		value[i] = map[string]interface{}{
			"@search.action": "mergeOrUpload", "id": d.ID, "content": d.Content,
			azureSearchVectorField: d.Vector, "metadata": azureMetadata(nonNilMap(d.Metadata), a.metadataKeys),
		}
	}
	return a.indexBatch(ctx, value)
}

// indexBatch posts a docs/index batch and fails when any document failed.
func (a *azureSearchBackend) indexBatch(ctx context.Context, value []map[string]interface{}) error {
	var resp struct {
		Value []struct {
			Key          string `json:"key"`
			Status       bool   `json:"status"`
			ErrorMessage string `json:"errorMessage"`
		} `json:"value"`
	}
	if err := doJSON(ctx, "POST", a.indexURL("/docs/index"), a.headers(), map[string]interface{}{"value": value}, &resp); err != nil {
		return err
	}
	for _, r := range resp.Value {
		if !r.Status {
			return fmt.Errorf("document %q: %s", r.Key, r.ErrorMessage)
		}
	}
	return nil
}

// azureFilter returns the OData filter text; Azure takes no other form.
func azureFilter(filter interface{}) (string, error) {
	switch f := filter.(type) {
	case nil:
		return "", nil
	case string:
		return f, nil
	}
	return "", fmt.Errorf("azure_ai_search filter must be OData text such as \"metadata/source eq 'a.pdf'\", got %T", filter)
}

func (a *azureSearchBackend) Search(ctx context.Context, vector []float64, topK int, filter interface{}) ([]Document, error) {
	return a.search(ctx, "", vector, topK, filter)
}

// SearchText adds the query text for hybrid and semantic hybrid search.
func (a *azureSearchBackend) SearchText(ctx context.Context, text string, vector []float64, topK int, filter interface{}) ([]Document, error) {
	if vector == nil {
		return nil, fmt.Errorf("azure_ai_search needs a query vector: set embedding.provider")
	}
	if a.queryType == "vector" {
		text = ""
	}
	return a.search(ctx, text, vector, topK, filter)
}

func (a *azureSearchBackend) search(ctx context.Context, text string, vector []float64, topK int, filter interface{}) ([]Document, error) {
	f, err := azureFilter(filter)
	if err != nil {
		return nil, err
	}
	body := map[string]interface{}{
		"top": topK, "select": "id,content,metadata",
		"vectorQueries": []map[string]interface{}{{"kind": "vector", "vector": vector, "k": topK, "fields": azureSearchVectorField}},
	}
	if f != "" {
		body["filter"] = f
	}
	if text != "" {
		body["search"] = text
		if a.queryType == "semantic_hybrid" {
			body["queryType"] = "semantic"
			if a.semanticConfig != "" {
				body["semanticConfiguration"] = a.semanticConfig
			}
		}
	}
	var resp struct {
		Value []map[string]interface{} `json:"value"`
	}
	if err := doJSON(ctx, "POST", a.indexURL("/docs/search"), a.headers(), body, &resp); err != nil {
		return nil, err
	}
	out := make([]Document, 0, len(resp.Value))
	for _, r := range resp.Value {
		d := Document{ID: valueText(r["id"]), Metadata: map[string]interface{}{}}
		d.Content, _ = r["content"].(string)
		if s, ok := r["@search.rerankerScore"].(float64); ok {
			d.Score = s
		} else {
			d.Score, _ = r["@search.score"].(float64)
		}
		if m := asMap(r["metadata"]); m != nil {
			if src, _ := m["source"].(string); src != "" {
				d.Metadata["source"] = src
			}
			if attrs, ok := m["attributes"].([]interface{}); ok {
				for _, at := range attrs {
					if kv := asMap(at); kv != nil {
						d.Metadata[valueText(kv["key"])] = kv["value"]
					}
				}
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// maxAzureFilterDeletes bounds the rounds of a filter delete's
// search-then-delete loop; each round removes up to 1000 documents.
const maxAzureFilterDeletes = 1000

func (a *azureSearchBackend) Delete(ctx context.Context, ids []string, filter interface{}, all bool) (int, error) {
	if all {
		// n8n's "Clear Index" drops the index; the next insert recreates it.
		if err := doJSON(ctx, "DELETE", a.indexURL(""), a.headers(), nil, nil); err != nil && !isNotFound(err) {
			return 0, err
		}
		a.exists = false
		return -1, nil
	}
	n := 0
	if len(ids) > 0 {
		if err := a.deleteKeys(ctx, ids); err != nil {
			return 0, err
		}
		n += len(ids)
	}
	if filter != nil {
		f, err := azureFilter(filter)
		if err != nil {
			return 0, err
		}
		deleted := map[string]bool{}
		for round := 0; round < maxAzureFilterDeletes; round++ {
			var resp struct {
				Value []struct {
					ID string `json:"id"`
				} `json:"value"`
			}
			body := map[string]interface{}{"filter": f, "select": "id", "top": 1000}
			if err := doJSON(ctx, "POST", a.indexURL("/docs/search"), a.headers(), body, &resp); err != nil {
				return 0, err
			}
			// Deletes reach search results after a short delay, so a
			// round that finds only documents already deleted ends it.
			var keys []string
			for _, v := range resp.Value {
				if !deleted[v.ID] {
					deleted[v.ID] = true
					keys = append(keys, v.ID)
				}
			}
			if len(keys) == 0 {
				break
			}
			if err := a.deleteKeys(ctx, keys); err != nil {
				return 0, err
			}
			n += len(keys)
		}
	}
	return n, nil
}

func (a *azureSearchBackend) deleteKeys(ctx context.Context, keys []string) error {
	value := make([]map[string]interface{}, len(keys))
	for i, k := range keys {
		value[i] = map[string]interface{}{"@search.action": "delete", "id": k}
	}
	return a.indexBatch(ctx, value)
}
