package vector

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

type fakeRedis struct {
	calls   [][]interface{}
	replies map[string]interface{} // by command name
	errs    map[string]error
	closed  int
}

func (f *fakeRedis) Do(_ context.Context, args ...interface{}) (interface{}, error) {
	f.calls = append(f.calls, args)
	cmd := args[0].(string)
	return f.replies[cmd], f.errs[cmd]
}
func (f *fakeRedis) Close() error { f.closed++; return nil }

func useRedisFake(t *testing.T) *fakeRedis {
	t.Helper()
	f := &fakeRedis{replies: map[string]interface{}{}, errs: map[string]error{}}
	old := redisOpen
	redisOpen = func(string) (redisDoer, error) { return f, nil }
	t.Cleanup(func() { redisOpen = old })
	return f
}

func openRedis(t *testing.T, cfg map[string]interface{}) backend {
	t.Helper()
	if cfg["connection_string"] == nil {
		cfg["connection_string"] = "redis://localhost:6379"
	}
	be, err := newRedisStore(storeTarget{cfg: cfg, collection: "idx"})
	if err != nil {
		t.Fatal(err)
	}
	return be
}

func TestFloat32Bytes(t *testing.T) {
	// 1.0 = 0x3f800000, -2.0 = 0xc0000000, little-endian.
	if got := float32Bytes([]float64{1, -2}); got != "\x00\x00\x80\x3f\x00\x00\x00\xc0" {
		t.Fatalf("got %q", got)
	}
}

func TestRedisUpsertCreatesIndex(t *testing.T) {
	f := useRedisFake(t)
	f.errs["FT.INFO"] = errors.New("Unknown index name")
	be := openRedis(t, map[string]interface{}{})
	docs := []Document{{ID: "a", Content: "alpha", Metadata: map[string]interface{}{"k": "v"}, Vector: []float64{1, -2}}}
	if err := be.Upsert(context.Background(), docs); err != nil {
		t.Fatal(err)
	}
	if err := be.Upsert(context.Background(), docs); err != nil {
		t.Fatal(err)
	}
	hset := []interface{}{"HSET", "doc:idx:a", "content", "alpha", "metadata", `{"k":"v"}`, "content_vector", "\x00\x00\x80\x3f\x00\x00\x00\xc0"}
	want := [][]interface{}{
		{"FT.INFO", "idx"},
		{"FT.CREATE", "idx", "ON", "HASH", "PREFIX", 1, "doc:idx:", "SCHEMA", "content", "TEXT", "metadata", "TEXT",
			"content_vector", "VECTOR", "HNSW", 6, "TYPE", "FLOAT32", "DIM", 2, "DISTANCE_METRIC", "COSINE"},
		hset, hset,
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls:\n got %#v\nwant %#v", f.calls, want)
	}
}

func TestRedisUpsertInfoErrorFails(t *testing.T) {
	f := useRedisFake(t)
	f.errs["FT.INFO"] = errors.New("NOAUTH Authentication required")
	be := openRedis(t, map[string]interface{}{})
	if err := be.Upsert(context.Background(), []Document{{ID: "a", Vector: []float64{1}}}); err == nil || !strings.Contains(err.Error(), "NOAUTH") {
		t.Fatalf("err = %v", err)
	}
}

func TestRedisSearch(t *testing.T) {
	f := useRedisFake(t)
	f.replies["FT.SEARCH"] = []interface{}{int64(2),
		"pfx:a", []interface{}{"body", "alpha", "meta", `{"lang":"en"}`, "vector_score", "0.25"},
		"pfx:b", []interface{}{"body", "beta", "vector_score", "0.5"},
	}
	be := openRedis(t, map[string]interface{}{"key_prefix": "pfx:", "content_key": "body", "metadata_key": "meta", "vector_key": "vec"})
	hits, err := be.Search(context.Background(), []float64{1}, 2, "@lang:{en}")
	if err != nil {
		t.Fatal(err)
	}
	want := []interface{}{"FT.SEARCH", "idx", "(@lang:{en})=>[KNN 2 @vec $vec AS vector_score]", "PARAMS", 2, "vec", "\x00\x00\x80\x3f",
		"SORTBY", "vector_score", "ASC", "RETURN", 3, "body", "meta", "vector_score", "LIMIT", 0, 2, "DIALECT", 2}
	if !reflect.DeepEqual(f.calls[0], want) {
		t.Fatalf("args:\n got %#v\nwant %#v", f.calls[0], want)
	}
	wantHits := []Document{
		{ID: "a", Content: "alpha", Metadata: map[string]interface{}{"lang": "en"}, Score: 0.75},
		{ID: "b", Content: "beta", Metadata: map[string]interface{}{}, Score: 0.5},
	}
	if !reflect.DeepEqual(hits, wantHits) {
		t.Fatalf("hits: %#v", hits)
	}
	if _, err := be.Search(context.Background(), []float64{1}, 2, map[string]interface{}{"a": 1}); err == nil {
		t.Fatal("object filter accepted")
	}
}

func TestRedisSearchL2Score(t *testing.T) {
	f := useRedisFake(t)
	f.replies["FT.SEARCH"] = []interface{}{int64(1), "doc:idx:a", []interface{}{"vector_score", "0.5"}}
	be := openRedis(t, map[string]interface{}{"distance_metric": "l2"})
	hits, err := be.Search(context.Background(), []float64{1}, 1, nil)
	if err != nil || len(hits) != 1 || hits[0].Score != -0.5 {
		t.Fatalf("hits %#v err %v", hits, err)
	}
	if q := f.calls[0][2]; q != "(*)=>[KNN 1 @content_vector $vec AS vector_score]" {
		t.Fatalf("query %v", q)
	}
}

func TestRedisDelete(t *testing.T) {
	f := useRedisFake(t)
	f.replies["DEL"] = int64(2)
	f.replies["FT.SEARCH"] = []interface{}{int64(1), "doc:idx:x"}
	be := openRedis(t, map[string]interface{}{})
	ctx := context.Background()
	if n, err := be.Delete(ctx, []string{"a", "b"}, nil, false); err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err := be.Delete(ctx, nil, "@lang:{en}", false); err != nil {
		t.Fatal(err)
	}
	if n, err := be.Delete(ctx, nil, nil, true); err != nil || n != -1 {
		t.Fatalf("all: n=%d err=%v", n, err)
	}
	want := [][]interface{}{
		{"DEL", "doc:idx:a", "doc:idx:b"},
		{"FT.SEARCH", "idx", "@lang:{en}", "NOCONTENT", "LIMIT", 0, 10000},
		{"DEL", "doc:idx:x"},
		{"FT.DROPINDEX", "idx", "DD"},
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls:\n got %#v\nwant %#v", f.calls, want)
	}
}

func TestRedisConfigErrors(t *testing.T) {
	useRedisFake(t)
	if _, err := newRedisStore(storeTarget{cfg: map[string]interface{}{}, collection: "i"}); err == nil || !strings.Contains(err.Error(), "connection_string") {
		t.Errorf("err = %v", err)
	}
	if _, err := newRedisStore(storeTarget{cfg: map[string]interface{}{"connection_string": "r", "distance_metric": "dot"}, collection: "i"}); err == nil || !strings.Contains(err.Error(), "distance_metric") {
		t.Errorf("err = %v", err)
	}
}

func TestRedisStoreNodeEndToEnd(t *testing.T) {
	f := useRedisFake(t)
	node := &StoreNode{}
	base := map[string]interface{}{"provider": "redis", "collection": "kb", "connection_string": "redis://x"}
	cfg := copyMap(base)
	cfg["mode"] = "insert"
	if _, err := node.Execute(context.Background(), workflow.NodeInput{Items: []workflow.Item{
		workflow.NewItem(map[string]interface{}{"id": "d1", "pageContent": "hello", "embedding": []interface{}{1.0}}),
	}}, cfg); err != nil {
		t.Fatal(err)
	}
	if last := f.calls[len(f.calls)-1]; last[0] != "HSET" || last[1] != "doc:kb:d1" {
		t.Fatalf("hset: %#v", last)
	}
	f.replies["FT.SEARCH"] = []interface{}{int64(1), "doc:kb:d1", []interface{}{"content", "hello", "vector_score", "0"}}
	cfg = copyMap(base)
	cfg["mode"] = "search"
	cfg["query_vector"] = []interface{}{1.0}
	out, err := node.Execute(context.Background(), workflow.NodeInput{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if it := out[0].Items; len(it) != 1 || it[0].JSON["id"] != "d1" || it[0].JSON["score"] != 1.0 {
		t.Fatalf("items: %#v", it)
	}
	if f.closed != 2 {
		t.Errorf("closed = %d, want 2", f.closed)
	}
}
