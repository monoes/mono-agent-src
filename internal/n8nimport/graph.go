package n8nimport

import (
	"fmt"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// edge is one n8n "main" connection between mapped nodes.
type edge struct {
	src    string
	out    int
	dst    string
	in     int
	viaOut string // set when a flattened loop re-routes the edge: src output already resolved
}

const splitInBatchesType = "n8n-nodes-base.splitInBatches"

// connect adds a mono connection.
func (b *builder) connect(src, handle, dst string) {
	for _, c := range b.out.Connections {
		if c.Source == src && c.SourceHandle == handle && c.Target == dst {
			return
		}
	}
	b.nextConn++
	b.out.Connections = append(b.out.Connections, workflow.WorkflowFileEdge{
		ID: fmt.Sprintf("c%d", b.nextConn), Source: src, SourceHandle: handle, Target: dst, TargetHandle: "main",
	})
}

// collectEdges reads the n8n "main" connections between mapped nodes.
func (b *builder) collectEdges() []edge {
	var edges []edge
	for _, srcName := range sortedKeys(b.src.Connections) {
		outs := b.src.Connections[srcName]["main"]
		for outIdx, ends := range outs {
			for _, end := range ends {
				if end == nil {
					continue
				}
				_, srcOK := b.mapped[srcName]
				_, dstOK := b.mapped[end.Node]
				if !srcOK || !dstOK {
					if b.byName[srcName] == nil || b.byName[end.Node] == nil {
						b.rep.note("connection %q → %q names a node that does not exist; dropped", srcName, end.Node)
					}
					continue
				}
				edges = append(edges, edge{src: srcName, out: outIdx, dst: end.Node, in: end.Index})
			}
		}
	}
	return edges
}

// buildEdges converts the connection graph: loops are flattened (mono
// workflows are acyclic), outputs become handles, tagged inputs get their
// tagging node.
func (b *builder) buildEdges() {
	edges := b.flattenLoops(b.collectEdges())
	tags := map[string]string{}
	for _, e := range edges {
		src, dst := b.mapped[e.src], b.mapped[e.dst]
		handle := e.viaOut
		if handle == "" {
			handle = b.outputHandle(e.src, src, e.out)
		}
		target := dst.entryID
		if dst.tagInputs {
			key := fmt.Sprintf("%s#%d", e.dst, e.in)
			tag, ok := tags[key]
			if !ok {
				tag = b.tagNode(dst, e.dst, e.in)
				tags[key] = tag
				b.connect(tag, "main", dst.entryID)
			}
			target = tag
		}
		b.connect(src.exitID, handle, target)
		for _, alt := range src.alternates {
			b.connect(alt, handle, target)
		}
	}
}

// outputHandle names the mono handle for n8n output index out.
func (b *builder) outputHandle(name string, m *mappedNode, out int) string {
	regular := len(m.outputs)
	if regular == 0 {
		regular = 1
	}
	if m.errorOutput && out == regular {
		return "error"
	}
	if out < len(m.outputs) {
		return m.outputs[out]
	}
	if out == 0 {
		return "main"
	}
	nr := b.nodeReport(name)
	nr.Issues = append(nr.Issues, Issue{Severity: SevWarning, Param: "outputs", Value: out,
		Message: "this output has no mono-agent equivalent; its connections now leave the main output"})
	return "main"
}

// tagNode inserts the core.set that marks items with their input index.
func (b *builder) tagNode(dst *mappedNode, name string, in int) string {
	tagName := fmt.Sprintf("%s (input %d)", name, in+1)
	id := b.newID(tagName)
	x, y := dst.n8n.position()
	fn := workflow.WorkflowFileNode{ID: id, Type: "core.set", Name: tagName, Config: map[string]interface{}{
		"assignments":   []interface{}{map[string]interface{}{"field": "__n8n_input", "value": fmt.Sprint(in), "type": "number"}},
		"include_input": true,
	}}
	fn.Position.X, fn.Position.Y = x-160, y+float64(in*120)-60
	b.out.Nodes = append(b.out.Nodes, fn)
	return id
}

// flattenLoops removes back-edges. A Loop Over Items (splitInBatches v3)
// back-edge is folded away: the loop body already runs over every item in
// one pass, so the "done" branch is re-attached to the end of the body.
func (b *builder) flattenLoops(edges []edge) []edge {
	adj := map[string][]int{}
	indeg := map[string]int{}
	for i, e := range edges {
		adj[e.src] = append(adj[e.src], i)
		indeg[e.dst]++
	}
	order := b.dfsRoots(indeg)
	state := map[string]int{} // 0 new, 1 on stack, 2 done
	back := map[int]bool{}
	var visit func(n string)
	visit = func(n string) {
		state[n] = 1
		for _, i := range adj[n] {
			d := edges[i].dst
			switch state[d] {
			case 0:
				visit(d)
			case 1:
				back[i] = true
			}
		}
		state[n] = 2
	}
	for _, n := range order {
		if state[n] == 0 {
			visit(n)
		}
	}
	if len(back) == 0 {
		return edges
	}
	var kept []edge
	for i, e := range edges {
		if !back[i] {
			kept = append(kept, e)
		}
	}
	for i := range edges {
		if back[i] {
			kept = b.foldBackEdge(edges[i], kept)
		}
	}
	return kept
}

// dfsRoots orders DFS start points: nodes without inputs (triggers first,
// then left to right), then everything else.
func (b *builder) dfsRoots(indeg map[string]int) []string {
	var names []string
	for name := range b.mapped {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		a, c := names[i], names[j]
		if (indeg[a] == 0) != (indeg[c] == 0) {
			return indeg[a] == 0
		}
		ta, tc := isTriggerType(b.byName[a].Type), isTriggerType(b.byName[c].Type)
		if ta != tc {
			return ta
		}
		xa, _ := b.byName[a].position()
		xc, _ := b.byName[c].position()
		if xa != xc {
			return xa < xc
		}
		return a < c
	})
	return names
}

func isTriggerType(t string) bool {
	s := strings.ToLower(shortType(t))
	return strings.HasSuffix(s, "trigger") || s == "webhook" || s == "cron" || s == "interval" || s == "start"
}

func (b *builder) foldBackEdge(e edge, edges []edge) []edge {
	b.rep.Counts.RemovedLoops++
	loop := b.byName[e.dst]
	nr := b.nodeReport(e.dst)
	if loop.Type != splitInBatchesType || loop.TypeVersion < 3 {
		nr.Issues = append(nr.Issues, Issue{Severity: SevWarning, Param: "connections",
			Value:   fmt.Sprintf("%s → %s", e.src, e.dst),
			Message: "mono-agent workflows cannot loop; this back-edge was removed, so the loop body runs once"})
		return edges
	}
	// The body ends at e.src; whatever followed the loop's "done" output
	// now follows the body.
	srcHandle := b.outputHandle(e.src, b.mapped[e.src], e.out)
	var out []edge
	moved := 0
	for _, x := range edges {
		if x.src == e.dst && x.out == 0 && x.viaOut == "" {
			x.src, x.out, x.viaOut = e.src, e.out, srcHandle
			moved++
		}
		out = append(out, x)
	}
	msg := fmt.Sprintf("loop flattened: mono-agent runs the loop body once over all items; the back-edge from %q was removed", e.src)
	if moved > 0 {
		msg += fmt.Sprintf(" and the \"done\" branch now continues from %q", e.src)
	}
	nr.Issues = append(nr.Issues, Issue{Severity: SevInfo, Message: msg})
	return out
}

// applyNodeSettings carries n8n's per-node settings over.
// Error handling and retries go on the node that produces the outputs
// (the last of a chain), where the error output leaves.
func (b *builder) applyNodeSettings(n *n8nNode, m *Mapping, nr *NodeReport) {
	cfg := m.Config
	if len(m.Chain) > 0 {
		last := &m.Chain[len(m.Chain)-1]
		if last.Config == nil {
			last.Config = map[string]interface{}{}
		}
		cfg = last.Config
	}
	add := func(sev Severity, param string, value interface{}, msg string) {
		nr.Issues = append(nr.Issues, Issue{Severity: sev, Param: param, Value: value, Message: msg})
	}
	switch {
	case n.OnError == "continueErrorOutput":
		if b.errorOutputWired(n, len(m.Outputs)) {
			cfg["on_error"] = "error_branch"
		} else {
			cfg["on_error"] = "skip"
		}
	case n.OnError == "continueRegularOutput" || n.ContinueOnFail:
		cfg["on_error"] = "continue"
		add(SevInfo, "onError", "continueRegularOutput", "on failure mono-agent passes the input items on (n8n passed an item with the error)")
	}
	if n.RetryOnFail {
		tries := n.MaxTries
		if tries == 0 {
			tries = 3
		}
		wait := n.WaitBetweenTries
		if wait == 0 {
			wait = 1000
		}
		retries := tries - 1
		if retries > 10 {
			retries = 10
		}
		delay := wait / 1000
		if delay < 1 {
			delay = 1
		}
		cfg["retry_policy"] = map[string]interface{}{"max_retries": retries, "backoff_type": "fixed", "initial_delay": delay}
	}
	if n.AlwaysOutputData {
		add(SevWarning, "alwaysOutputData", true, "has no mono-agent equivalent")
	}
	if n.ExecuteOnce {
		add(SevWarning, "executeOnce", true, "has no mono-agent equivalent; the node runs for every item")
	}
	if strings.TrimSpace(n.Notes) != "" {
		add(SevInfo, "notes", nil, "node notes are kept in the workflow description")
	}
	if n.Disabled {
		add(SevInfo, "disabled", true, "kept disabled; mono-agent skips a disabled node without passing its items on (n8n passed them through)")
	}
}

// errorOutputWired reports whether the n8n error output (the one after the
// regular outputs) has a connection.
func (b *builder) errorOutputWired(n *n8nNode, outputs int) bool {
	if outputs == 0 {
		outputs = 1
	}
	outs := b.src.Connections[n.Name]["main"]
	return len(outs) > outputs && len(outs[outputs]) > 0
}

// applySettings handles workflow-level settings, pinned and static data.
func (b *builder) applySettings() {
	if b.src.Active {
		b.rep.note("the n8n workflow was active; the import is inactive — connect its credentials, then run `monoagentcli workflow activate <id>`")
	}
	for _, k := range sortedKeys(b.src.Settings) {
		v := b.src.Settings[k]
		switch k {
		case "timezone":
			tz, _ := v.(string)
			if tz == "" || tz == "DEFAULT" {
				continue
			}
			applied := 0
			for i := range b.out.Nodes {
				if b.out.Nodes[i].Type == "trigger.schedule" {
					if _, set := b.out.Nodes[i].Config["timezone"]; !set {
						b.out.Nodes[i].Config["timezone"] = tz
						applied++
					}
				}
			}
			if applied > 0 {
				b.rep.note("workflow timezone %s applied to the schedule triggers; date expressions ($now) use UTC", tz)
			} else {
				b.rep.note("workflow timezone %s: date expressions ($now) use UTC in mono-agent", tz)
			}
		case "errorWorkflow":
			if s := fmt.Sprint(v); s != "" {
				b.rep.note("n8n error workflow %s is not carried over: import it, then set settings.error_workflow to its mono-agent id", s)
			}
		case "executionTimeout":
			if f, ok := v.(float64); ok && f > 0 {
				b.rep.note("execution timeout %vs is not carried over; pass `workflow run --timeout`", f)
			}
		case "executionOrder", "saveManualExecutions", "saveExecutionProgress", "saveDataErrorExecution",
			"saveDataSuccessExecution", "callerPolicy", "callerIds", "availableInMCP", "timeSavedPerExecution":
			// Execution bookkeeping with no effect on what the workflow does.
		default:
			b.rep.note("workflow setting %s=%s is not carried over", k, compactValue(v))
		}
	}
	if len(b.src.PinData) > 0 {
		b.rep.note("pinned test data for %s dropped: mono-agent has no pinned data", strings.Join(sortedKeys(b.src.PinData), ", "))
	}
	if sd, ok := b.src.StaticData.(map[string]interface{}); ok && len(sd) > 0 {
		b.rep.note("workflow static data (%s) dropped", strings.Join(sortedKeys(sd), ", "))
	}
}
