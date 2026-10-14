package workflow

// alwaysOutputData is n8n's "Always Output Data" node setting
// (always_output_data, or alwaysOutputData): a node whose run emits no item
// on any of its outputs emits one empty item on its first output instead, so
// the nodes after it still run. The error output does not count.
func alwaysOutputData(config map[string]interface{}, outputs []NodeOutput) []NodeOutput {
	on, _ := config["always_output_data"].(bool)
	if alt, ok := config["alwaysOutputData"].(bool); ok && alt {
		on = true
	}
	if !on {
		return outputs
	}
	first := -1
	for i, o := range outputs {
		if o.Handle == "error" {
			continue
		}
		if len(o.Items) > 0 {
			return outputs
		}
		if first < 0 {
			first = i
		}
	}
	empty := []Item{NewItem(map[string]interface{}{})}
	if first < 0 {
		return append(outputs, NodeOutput{Handle: "main", Items: empty})
	}
	out := append([]NodeOutput(nil), outputs...)
	out[first].Items = empty
	return out
}
