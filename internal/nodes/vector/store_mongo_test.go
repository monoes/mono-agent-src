package vector

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/monoes/mono-agent/internal/workflow"
)

type fakeMongo struct {
	opened    []string // connStr|db|coll
	upserts   [][]bson.M
	pipelines [][]interface{}
	deletes   []interface{}
	rows      []bson.M
	deleted   int64
	closed    int
}

func (f *fakeMongo) UpsertByID(_ context.Context, docs []bson.M) error {
	f.upserts = append(f.upserts, docs)
	return nil
}
func (f *fakeMongo) Aggregate(_ context.Context, p []interface{}) ([]bson.M, error) {
	f.pipelines = append(f.pipelines, p)
	return f.rows, nil
}
func (f *fakeMongo) DeleteMany(_ context.Context, filter interface{}) (int64, error) {
	f.deletes = append(f.deletes, filter)
	return f.deleted, nil
}

func useMongoFake(t *testing.T) *fakeMongo {
	t.Helper()
	f := &fakeMongo{}
	old := mongoOpen
	mongoOpen = func(_ context.Context, connStr, db, coll string) (mongoColl, func() error, error) {
		f.opened = append(f.opened, connStr+"|"+db+"|"+coll)
		return f, func() error { f.closed++; return nil }, nil
	}
	t.Cleanup(func() { mongoOpen = old })
	return f
}

func TestMongoAtlasUpsertShape(t *testing.T) {
	f := useMongoFake(t)
	be, err := newMongoAtlas(context.Background(), storeTarget{
		cfg: map[string]interface{}{"connection_string": "mongodb://x", "embedding_field": "vec", "text_field": "body"}, collection: "kb", namespace: "app",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = be.Upsert(context.Background(), []Document{{ID: "a", Content: "alpha", Vector: []float64{1, 2},
		Metadata: map[string]interface{}{"src": "s", "_id": "spoof", "body": "spoof"}}})
	if err != nil {
		t.Fatal(err)
	}
	be.Close()
	want := []bson.M{{"_id": "a", "body": "alpha", "vec": []float64{1, 2}, "src": "s"}}
	if !reflect.DeepEqual(f.upserts[0], want) {
		t.Fatalf("upsert docs: %#v", f.upserts[0])
	}
	if !reflect.DeepEqual(f.opened, []string{"mongodb://x|app|kb"}) || f.closed != 1 {
		t.Fatalf("opened %v closed %d", f.opened, f.closed)
	}
}

func TestMongoAtlasSearchPipeline(t *testing.T) {
	f := useMongoFake(t)
	f.rows = []bson.M{{"_id": "a", "text": "alpha", "embedding": []float64{1}, "score": 0.9, "lang": "en"}}
	be, err := newMongoAtlas(context.Background(), storeTarget{cfg: map[string]interface{}{
		"connection_string": "mongodb://x", "database": "app", "vector_index": "vidx",
		"post_filter_pipeline": `[{"$match":{"score":{"$gt":0.5}}}]`,
	}, collection: "kb"})
	if err != nil {
		t.Fatal(err)
	}
	filter := map[string]interface{}{"lang": "en"}
	hits, err := be.Search(context.Background(), []float64{0.5, 0.5}, 3, filter)
	if err != nil {
		t.Fatal(err)
	}
	want := []interface{}{
		bson.D{{Key: "$vectorSearch", Value: bson.D{
			{Key: "index", Value: "vidx"}, {Key: "path", Value: "embedding"},
			{Key: "queryVector", Value: []float64{0.5, 0.5}}, {Key: "numCandidates", Value: 30},
			{Key: "limit", Value: 3}, {Key: "filter", Value: filter},
		}}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "score", Value: bson.D{{Key: "$meta", Value: "vectorSearchScore"}}}}}},
		map[string]interface{}{"$match": map[string]interface{}{"score": map[string]interface{}{"$gt": 0.5}}},
	}
	if !reflect.DeepEqual(f.pipelines[0], want) {
		t.Fatalf("pipeline:\n got %#v\nwant %#v", f.pipelines[0], want)
	}
	wantHits := []Document{{ID: "a", Content: "alpha", Score: 0.9, Metadata: map[string]interface{}{"lang": "en"}}}
	if !reflect.DeepEqual(hits, wantHits) {
		t.Fatalf("hits: %#v", hits)
	}
}

func TestMongoAtlasSearchNeedsIndex(t *testing.T) {
	useMongoFake(t)
	be, _ := newMongoAtlas(context.Background(), storeTarget{cfg: map[string]interface{}{"connection_string": "m", "database": "d"}, collection: "c"})
	if _, err := be.Search(context.Background(), []float64{1}, 1, nil); err == nil || !strings.Contains(err.Error(), "vector_index") {
		t.Fatalf("err = %v", err)
	}
}

func TestMongoAtlasDelete(t *testing.T) {
	f := useMongoFake(t)
	f.deleted = 2
	be, _ := newMongoAtlas(context.Background(), storeTarget{cfg: map[string]interface{}{"connection_string": "m", "database": "d"}, collection: "c"})
	ctx := context.Background()
	if n, err := be.Delete(ctx, []string{"a", "b"}, nil, false); err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	be.Delete(ctx, nil, map[string]interface{}{"lang": "en"}, false)
	be.Delete(ctx, nil, nil, true)
	want := []interface{}{
		bson.M{"_id": bson.M{"$in": []string{"a", "b"}}},
		bson.M{"lang": "en"},
		bson.M{},
	}
	if !reflect.DeepEqual(f.deletes, want) {
		t.Fatalf("deletes: %#v", f.deletes)
	}
}

func TestMongoAtlasConfigErrors(t *testing.T) {
	useMongoFake(t)
	for _, c := range []struct {
		cfg  map[string]interface{}
		want string
	}{
		{map[string]interface{}{}, "connection_string"},
		{map[string]interface{}{"connection_string": "m"}, "database"},
		{map[string]interface{}{"connection_string": "m", "database": "d", "post_filter_pipeline": `{"a":1}`}, "post_filter_pipeline"},
	} {
		if _, err := newMongoAtlas(context.Background(), storeTarget{cfg: c.cfg, collection: "c"}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("cfg %v: err = %v", c.cfg, err)
		}
	}
}

func TestMongoAtlasStoreNodeEndToEnd(t *testing.T) {
	f := useMongoFake(t)
	node := &StoreNode{}
	base := map[string]interface{}{"provider": "mongodb_atlas", "collection": "kb", "database": "app", "connection_string": "m", "vector_index": "v"}
	cfg := copyMap(base)
	cfg["mode"] = "insert"
	if _, err := node.Execute(context.Background(), workflow.NodeInput{Items: []workflow.Item{
		workflow.NewItem(map[string]interface{}{"id": "d1", "pageContent": "hello", "embedding": []interface{}{0.1, 0.2}}),
	}}, cfg); err != nil {
		t.Fatal(err)
	}
	if got := f.upserts[0][0]; got["_id"] != "d1" || got["text"] != "hello" {
		t.Fatalf("upsert: %#v", got)
	}
	f.rows = []bson.M{{"_id": "d1", "text": "hello", "score": 0.8}}
	cfg = copyMap(base)
	cfg["mode"] = "search"
	cfg["query_vector"] = []interface{}{0.1, 0.2}
	out, err := node.Execute(context.Background(), workflow.NodeInput{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if it := out[0].Items; len(it) != 1 || it[0].JSON["id"] != "d1" || it[0].JSON["score"] != 0.8 {
		t.Fatalf("items: %#v", it)
	}
}
