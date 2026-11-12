package vector

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/workflow"
)

// AI sub-nodes of the RAG nodes (n8n's embeddings, document loader and text
// splitter sub-nodes). They never run as steps: vector.store and
// vector.load_document read the ones attached to them through "ai_…"
// connections from workflow.NodeInput.SubNodes. They are types of their
// own rather than vector.embed / vector.load_document / vector.split_text
// with a sub_output, because a node with a sub_output has no main ports in
// the editor, and those three run in the main flow of existing workflows.
const (
	TypeEmbeddings     = "vector.embeddings"
	TypeDocumentLoader = "vector.document_loader"
	TypeTextSplitter   = "vector.text_splitter"

	KindEmbedding    = "ai_embedding"
	KindDocument     = "ai_document"
	KindTextSplitter = "ai_textSplitter"
)

// subNode is the executor of a sub-node type: it only fails, since the
// engine never runs an attached sub-node as a step.
type subNode struct{ typ, kind, root string }

func (n *subNode) Type() string { return n.typ }

// PublishesToThirdParty: the vector sub-nodes sends nothing off this machine (issue #1418).
func (*subNode) PublishesToThirdParty() bool { return false }

func (n *subNode) Execute(context.Context, workflow.NodeInput, map[string]interface{}) ([]workflow.NodeOutput, error) {
	return nil, fmt.Errorf("%w: %s is a sub-node: connect its %s output to %s", workflow.ErrInvalidConfig, n.typ, n.kind, n.root)
}

// CredentialDestinationKeys: an embeddings sub-node's are vector.embed's
// (workflow.CheckCredentialDestinations).
func (n *subNode) CredentialDestinationKeys() [][]string {
	if n.typ == TypeEmbeddings {
		return embedDestinations
	}
	return nil
}

// PerItemConfigFields leaves a document loader's text unresolved, so the
// store resolves it against each item, as vector.load_document does.
func (n *subNode) PerItemConfigFields() []string {
	if n.typ == TypeDocumentLoader {
		return []string{"text"}
	}
	return nil
}

var subNodeTypes = []subNode{
	{TypeEmbeddings, KindEmbedding, "a Vector Store"},
	{TypeDocumentLoader, KindDocument, "a Vector Store"},
	{TypeTextSplitter, KindTextSplitter, "a Document Loader"},
}

// firstSub returns the first attached sub-node of kind (a second one of a
// one-only kind is ignored).
func firstSub(in workflow.NodeInput, kind string) (workflow.SubNode, bool) {
	subs := in.SubNodesOf(kind)
	if len(subs) == 0 {
		return workflow.SubNode{}, false
	}
	return subs[0], true
}

func subName(s workflow.SubNode) string {
	if s.Name != "" {
		return s.Name
	}
	return s.ID
}

// splitterKeys are the config keys a text splitter sub-node sets on the
// loader it is attached to.
var splitterKeys = []string{"splitter", "chunk_size", "chunk_overlap", "separator", "separators"}

// withSplitter returns cfg with the attached text splitter's settings in
// place of its own split settings, or cfg itself when none is attached.
func withSplitter(in workflow.NodeInput, cfg map[string]interface{}) map[string]interface{} {
	s, ok := firstSub(in, KindTextSplitter)
	if !ok {
		return cfg
	}
	out := copyMap(cfg)
	for _, k := range splitterKeys {
		delete(out, k)
		if v, set := s.Config[k]; set {
			out[k] = v
		}
	}
	out["split"] = true
	return out
}

// loadDocuments runs the attached document loader over the input items,
// with the loader's own sub-nodes (its text splitter).
func loadDocuments(ctx context.Context, in workflow.NodeInput, loader workflow.SubNode) ([]workflow.Item, error) {
	li := in
	li.SubNodes = loader.SubNodes
	out, err := (&LoadDocumentNode{}).Execute(ctx, li, loader.Config)
	if err != nil {
		return nil, fmt.Errorf("document loader %q: %w", subName(loader), err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out[0].Items, nil
}
