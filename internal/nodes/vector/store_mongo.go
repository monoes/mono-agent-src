package vector

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// mongoColl is the slice of a MongoDB collection the Atlas backend uses;
// tests replace it with a recording fake.
type mongoColl interface {
	UpsertByID(ctx context.Context, docs []bson.M) error
	Aggregate(ctx context.Context, pipeline []interface{}) ([]bson.M, error)
	DeleteMany(ctx context.Context, filter interface{}) (int64, error)
}

// mongoOpen connects to a collection; it returns the collection and a
// close function. Swapped by tests.
var mongoOpen = func(ctx context.Context, connStr, db, coll string) (mongoColl, func() error, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(connStr))
	if err != nil {
		return nil, nil, fmt.Errorf("connect: %w", err)
	}
	c := client.Database(db).Collection(coll)
	return &realMongoColl{c: c}, func() error { return client.Disconnect(context.Background()) }, nil
}

type realMongoColl struct{ c *mongo.Collection }

func (r *realMongoColl) UpsertByID(ctx context.Context, docs []bson.M) error {
	models := make([]mongo.WriteModel, len(docs))
	for i, d := range docs {
		models[i] = mongo.NewReplaceOneModel().SetFilter(bson.M{"_id": d["_id"]}).SetReplacement(d).SetUpsert(true)
	}
	_, err := r.c.BulkWrite(ctx, models)
	return err
}

func (r *realMongoColl) Aggregate(ctx context.Context, pipeline []interface{}) ([]bson.M, error) {
	cur, err := r.c.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	var out []bson.M
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *realMongoColl) DeleteMany(ctx context.Context, filter interface{}) (int64, error) {
	res, err := r.c.DeleteMany(ctx, filter)
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

// mongoAtlasBackend stores documents LangChain-style: the text and the
// vector in their own fields, metadata flattened at the top level.
type mongoAtlasBackend struct {
	coll       mongoColl
	close      func() error
	index      string
	embField   string
	textField  string
	postFilter []interface{}
}

func newMongoAtlas(ctx context.Context, t storeTarget) (backend, error) {
	connStr := str(t.cfg, "connection_string")
	if connStr == "" {
		return nil, fmt.Errorf("connection_string is required (use a mongodb connection)")
	}
	db := t.namespace
	if db == "" {
		db = str(t.cfg, "database")
	}
	if db == "" {
		return nil, fmt.Errorf("database is required for provider mongodb_atlas")
	}
	b := &mongoAtlasBackend{
		index:     str(t.cfg, "vector_index"),
		embField:  strOr(t.cfg, "embedding", "embedding_field"),
		textField: strOr(t.cfg, "text", "text_field"),
	}
	if raw := jsonValue(t.cfg, "post_filter_pipeline"); raw != nil {
		stages, ok := raw.([]interface{})
		if !ok {
			return nil, fmt.Errorf("post_filter_pipeline must be a JSON array of aggregation stages, got %T", raw)
		}
		b.postFilter = stages
	}
	coll, closeFn, err := mongoOpen(ctx, connStr, db, t.collection)
	if err != nil {
		return nil, err
	}
	b.coll, b.close = coll, closeFn
	return b, nil
}

func (b *mongoAtlasBackend) Close() error {
	if b.close == nil {
		return nil
	}
	return b.close()
}

func (b *mongoAtlasBackend) Upsert(ctx context.Context, docs []Document) error {
	out := make([]bson.M, len(docs))
	for i, d := range docs {
		m := bson.M{}
		for k, v := range d.Metadata {
			if k == "_id" || k == b.textField || k == b.embField {
				continue
			}
			m[k] = v
		}
		m["_id"] = d.ID
		m[b.textField] = d.Content
		m[b.embField] = d.Vector
		out[i] = m
	}
	if err := b.coll.UpsertByID(ctx, out); err != nil {
		return fmt.Errorf("upsert: %w", err)
	}
	return nil
}

// searchPipeline builds the $vectorSearch aggregation.
func (b *mongoAtlasBackend) searchPipeline(vector []float64, topK int, filter interface{}) ([]interface{}, error) {
	if b.index == "" {
		return nil, fmt.Errorf("vector_index is required for search (the Atlas Vector Search index name)")
	}
	vs := bson.D{
		{Key: "index", Value: b.index},
		{Key: "path", Value: b.embField},
		{Key: "queryVector", Value: vector},
		{Key: "numCandidates", Value: topK * 10},
		{Key: "limit", Value: topK},
	}
	if filter != nil {
		f, ok := filter.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("mongodb_atlas filter must be a JSON object (an MQL pre-filter), got %T", filter)
		}
		vs = append(vs, bson.E{Key: "filter", Value: f})
	}
	pipeline := []interface{}{
		bson.D{{Key: "$vectorSearch", Value: vs}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "score", Value: bson.D{{Key: "$meta", Value: "vectorSearchScore"}}}}}},
	}
	return append(pipeline, b.postFilter...), nil
}

func (b *mongoAtlasBackend) Search(ctx context.Context, vector []float64, topK int, filter interface{}) ([]Document, error) {
	pipeline, err := b.searchPipeline(vector, topK, filter)
	if err != nil {
		return nil, err
	}
	rows, err := b.coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("$vectorSearch: %w", err)
	}
	out := make([]Document, 0, len(rows))
	for _, r := range rows {
		d := Document{ID: mongoIDString(r["_id"]), Metadata: map[string]interface{}{}}
		d.Content, _ = r[b.textField].(string)
		switch s := r["score"].(type) {
		case float64:
			d.Score = s
		case float32:
			d.Score = float64(s)
		}
		for k, v := range r {
			if k == "_id" || k == b.textField || k == b.embField || k == "score" {
				continue
			}
			d.Metadata[k] = v
		}
		out = append(out, d)
	}
	return out, nil
}

func mongoIDString(v interface{}) string {
	switch id := v.(type) {
	case string:
		return id
	case bson.ObjectID:
		return id.Hex()
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

func (b *mongoAtlasBackend) Delete(ctx context.Context, ids []string, filter interface{}, all bool) (int, error) {
	var f interface{}
	switch {
	case len(ids) > 0:
		f = bson.M{"_id": bson.M{"$in": ids}}
	case filter != nil:
		m, ok := filter.(map[string]interface{})
		if !ok {
			return 0, fmt.Errorf("mongodb_atlas filter must be a JSON object, got %T", filter)
		}
		f = bson.M(m)
	case all:
		f = bson.M{}
	default:
		return 0, nil
	}
	n, err := b.coll.DeleteMany(ctx, f)
	if err != nil {
		return 0, fmt.Errorf("delete: %w", err)
	}
	return int(n), nil
}
