package vector

import (
	"fmt"
	"sort"
	"strings"
)

// N8nSubNode is one node of an n8n sub-node tree, as the importer reads it
// from the workflow's connections.
type N8nSubNode struct {
	Type       string                 // full n8n type, e.g. "@n8n/n8n-nodes-langchain.embeddingsOpenAi"
	Parameters map[string]interface{} // the node's n8n parameters
	// ConnectionType is how this node attaches to its parent:
	// "ai_embedding", "ai_document", "ai_textSplitter" or "ai_vectorStore".
	ConnectionType string
	// CredentialID is the mono connection the importer chose for this
	// node's n8n credential (see N8nCredentialMappings); "" for none.
	CredentialID string
	Children     []N8nSubNode
}

// N8nConverted is one mono node produced by the mapping.
type N8nConverted struct {
	Type   string
	Config map[string]interface{}
}

// N8nCollapsed is the result of folding an n8n sub-node tree.
type N8nCollapsed struct {
	Node N8nConverted
	// Before lists nodes the importer inserts, in order, between the
	// root's main input and the root — e.g. the document loader feeding
	// an inserting vector store.
	Before   []N8nConverted
	Warnings []string
}

// ignoredN8nParams are UI-only parameters that never carry settings.
var ignoredN8nParams = map[string]bool{
	"notice": true, "setupNotice": true, "deprecationNotice": true, "ragStarterCallout": true,
	"includeDocumentMetadata": true, "useReranker": true, "textSplittingMode": true,
	"jsonMode": true, "binaryMode": true, "executeNotice": true,
}

// ConvertN8nParams maps one n8n node's parameters onto mono config, without
// looking at sub-nodes. Unsupported types return an error naming the reason.
func ConvertN8nParams(n8nType string, params map[string]interface{}) (N8nConverted, []string, error) {
	m, ok := N8nNodeMappings[n8nType]
	if !ok {
		return N8nConverted{}, nil, fmt.Errorf("n8n node %s has no RAG mapping", n8nType)
	}
	if m.Coverage == "unsupported" {
		return N8nConverted{}, nil, fmt.Errorf("n8n node %s is not supported: %s", n8nType, m.Notes)
	}
	cfg := map[string]interface{}{}
	for k, v := range m.Fixed {
		cfg[k] = v
	}
	var warnings []string
	if m.Coverage == "partial" && m.Notes != "" {
		warnings = append(warnings, shortType(n8nType)+": "+m.Notes)
	}
	used := map[string]bool{}
	for path, ours := range m.Params {
		v, ok := n8nParam(params, path)
		if !ok || v == nil || v == "" {
			continue
		}
		used[path] = true
		if vals, ok := m.Values[ours]; ok {
			if s, isStr := v.(string); isStr {
				if mapped, ok := vals[s]; ok {
					if s == "epubLoader" {
						warnings = append(warnings, shortType(n8nType)+": EPUB is not supported; loader set to auto")
					}
					v = mapped
				}
			}
		}
		if ours == "mode" {
			if s, isStr := v.(string); isStr {
				if s == "retrieve-as-tool" {
					warnings = append(warnings, shortType(n8nType)+": retrieve-as-tool became a search; give the agent a tool that runs this node")
				}
				v = normalizeMode(s)
			}
		}
		cfg[ours] = v
	}
	for _, leaf := range n8nLeafPaths(params, "") {
		if !used[leaf] && !ignoredN8nParams[strings.TrimPrefix(leaf, "options.")] {
			warnings = append(warnings, fmt.Sprintf("%s: parameter %s is not mapped", shortType(n8nType), leaf))
		}
	}
	sort.Strings(warnings)
	return N8nConverted{Type: m.OurType, Config: cfg}, warnings, nil
}

// CollapseN8nSubNodes folds an n8n root node (a vector store, a document
// loader or a vector-store retriever) and its attached sub-nodes into flat
// mono config.
func CollapseN8nSubNodes(rootType string, rootParams map[string]interface{}, subs []N8nSubNode) (N8nCollapsed, error) {
	var res N8nCollapsed
	m, ok := N8nNodeMappings[rootType]
	if !ok {
		return res, fmt.Errorf("n8n node %s has no RAG mapping", rootType)
	}
	if m.Role == N8nRoleRetriever {
		return collapseRetriever(rootType, rootParams, subs)
	}
	conv, warns, err := ConvertN8nParams(rootType, rootParams)
	if err != nil {
		return res, err
	}
	res.Node, res.Warnings = conv, warns
	if conv.Type == "vector.load_document" {
		applyLoaderSplitting(&res, rootParams, subs)
		return res, nil
	}
	for _, sub := range subs {
		switch sub.ConnectionType {
		case "ai_embedding":
			emb, w, err := ConvertN8nParams(sub.Type, sub.Parameters)
			res.Warnings = append(res.Warnings, w...)
			if err != nil {
				res.Warnings = append(res.Warnings, err.Error())
				continue
			}
			if sub.CredentialID != "" {
				emb.Config["credential_id"] = sub.CredentialID
			}
			res.Node.Config["embedding"] = emb.Config
		case "ai_document":
			loader, err := CollapseN8nSubNodes(sub.Type, sub.Parameters, sub.Children)
			res.Warnings = append(res.Warnings, loader.Warnings...)
			if err != nil {
				res.Warnings = append(res.Warnings, err.Error())
				continue
			}
			res.Before = append(res.Before, loader.Before...)
			res.Before = append(res.Before, loader.Node)
		default:
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %s sub-node %s is not used", shortType(rootType), sub.ConnectionType, shortType(sub.Type)))
		}
	}
	if _, has := res.Node.Config["embedding"]; !has && conv.Type == "vector.store" {
		res.Warnings = append(res.Warnings, shortType(rootType)+": no embedding sub-node; items must carry precomputed vectors in \"embedding\"")
	}
	return res, nil
}

// applyLoaderSplitting folds a loader's text splitter. n8n's Default Data
// Loader "simple" mode is a recursive splitter of 1000 characters with 200
// overlap; "custom" (and older loader versions) use the attached splitter.
func applyLoaderSplitting(res *N8nCollapsed, params map[string]interface{}, subs []N8nSubNode) {
	cfg := res.Node.Config
	for _, sub := range subs {
		if sub.ConnectionType != "ai_textSplitter" {
			res.Warnings = append(res.Warnings, fmt.Sprintf("loader: %s sub-node %s is not used", sub.ConnectionType, shortType(sub.Type)))
			continue
		}
		sp, w, err := ConvertN8nParams(sub.Type, sub.Parameters)
		res.Warnings = append(res.Warnings, w...)
		if err != nil {
			res.Warnings = append(res.Warnings, err.Error())
			continue
		}
		cfg["split"] = true
		for k, v := range sp.Config {
			cfg[k] = v
		}
		return
	}
	if mode, _ := params["textSplittingMode"].(string); mode == "simple" {
		cfg["split"] = true
		cfg["splitter"] = SplitterRecursive
		cfg["chunk_size"] = 1000
		cfg["chunk_overlap"] = 200
	}
}

func collapseRetriever(rootType string, params map[string]interface{}, subs []N8nSubNode) (N8nCollapsed, error) {
	m := N8nNodeMappings[rootType]
	if m.Coverage == "unsupported" {
		return N8nCollapsed{}, fmt.Errorf("n8n node %s is not supported: %s", rootType, m.Notes)
	}
	for _, sub := range subs {
		if sub.ConnectionType != "ai_vectorStore" {
			continue
		}
		res, err := CollapseN8nSubNodes(sub.Type, sub.Parameters, sub.Children)
		if err != nil {
			return res, err
		}
		for k, v := range m.Fixed {
			res.Node.Config[k] = v
		}
		for path, ours := range m.Params {
			if v, ok := n8nParam(params, path); ok && v != nil {
				res.Node.Config[ours] = v
			}
		}
		return res, nil
	}
	return N8nCollapsed{}, fmt.Errorf("%s has no ai_vectorStore sub-node", shortType(rootType))
}

// n8nParam reads a dotted parameter path, unwrapping resource-locator
// values ({"__rl": true, "mode": "list", "value": "x"}).
func n8nParam(params map[string]interface{}, path string) (interface{}, bool) {
	v, ok := lookupPath(params, path)
	if !ok {
		return nil, false
	}
	if m, isMap := v.(map[string]interface{}); isMap {
		if _, rl := m["__rl"]; rl {
			return m["value"], true
		}
	}
	return v, true
}

// n8nLeafPaths lists the parameter paths that carry values, descending into
// "options" and fixedCollection objects but not into resource locators.
func n8nLeafPaths(params map[string]interface{}, prefix string) []string {
	var out []string
	for k, v := range params {
		p := prefix + k
		if m, isMap := v.(map[string]interface{}); isMap {
			if _, rl := m["__rl"]; !rl && len(m) > 0 {
				out = append(out, n8nLeafPaths(m, p+".")...)
				continue
			}
		}
		if v == nil || v == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func shortType(t string) string { return strings.TrimPrefix(t, N8nPrefix) }
