package ref

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// fakeDeps documents a registry of two types (comm.slack, with the legacy alias slack, and
// service.github) with a catalogue of comm.slack and the unregistered trigger.manual.
func fakeDeps() Deps {
	reg := workflow.NewNodeTypeRegistry()
	reg.Register("comm.slack", nil)
	reg.Register("service.github", nil)
	reg.Alias("slack", "comm.slack")
	docs := []NodeDoc{
		{Type: "trigger.manual", Category: "trigger", Short: "Start by hand"},
		{Type: "comm.slack", Category: "comm", Short: "Slack, by hand", Notes: "a note"},
	}
	return Deps{
		Registry: func() *workflow.NodeTypeRegistry { return reg },
		NodeDocs: func() []NodeDoc { return docs },
		FindNodeDoc: func(typ string) *NodeDoc {
			for i := range docs {
				if strings.EqualFold(docs[i].Type, typ) {
					return &docs[i]
				}
			}
			return nil
		},
		CommandDocs: func() []CmdDoc { return nil },
	}
}

func TestNodeTypesAreTheRegistryPlusDocumentedTriggers(t *testing.T) {
	t.Parallel()
	d := fakeDeps()
	if got := strings.Join(d.NodeTypes(d.Registry()), ","); got != "comm.slack,service.github,trigger.manual" {
		t.Errorf("NodeTypes = %s", got)
	}
}

func TestBuildNodeRefPageLayersTheCatalogueOverTheSchema(t *testing.T) {
	t.Parallel()
	d := fakeDeps()
	page, err := d.BuildNodeRefPage(d.Registry(), "slack")
	if err != nil {
		t.Fatal(err)
	}
	if page.Type != "comm.slack" || !page.HandWritten || page.Short != "Slack, by hand" || page.Category != "comm" || !page.Registered {
		t.Errorf("slack page = %+v", page)
	}
	page, err = d.BuildNodeRefPage(d.Registry(), "service.github")
	if err != nil {
		t.Fatal(err)
	}
	if page.HandWritten || page.Category != "service" || len(page.Operations) == 0 {
		t.Errorf("schema-only page = %+v", page)
	}
	var buf bytes.Buffer
	WriteNodeRefPage(&buf, page)
	if !strings.Contains(buf.String(), "NODE: service.github") || !strings.Contains(buf.String(), "No hand-written guide") {
		t.Errorf("schema-only text:\n%s", buf.String())
	}
	if _, err := d.BuildNodeRefPage(d.Registry(), "no.such_type"); err == nil || !strings.Contains(err.Error(), "unknown node type") {
		t.Errorf("unknown type: %v", err)
	}
}

func TestWriteRefNodesKeepsCategoryAndCatalogueOrder(t *testing.T) {
	t.Parallel()
	d := fakeDeps()
	var buf bytes.Buffer
	d.WriteRefNodes(&buf, d.Registry(), false)
	out := buf.String()
	trigger, comm, service := strings.Index(out, "TRIGGER"), strings.Index(out, "COMM"), strings.Index(out, "SERVICE")
	if trigger < 0 || !(trigger < comm && comm < service) {
		t.Errorf("categories out of order:\n%s", out)
	}
	if !strings.Contains(out, "Slack, by hand") || !strings.Contains(out, "service.github") || !strings.Contains(out, "[schema only]") {
		t.Errorf("ref nodes:\n%s", out)
	}
}

func TestRefNodeCommandPrintsJSON(t *testing.T) {
	t.Parallel()
	cmd := NewCommand(fakeDeps())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"node", "slack", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var page NodeRefPage
	if err := json.Unmarshal(buf.Bytes(), &page); err != nil || page.Type != "comm.slack" || page.Notes != "a note" {
		t.Errorf("page = %+v (%v)\n%s", page, err, buf.String())
	}
}
