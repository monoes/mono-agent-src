package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	refcli "github.com/monoes/mono-agent/internal/cli/ref"
	"github.com/monoes/mono-agent/internal/workflow"
)

var updateRefGolden = flag.Bool("update-ref-golden", false, "rewrite testdata/ref_node/*.golden from the current output")

// TestRefNodeGolden pins the generated page for an API service node, a comm
// node and a browser automation node.
func TestRefNodeGolden(t *testing.T) {
	reg := refNodeRegistry()
	for _, typ := range []string{"service.github", "comm.slack", "instagram.like_posts"} {
		t.Run(typ, func(t *testing.T) {
			if !reg.Has(typ) {
				t.Skipf("%s is not registered in this build", typ)
			}
			page, err := refDeps().BuildNodeRefPage(reg, typ)
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			refcli.WriteNodeRefPage(&buf, page)
			golden := filepath.Join("testdata", "ref_node", typ+".golden")
			if *updateRefGolden {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden (run with -update-ref-golden to create): %v", err)
			}
			if got := buf.String(); got != string(want) {
				t.Errorf("ref node %s differs from %s (rerun with -update-ref-golden if intended)\n--- got ---\n%s", typ, golden, got)
			}
		})
	}
}

// The generated reference must list every operation and, under each, the
// fields whose depends_on names it.
func TestRefNodeOperationsFromSchema(t *testing.T) {
	page, err := refDeps().BuildNodeRefPage(refNodeRegistry(), "service.github")
	if err != nil {
		t.Fatal(err)
	}
	if page.CredentialPlatform != "github" || page.OperationKey != "operation" {
		t.Fatalf("credential/operation key = %q/%q", page.CredentialPlatform, page.OperationKey)
	}
	if len(page.Operations) != 9 {
		t.Fatalf("operations = %d, want 9", len(page.Operations))
	}
	fieldsOf := map[string][]string{}
	for _, op := range page.Operations {
		for _, f := range op.Fields {
			fieldsOf[op.Name] = append(fieldsOf[op.Name], f.Key)
		}
	}
	if got := strings.Join(fieldsOf["create_pr"], ","); got != "owner,repo,title,body,head,base" {
		t.Errorf("create_pr fields = %s", got)
	}
	if len(fieldsOf["list_repos"]) != 0 {
		t.Errorf("list_repos fields = %v, want none", fieldsOf["list_repos"])
	}
}

// Every type the manual covers renders as text and as JSON, so `ref node`
// never answers "unknown" for a registered type.
func TestRefNodeRendersEveryRegisteredType(t *testing.T) {
	reg := refNodeRegistry()
	types := refDeps().NodeTypes(reg)
	for _, typ := range reg.Types() {
		if !contains(types, typ) {
			t.Errorf("%s is registered but not covered by ref", typ)
		}
	}
	for _, typ := range types {
		page, err := refDeps().BuildNodeRefPage(reg, typ)
		if err != nil {
			t.Errorf("%s: %v", typ, err)
			continue
		}
		var buf bytes.Buffer
		refcli.WriteNodeRefPage(&buf, page)
		if !strings.Contains(buf.String(), "NODE: "+typ) || !strings.Contains(buf.String(), "Credential platform:") {
			t.Errorf("%s: page is missing its header or schema section:\n%s", typ, buf.String())
		}
		if _, err := json.Marshal(page); err != nil {
			t.Errorf("%s: json: %v", typ, err)
		}
	}
}

func TestRefNodeCommandJSONAndAlias(t *testing.T) {
	cmd := newRefCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"node", "slack", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var page refcli.NodeRefPage
	if err := json.Unmarshal(buf.Bytes(), &page); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if page.Type != "comm.slack" || !page.HandWritten || len(page.Operations) != 4 {
		t.Errorf("page = %+v", page)
	}

	cmd = newRefCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"node", "no.such_type"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unknown node type") {
		t.Errorf("unknown type error = %v", err)
	}
}

// A registered type with no hand-written entry still gets a page, marked
// as schema-only.
func TestRefNodeWithoutHandProse(t *testing.T) {
	reg := refNodeRegistry()
	saved := nodeDocs
	t.Cleanup(func() { nodeDocs = saved })
	var kept []nodeDoc
	for _, d := range nodeDocs {
		if d.Type != "service.github" {
			kept = append(kept, d)
		}
	}
	nodeDocs = kept

	page, err := refDeps().BuildNodeRefPage(reg, "service.github")
	if err != nil {
		t.Fatal(err)
	}
	if page.HandWritten || len(page.Operations) != 9 {
		t.Errorf("page = %+v", page)
	}
	var buf bytes.Buffer
	refcli.WriteNodeRefPage(&buf, page)
	if !strings.Contains(buf.String(), "No hand-written guide") {
		t.Errorf("page does not say it is schema-only:\n%s", buf.String())
	}
	buf.Reset()
	refDeps().WriteRefNodes(&buf, reg, false)
	if !strings.Contains(buf.String(), "service.github") || !strings.Contains(buf.String(), "[schema only]") {
		t.Errorf("ref nodes does not list service.github as schema-only")
	}
}

func TestRefNodesHidesDeprecatedByDefault(t *testing.T) {
	reg := refNodeRegistry()
	var def, all bytes.Buffer
	refDeps().WriteRefNodes(&def, reg, false)
	refDeps().WriteRefNodes(&all, reg, true)
	for typ := range workflow.DeprecatedNodeTypes {
		if !reg.Has(typ) {
			continue // still registered is the point; skip if a build drops it
		}
		if strings.Contains(def.String(), "  "+typ+" ") {
			t.Errorf("ref nodes lists deprecated %s by default", typ)
		}
		if !strings.Contains(all.String(), "  "+typ+" ") {
			t.Errorf("ref nodes --all does not list %s", typ)
		}
	}
	if !strings.Contains(def.String(), "deprecated node types hidden") {
		t.Error("ref nodes does not say deprecated types are hidden")
	}
	// Still documented on request.
	if _, err := refDeps().BuildNodeRefPage(reg, "ai.chat"); err != nil {
		t.Errorf("ref node ai.chat: %v", err)
	}
}

func TestNodePaletteHidesDeprecatedByDefault(t *testing.T) {
	has := func(p map[string][]paletteNode, typ string) bool {
		for _, list := range p {
			for _, n := range list {
				if n.Type == typ {
					return true
				}
			}
		}
		return false
	}
	def, all := nodePalette(nil, false), nodePalette(nil, true)
	for typ := range workflow.DeprecatedNodeTypes {
		if has(def, typ) {
			t.Errorf("palette offers deprecated %s by default", typ)
		}
		if !has(all, typ) {
			t.Errorf("palette --all is missing %s", typ)
		}
	}
	if !has(def, "agent.ask") {
		t.Error("palette lost agent.ask")
	}
}
