package connections

import "testing"

// ragPlatformIDs are the embedding-provider and vector-database platforms
// the vector.* nodes use.
var ragPlatformIDs = []string{
	"openai", "azure_openai", "ollama", "cohere", "mistral", "gemini_api", "aws",
	"qdrant", "pinecone", "supabase", "weaviate", "milvus", "chroma", "azure_ai_search",
}

func TestRegistryHasRAGPlatforms(t *testing.T) {
	for _, id := range ragPlatformIDs {
		p, ok := Registry[id]
		if !ok {
			t.Errorf("Registry missing platform %q", id)
			continue
		}
		if p.ID != id || p.ConnectVia != "API" || len(p.Fields[MethodAPIKey]) == 0 {
			t.Errorf("platform %q is malformed: %+v", id, p)
		}
		secret := false
		for _, f := range p.Fields[MethodAPIKey] {
			secret = secret || f.Secret
		}
		if !secret {
			t.Errorf("platform %q has no secret field", id)
		}
	}
}
