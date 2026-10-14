// cmd/monoagentcli/template_test.go
package main

import (
	"strings"
	"testing"
)

// Message templates are `message-template`, so they do not read as workflow
// templates; `template` stays an alias (the GUI calls `template list`).
func TestMessageTemplateCommandKeepsTemplateAlias(t *testing.T) {
	root := newRootCmd()
	for _, name := range []string{"message-template", "template"} {
		sub, _, err := root.Find([]string{name, "list"})
		if err != nil || sub.Parent() == nil || sub.Parent().Name() != "message-template" {
			t.Fatalf("%s list: resolved to %v (%v)", name, sub, err)
		}
	}
	sub, _, _ := root.Find([]string{"message-template"})
	if !strings.Contains(sub.Short, "alias: template") {
		t.Errorf("Short = %q", sub.Short)
	}
}
