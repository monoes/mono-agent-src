package n8nimport

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// Convert turns an n8n workflow export into a mono workflow file (inactive,
// without an id, ready for `workflow import`) and a report of what was and
// was not carried over. Only unreadable input is an error: nodes without a
// mono equivalent become failing placeholders listed in the report.
func Convert(raw []byte) (*workflow.WorkflowFile, *Report, error) {
	refreshAppSources()
	src, err := parseN8n(raw)
	if err != nil {
		return nil, nil, err
	}
	name := strings.TrimSpace(src.Name)
	if name == "" {
		name = "Imported n8n workflow"
	}
	b := &builder{
		src:     src,
		rep:     &Report{Workflow: name},
		out:     &workflow.WorkflowFile{Name: name, Version: 1},
		byName:  map[string]*n8nNode{},
		mapped:  map[string]*mappedNode{},
		reports: map[string]int{},
		ids:     map[string]bool{},
	}
	for _, n := range src.Nodes {
		b.byName[n.Name] = n
	}
	b.mapNodes()
	b.buildEdges()
	b.applySettings()
	b.out.Description = b.description()
	b.noteBatchNodes()
	b.rep.Counts.MonoNodes = len(b.out.Nodes)
	b.rep.Counts.Connections = len(b.out.Connections)
	if len(b.out.Nodes) > 200 {
		b.rep.note("the converted workflow has %d nodes; mono-agent workflows hold at most 200, so split it before importing", len(b.out.Nodes))
	}
	b.rep.finish()
	return b.out, b.rep, nil
}

// mappedNode is where one n8n node landed in the mono workflow.
type mappedNode struct {
	entryID, exitID string
	alternates      []string
	outputs         []string
	tagInputs       bool
	errorOutput     bool // onError = continueErrorOutput
	n8n             *n8nNode
}

type builder struct {
	src      *n8nWorkflow
	rep      *Report
	out      *workflow.WorkflowFile
	byName   map[string]*n8nNode
	mapped   map[string]*mappedNode
	reports  map[string]int // n8n node name → index in rep.Nodes
	ids      map[string]bool
	notes    []string // node notes and sticky notes for the description
	nextConn int
}

const stickyNoteType = "n8n-nodes-base.stickyNote"

func (b *builder) nodeReport(name string) *NodeReport {
	return &b.rep.Nodes[b.reports[name]]
}

// subNodes finds nodes attached to another through non-"main" connections
// (LangChain models, tools, memory, …), keyed by the parent's name.
func (b *builder) subNodes() (map[string][]SubNode, map[string]string) {
	subs := map[string][]SubNode{}
	parent := map[string]string{}
	for _, srcName := range sortedKeys(b.src.Connections) {
		for kind, outs := range b.src.Connections[srcName] {
			if kind == "main" {
				continue
			}
			n := b.byName[srcName]
			if n == nil {
				continue
			}
			for _, ends := range outs {
				for _, end := range ends {
					if end == nil {
						continue
					}
					parent[srcName] = end.Node
					subs[end.Node] = append(subs[end.Node], SubNode{Name: n.Name, Type: n.Type,
						TypeVersion: n.TypeVersion, Kind: kind, Parameters: n.Parameters, Credentials: n.Credentials})
				}
			}
		}
	}
	for name := range subs {
		for i := range subs[name] {
			subs[name][i].Children = subTree(subs, subs[name][i].Name, 8)
		}
	}
	return subs, parent
}

// subTree copies the sub-nodes attached to name, with theirs, down to
// depth levels (n8n sub-node trees are shallow; the limit guards cycles).
func subTree(subs map[string][]SubNode, name string, depth int) []SubNode {
	if depth == 0 || len(subs[name]) == 0 {
		return nil
	}
	out := make([]SubNode, len(subs[name]))
	copy(out, subs[name])
	for i := range out {
		out[i].Children = subTree(subs, out[i].Name, depth-1)
	}
	return out
}

func (b *builder) mapNodes() {
	subs, parent := b.subNodes()
	for _, n := range b.src.Nodes {
		b.reports[n.Name] = len(b.rep.Nodes)
		b.rep.Nodes = append(b.rep.Nodes, NodeReport{Name: n.Name, N8nType: n.Type, TypeVersion: n.TypeVersion})
		nr := b.nodeReport(n.Name)
		if n.Type == stickyNoteType {
			nr.Status = StatusDropped
			nr.Issues = append(nr.Issues, Issue{Severity: SevInfo, Message: "sticky note: its text is kept in the workflow description"})
			if text := strings.TrimSpace(fmt.Sprint(n.Parameters["content"])); text != "" && text != "<nil>" {
				b.notes = append(b.notes, text)
			}
			continue
		}
		if p, ok := parent[n.Name]; ok {
			nr.Status = StatusFolded
			nr.Issues = append(nr.Issues, Issue{Severity: SevInfo, Message: fmt.Sprintf("sub-node folded into %q", p)})
			continue
		}
		ctx := newMapContext(n, b.rep.Workflow)
		ctx.SubNodes = subs[n.Name]
		ctx.OnErrorOutput = n.OnError == "continueErrorOutput"
		m := b.runMapper(ctx, nr)
		b.place(n, ctx, m, nr)
	}
}

// runMapper picks the native Mapper, then an AppMapper, then the stub.
func (b *builder) runMapper(ctx *MapContext, nr *NodeReport) Mapping {
	var m Mapping
	native, hasNative := lookup(ctx.Type)
	app, hasApp := lookupApp(ctx.Type)
	saw := rewriteSendAndWait(ctx)
	switch {
	case hasNative:
		m = native(ctx)
		if ctx.opUnsupported && hasApp {
			fresh := newMapContext(b.byName[ctx.NodeName], b.rep.Workflow)
			fresh.SubNodes, fresh.OnErrorOutput, fresh.Params = ctx.SubNodes, ctx.OnErrorOutput, ctx.Params
			*ctx = *fresh
			m = mapApp(app, ctx)
		}
	case hasApp:
		m = mapApp(app, ctx)
	default:
		m = Mapping{Unmapped: true}
		ctx.issues = append(ctx.issues, Issue{Severity: SevError,
			Message: fmt.Sprintf("n8n node type %s is not available in mono-agent yet", ctx.Type)})
	}
	if saw != nil && !m.Unmapped {
		m = saw.attach(ctx, m)
	}
	if !m.Unmapped {
		ctx.reportUnused()
	}
	nr.Issues = append(nr.Issues, ctx.issues...)
	for _, e := range ctx.exprs {
		nr.Expressions = append(nr.Expressions, e)
	}
	for _, u := range ctx.creds {
		b.rep.addCredential(ctx.NodeName, u)
	}
	for _, note := range ctx.tr.notes {
		nr.Issues = append(nr.Issues, Issue{Severity: SevInfo, Message: note})
	}
	return m
}

var nonIDChars = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// newID returns a workflow-unique node id based on want.
func (b *builder) newID(want string) string {
	base := strings.Trim(nonIDChars.ReplaceAllString(want, "-"), "-")
	if base == "" {
		base = "node"
	}
	id := base
	for i := 2; b.ids[id]; i++ {
		id = fmt.Sprintf("%s-%d", base, i)
	}
	b.ids[id] = true
	return id
}

// place writes the mapped node(s) into the mono workflow.
func (b *builder) place(n *n8nNode, ctx *MapContext, m Mapping, nr *NodeReport) {
	if m.Unmapped {
		m = b.stub(n)
		nr.Status = StatusUnmapped
	}
	if m.Config == nil {
		m.Config = map[string]interface{}{}
	}
	b.applyNodeSettings(n, &m, nr)
	x, y := n.position()
	steps := append([]ChainStep{{Type: m.Type, Config: m.Config}}, m.Chain...)
	mn := &mappedNode{outputs: m.Outputs, tagInputs: m.TagInputs, n8n: n, errorOutput: ctx.OnErrorOutput}
	var prev string
	for i, s := range steps {
		name, idWant := n.Name, string(n.ID)
		if i < len(steps)-1 {
			// The last node of a chain carries the n8n name, so
			// expressions that reference the node read its output.
			suffix := s.Suffix
			if i == 0 {
				suffix = m.EntrySuffix
				if suffix == "" {
					suffix = " (" + strings.TrimPrefix(strings.TrimPrefix(m.Type, "core."), "trigger.") + ")"
				}
			}
			name += suffix
			idWant = name
		}
		if idWant == "" {
			idWant = name
		}
		id := b.newID(idWant)
		fn := workflow.WorkflowFileNode{ID: id, Type: s.Type, Name: name, Disabled: n.Disabled, Config: s.Config}
		fn.Position.X, fn.Position.Y = x+float64(i)*220-float64(len(steps)-1)*220, y
		b.out.Nodes = append(b.out.Nodes, fn)
		if i == 0 {
			mn.entryID = id
		} else {
			b.connect(prev, "main", id)
		}
		prev = id
	}
	mn.exitID = prev
	for i, alt := range m.Alternates {
		name := n.Name + alt.Suffix
		id := b.newID(name)
		fn := workflow.WorkflowFileNode{ID: id, Type: alt.Type, Name: name, Disabled: n.Disabled, Config: alt.Config}
		fn.Position.X, fn.Position.Y = x, y+float64(i+1)*140
		b.out.Nodes = append(b.out.Nodes, fn)
		mn.alternates = append(mn.alternates, id)
	}
	nr.MonoType = m.Type
	nr.MonoID = mn.exitID
	if len(m.Chain) > 0 {
		types := []string{m.Type}
		for _, s := range m.Chain {
			types = append(types, s.Type)
		}
		nr.MonoType = strings.Join(types, " → ")
	}
	b.mapped[n.Name] = mn
}

// stub is the placeholder for a node mono cannot run: a core.code node that
// fails with a clear message and keeps the n8n parameters for reference.
func (b *builder) stub(n *n8nNode) Mapping {
	msg := fmt.Sprintf("n8n node %q (%s) is not available in mono-agent yet: replace this node. Its n8n parameters are kept in this node's config (n8n_parameters).", n.Name, n.Type)
	return Mapping{Type: "core.code", Config: map[string]interface{}{
		"code":           escapeTemplateText("throw new Error(" + jsQuote(msg) + ");"),
		"n8n_type":       n.Type,
		"n8n_parameters": escapeDeep(n.Parameters),
	}}
}

// escapeDeep escapes "{{" in every string so mono stores the values as
// literal text.
func escapeDeep(v interface{}) interface{} {
	switch x := v.(type) {
	case string:
		return escapeTemplateText(x)
	case map[string]interface{}:
		out := make(map[string]interface{}, len(x))
		for k, val := range x {
			out[k] = escapeDeep(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(x))
		for i, val := range x {
			out[i] = escapeDeep(val)
		}
		return out
	}
	return v
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// description collects sticky notes and node notes.
func (b *builder) description() string {
	parts := []string{"Imported from n8n."}
	for _, n := range b.src.Nodes {
		if strings.TrimSpace(n.Notes) != "" {
			parts = append(parts, fmt.Sprintf("Note on %q: %s", n.Name, strings.TrimSpace(n.Notes)))
		}
	}
	parts = append(parts, b.notes...)
	return strings.Join(parts, "\n\n")
}

// noteBatchNodes reports app and database nodes: mono runs them once per
// batch, resolving their expressions from the first item, where n8n ran
// them once per item.
func (b *builder) noteBatchNodes() {
	var names []string
	for _, n := range b.out.Nodes {
		for _, p := range []string{"comm.", "service.", "db."} {
			if strings.HasPrefix(n.Type, p) {
				names = append(names, n.Name)
			}
		}
	}
	if len(names) > 0 {
		b.rep.note("%s run once per batch of items in mono-agent, with expressions resolved from the first item (n8n ran them once per item)", strings.Join(names, ", "))
	}
}
