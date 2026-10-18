package noderegistry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/apipkg"
	"github.com/monoes/mono-agent/internal/n8nimport"
)

// shippedRoots are where the repo keeps workflows it ships: what `make
// library-official` publishes to monoes.me/library (the bundled workflow
// templates, the starter orgs, the automation packages), data/ and
// examples/. Community library items live in monoes.me's database, out of
// reach here.
var shippedRoots = []string{"../workflow/templates", "../../orgtemplates", "../../automations", "../../data", "../../examples"}

// shippedNode is one node of a shipped workflow: its type and config.
type shippedNode struct {
	where, name, nodeType string
	config                map[string]interface{}
}

// nodesIn finds every workflow in v, however deep (a template, an org's
// embedded workflow): an object whose "nodes" lists typed nodes.
func nodesIn(where string, v interface{}) []shippedNode {
	var out []shippedNode
	switch x := v.(type) {
	case map[string]interface{}:
		if list, ok := x["nodes"].([]interface{}); ok {
			for _, e := range list {
				n, _ := e.(map[string]interface{})
				t, _ := n["type"].(string)
				if t == "" {
					continue
				}
				cfg, _ := n["config"].(map[string]interface{})
				name, _ := n["name"].(string)
				out = append(out, shippedNode{where, name, t, cfg})
			}
		}
		for _, e := range x {
			out = append(out, nodesIn(where, e)...)
		}
	case []interface{}:
		for _, e := range x {
			out = append(out, nodesIn(where, e)...)
		}
	}
	return out
}

// The workflows the repo ships, and the n8n workflows the importer is
// tested on, use only keys their API package declares: a key it doesn't
// would hold an agent's run of them for approval for nothing
// (apipkg.Undeclared).
func TestShippedWorkflowsDeclareTheirKeys(t *testing.T) {
	Build(nil)
	var nodes []shippedNode
	files := 0
	for _, root := range shippedRoots {
		if err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(p, ".json") {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			var v interface{}
			if json.Unmarshal(b, &v) != nil {
				return nil // not JSON this test reads
			}
			files++
			nodes = append(nodes, nodesIn(p, v)...)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	n8n, err := filepath.Glob("../n8nimport/testdata/n8n/*.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range n8n {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		wf, _, err := n8nimport.Convert(b)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		for _, n := range wf.Nodes {
			nodes = append(nodes, shippedNode{p, n.Name, n.Type, n.Config})
		}
	}
	served := 0
	for _, n := range nodes {
		if _, ok := apipkg.ForNodeType(n.nodeType); ok || apipkg.SendsSome(n.nodeType) {
			served++
		}
		if keys := apipkg.Undeclared(n.nodeType, n.config); len(keys) > 0 {
			t.Errorf("%s, node %q (%s): undeclared %v would hold an agent's run of it", n.where, n.name, n.nodeType, keys)
		}
	}
	if len(nodes) < 200 || served == 0 {
		t.Errorf("walked %d files and %d n8n workflows: %d nodes, %d an API package serves; the walk lost its sources?",
			files, len(n8n), len(nodes), served)
	}
	t.Logf("walked %d files and %d n8n workflows: %d nodes, %d an API package serves", files, len(n8n), len(nodes), served)
}
