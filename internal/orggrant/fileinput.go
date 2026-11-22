package orggrant

import (
	"fmt"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/workflow"
)

// File access kinds a FileInputNode reports.
const (
	AccessRead      = "read"
	AccessWrite     = "write"
	AccessReadWrite = "read_write"
	AccessExec      = "exec"
)

// FileInputNode is a node of a workflow whose file path can come from the
// run's input (caveat C-46): a template expression in a path field, or, for
// image nodes, the path an incoming item carries. Confined is true when the
// node confines its paths to org.workdir in runs a role's grant starts, and
// false for nodes that cannot be confined (shell commands).
type FileInputNode struct {
	Node     string `json:"node"`
	Type     string `json:"type"`
	Field    string `json:"field"`
	Access   string `json:"access"`
	Confined bool   `json:"confined"`
}

// fileField is one path-bearing config key of a node type.
type fileField struct {
	key    string
	access string // "" = decided by accessFor
}

// fileNodeTypes lists every node type that touches local files by a path
// from config, with its path keys. Keep in step with the nodes that call
// fsconfine.Path (internal/nodes/...).
var fileNodeTypes = map[string][]fileField{
	"data.write_binary_file": {{"file_path", AccessWrite}},
	"data.spreadsheet":       {{"file_path", ""}},
	"data.read_write_file":   {{"file_selector", ""}, {"file_name", ""}},
	"vector.load_document":   {{"file_path", AccessRead}},
	"comm.email_send":        {{"attachments", AccessRead}},
	"comm.outlook_send":      {{"attachments", AccessRead}},
	"service.outlook_mail":   {{"attachments", AccessRead}},
	"comm.slack":             {{"file_path", AccessRead}},
	"comm.telegram":          {{"photo_url", AccessRead}},
	"http.ftp":               {{"local_path", ""}},
	"service.youtube":        {{"video_file_path", AccessRead}},
	"service.google_drive":   {{"file_path", AccessRead}},
	"system.execute_command": {{"command", AccessExec}, {"args", AccessExec}, {"working_dir", AccessExec}},
}

// imageNodeTypes read the image path from the incoming item (config
// "field", falling back to image_path) — the caller's data by construction.
var imageNodeTypes = map[string]string{
	"image.info": AccessRead, "image.vault_save": AccessRead,
	"image.edit":   AccessReadWrite,
	"image.resize": AccessReadWrite, "image.crop": AccessReadWrite, "image.thumbnail": AccessReadWrite,
	"image.convert": AccessReadWrite, "image.adjust": AccessReadWrite, "image.remove_background": AccessReadWrite,
}

// FileInputNodes lists the enabled nodes of wf whose file paths can come
// from the run's input. Like OutboundNodes it over-reports: any template in
// a path field counts, since in a granted run every item descends from the
// role's arguments. A false positive costs one warning.
func FileInputNodes(wf *workflow.Workflow) []FileInputNode {
	if wf == nil {
		return nil
	}
	var out []FileInputNode
	for _, n := range wf.Nodes {
		if n.Disabled {
			continue
		}
		if f, ok := fileInputOf(n); ok {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out
}

func fileInputOf(n workflow.WorkflowNode) (FileInputNode, bool) {
	label := n.Type
	if n.Name != "" {
		label = n.Name + " (" + n.Type + ")"
	}
	f := FileInputNode{Node: label, Type: n.Type, Confined: n.Type != "system.execute_command"}
	if access, ok := imageNodeTypes[n.Type]; ok {
		field, _ := n.Config["field"].(string)
		if field == "" {
			field = "image_path"
		}
		f.Field, f.Access = "item:"+field, access
		return f, true
	}
	for _, ff := range fileNodeTypes[n.Type] {
		if !templated(n.Config[ff.key]) {
			continue
		}
		f.Field, f.Access = ff.key, ff.access
		if f.Access == "" {
			f.Access = accessFor(n)
		}
		return f, true
	}
	return FileInputNode{}, false
}

// accessFor decides read or write from the node's operation.
func accessFor(n workflow.WorkflowNode) string {
	op, _ := n.Config["operation"].(string)
	switch {
	case strings.HasPrefix(op, "read"), op == "upload":
		return AccessRead
	case strings.HasPrefix(op, "write"), op == "download":
		return AccessWrite
	}
	return AccessReadWrite
}

// templated reports whether v (a string or a list of them) holds a
// template expression.
func templated(v interface{}) bool {
	switch x := v.(type) {
	case string:
		return strings.Contains(x, "{{")
	case []interface{}:
		for _, e := range x {
			if templated(e) {
				return true
			}
		}
	case []string:
		for _, e := range x {
			if templated(e) {
				return true
			}
		}
	}
	return false
}

// FileInputWarning is the one-line warning the CLI prints for nodes, or ""
// for none.
func FileInputWarning(nodes []FileInputNode) string {
	if len(nodes) == 0 {
		return ""
	}
	var open []string
	for _, n := range nodes {
		if !n.Confined {
			open = append(open, n.Node)
		}
	}
	msg := fmt.Sprintf("the workflow reads or writes files at paths taken from its input (%d node(s)); in runs a role starts, those nodes are confined to the role's workdir", len(nodes))
	if len(open) > 0 {
		msg += fmt.Sprintf(", except %s, which cannot be confined and can reach any file you can", strings.Join(open, ", "))
	}
	return msg
}
