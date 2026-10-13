package vector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// weaviateBackend talks to Weaviate's REST and GraphQL APIs. The
// collection is the Weaviate class (first letter upper-cased, as Weaviate
// requires). Content is stored in the text_key property; metadata keys are
// flattened into top-level properties (nested values JSON-encoded).
//
// Filters differ by mode:
//   - search: filter is the raw GraphQL `where` argument text, e.g.
//     {path:["source"], operator:Equal, valueText:"faq.md"}
//   - delete: filter is Weaviate's where filter as JSON, e.g.
//     {"path":["source"],"operator":"Equal","valueText":"faq.md"}
//     (sent to the batch delete endpoint).
type weaviateBackend struct {
	base, apiKey, class, textKey string
	metaKeys                     []string
}

var graphQLName = regexp.MustCompile(`^[_A-Za-z][_0-9A-Za-z]*$`)

func newWeaviate(t storeTarget) (backend, error) {
	base := trimSlash(strOr(t.cfg, "", "url", "weaviate_cloud_endpoint"))
	if base == "" {
		return nil, fmt.Errorf("url is required for provider weaviate (e.g. http://localhost:8080)")
	}
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	r := []rune(t.collection)
	r[0] = unicode.ToUpper(r[0])
	w := &weaviateBackend{
		base: base, apiKey: strOr(t.cfg, "", "api_key", "weaviate_api_key"), class: string(r),
		textKey: strOr(t.cfg, "text", "text_key"), metaKeys: stringList(t.cfg, "metadata_keys"),
	}
	for _, n := range append([]string{w.class, w.textKey}, w.metaKeys...) {
		if !graphQLName.MatchString(n) {
			return nil, fmt.Errorf("%q is not a valid Weaviate class/property name (collection, text_key, metadata_keys)", n)
		}
	}
	return w, nil
}

func (w *weaviateBackend) Close() error { return nil }

func (w *weaviateBackend) headers() map[string]string {
	return map[string]string{"Authorization": bearer(w.apiKey)}
}

func (w *weaviateBackend) Upsert(ctx context.Context, docs []Document) error {
	objs := make([]map[string]interface{}, len(docs))
	for i, d := range docs {
		props := map[string]interface{}{}
		for k, v := range d.Metadata {
			switch v.(type) {
			case map[string]interface{}, []interface{}:
				b, _ := json.Marshal(v)
				props[k] = string(b)
			default:
				props[k] = v
			}
		}
		props[w.textKey] = d.Content
		sid, mapped := storageID(d.ID, false)
		if mapped {
			props["doc_id"] = d.ID
		}
		objs[i] = map[string]interface{}{"class": w.class, "id": sid, "properties": props, "vector": d.Vector}
	}
	var resp []struct {
		ID     string `json:"id"`
		Result struct {
			Errors *struct {
				Error []struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"errors"`
		} `json:"result"`
	}
	if err := doJSON(ctx, "POST", w.base+"/v1/batch/objects", w.headers(), map[string]interface{}{"objects": objs}, &resp); err != nil {
		return err
	}
	for _, r := range resp {
		if r.Result.Errors != nil && len(r.Result.Errors.Error) > 0 {
			return fmt.Errorf("object %s: %s", r.ID, r.Result.Errors.Error[0].Message)
		}
	}
	return nil
}

func (w *weaviateBackend) Search(ctx context.Context, vector []float64, topK int, filter interface{}) ([]Document, error) {
	where := ""
	switch f := filter.(type) {
	case nil:
	case string:
		where = " where: " + f
	default:
		return nil, fmt.Errorf("weaviate search filter must be GraphQL where-argument text, e.g. {path:[\"source\"], operator:Equal, valueText:\"a\"}; got %T", filter)
	}
	vec := make([]string, len(vector))
	for i, f := range vector {
		vec[i] = strconv.FormatFloat(f, 'g', -1, 64)
	}
	fields := append([]string{w.textKey, "doc_id"}, w.metaKeys...)
	q := fmt.Sprintf("{ Get { %s(nearVector:{vector:[%s]} limit:%d%s) { %s _additional { id distance } } } }",
		w.class, strings.Join(vec, ","), topK, where, strings.Join(fields, " "))
	var resp struct {
		Data struct {
			Get map[string][]map[string]interface{} `json:"Get"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := doJSON(ctx, "POST", w.base+"/v1/graphql", w.headers(), map[string]interface{}{"query": q}, &resp); err != nil {
		return nil, err
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("graphql: %s", resp.Errors[0].Message)
	}
	rows := resp.Data.Get[w.class]
	out := make([]Document, 0, len(rows))
	for _, row := range rows {
		add := asMap(row["_additional"])
		id, _ := add["id"].(string)
		if s, ok := row["doc_id"].(string); ok && s != "" {
			id = s
		}
		dist, _ := add["distance"].(float64)
		content, _ := row[w.textKey].(string)
		meta := map[string]interface{}{}
		for _, k := range w.metaKeys {
			if v, ok := row[k]; ok && v != nil {
				meta[k] = v
			}
		}
		out = append(out, Document{ID: id, Content: content, Metadata: meta, Score: 1 - dist})
	}
	return out, nil
}

func (w *weaviateBackend) Delete(ctx context.Context, ids []string, filter interface{}, all bool) (int, error) {
	if all {
		if err := doJSON(ctx, "DELETE", w.base+"/v1/schema/"+url.PathEscape(w.class), w.headers(), nil, nil); err != nil && !isNotFound(err) {
			return 0, err
		}
		return -1, nil
	}
	n := 0
	for _, id := range ids {
		sid, _ := storageID(id, false)
		err := doJSON(ctx, "DELETE", w.base+"/v1/objects/"+url.PathEscape(w.class)+"/"+url.PathEscape(valueText(sid)), w.headers(), nil, nil)
		if err != nil && !isNotFound(err) {
			return n, err
		}
		if err == nil {
			n++
		}
	}
	if filter != nil {
		fm, ok := filter.(map[string]interface{})
		if !ok {
			return n, fmt.Errorf("weaviate delete filter must be a where filter JSON object, e.g. {\"path\":[\"source\"],\"operator\":\"Equal\",\"valueText\":\"a\"}; got %T", filter)
		}
		var resp struct {
			Results struct {
				Successful int `json:"successful"`
			} `json:"results"`
		}
		body := map[string]interface{}{"match": map[string]interface{}{"class": w.class, "where": fm}}
		if err := doJSON(ctx, "DELETE", w.base+"/v1/batch/objects", w.headers(), body, &resp); err != nil {
			return n, err
		}
		n += resp.Results.Successful
	}
	return n, nil
}
