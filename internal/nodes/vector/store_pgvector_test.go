package vector

import (
	"context"
	"database/sql/driver"
	"reflect"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

func usePGFake(t *testing.T, dsn string) *fakeSQLState {
	t.Helper()
	old := pgDriverName
	pgDriverName = "vector-fakesql"
	t.Cleanup(func() { pgDriverName = old })
	return newFakeSQL(dsn)
}

func openPG(t *testing.T, cfg map[string]interface{}, table string) *pgBackend {
	t.Helper()
	be, err := newPGVector(context.Background(), storeTarget{cfg: cfg, collection: table})
	if err != nil {
		t.Fatalf("newPGVector: %v", err)
	}
	t.Cleanup(func() { be.Close() })
	return be.(*pgBackend)
}

func TestPGVectorUpsertCreatesTableAndUpserts(t *testing.T) {
	s := usePGFake(t, "pg-upsert")
	be := openPG(t, map[string]interface{}{"connection_string": "pg-upsert"}, "public.n8n_vectors")
	docs := []Document{
		{ID: "a", Content: "alpha", Metadata: map[string]interface{}{"src": "x"}, Vector: []float64{0.1, 0.25}},
		{ID: "b", Content: "beta", Vector: []float64{1, -2}},
	}
	if err := be.Upsert(context.Background(), docs); err != nil {
		t.Fatal(err)
	}
	// Second upsert must not re-run the DDL.
	if err := be.Upsert(context.Background(), docs[:1]); err != nil {
		t.Fatal(err)
	}
	ins := `INSERT INTO "public"."n8n_vectors" ("id", "text", "metadata", "embedding") VALUES ($1, $2, $3::jsonb, $4::vector) ON CONFLICT ("id") DO UPDATE SET "text" = EXCLUDED."text", "metadata" = EXCLUDED."metadata", "embedding" = EXCLUDED."embedding"`
	want := []fakeSQLCall{
		{Query: "CREATE EXTENSION IF NOT EXISTS vector", Args: []interface{}{}},
		{Query: `CREATE TABLE IF NOT EXISTS "public"."n8n_vectors" ("id" text PRIMARY KEY, "text" text, "metadata" jsonb, "embedding" vector)`, Args: []interface{}{}},
		{Query: ins, Args: []interface{}{"a", "alpha", `{"src":"x"}`, "[0.1,0.25]"}},
		{Query: ins, Args: []interface{}{"b", "beta", `{}`, "[1,-2]"}},
		{Query: ins, Args: []interface{}{"a", "alpha", `{"src":"x"}`, "[0.1,0.25]"}},
	}
	if !reflect.DeepEqual(s.calls, want) {
		t.Fatalf("calls:\n got %#v\nwant %#v", s.calls, want)
	}
	if s.commits != 2 {
		t.Errorf("commits = %d, want 2", s.commits)
	}
}

func TestPGVectorCustomColumnsNoCreate(t *testing.T) {
	s := usePGFake(t, "pg-cols")
	be := openPG(t, map[string]interface{}{
		"connection_string": "pg-cols", "create_table": false,
		"id_column": "doc_id", "content_column": "body", "metadata_column": "meta", "vector_column": "vec",
	}, "docs")
	if err := be.Upsert(context.Background(), []Document{{ID: "1", Content: "c", Vector: []float64{1}}}); err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 1 || !strings.HasPrefix(s.calls[0].Query, `INSERT INTO "docs" ("doc_id", "body", "meta", "vec")`) {
		t.Fatalf("calls: %#v", s.calls)
	}
}

func TestPGVectorSearchDistances(t *testing.T) {
	cases := []struct {
		distance, op string
		filter       interface{}
		wantWhere    string
		wantArgs     []interface{}
		wantScore    float64
	}{
		{"cosine", "<=>", nil, "", []interface{}{"[1,0]", 3}, 0.75},
		{"euclidean", "<->", map[string]interface{}{"lang": "en"}, ` WHERE "metadata" @> $2::jsonb`, []interface{}{"[1,0]", `{"lang":"en"}`, 3}, -0.25},
		{"inner_product", "<#>", nil, "", []interface{}{"[1,0]", 3}, -0.25},
	}
	for _, c := range cases {
		t.Run(c.distance, func(t *testing.T) {
			dsn := "pg-search-" + c.distance
			s := usePGFake(t, dsn)
			s.cols = []string{"id", "text", "metadata", "distance"}
			s.rows = [][]driver.Value{{"a", "alpha", []byte(`{"lang":"en"}`), 0.25}}
			be := openPG(t, map[string]interface{}{"connection_string": dsn, "distance": c.distance}, "n8n_vectors")
			hits, err := be.Search(context.Background(), []float64{1, 0}, 3, c.filter)
			if err != nil {
				t.Fatal(err)
			}
			n := len(c.wantArgs)
			wantQ := `SELECT "id", "text", "metadata", "embedding" ` + c.op + ` $1::vector AS distance FROM "n8n_vectors"` + c.wantWhere + ` ORDER BY distance LIMIT $` + string(rune('0'+n))
			if s.calls[0].Query != wantQ {
				t.Errorf("query:\n got %s\nwant %s", s.calls[0].Query, wantQ)
			}
			if !reflect.DeepEqual(s.calls[0].Args, c.wantArgs) {
				t.Errorf("args: got %#v want %#v", s.calls[0].Args, c.wantArgs)
			}
			want := []Document{{ID: "a", Content: "alpha", Metadata: map[string]interface{}{"lang": "en"}, Score: c.wantScore}}
			if !reflect.DeepEqual(hits, want) {
				t.Errorf("hits: got %#v want %#v", hits, want)
			}
		})
	}
}

func TestPGVectorSearchFilterMustBeObject(t *testing.T) {
	usePGFake(t, "pg-badfilter")
	be := openPG(t, map[string]interface{}{"connection_string": "pg-badfilter"}, "t")
	if _, err := be.Search(context.Background(), []float64{1}, 1, "lang = 'en'"); err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("err = %v", err)
	}
}

func TestPGVectorDelete(t *testing.T) {
	s := usePGFake(t, "pg-del")
	s.affected = 2
	be := openPG(t, map[string]interface{}{"connection_string": "pg-del"}, "t")
	ctx := context.Background()
	n, err := be.Delete(ctx, []string{"a", "b"}, nil, false)
	if err != nil || n != 2 {
		t.Fatalf("ids: n=%d err=%v", n, err)
	}
	if _, err := be.Delete(ctx, nil, map[string]interface{}{"k": "v"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := be.Delete(ctx, nil, nil, true); err != nil {
		t.Fatal(err)
	}
	want := []fakeSQLCall{
		{Query: `DELETE FROM "t" WHERE "id" = ANY($1)`, Args: []interface{}{`{"a","b"}`}},
		{Query: `DELETE FROM "t" WHERE "metadata" @> $1::jsonb`, Args: []interface{}{`{"k":"v"}`}},
		{Query: "CREATE EXTENSION IF NOT EXISTS vector", Args: []interface{}{}},
		{Query: `CREATE TABLE IF NOT EXISTS "t" ("id" text PRIMARY KEY, "text" text, "metadata" jsonb, "embedding" vector)`, Args: []interface{}{}},
		{Query: `DELETE FROM "t"`, Args: []interface{}{}},
	}
	if !reflect.DeepEqual(s.calls, want) {
		t.Fatalf("calls:\n got %#v\nwant %#v", s.calls, want)
	}
}

func TestPGVectorConfigErrors(t *testing.T) {
	usePGFake(t, "pg-err")
	cases := []struct {
		cfg   map[string]interface{}
		table string
		want  string
	}{
		{map[string]interface{}{}, "t", "connection_string is required"},
		{map[string]interface{}{"connection_string": "pg-err"}, "t; DROP", "collection (table)"},
		{map[string]interface{}{"connection_string": "pg-err", "vector_column": "a-b"}, "t", "vector_column"},
		{map[string]interface{}{"connection_string": "pg-err", "distance": "manhattan"}, "t", "distance"},
	}
	for _, c := range cases {
		_, err := newPGVector(context.Background(), storeTarget{cfg: c.cfg, collection: c.table})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("cfg %v: err = %v, want %q", c.cfg, err, c.want)
		}
	}
}

func TestPGVectorStoreNodeEndToEnd(t *testing.T) {
	s := usePGFake(t, "pg-e2e")
	node := &StoreNode{}
	_, err := node.Execute(context.Background(), workflow.NodeInput{Items: []workflow.Item{
		workflow.NewItem(map[string]interface{}{"id": "d1", "pageContent": "hello", "embedding": []interface{}{0.5, 0.5}}),
	}}, map[string]interface{}{"provider": "pgvector", "mode": "insert", "collection": "kb", "connection_string": "pg-e2e"})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.calls[len(s.calls)-1].Args; !reflect.DeepEqual(got, []interface{}{"d1", "hello", "{}", "[0.5,0.5]"}) {
		t.Fatalf("insert args: %#v", got)
	}
	s.cols = []string{"id", "text", "metadata", "distance"}
	s.rows = [][]driver.Value{{"d1", "hello", nil, 0.0}}
	out, err := node.Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{
		"provider": "pgvector", "mode": "search", "collection": "kb", "connection_string": "pg-e2e",
		"query_vector": []interface{}{0.5, 0.5}, "top_k": 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	items := out[0].Items
	if len(items) != 1 || items[0].JSON["id"] != "d1" || items[0].JSON["score"] != 1.0 || items[0].JSON["pageContent"] != "hello" {
		t.Fatalf("search items: %#v", items)
	}
}
