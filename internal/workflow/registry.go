package workflow

import (
	"fmt"
	"sort"
)

// NodeFactory creates a new NodeExecutor for the given type.
type NodeFactory func() NodeExecutor

// NodeTypeRegistry maps node type strings to factory functions.
// Thread-safe for concurrent reads after initialization.
type NodeTypeRegistry struct {
	factories map[string]NodeFactory
	aliases   map[string]string // legacy name → canonical name
}

// NewNodeTypeRegistry creates an empty registry.
func NewNodeTypeRegistry() *NodeTypeRegistry {
	return &NodeTypeRegistry{
		factories: make(map[string]NodeFactory),
		aliases:   make(map[string]string),
	}
}

// Register adds a factory for a specific node type string.
// Panics if the type is already registered (programming error).
func (r *NodeTypeRegistry) Register(nodeType string, factory NodeFactory) {
	if _, exists := r.factories[nodeType]; exists {
		panic(fmt.Sprintf("workflow: node type %q is already registered", nodeType))
	}
	r.factories[nodeType] = factory
}

// Alias registers an alternative name that resolves to an existing type.
func (r *NodeTypeRegistry) Alias(from, to string) {
	r.aliases[from] = to
}

// resolve maps a node type through aliases if needed.
func (r *NodeTypeRegistry) resolve(nodeType string) string {
	if canonical, ok := r.aliases[nodeType]; ok {
		return canonical
	}
	return nodeType
}

// Get returns a factory for the given node type.
// Returns (factory, true) if found, (nil, false) if not.
// Resolves legacy aliases automatically.
func (r *NodeTypeRegistry) Get(nodeType string) (NodeFactory, bool) {
	factory, ok := r.factories[r.resolve(nodeType)]
	return factory, ok
}

// Types returns all registered type strings sorted alphabetically.
func (r *NodeTypeRegistry) Types() []string {
	types := make([]string, 0, len(r.factories))
	for nodeType := range r.factories {
		types = append(types, nodeType)
	}
	sort.Strings(types)
	return types
}

// Aliases returns a copy of the legacy name → canonical name map.
func (r *NodeTypeRegistry) Aliases() map[string]string {
	out := make(map[string]string, len(r.aliases))
	for from, to := range r.aliases {
		out[from] = to
	}
	return out
}

// Has reports whether the type is registered.
func (r *NodeTypeRegistry) Has(nodeType string) bool {
	_, ok := r.factories[r.resolve(nodeType)]
	return ok
}
