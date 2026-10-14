package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNodePaletteGroupsRunnableTypes(t *testing.T) {
	dbPath := newApplicationCLITestDB(t)
	var runErr error
	var buf strings.Builder
	cmd := newNodeCmd(&globalConfig{DBPath: dbPath, JSONOutput: true})
	cmd.SetArgs([]string{"palette"})
	cmd.SetOut(&buf)
	runErr = cmd.Execute()
	if runErr != nil {
		t.Fatal(runErr)
	}
	var got map[string][]paletteNode
	if err := json.Unmarshal([]byte(buf.String()), &got); err != nil {
		t.Fatalf("palette is not JSON: %v", err)
	}
	if len(got["triggers"]) != 5 {
		t.Errorf("triggers = %+v", got["triggers"])
	}
	var ifNode *paletteNode
	for i, n := range got["control"] {
		if n.Type == "core.if" {
			ifNode = &got["control"][i]
		}
	}
	if ifNode == nil || ifNode.Label != "If" || ifNode.Category != "control" || ifNode.Schema == nil {
		t.Fatalf("core.if = %+v", ifNode)
	}
	reg := buildNodeRegistry(false, nil)
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
