package vector

import (
	"reflect"
	"strings"
	"testing"
)

func TestN8nStoreWithEmbeddingAndLoaderCollapses(t *testing.T) {
	root := N8nPrefix + "vectorStoreQdrant"
	params := map[string]interface{}{
		"mode":             "insert",
		"qdrantCollection": map[string]interface{}{"__rl": true, "mode": "list", "value": "docs"},
		"options":          map[string]interface{}{"collectionConfig": "{}"},
	}
	subs := []N8nSubNode{
		{Type: N8nPrefix + "embeddingsOpenAi", ConnectionType: "ai_embedding", CredentialID: "openai-1",
			Parameters: map[string]interface{}{"model": "text-embedding-3-large", "options": map[string]interface{}{"dimensions": 1024.0}}},
		{Type: N8nPrefix + "documentDefaultDataLoader", ConnectionType: "ai_document",
			Parameters: map[string]interface{}{"dataType": "binary", "loader": "pdfLoader", "textSplittingMode": "custom"},
			Children: []N8nSubNode{{Type: N8nPrefix + "textSplitterRecursiveCharacterTextSplitter", ConnectionType: "ai_textSplitter",
				Parameters: map[string]interface{}{"chunkSize": 500.0, "chunkOverlap": 50.0}}}},
	}
	res, err := CollapseN8nSubNodes(root, params, subs)
	if err != nil {
		t.Fatal(err)
	}
	wantStore := map[string]interface{}{
		"provider": "qdrant", "mode": "insert", "collection": "docs",
		"embedding": map[string]interface{}{"provider": "openai_compatible", "model": "text-embedding-3-large", "dimensions": 1024.0, "credential_id": "openai-1"},
	}
	if res.Node.Type != "vector.store" || !reflect.DeepEqual(res.Node.Config, wantStore) {
		t.Fatalf("store %s %#v", res.Node.Type, res.Node.Config)
	}
	wantLoader := N8nConverted{Type: "vector.load_document", Config: map[string]interface{}{
		"source": "binary", "loader": "pdf", "split": true, "splitter": "recursive", "chunk_size": 500.0, "chunk_overlap": 50.0,
	}}
	if len(res.Before) != 1 || !reflect.DeepEqual(res.Before[0], wantLoader) {
		t.Fatalf("before %#v", res.Before)
	}
	joined := strings.Join(res.Warnings, "\n")
	for _, w := range []string{"collectionConfig", "language presets", "parameter options.collectionConfig is not mapped"} {
		if !strings.Contains(joined, w) {
			t.Errorf("warnings missing %q:\n%s", w, joined)
		}
	}
}

func TestN8nSimpleSplittingAndModes(t *testing.T) {
	res, err := CollapseN8nSubNodes(N8nPrefix+"documentDefaultDataLoader",
		map[string]interface{}{"dataType": "json", "jsonMode": "expressionData", "jsonData": "={{ $json.body }}", "textSplittingMode": "simple"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{"source": "json", "text": "={{ $json.body }}", "split": true, "splitter": "recursive", "chunk_size": 1000, "chunk_overlap": 200}
	if !reflect.DeepEqual(res.Node.Config, want) {
		t.Fatalf("loader %#v", res.Node.Config)
	}

	conv, warns, err := ConvertN8nParams(N8nPrefix+"vectorStorePineconeLoad", map[string]interface{}{
		"pineconeIndex": map[string]interface{}{"__rl": true, "value": "kb"}, "prompt": "refunds", "topK": 6.0,
		"options": map[string]interface{}{"pineconeNamespace": "support"},
	})
	if err != nil || len(warns) != 0 {
		t.Fatalf("err %v warns %v", err, warns)
	}
	want = map[string]interface{}{"provider": "pinecone", "mode": "search", "collection": "kb", "query": "refunds", "top_k": 6.0, "namespace": "support"}
	if !reflect.DeepEqual(conv.Config, want) {
		t.Fatalf("pinecone %#v", conv.Config)
	}

	conv, warns, _ = ConvertN8nParams(N8nPrefix+"vectorStoreInMemory", map[string]interface{}{"mode": "retrieve-as-tool", "memoryKey": map[string]interface{}{"__rl": true, "value": "kb"}})
	if conv.Config["mode"] != "search" || conv.Config["memory_key"] != "kb" || len(warns) != 1 {
		t.Fatalf("in-memory %#v %v", conv.Config, warns)
	}
}

func TestN8nRetrieverFoldsVectorStore(t *testing.T) {
	res, err := CollapseN8nSubNodes(N8nPrefix+"retrieverVectorStore", map[string]interface{}{"topK": 8.0}, []N8nSubNode{{
		Type: N8nPrefix + "vectorStoreSupabase", ConnectionType: "ai_vectorStore",
		Parameters: map[string]interface{}{"tableName": map[string]interface{}{"__rl": true, "value": "documents"}},
		Children:   []N8nSubNode{{Type: N8nPrefix + "embeddingsOllama", ConnectionType: "ai_embedding", Parameters: map[string]interface{}{"model": "nomic-embed-text"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{"provider": "supabase", "collection": "documents", "mode": "search", "top_k": 8.0,
		"embedding": map[string]interface{}{"provider": "ollama", "model": "nomic-embed-text"}}
	if !reflect.DeepEqual(res.Node.Config, want) {
		t.Fatalf("retriever %#v", res.Node.Config)
	}
	if _, err := CollapseN8nSubNodes(N8nPrefix+"retrieverVectorStore", nil, nil); err == nil {
		t.Fatal("retriever without a store should fail")
	}
}

func TestN8nUnsupportedAndTableIntegrity(t *testing.T) {
	if _, _, err := ConvertN8nParams(N8nPrefix+"vectorStoreZep", nil); err == nil || !strings.Contains(err.Error(), "deprecated") {
		t.Fatalf("zep: %v", err)
	}
	if _, _, err := ConvertN8nParams("n8n-nodes-base.set", nil); err == nil {
		t.Fatal("non-RAG node should not map")
	}
	known := map[string]bool{"vector.embed": true, "vector.store": true, "vector.split_text": true, "vector.load_document": true, "": true}
	for typ, m := range N8nNodeMappings {
		if !strings.HasPrefix(typ, N8nPrefix) {
			t.Errorf("%s lacks the langchain prefix", typ)
		}
		if !known[m.OurType] {
			t.Errorf("%s maps to unknown type %q", typ, m.OurType)
		}
		switch m.Coverage {
		case "full", "partial", "unsupported":
		default:
			t.Errorf("%s has coverage %q", typ, m.Coverage)
		}
		if m.Coverage != "unsupported" && m.OurType == "" {
			t.Errorf("%s is supported but has no type", typ)
		}
		if p, ok := m.Fixed["provider"].(string); ok && m.Role == N8nRoleNode {
			if _, err := parseStoreConfig(map[string]interface{}{"provider": p, "collection": "c"}); err != nil {
				t.Errorf("%s: %v", typ, err)
			}
		}
	}
	for cred, c := range N8nCredentialMappings {
		if c.Platform == "" {
			t.Errorf("credential %s has no platform", cred)
		}
	}
}
