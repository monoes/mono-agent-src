package vector

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// embedRecorded is one request seen by a fake provider.
type embedRecorded struct {
	Method, Path, RawQuery string
	Header                 http.Header
	Body                   map[string]interface{}
}

// embedFakeServer answers every request with respond(path) and records it.
func embedFakeServer(t *testing.T, respond func(r *http.Request, body map[string]interface{}) (int, interface{})) (*httptest.Server, *[]embedRecorded) {
	t.Helper()
	var reqs []embedRecorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]interface{}
		_ = json.Unmarshal(raw, &body)
		reqs = append(reqs, embedRecorded{Method: r.Method, Path: r.URL.EscapedPath(), RawQuery: r.URL.RawQuery, Header: r.Header.Clone(), Body: body})
		status, resp := respond(r, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

func runEmbed(t *testing.T, cfg map[string]interface{}, texts ...string) [][]float64 {
	t.Helper()
	items := make([]workflow.Item, len(texts))
	for i, s := range texts {
		items[i] = workflow.NewItem(map[string]interface{}{"pageContent": s})
	}
	out, err := (&EmbedNode{}).Execute(context.Background(), workflow.NodeInput{Items: items}, cfg)
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	vecs := make([][]float64, len(out[0].Items))
	for i, it := range out[0].Items {
		vecs[i] = it.JSON["embedding"].([]float64)
	}
	return vecs
}

func TestEmbedOpenAICompatibleBatchesAndOrders(t *testing.T) {
	srv, reqs := embedFakeServer(t, func(r *http.Request, body map[string]interface{}) (int, interface{}) {
		in := body["input"].([]interface{})
		data := []map[string]interface{}{}
		for i := len(in) - 1; i >= 0; i-- { // out of order on purpose
			data = append(data, map[string]interface{}{"index": i, "embedding": []float64{float64(len(in[i].(string)))}})
		}
		return 200, map[string]interface{}{"data": data}
	})
	vecs := runEmbed(t, map[string]interface{}{
		"provider": "openai_compatible", "base_url": srv.URL + "/v1/", "api_key": "sk-test",
		"model": "text-embedding-3-small", "batch_size": float64(2), "dimensions": float64(256),
	}, "a", "bb", "ccc")
	if !reflect.DeepEqual(vecs, [][]float64{{1}, {2}, {3}}) {
		t.Fatalf("vectors %v", vecs)
	}
	if len(*reqs) != 2 {
		t.Fatalf("want 2 batched requests, got %d", len(*reqs))
	}
	r := (*reqs)[0]
	if r.Method != "POST" || r.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer sk-test" {
		t.Fatalf("request %+v", r)
	}
	if r.Body["model"] != "text-embedding-3-small" || r.Body["dimensions"] != float64(256) || len(r.Body["input"].([]interface{})) != 2 {
		t.Fatalf("body %v", r.Body)
	}
}

func TestEmbedProvidersRequestShapes(t *testing.T) {
	cases := []struct {
		name     string
		cfg      map[string]interface{}
		wantPath string
		wantQS   string
		auth     [2]string // header, value
		check    func(t *testing.T, body map[string]interface{})
		reply    interface{}
	}{
		{
			name: "ollama", cfg: map[string]interface{}{"provider": "ollama", "model": "nomic-embed-text"},
			wantPath: "/api/embed", reply: map[string]interface{}{"embeddings": [][]float64{{0.5}}},
			check: func(t *testing.T, b map[string]interface{}) {
				if b["model"] != "nomic-embed-text" || b["input"].([]interface{})[0] != "hi" {
					t.Fatalf("body %v", b)
				}
			},
		},
		{
			name: "cohere", cfg: map[string]interface{}{"provider": "cohere", "api_key": "co"},
			wantPath: "/v2/embed", auth: [2]string{"Authorization", "Bearer co"},
			reply: map[string]interface{}{"embeddings": map[string]interface{}{"float": [][]float64{{0.5}}}},
			check: func(t *testing.T, b map[string]interface{}) {
				if b["model"] != "embed-english-v3.0" || b["input_type"] != "search_document" || b["texts"].([]interface{})[0] != "hi" {
					t.Fatalf("body %v", b)
				}
			},
		},
		{
			name: "gemini", cfg: map[string]interface{}{"provider": "gemini", "api_key": "g", "model": "models/text-embedding-004"},
			wantPath: "/v1beta/models/text-embedding-004:batchEmbedContents", auth: [2]string{"X-Goog-Api-Key", "g"},
			reply: map[string]interface{}{"embeddings": []map[string]interface{}{{"values": []float64{0.5}}}},
			check: func(t *testing.T, b map[string]interface{}) {
				r := b["requests"].([]interface{})[0].(map[string]interface{})
				parts := r["content"].(map[string]interface{})["parts"].([]interface{})
				if r["model"] != "models/text-embedding-004" || r["taskType"] != "RETRIEVAL_DOCUMENT" || parts[0].(map[string]interface{})["text"] != "hi" {
					t.Fatalf("body %v", b)
				}
			},
		},
		{
			name: "mistral", cfg: map[string]interface{}{"provider": "mistral", "api_key": "m"},
			wantPath: "/embeddings", auth: [2]string{"Authorization", "Bearer m"},
			reply: map[string]interface{}{"data": []map[string]interface{}{{"index": 0, "embedding": []float64{0.5}}}},
			check: func(t *testing.T, b map[string]interface{}) {
				if b["model"] != "mistral-embed" {
					t.Fatalf("body %v", b)
				}
			},
		},
		{
			name: "huggingface token-level output is mean-pooled", cfg: map[string]interface{}{"provider": "huggingface", "api_key": "hf", "model": "org/model"},
			wantPath: "/models/org/model/pipeline/feature-extraction", auth: [2]string{"Authorization", "Bearer hf"},
			reply: [][][]float64{{{0}, {1}}},
			check: func(t *testing.T, b map[string]interface{}) {
				if b["inputs"].([]interface{})[0] != "hi" {
					t.Fatalf("body %v", b)
				}
			},
		},
		{
			name: "azure", cfg: map[string]interface{}{"provider": "azure_openai", "api_key": "az", "model": "my-deploy", "api_version": "2024-10-21"},
			wantPath: "/openai/deployments/my-deploy/embeddings", wantQS: "api-version=2024-10-21", auth: [2]string{"Api-Key", "az"},
			reply: map[string]interface{}{"data": []map[string]interface{}{{"index": 0, "embedding": []float64{0.5}}}},
			check: func(t *testing.T, b map[string]interface{}) {
				if _, has := b["model"]; has {
					t.Fatalf("azure body must not carry model: %v", b)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, reqs := embedFakeServer(t, func(*http.Request, map[string]interface{}) (int, interface{}) { return 200, c.reply })
			c.cfg["base_url"] = srv.URL
			vecs := runEmbed(t, c.cfg, "hi")
			if !reflect.DeepEqual(vecs, [][]float64{{0.5}}) {
				t.Fatalf("vectors %v", vecs)
			}
			r := (*reqs)[0]
			if r.Method != "POST" || r.Path != c.wantPath || r.RawQuery != c.wantQS {
				t.Fatalf("got %s %s?%s", r.Method, r.Path, r.RawQuery)
			}
			if c.auth[0] != "" && r.Header.Get(c.auth[0]) != c.auth[1] {
				t.Fatalf("header %s = %q", c.auth[0], r.Header.Get(c.auth[0]))
			}
			c.check(t, r.Body)
		})
	}
}

func TestEmbedBedrockSignsAndCallsPerText(t *testing.T) {
	bedrockNow = func() time.Time { return time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC) }
	defer func() { bedrockNow = func() time.Time { return time.Now().UTC() } }()
	srv, reqs := embedFakeServer(t, func(r *http.Request, b map[string]interface{}) (int, interface{}) {
		return 200, map[string]interface{}{"embedding": []float64{float64(len(b["inputText"].(string)))}}
	})
	vecs := runEmbed(t, map[string]interface{}{
		"provider": "bedrock", "base_url": srv.URL, "region": "eu-west-1",
		"access_key_id": "AKIDEXAMPLE", "secret_access_key": "secret", "session_token": "tok",
	}, "a", "bb")
	if !reflect.DeepEqual(vecs, [][]float64{{1}, {2}}) || len(*reqs) != 2 {
		t.Fatalf("vectors %v, %d requests", vecs, len(*reqs))
	}
	r := (*reqs)[0]
	if r.Path != "/model/amazon.titan-embed-text-v2%3A0/invoke" {
		t.Fatalf("path %s", r.Path)
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261013/eu-west-1/bedrock/aws4_request, SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date;x-amz-security-token, Signature=") {
		t.Fatalf("authorization %q", auth)
	}
	if r.Header.Get("X-Amz-Date") != "20261013T120000Z" || r.Header.Get("X-Amz-Security-Token") != "tok" {
		t.Fatalf("amz headers %v", r.Header)
	}
}

func TestSignV4KnownVector(t *testing.T) {
	// Recomputing the same request twice must give the same signature, and
	// any change to the payload must change it.
	mk := func(payload string) string {
		req, _ := http.NewRequest("POST", "https://bedrock-runtime.us-east-1.amazonaws.com/model/x/invoke", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		signV4(req, []byte(payload), "AK", "SK", "", "us-east-1", "bedrock", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
		return req.Header.Get("Authorization")
	}
	if mk("{}") != mk("{}") || mk("{}") == mk(`{"a":1}`) {
		t.Fatal("signature is not a deterministic function of the payload")
	}
}

func TestEmbedErrors(t *testing.T) {
	srv, _ := embedFakeServer(t, func(*http.Request, map[string]interface{}) (int, interface{}) {
		return 401, map[string]string{"error": "bad key"}
	})
	_, err := (&EmbedNode{}).Execute(context.Background(), workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"text": "x"})}},
		map[string]interface{}{"provider": "openai_compatible", "base_url": srv.URL})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("err %v", err)
	}
	for _, c := range []struct {
		cfg  map[string]interface{}
		want string
	}{
		{map[string]interface{}{"provider": "nope"}, `provider "nope" is not supported`},
		{map[string]interface{}{"provider": "cohere"}, "api_key is required for provider cohere"},
		{map[string]interface{}{"provider": "azure_openai", "api_key": "k", "model": "d"}, "resource_name or endpoint is required"},
		{map[string]interface{}{"provider": "bedrock"}, "access_key_id and secret_access_key are required"},
	} {
		if _, err := parseEmbedConfig(c.cfg, ""); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err %v, want %q", c.cfg, err, c.want)
		}
	}
	_, err = (&EmbedNode{}).Execute(context.Background(), workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"a": 1.0})}},
		map[string]interface{}{"provider": "ollama", "input_field": "missing"})
	if err == nil || !strings.Contains(err.Error(), `item 0: input field "missing" is missing`) {
		t.Fatalf("err %v", err)
	}
}

func TestItemTextFallbacks(t *testing.T) {
	if s, _ := itemText(map[string]interface{}{"text": "t", "x": 1.0}, nil); s != "t" {
		t.Fatalf("text fallback: %q", s)
	}
	if s, _ := itemText(map[string]interface{}{"x": 1.0}, nil); s != `{"x":1}` {
		t.Fatalf("json fallback: %q", s)
	}
	if s, _ := itemText(map[string]interface{}{"a": "A", "b": map[string]interface{}{"c": "C"}}, []string{"a", "b.c"}); s != "A\n\nC" {
		t.Fatalf("fields: %q", s)
	}
}
