package noderegistry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/apipkg"
	"github.com/monoes/mono-agent/internal/n8nimport"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The workflows the repo ships (examples/, and the n8n workflows the
// importer is tested on) use only keys their API package declares: a key
// it doesn't would be refused when they run (apipkg.CheckConfig).
func TestShippedWorkflowsDeclareTheirKeys(t *testing.T) {
	Build(nil)
	type source struct {
		name string
		wf   *workflow.WorkflowFile
	}
	var sources []source
	if err := filepath.Walk("../../examples", func(p string, info os.FileInfo, err error) error {
		if err != nil || !strings.HasSuffix(p, ".json") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var wf workflow.WorkflowFile
		if json.Unmarshal(b, &wf) == nil && len(wf.Nodes) > 0 {
			sources = append(sources, source{p, &wf})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
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
		sources = append(sources, source{p, wf})
	}
	served := 0
	for _, s := range sources {
		for _, n := range s.wf.Nodes {
			if _, ok := apipkg.ForNodeType(n.Type); ok || apipkg.SendsSome(n.Type) {
				served++
			}
			if err := apipkg.CheckConfig(n.Type, n.Config); err != nil {
				t.Errorf("%s, node %q: %v", s.name, n.Name, err)
			}
		}
	}
	if len(sources) < 30 || served == 0 {
		t.Errorf("walked %d workflows and %d nodes an API package serves: the walk lost its sources?", len(sources), served)
	}
}
