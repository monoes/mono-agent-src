package n8nimport

import (
	"fmt"
	"sort"
	"strconv"
	"sync"
)

// Mapping is what a Mapper returns for one n8n node.
type Mapping struct {
	// Type is the mono node type, e.g. "comm.slack".
	Type string
	// Config is the mono node config. Expression strings must already be
	// translated (use MapContext.Str / Value).
	Config map[string]interface{}
	// Outputs names the mono output handle for each n8n "main" output
	// index, on the node that produces the outputs (the last of a chain).
	// Nil means a single output on "main".
	Outputs []string
	// Chain lists nodes that run after this one (the n8n node becomes a
	// small chain, e.g. a schedule trigger followed by an IMAP read).
	// Incoming connections go to the first node, outgoing ones leave the
	// last node.
	Chain []ChainStep
	// Alternates are extra trigger nodes that start the same downstream
	// path (an n8n schedule with several rules becomes one trigger per rule).
	Alternates []ChainStep
	// EntrySuffix names the first node of a chain (the n8n name plus this
	// suffix); the last node keeps the n8n name.
	EntrySuffix string
	// TagInputs inserts a core.set before every incoming connection that
	// records the n8n input index in the item field "__n8n_input", for
	// nodes whose behaviour depends on which input an item came from.
	TagInputs bool
	// Unmapped marks a node with no mono equivalent. The converter writes
	// a core.code stub that fails with a clear message.
	Unmapped bool
}

// ChainStep is one extra node of a Mapping chain.
type ChainStep struct {
	Type   string
	Suffix string // appended to the n8n name for steps before the last
	Config map[string]interface{}
}

// Mapper converts one n8n node (parameters, typeVersion, credentials, all
// reachable through the MapContext) into a mono node. Issues and notes
// for the report are recorded on the context.
type Mapper func(c *MapContext) Mapping

// AppMapper maps the n8n node types of a declarative API package. Package
// node configs use n8n parameter names verbatim (resource, operation and
// the operation's fields), so the mapping is near-identity: the type
// becomes the package node type, parameters are copied with expressions
// translated, and the n8n credential type picks the connection platform.
type AppMapper struct {
	// N8nTypes are the n8n node types the package imports,
	// e.g. "n8n-nodes-base.trello".
	N8nTypes []string
	// NodeType is the package's mono node type ("app.trello"). Empty when
	// Extends is set.
	NodeType string
	// Extends is the native mono node the package adds operations to
	// ("service.github"); used as the node type when NodeType is empty.
	Extends string
	// CredentialPlatform maps an n8n credential type ("trelloApi") to the
	// mono connection platform ("trello").
	CredentialPlatform map[string]string
	// DefaultResource and DefaultOperation are filled in when the n8n
	// export omits them (n8n leaves out parameters equal to their default).
	// DefaultOperations, when set, gives the default operation per
	// resource and wins over DefaultOperation.
	DefaultResource   string
	DefaultOperation  string
	DefaultOperations map[string]string
	// OperationParam is the n8n parameter that selects the operation when
	// it is not "operation" (e.g. "action"). The parameter is copied under
	// its own name, which the package reads.
	OperationParam string
	// ResourceParam is the n8n parameter that selects the resource when
	// it is not "resource" (uProc: "group"); copied under its own name.
	ResourceParam string
	// ParamMap renames n8n parameters per operation, keyed
	// "resource.operation" (or "operation" when there is no resource):
	// n8n parameter name → package field name. Only for names that differ.
	ParamMap map[string]map[string]string
	// CredentialHints is the report hint per n8n credential type (how to
	// create the connection, which n8n fields become which of ours).
	CredentialHints map[string]string
	// Trigger marks an n8n trigger type the package lists but cannot run
	// yet: it converts to a placeholder with a clear message.
	Trigger bool
	// PackageID names the API package, for report messages.
	PackageID string
	// VersionDefaults: n8n typeVersion ("1", "2", "1.1") → param defaults
	// filled in when the export omits them.
	VersionDefaults map[string]map[string]any
}

var (
	regMu   sync.RWMutex
	mappers = map[string]Mapper{}
	apps    = map[string]AppMapper{}
	// srcApps holds the AppMappers of the app sources, refreshed on every
	// Convert so packages installed meanwhile are seen. apps wins.
	srcApps    = map[string]AppMapper{}
	appSources []func() []AppMapper
)

// RegisterAppSource adds a function listing AppMappers (the declarative
// API packages). It is called at the start of every Convert, so the
// importer always sees the currently installed packages without importing
// them; a type registered with RegisterApp wins over a source's.
func RegisterAppSource(f func() []AppMapper) {
	regMu.Lock()
	defer regMu.Unlock()
	appSources = append(appSources, f)
}

// refreshAppSources rebuilds srcApps from the app sources.
func refreshAppSources() {
	regMu.RLock()
	sources := append([]func() []AppMapper(nil), appSources...)
	regMu.RUnlock()
	if len(sources) == 0 {
		return
	}
	next := map[string]AppMapper{}
	for _, f := range sources {
		for _, a := range f() {
			for _, t := range a.N8nTypes {
				if _, ok := next[t]; !ok {
					next[t] = a
				}
			}
		}
	}
	regMu.Lock()
	srcApps = next
	regMu.Unlock()
}

// Register installs the Mapper for an n8n node type (the full type string,
// e.g. "n8n-nodes-base.slack"). A later registration replaces an earlier one.
func Register(n8nType string, m Mapper) {
	regMu.Lock()
	defer regMu.Unlock()
	mappers[n8nType] = m
}

// RegisterApp installs an AppMapper for each of its n8n types. A native
// Mapper registered for the same type still wins; the app mapping is used
// for operations the native mapper reports as unsupported.
func RegisterApp(a AppMapper) {
	regMu.Lock()
	defer regMu.Unlock()
	for _, t := range a.N8nTypes {
		apps[t] = a
	}
}

func lookup(n8nType string) (Mapper, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	m, ok := mappers[n8nType]
	return m, ok
}

func lookupApp(n8nType string) (AppMapper, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	if a, ok := apps[n8nType]; ok {
		return a, ok
	}
	a, ok := srcApps[n8nType]
	return a, ok
}

// SupportedTypes lists the n8n node types with a native Mapper and those
// handled by an AppMapper (triggers left out), both sorted.
func SupportedTypes() (native, app []string) {
	refreshAppSources()
	regMu.RLock()
	defer regMu.RUnlock()
	for t := range mappers {
		native = append(native, t)
	}
	seen := map[string]bool{}
	for _, m := range []map[string]AppMapper{apps, srcApps} {
		for t, a := range m {
			if _, ok := mappers[t]; !ok && !a.Trigger && !seen[t] {
				seen[t] = true
				app = append(app, t)
			}
		}
	}
	sort.Strings(native)
	sort.Strings(app)
	return native, app
}

// mapApp is the generic AppMapper conversion.
func mapApp(a AppMapper, c *MapContext) Mapping {
	if a.Trigger {
		c.issues = append(c.issues, Issue{Severity: SevError,
			Message: fmt.Sprintf("n8n trigger %s (API package %s) is not supported yet; start the workflow with a schedule or webhook trigger instead", c.Type, a.PackageID)})
		return Mapping{Unmapped: true}
	}
	nodeType := a.NodeType
	if nodeType == "" {
		nodeType = a.Extends
	}
	opParam := a.OperationParam
	if opParam == "" {
		opParam = "operation"
	}
	cfg := map[string]interface{}{}
	resParam := a.ResourceParam
	if resParam == "" {
		resParam = "resource"
	}
	resource := c.Literal(resParam, a.DefaultResource)
	defOp := a.DefaultOperation
	if d, ok := a.DefaultOperations[resource]; ok {
		defOp = d
	}
	operation := c.Literal(opParam, defOp)
	if resource != "" {
		cfg[resParam] = resource
	}
	if operation != "" {
		cfg[opParam] = operation
	}
	key := operation
	if resource != "" {
		key = resource + "." + operation
	}
	rename := a.ParamMap[key]
	for k, v := range a.VersionDefaults[strconv.FormatFloat(c.TypeVersion, 'f', -1, 64)] {
		if _, set := c.Params[k]; !set {
			dst := k
			if r, ok := rename[k]; ok {
				dst = r
			}
			cfg[dst] = v
		}
	}
	for k := range c.Params {
		if k == resParam || k == opParam {
			continue
		}
		dst := k
		if r, ok := rename[k]; ok {
			dst = r
		}
		cfg[dst] = c.Value(k)
	}
	if id := a.credential(c); id != "" {
		cfg["credential_id"] = id
	}
	return Mapping{Type: nodeType, Config: cfg}
}

// credential records the connection the package node needs: the platform
// of the n8n credential the node uses (else the package's first one). It
// returns the credential_id placeholder, or "" when the package has no
// credential.
func (a AppMapper) credential(c *MapContext) string {
	credTypes := make([]string, 0, len(a.CredentialPlatform))
	for t := range a.CredentialPlatform {
		credTypes = append(credTypes, t)
	}
	sort.Strings(credTypes)
	used := ""
	for _, t := range credTypes {
		if _, ok := c.Credentials[t]; ok {
			used = t
			break
		}
	}
	if used == "" && len(credTypes) > 0 {
		used = credTypes[0]
	}
	if used == "" {
		return ""
	}
	var id string
	if _, inNode := c.Credentials[used]; inNode {
		id = c.Credential(a.CredentialPlatform[used], used)
	} else {
		id = c.Credential(a.CredentialPlatform[used])
	}
	if h := a.CredentialHints[used]; h != "" {
		c.creds[len(c.creds)-1].Hint = h
	}
	return id
}
