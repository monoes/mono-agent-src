package vector

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/workflow"
)

// Vector store providers accepted by vector.store.
const (
	StoreQdrant       = "qdrant"
	StorePinecone     = "pinecone"
	StorePGVector     = "pgvector"
	StoreSupabase     = "supabase"
	StoreWeaviate     = "weaviate"
	StoreMilvus       = "milvus"
	StoreMongoDBAtlas = "mongodb_atlas"
	StoreRedis        = "redis"
	StoreChroma       = "chroma"
	StoreMemory       = "memory"
)

// StoreProviders lists every provider in display order.
var StoreProviders = []string{
	StoreQdrant, StorePinecone, StorePGVector, StoreSupabase, StoreWeaviate,
	StoreMilvus, StoreMongoDBAtlas, StoreRedis, StoreChroma, StoreMemory,
}

// Document is one stored chunk: text, metadata, its vector and — on search
// results — the provider's similarity score.
type Document struct {
	ID       string
	Content  string
	Metadata map[string]interface{}
	Vector   []float64
	Score    float64
}

// backend is one vector database. Upsert writes documents that all carry an
// ID and a vector. Delete removes by ID list and/or a provider-native
// filter; all=true clears the whole collection. Delete returns the number
// removed, or -1 when the provider does not report it.
type backend interface {
	Upsert(ctx context.Context, docs []Document) error
	Search(ctx context.Context, vector []float64, topK int, filter interface{}) ([]Document, error)
	Delete(ctx context.Context, ids []string, filter interface{}, all bool) (int, error)
	Close() error
}

// storeTarget is what every backend constructor receives.
type storeTarget struct {
	cfg        map[string]interface{} // full node config (credentials merged in)
	collection string                 // collection / index / table / class / memory key
	namespace  string                 // Pinecone namespace, Chroma database, Mongo database …
}

func openBackend(ctx context.Context, provider string, t storeTarget) (backend, error) {
	switch provider {
	case StoreQdrant:
		return newQdrant(t)
	case StorePinecone:
		return newPinecone(ctx, t)
	case StoreWeaviate:
		return newWeaviate(t)
	case StoreMilvus:
		return newMilvus(t)
	case StoreChroma:
		return newChroma(t)
	case StoreSupabase:
		return newSupabase(t)
	case StorePGVector:
		return newPGVector(ctx, t)
	case StoreMongoDBAtlas:
		return newMongoAtlas(ctx, t)
	case StoreRedis:
		return newRedisStore(t)
	case StoreMemory:
		return newMemory(t), nil
	}
	return nil, fmt.Errorf("provider %q is not supported (want one of %s)", provider, strings.Join(StoreProviders, ", "))
}

// StoreNode implements vector.store.
type StoreNode struct{}

func (n *StoreNode) Type() string { return "vector.store" }

// PerItemConfigFields keeps "query" unresolved by the engine so a template
// such as {{ $json.chatInput }} is evaluated once per input item.
func (n *StoreNode) PerItemConfigFields() []string { return []string{"query"} }

// normalizeMode maps n8n's mode names onto ours.
func normalizeMode(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case "", "search", "load", "retrieve", "retrieve-as-tool", "retrieve_as_tool":
		return "search"
	case "insert", "upsert":
		return "insert"
	}
	return strings.ToLower(strings.TrimSpace(m))
}

func (n *StoreNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	sc, err := parseStoreConfig(config)
	if err != nil {
		return nil, fmt.Errorf("vector.store: %w", err)
	}
	if sc.embedCfg != nil {
		if err := resolveNestedCredential(ctx, sc.embedCfg); err != nil {
			return nil, fmt.Errorf("vector.store: %w", err)
		}
		ec, err := parseEmbedConfig(sc.embedCfg, "embedding.")
		if err != nil {
			return nil, fmt.Errorf("vector.store: %w", err)
		}
		sc.embed = &ec
	}
	be, err := openBackend(ctx, sc.provider, storeTarget{cfg: config, collection: sc.collection, namespace: sc.namespace})
	if err != nil {
		return nil, fmt.Errorf("vector.store (%s): %w", sc.provider, err)
	}
	defer be.Close()

	var items []workflow.Item
	switch sc.mode {
	case "insert", "update":
		items, err = sc.runUpsert(ctx, be, input.Items)
	case "search":
		items, err = sc.runSearch(ctx, be, input)
	case "delete":
		items, err = sc.runDelete(ctx, be, input.Items)
	default:
		err = fmt.Errorf("mode %q is not supported (want insert, search, update or delete)", sc.mode)
	}
	if err != nil {
		return nil, fmt.Errorf("vector.store (%s %s): %w", sc.provider, sc.mode, err)
	}
	return []workflow.NodeOutput{{Handle: "main", Items: items}}, nil
}

type storeConfig struct {
	provider, mode        string
	collection, namespace string
	topK, batch           int
	filter                interface{}
	ids                   []string
	clear, deleteAll      bool
	query                 string
	queryVector           []float64
	contentFields         []string
	metaFields            []string
	idField               string
	vectorField           string
	queryField            string
	includeVectors        bool
	embedCfg              map[string]interface{}
	embed                 *embedConfig
}

func parseStoreConfig(cfg map[string]interface{}) (*storeConfig, error) {
	sc := &storeConfig{
		provider:       strings.ToLower(str(cfg, "provider")),
		mode:           normalizeMode(str(cfg, "mode")),
		collection:     strOr(cfg, "", "collection", "index", "table", "memory_key"),
		namespace:      strOr(cfg, "", "namespace", "database"),
		filter:         jsonValue(cfg, "filter"),
		ids:            stringList(cfg, "ids"),
		clear:          boolField(cfg, "clear", false),
		deleteAll:      boolField(cfg, "delete_all", false),
		query:          str(cfg, "query"),
		contentFields:  stringList(cfg, "content_field"),
		metaFields:     stringList(cfg, "metadata_fields"),
		idField:        strOr(cfg, "id", "id_field"),
		vectorField:    strOr(cfg, "embedding", "vector_field"),
		queryField:     str(cfg, "query_field"),
		includeVectors: boolField(cfg, "include_vectors", false),
	}
	if sc.provider == "" {
		return nil, fmt.Errorf("provider is required (one of %s)", strings.Join(StoreProviders, ", "))
	}
	if sc.provider == StoreMemory && sc.collection == "" {
		sc.collection = "vector_store_key"
	}
	if sc.collection == "" {
		return nil, fmt.Errorf("collection is required for provider %s (collection / index / table name)", sc.provider)
	}
	var err error
	if sc.topK, err = intField(cfg, "top_k", 4); err != nil {
		return nil, err
	}
	if sc.topK == 0 {
		sc.topK = 4
	}
	if sc.batch, err = intField(cfg, "upsert_batch_size", 100); err != nil {
		return nil, err
	}
	if sc.batch == 0 {
		sc.batch = 100
	}
	if v, ok := toFloats(jsonValue(cfg, "query_vector")); ok {
		sc.queryVector = v
	}
	if raw := jsonValue(cfg, "embedding"); raw != nil && asMap(raw) == nil {
		return nil, fmt.Errorf("embedding must be a JSON object like {\"provider\":\"ollama\"}, got %T", raw)
	}
	if e := asMap(jsonValue(cfg, "embedding")); len(e) > 0 {
		sc.embedCfg = copyMap(e)
	}
	return sc, nil
}

// itemDocument builds a Document from an insert/update input item.
func (sc *storeConfig) itemDocument(j map[string]interface{}) (Document, error) {
	text, err := itemText(j, sc.contentFields)
	if err != nil {
		return Document{}, err
	}
	d := Document{Content: text, Metadata: documentMetadata(j, sc.metaFields)}
	if v, ok := lookupPath(j, sc.idField); ok && v != nil {
		d.ID = strings.TrimSpace(valueText(v))
	}
	if v, ok := lookupPath(j, sc.vectorField); ok {
		if vec, ok := toFloats(v); ok {
			d.Vector = vec
		}
	}
	return d, nil
}

func (sc *storeConfig) runUpsert(ctx context.Context, be backend, items []workflow.Item) ([]workflow.Item, error) {
	docs := make([]Document, len(items))
	var missing []int
	for i, item := range items {
		d, err := sc.itemDocument(item.JSON)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		if d.ID == "" {
			if sc.mode == "update" {
				return nil, fmt.Errorf("item %d: update needs a document id in field %q", i, sc.idField)
			}
			d.ID = uuid.NewString()
		}
		if d.Vector == nil {
			missing = append(missing, i)
		}
		docs[i] = d
	}
	if len(missing) > 0 {
		if sc.embed == nil {
			return nil, fmt.Errorf("item %d has no vector in field %q and no embedding provider is configured (set embedding.provider, or run vector.embed first)", missing[0], sc.vectorField)
		}
		texts := make([]string, len(missing))
		for k, i := range missing {
			texts[k] = docs[i].Content
		}
		vecs, err := sc.embed.embed(ctx, texts, purposeDocument)
		if err != nil {
			return nil, err
		}
		for k, i := range missing {
			docs[i].Vector = vecs[k]
		}
	}
	if sc.clear && sc.mode == "insert" {
		if _, err := be.Delete(ctx, nil, nil, true); err != nil {
			return nil, fmt.Errorf("clear before insert: %w", err)
		}
	}
	for start := 0; start < len(docs); start += sc.batch {
		end := start + sc.batch
		if end > len(docs) {
			end = len(docs)
		}
		if err := be.Upsert(ctx, docs[start:end]); err != nil {
			return nil, err
		}
	}
	out := make([]workflow.Item, len(docs))
	for i, d := range docs {
		out[i] = workflow.NewItem(map[string]interface{}{"id": d.ID, "pageContent": d.Content, "metadata": d.Metadata})
	}
	return out, nil
}

func (sc *storeConfig) withQuery(q string) *storeConfig {
	c := *sc
	c.query = strings.TrimSpace(q)
	return &c
}

// defaultQueryFields are tried when neither query nor query_field is set;
// chatInput is what a chat trigger emits.
var defaultQueryFields = []string{"query", "chatInput", "pageContent", "text"}

func (sc *storeConfig) queryFor(j map[string]interface{}) (string, []float64, error) {
	if sc.queryVector != nil {
		return "", sc.queryVector, nil
	}
	if sc.query != "" {
		return sc.query, nil, nil
	}
	if v, ok := lookupPath(j, sc.vectorField); ok {
		if vec, ok := toFloats(v); ok {
			return "", vec, nil
		}
	}
	fields := defaultQueryFields
	if sc.queryField != "" {
		fields = []string{sc.queryField}
	}
	for _, f := range fields {
		if v, ok := lookupPath(j, f); ok {
			if s := strings.TrimSpace(valueText(v)); s != "" {
				return s, nil, nil
			}
		}
	}
	return "", nil, fmt.Errorf("no query: set query, query_vector or query_field (looked for %s)", strings.Join(fields, ", "))
}

func (sc *storeConfig) runSearch(ctx context.Context, be backend, input workflow.NodeInput) ([]workflow.Item, error) {
	items := input.Items
	if len(items) == 0 {
		items = []workflow.Item{workflow.NewItem(map[string]interface{}{})}
	}
	var out []workflow.Item
	for i, item := range items {
		q, err := perItemString(sc.query, item, input)
		if err != nil {
			return nil, fmt.Errorf("item %d: query: %w", i, err)
		}
		text, vec, err := sc.withQuery(q).queryFor(item.JSON)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		if vec == nil {
			if sc.embed == nil {
				return nil, fmt.Errorf("item %d: a text query needs an embedding provider (set embedding.provider) or pass a vector in %q", i, sc.vectorField)
			}
			vecs, err := sc.embed.embed(ctx, []string{text}, purposeQuery)
			if err != nil {
				return nil, err
			}
			vec = vecs[0]
		}
		hits, err := be.Search(ctx, vec, sc.topK, sc.filter)
		if err != nil {
			return nil, err
		}
		for rank, h := range hits {
			j := map[string]interface{}{
				"id": h.ID, "pageContent": h.Content, "metadata": nonNilMap(h.Metadata),
				"score": h.Score, "rank": rank + 1, "query": text, "source_item": i,
			}
			if sc.includeVectors && h.Vector != nil {
				j["embedding"] = h.Vector
			}
			out = append(out, workflow.NewItem(j))
		}
	}
	return out, nil
}

func (sc *storeConfig) runDelete(ctx context.Context, be backend, items []workflow.Item) ([]workflow.Item, error) {
	ids := sc.ids
	if len(ids) == 0 && sc.filter == nil && !sc.deleteAll {
		for _, item := range items {
			if v, ok := lookupPath(item.JSON, sc.idField); ok && v != nil {
				if s := strings.TrimSpace(valueText(v)); s != "" {
					ids = append(ids, s)
				}
			}
		}
	}
	if len(ids) == 0 && sc.filter == nil && !sc.deleteAll {
		return nil, fmt.Errorf("nothing to delete: set ids, filter or delete_all (or pass items with an %q field)", sc.idField)
	}
	n, err := be.Delete(ctx, ids, sc.filter, sc.deleteAll && len(ids) == 0 && sc.filter == nil)
	if err != nil {
		return nil, err
	}
	res := map[string]interface{}{"ids": ids, "collection": sc.collection}
	if n >= 0 {
		res["deleted"] = n
	}
	return []workflow.Item{workflow.NewItem(res)}, nil
}

func nonNilMap(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return map[string]interface{}{}
	}
	return m
}
