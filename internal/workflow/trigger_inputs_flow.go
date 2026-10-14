package workflow

import "strings"

// TriggerInputsWorkflow is TriggerInputs for a whole workflow: a $json field a
// node reads counts as a trigger input only when the trigger's item can reach
// that node carrying it — through nodes that pass their input through ("*" in
// their schema outputs, or no declared outputs: unknown counts as passing) and
// do not produce the field themselves. A field a Code node or an HTTP request
// produces upstream is not something the caller has to send. Without
// connections it is TriggerInputs.
func TriggerInputsWorkflow(wf *Workflow, lookup SchemaLookup) []TriggerField {
	if wf == nil {
		return nil
	}
	fields := TriggerInputs(wf.Nodes, lookup)
	if len(wf.Connections) == 0 || len(fields) == 0 {
		return fields
	}
	f := newTriggerFlow(wf)
	out := make([]TriggerField, 0, len(fields))
	for _, field := range fields {
		var readers []string
		for _, name := range field.Nodes {
			if id, ok := f.idByName[name]; ok && f.fromTrigger(id, field.Name) {
				readers = append(readers, name)
			}
		}
		if len(readers) == 0 {
			continue
		}
		field.Nodes = readers
		out = append(out, field)
	}
	return out
}

type triggerFlow struct {
	byID     map[string]WorkflowNode
	idByName map[string]string
	preds    map[string][]string
	memo     map[string]bool
	busy     map[string]bool
}

func newTriggerFlow(wf *Workflow) *triggerFlow {
	f := &triggerFlow{byID: map[string]WorkflowNode{}, idByName: map[string]string{},
		preds: map[string][]string{}, memo: map[string]bool{}, busy: map[string]bool{}}
	for _, n := range wf.Nodes {
		f.byID[n.ID] = n
		if n.Name != "" {
			f.idByName[n.Name] = n.ID
		}
	}
	for _, c := range wf.Connections {
		if c.SourceHandle == "error" {
			continue
		}
		f.preds[c.TargetNodeID] = append(f.preds[c.TargetNodeID], c.SourceNodeID)
	}
	return f
}

// fromTrigger reports whether field, read by node id, can come from a
// trigger's item.
func (f *triggerFlow) fromTrigger(id, field string) bool {
	key := id + "\x00" + field
	if v, ok := f.memo[key]; ok {
		return v
	}
	if f.busy[key] {
		return false // a loop back-edge adds no new source
	}
	f.busy[key] = true
	defer delete(f.busy, key)
	got := false
	for _, p := range f.preds[id] {
		if f.carries(p, field) {
			got = true
			break
		}
	}
	f.memo[key] = got
	return got
}

// carries reports whether node id's output can hold field as the trigger
// sent it.
func (f *triggerFlow) carries(id, field string) bool {
	n, ok := f.byID[id]
	if !ok {
		return false
	}
	if strings.HasPrefix(n.Type, "trigger.") {
		return true
	}
	outs := schemaOutputs(n.Type)
	if len(outs) == 0 {
		return f.fromTrigger(id, field) // unknown: assume it passes items on
	}
	passes := false
	for _, o := range outs {
		if o.Key == "*" {
			passes = true
		} else if o.Key == field {
			return false // the node sets the field itself
		}
	}
	return passes && f.fromTrigger(id, field)
}
