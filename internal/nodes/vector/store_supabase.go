package vector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// supabaseBackend stores documents in a Supabase (PostgREST) table with
// pgvector and searches through a SQL function called over /rpc — the same
// layout n8n's Supabase vector store expects, so existing tables work.
//
// One-time setup in the Supabase SQL editor (replace 1536 with the
// embedding model's dimension count):
//
//	create extension if not exists vector;
//
//	create table documents (
//	  id        text primary key default gen_random_uuid()::text,
//	  content   text,
//	  metadata  jsonb,
//	  embedding vector(1536)
//	);
//
//	create function match_documents(query_embedding vector(1536), match_count int, filter jsonb default '{}')
//	returns table (id text, content text, metadata jsonb, similarity float)
//	language sql stable as $$
//	  select d.id, d.content, d.metadata, 1 - (d.embedding <=> query_embedding) as similarity
//	  from documents d
//	  where d.metadata @> filter
//	  order by d.embedding <=> query_embedding
//	  limit match_count;
//	$$;
//
// The function name is configurable (query_name); its parameters must be
// query_embedding, match_count and filter.
type supabaseBackend struct {
	baseURL   string
	key       string
	table     string
	queryName string
}

func newSupabase(t storeTarget) (backend, error) {
	b := &supabaseBackend{
		baseURL:   trimSlash(strOr(t.cfg, "", "url", "host")),
		key:       strOr(t.cfg, "", "api_key", "service_role", "serviceRole"),
		table:     t.collection,
		queryName: strOr(t.cfg, "match_documents", "query_name"),
	}
	if b.baseURL == "" {
		return nil, fmt.Errorf("url is required (the Supabase project URL, e.g. https://xyz.supabase.co)")
	}
	if b.key == "" {
		return nil, fmt.Errorf("api_key is required (the Supabase service_role key)")
	}
	return b, nil
}

func (b *supabaseBackend) Close() error { return nil }

func (b *supabaseBackend) headers(prefer string) map[string]string {
	return map[string]string{"apikey": b.key, "Authorization": "Bearer " + b.key, "Prefer": prefer}
}

func (b *supabaseBackend) tableURL() string {
	return b.baseURL + "/rest/v1/" + url.PathEscape(b.table)
}

func (b *supabaseBackend) Upsert(ctx context.Context, docs []Document) error {
	if len(docs) == 0 {
		return nil
	}
	rows := make([]map[string]interface{}, len(docs))
	for i, d := range docs {
		rows[i] = map[string]interface{}{
			"id": d.ID, "content": d.Content, "metadata": nonNilMap(d.Metadata), "embedding": d.Vector,
		}
	}
	return doJSON(ctx, "POST", b.tableURL(), b.headers("resolution=merge-duplicates,return=minimal"), rows, nil)
}

func (b *supabaseBackend) Search(ctx context.Context, vector []float64, topK int, filter interface{}) ([]Document, error) {
	f := map[string]interface{}{}
	if filter != nil {
		m, ok := filter.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("supabase filter must be a JSON object matched against metadata, got %T", filter)
		}
		f = m
	}
	body := map[string]interface{}{"query_embedding": vector, "match_count": topK, "filter": f}
	var rows []struct {
		ID         interface{}            `json:"id"`
		Content    string                 `json:"content"`
		Metadata   map[string]interface{} `json:"metadata"`
		Similarity float64                `json:"similarity"`
	}
	endpoint := b.baseURL + "/rest/v1/rpc/" + url.PathEscape(b.queryName)
	if err := doJSON(ctx, "POST", endpoint, b.headers(""), body, &rows); err != nil {
		return nil, err
	}
	out := make([]Document, len(rows))
	for i, r := range rows {
		out[i] = Document{ID: valueText(r.ID), Content: r.Content, Metadata: r.Metadata, Score: r.Similarity}
	}
	return out, nil
}

// postgrestList renders ids as a PostgREST in.(…) list of quoted values.
func postgrestList(ids []string) string {
	q := make([]string, len(ids))
	for i, id := range ids {
		q[i] = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(id) + `"`
	}
	return "in.(" + strings.Join(q, ",") + ")"
}

func (b *supabaseBackend) Delete(ctx context.Context, ids []string, filter interface{}, all bool) (int, error) {
	q := url.Values{}
	switch {
	case all:
		q.Set("id", "not.is.null")
	default:
		if len(ids) > 0 {
			q.Set("id", postgrestList(ids))
		}
		if filter != nil {
			m, ok := filter.(map[string]interface{})
			if !ok {
				return 0, fmt.Errorf("supabase filter must be a JSON object matched against metadata, got %T", filter)
			}
			j, _ := json.Marshal(m)
			q.Set("metadata", "cs."+string(j))
		}
	}
	if len(q) == 0 {
		return 0, fmt.Errorf("supabase delete needs ids or a filter")
	}
	var rows []interface{}
	if err := doJSON(ctx, "DELETE", b.tableURL()+"?"+q.Encode(), b.headers("return=representation"), nil, &rows); err != nil {
		return 0, err
	}
	return len(rows), nil
}
