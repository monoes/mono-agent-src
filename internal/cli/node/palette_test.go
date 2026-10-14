package node

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/noderegistry"
	"github.com/monoes/mono-agent/internal/workflow"
)

func TestNodePaletteGroupsRunnableTypes(t *testing.T) {
	t.Parallel()
	var buf strings.Builder
	cmd := NewCommand(testDeps(filepath.Join(t.TempDir(), "m.db"), "default", true))
	cmd.SetArgs([]string{"palette"})
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var got map[string][]PaletteNode
	if err := json.Unmarshal([]byte(buf.String()), &got); err != nil {
		t.Fatalf("palette is not JSON: %v", err)
	}
	if len(got["triggers"]) != 8 {
		t.Errorf("triggers = %+v", got["triggers"])
	}
	var ifNode *PaletteNode
	for i, n := range got["control"] {
		if n.Type == "core.if" {
			ifNode = &got["control"][i]
		}
	}
	if ifNode == nil || ifNode.Label != "If" || ifNode.Category != "control" || ifNode.Schema == nil {
		t.Fatalf("core.if = %+v", ifNode)
	}
	reg := noderegistry.Build(nil)
	aliases := reg.Aliases()
	for group, list := range got {
		for _, n := range list {
			if group != "triggers" && !reg.Has(n.Type) {
				t.Errorf("palette offers %s, which is not registered", n.Type)
			}
			if canonical, ok := aliases[n.Type]; ok {
				t.Errorf("palette offers legacy alias %s; it should offer %s", n.Type, canonical)
			}
		}
	}
}

func TestNodePaletteHidesDeprecatedByDefault(t *testing.T) {
	t.Parallel()
	has := func(p map[string][]PaletteNode, typ string) bool {
		for _, list := range p {
			for _, n := range list {
				if n.Type == typ {
					return true
				}
			}
		}
		return false
	}
	def, all := Palette(nil, false), Palette(nil, true)
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

// Names and acronyms keep their owners' spelling, curated or derived.
func TestNodePaletteBrandLabels(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		"http.graphql": "GraphQL", "service.youtube": "YouTube", "service.github": "GitHub",
		"service.hubspot": "HubSpot", "data.jwt": "JWT", "db.ldap": "LDAP",
	}
	got := map[string]PaletteNode{}
	for _, list := range Palette(nil, false) {
		for _, n := range list {
			got[n.Type] = n
		}
	}
	for typ, label := range want {
		n, ok := got[typ]
		if !ok {
			t.Errorf("%s missing from the palette", typ)
			continue
		}
		if n.Label != label {
			t.Errorf("%s label = %q, want %q", typ, n.Label, label)
		}
	}
	if d := got["http.graphql"].Description; d == "" {
		t.Error("http.graphql has no description")
	}
}
