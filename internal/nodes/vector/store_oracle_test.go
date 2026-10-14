package vector

import (
	"context"
	"database/sql/driver"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func useOracleFake(t *testing.T, dsn string) *fakeSQLState {
	t.Helper()
	old := oracleDriverName
	oracleDriverName = "vector-fakesql"
	t.Cleanup(func() { oracleDriverName = old })
	return newFakeSQL(dsn)
}

func openOracle(t *testing.T, cfg map[string]interface{}) *oracleBackend {
	t.Helper()
	be, err := newOracle(storeTarget{cfg: cfg, collection: "n8n_vectors"})
	if err != nil {
		t.Fatalf("newOracle: %v", err)
	}
	t.Cleanup(func() { be.Close() })
	return be.(*oracleBackend)
}

func TestOracleUpsertCreatesTableAndMerges(t *testing.T) {
	s := useOracleFake(t, "ora-upsert")
	be := openOracle(t, map[string]interface{}{"connection_string": "ora-upsert"})
	docs := []Document{{ID: "a", Content: "alpha", Metadata: map[string]interface{}{"src": "x"}, Vector: []float64{0.5, 1}}}
	if err := be.Upsert(context.Background(), docs); err != nil {
		t.Fatal(err)
	}
	if err := be.Upsert(context.Background(), docs); err != nil {
		t.Fatal(err)
	}
	merge := "MERGE INTO n8n_vectors t USING (SELECT :1 AS id FROM dual) s ON (t.id = s.id) " +
		"WHEN MATCHED THEN UPDATE SET t.text = :2, t.metadata = :3, t.embedding = TO_VECTOR(:4) " +
		"WHEN NOT MATCHED THEN INSERT (id, text, metadata, embedding) VALUES (:5, :6, :7, TO_VECTOR(:8))"
	args := []interface{}{"a", "alpha", `{"src":"x"}`, "[0.5,1]", "a", "alpha", `{"src":"x"}`, "[0.5,1]"}
	want := []fakeSQLCall{
		{Query: "CREATE TABLE IF NOT EXISTS n8n_vectors (id VARCHAR2(512) PRIMARY KEY, text CLOB, metadata CLOB CHECK (metadata IS JSON), embedding VECTOR)", Args: []interface{}{}},
		{Query: merge, Args: args},
		{Query: merge, Args: args},
	}
	if !reflect.DeepEqual(s.calls, want) {
		t.Fatalf("calls:\n got %#v\nwant %#v", s.calls, want)
	}
	if s.commits != 2 {
		t.Errorf("commits = %d", s.commits)
	}
}

func TestOracleSearchDistancesAndFilters(t *testing.T) {
	cases := []struct {
		distance  string
		filter    interface{}
		wantWhere string
		wantArgs  []interface{}
		score     float64
	}{
		{"", nil, "", []interface{}{"[1,0]", 3}, 0.75},
		{"DOT", map[string]interface{}{"lang": "en", "year": 2024.0}, " WHERE JSON_VALUE(metadata, '$.lang') = :2 AND JSON_VALUE(metadata, '$.year') = :3",
			[]interface{}{"[1,0]", "en", "2024", 3}, -0.25},
		{"euclidean", []interface{}{map[string]interface{}{"name": "lang", "value": "en"}}, " WHERE JSON_VALUE(metadata, '$.lang') = :2",
			[]interface{}{"[1,0]", "en", 3}, -0.25},
	}
	for _, c := range cases {
		t.Run(c.distance, func(t *testing.T) {
			dsn := "ora-search-" + c.distance
			s := useOracleFake(t, dsn)
			s.cols = []string{"id", "text", "metadata", "distance"}
			s.rows = [][]driver.Value{{"a", "alpha", `{"lang":"en"}`, 0.25}}
			be := openOracle(t, map[string]interface{}{"connection_string": dsn, "distance": c.distance})
			hits, err := be.Search(context.Background(), []float64{1, 0}, 3, c.filter)
			if err != nil {
				t.Fatal(err)
			}
			metric := strings.ToUpper(c.distance)
			if metric == "" {
				metric = "COSINE"
			}
			wantQ := "SELECT id, text, metadata, VECTOR_DISTANCE(embedding, TO_VECTOR(:1), " + metric + ") AS distance FROM n8n_vectors" +
				c.wantWhere + " ORDER BY distance FETCH FIRST :" + strconv.Itoa(len(c.wantArgs)) + " ROWS ONLY"
			if s.calls[0].Query != wantQ || !reflect.DeepEqual(s.calls[0].Args, c.wantArgs) {
				t.Errorf("query:\n got %q %#v\nwant %q %#v", s.calls[0].Query, s.calls[0].Args, wantQ, c.wantArgs)
			}
			want := []Document{{ID: "a", Content: "alpha", Metadata: map[string]interface{}{"lang": "en"}, Score: c.score}}
			if !reflect.DeepEqual(hits, want) {
				t.Errorf("hits = %+v", hits)
			}
		})
	}
}

// A table LangChain's OracleVS created for n8n has a RAW(16) id; it comes
// back as Oracle's uppercase hex, not raw bytes.
func TestOracleSearchRawIDAsHex(t *testing.T) {
	s := useOracleFake(t, "ora-raw")
	s.cols = []string{"id", "text", "metadata", "distance"}
	s.rows = [][]driver.Value{
		{[]byte{0x2f, 0x1a, 0xb0, 0x00, 0x9c, 0xde, 0x41, 0x7e, 0xe0, 0x63, 0x02, 0x00, 0x11, 0xac, 0x7f, 0x01}, "alpha", nil, 0.5},
		{nil, "beta", nil, 0.75},
	}
	be := openOracle(t, map[string]interface{}{"connection_string": "ora-raw"})
	hits, err := be.Search(context.Background(), []float64{1}, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].ID != "2F1AB0009CDE417EE063020011AC7F01" || hits[1].ID != "" {
		t.Errorf("hits = %+v", hits)
	}
}

func TestOracleDeleteAndConfigErrors(t *testing.T) {
	s := useOracleFake(t, "ora-del")
	s.affected = 2
	be := openOracle(t, map[string]interface{}{"connection_string": "ora-del"})
	n, err := be.Delete(context.Background(), []string{"a", "b"}, nil, false)
	if err != nil || n != 2 {
		t.Fatalf("delete ids = %d, %v", n, err)
	}
	if _, err := be.Delete(context.Background(), nil, map[string]interface{}{"src": "x"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := be.Delete(context.Background(), nil, nil, true); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range s.calls {
		got = append(got, c.Query)
	}
	want := []string{
		"DELETE FROM n8n_vectors WHERE id IN (:1, :2)",
		"DELETE FROM n8n_vectors WHERE JSON_VALUE(metadata, '$.src') = :1",
		"CREATE TABLE IF NOT EXISTS n8n_vectors (id VARCHAR2(512) PRIMARY KEY, text CLOB, metadata CLOB CHECK (metadata IS JSON), embedding VECTOR)",
		"DELETE FROM n8n_vectors",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("queries:\n got %q\nwant %q", got, want)
	}
	if _, err := be.Search(context.Background(), []float64{1}, 1, map[string]interface{}{"a'; DROP": "x"}); err == nil {
		t.Error("unsafe filter key accepted")
	}
	for _, cfg := range []map[string]interface{}{
		{},
		{"connection_string": "x", "distance": "jaccard"},
		{"connection_string": "x", "vector_column": "bad col"},
	} {
		if _, err := newOracle(storeTarget{cfg: cfg, collection: "t"}); err == nil {
			t.Errorf("newOracle(%v) accepted", cfg)
		}
	}
}
