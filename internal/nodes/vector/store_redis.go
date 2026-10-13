package vector

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
)

// redisDoer is the slice of a Redis client the RediSearch backend uses;
// tests replace it with a recording fake.
type redisDoer interface {
	Do(ctx context.Context, args ...interface{}) (interface{}, error)
	Close() error
}

// redisOpen connects with RESP2 so FT.SEARCH replies are flat arrays.
var redisOpen = func(connStr string) (redisDoer, error) {
	opts, err := redis.ParseURL(connStr)
	if err != nil {
		return nil, fmt.Errorf("invalid connection_string: %w", err)
	}
	opts.Protocol = 2
	return &realRedis{c: redis.NewClient(opts)}, nil
}

type realRedis struct{ c *redis.Client }

func (r *realRedis) Do(ctx context.Context, args ...interface{}) (interface{}, error) {
	return r.c.Do(ctx, args...).Result()
}
func (r *realRedis) Close() error { return r.c.Close() }

// redisBackend stores each document as a hash under key_prefix+id and
// searches it through a RediSearch HNSW vector index.
type redisBackend struct {
	r                           redisDoer
	index, prefix               string
	contentKey, metaKey, vecKey string
	metric                      string
	indexChecked                bool
}

func newRedisStore(t storeTarget) (backend, error) {
	connStr := str(t.cfg, "connection_string")
	if connStr == "" {
		return nil, fmt.Errorf("connection_string is required (use a redis connection)")
	}
	b := &redisBackend{
		index:      t.collection,
		prefix:     strOr(t.cfg, "doc:"+t.collection+":", "key_prefix"),
		contentKey: strOr(t.cfg, "content", "content_key"),
		metaKey:    strOr(t.cfg, "metadata", "metadata_key"),
		vecKey:     strOr(t.cfg, "content_vector", "vector_key"),
		metric:     strings.ToUpper(strOr(t.cfg, "COSINE", "distance_metric")),
	}
	switch b.metric {
	case "COSINE", "L2", "IP":
	default:
		return nil, fmt.Errorf("distance_metric %q is not supported (want COSINE, L2 or IP)", b.metric)
	}
	r, err := redisOpen(connStr)
	if err != nil {
		return nil, err
	}
	b.r = r
	return b, nil
}

func (b *redisBackend) Close() error { return b.r.Close() }

// float32Bytes encodes a vector as little-endian float32, RediSearch's
// FLOAT32 blob format.
func float32Bytes(v []float64) string {
	buf := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(float32(f)))
	}
	return string(buf)
}

func isUnknownIndex(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unknown index") || strings.Contains(msg, "no such index")
}

func (b *redisBackend) ensureIndex(ctx context.Context, dim int) error {
	if b.indexChecked {
		return nil
	}
	if _, err := b.r.Do(ctx, "FT.INFO", b.index); err != nil {
		if !isUnknownIndex(err) {
			return fmt.Errorf("FT.INFO %s: %w", b.index, err)
		}
		if _, err := b.r.Do(ctx, "FT.CREATE", b.index, "ON", "HASH", "PREFIX", 1, b.prefix,
			"SCHEMA", b.contentKey, "TEXT", b.metaKey, "TEXT",
			b.vecKey, "VECTOR", "HNSW", 6, "TYPE", "FLOAT32", "DIM", dim, "DISTANCE_METRIC", b.metric); err != nil {
			return fmt.Errorf("FT.CREATE %s: %w", b.index, err)
		}
	}
	b.indexChecked = true
	return nil
}

func (b *redisBackend) Upsert(ctx context.Context, docs []Document) error {
	if len(docs) == 0 {
		return nil
	}
	if err := b.ensureIndex(ctx, len(docs[0].Vector)); err != nil {
		return err
	}
	for _, d := range docs {
		meta, err := json.Marshal(nonNilMap(d.Metadata))
		if err != nil {
			return fmt.Errorf("document %q metadata: %w", d.ID, err)
		}
		if _, err := b.r.Do(ctx, "HSET", b.prefix+d.ID, b.contentKey, d.Content, b.metaKey, string(meta), b.vecKey, float32Bytes(d.Vector)); err != nil {
			return fmt.Errorf("HSET %s: %w", b.prefix+d.ID, err)
		}
	}
	return nil
}

func redisFilter(filter interface{}) (string, error) {
	if filter == nil {
		return "*", nil
	}
	s, ok := filter.(string)
	if !ok {
		return "", fmt.Errorf("redis filter must be a RediSearch query string such as @lang:{en}, got %T", filter)
	}
	return s, nil
}

func (b *redisBackend) Search(ctx context.Context, vector []float64, topK int, filter interface{}) ([]Document, error) {
	f, err := redisFilter(filter)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf("(%s)=>[KNN %d @%s $vec AS vector_score]", f, topK, b.vecKey)
	reply, err := b.r.Do(ctx, "FT.SEARCH", b.index, query, "PARAMS", 2, "vec", float32Bytes(vector),
		"SORTBY", "vector_score", "ASC", "RETURN", 3, b.contentKey, b.metaKey, "vector_score",
		"LIMIT", 0, topK, "DIALECT", 2)
	if err != nil {
		return nil, fmt.Errorf("FT.SEARCH %s: %w", b.index, err)
	}
	arr, ok := reply.([]interface{})
	if !ok || len(arr) == 0 {
		return nil, fmt.Errorf("FT.SEARCH %s: unexpected reply %T", b.index, reply)
	}
	var out []Document
	for i := 1; i+1 < len(arr); i += 2 {
		key := fmt.Sprint(arr[i])
		fields, _ := arr[i+1].([]interface{})
		d := Document{ID: strings.TrimPrefix(key, b.prefix), Metadata: map[string]interface{}{}}
		for j := 0; j+1 < len(fields); j += 2 {
			name, val := fmt.Sprint(fields[j]), fmt.Sprint(fields[j+1])
			switch name {
			case b.contentKey:
				d.Content = val
			case b.metaKey:
				if val != "" {
					if err := json.Unmarshal([]byte(val), &d.Metadata); err != nil {
						d.Metadata = map[string]interface{}{"raw": val}
					}
				}
			case "vector_score":
				dist, err := strconv.ParseFloat(val, 64)
				if err != nil {
					return nil, fmt.Errorf("FT.SEARCH %s: bad vector_score %q", b.index, val)
				}
				if b.metric == "COSINE" {
					d.Score = 1 - dist
				} else {
					d.Score = -dist
				}
			}
		}
		out = append(out, d)
	}
	return out, nil
}

func (b *redisBackend) Delete(ctx context.Context, ids []string, filter interface{}, all bool) (int, error) {
	var keys []interface{}
	switch {
	case len(ids) > 0:
		for _, id := range ids {
			keys = append(keys, b.prefix+id)
		}
	case filter != nil:
		f, err := redisFilter(filter)
		if err != nil {
			return 0, err
		}
		reply, err := b.r.Do(ctx, "FT.SEARCH", b.index, f, "NOCONTENT", "LIMIT", 0, 10000)
		if err != nil {
			return 0, fmt.Errorf("FT.SEARCH %s: %w", b.index, err)
		}
		arr, _ := reply.([]interface{})
		for i := 1; i < len(arr); i++ {
			keys = append(keys, fmt.Sprint(arr[i]))
		}
		if len(keys) == 0 {
			return 0, nil
		}
	case all:
		if _, err := b.r.Do(ctx, "FT.DROPINDEX", b.index, "DD"); err != nil {
			if isUnknownIndex(err) {
				return 0, nil
			}
			return 0, fmt.Errorf("FT.DROPINDEX %s: %w", b.index, err)
		}
		b.indexChecked = false
		return -1, nil
	default:
		return 0, nil
	}
	reply, err := b.r.Do(ctx, append([]interface{}{"DEL"}, keys...)...)
	if err != nil {
		return 0, fmt.Errorf("DEL: %w", err)
	}
	n, _ := reply.(int64)
	return int(n), nil
}
