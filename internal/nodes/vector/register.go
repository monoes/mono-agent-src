package vector

import "github.com/monoes/mono-agent/internal/workflow"

// RegisterAll registers the RAG node types.
func RegisterAll(r *workflow.NodeTypeRegistry) {
	r.Register("vector.embed", func() workflow.NodeExecutor { return &EmbedNode{} })
	r.Register("vector.split_text", func() workflow.NodeExecutor { return &SplitTextNode{} })
	r.Register("vector.load_document", func() workflow.NodeExecutor { return &LoadDocumentNode{} })
	r.Register("vector.store", func() workflow.NodeExecutor { return &StoreNode{} })
}
