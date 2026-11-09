package orggrant_test

import (
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/automations"
	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/bot"
	"github.com/monoes/mono-agent/internal/noderegistry"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/workflow"
)

// registry builds the node registry the CLI uses, with the official
// automation packages from the repo standing in for an installed home.
func registry(t *testing.T) *workflow.NodeTypeRegistry {
	t.Helper()
	testhome.SetHome(t, t.TempDir())
	action.SetDefSource(nil)
	action.SetTestFallback(automations.FS())
	t.Cleanup(func() { action.SetTestFallback(nil) })
	return noderegistry.Build(nil)
}

// TestEveryRegisteredNodeTypeIsClassified walks every node type this build
// registers (run it with and without -tags nosocial). A type that is
// neither on the read-only list nor matched by an explicit outbound rule
// fails: isOutboundNode would already treat it as outbound, but whoever
// added it has to decide that on purpose.
func TestEveryRegisteredNodeTypeIsClassified(t *testing.T) {
	r := registry(t)
	types := r.Types()
	browser := 0
	for _, nt := range types {
		if strings.HasPrefix(nt, "linkedin.") || strings.HasPrefix(nt, "gemini.") {
			browser++
		}
		if !orggrant.Classified(nt) {
			t.Errorf("node type %q is not classified for org grants: add it to readOnlyNodes "+
				"(only if it can't act on the outside world) or to outboundNodes in internal/orggrant/outbound.go", nt)
		}
	}
	if len(types) < 100 || browser == 0 {
		t.Fatalf("registry looks incomplete: %d types, %d browser automation types", len(types), browser)
	}
	// A legacy name must classify like the type it resolves to.
	for from, to := range r.Aliases() {
		n := workflow.WorkflowNode{Type: to}
		if orggrant.IsOutboundNode(workflow.WorkflowNode{Type: from}) != orggrant.IsOutboundNode(n) {
			t.Errorf("legacy node type %q classifies differently from %q", from, to)
		}
	}
	t.Logf("%d registered node types classified", len(types))
}

// bootOfficial installs the official automation packages from the repo
// into a temp home and points the action loader at them, as the app does
// after installing them from monoes.me. It returns the home.
func bootOfficial(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), ".monoagent")
	prev := automation.TestSeed
	automation.TestSeed = automations.FS()
	t.Cleanup(func() {
		automation.TestSeed = prev
		action.SetDefSource(nil)
		action.GetLoader().InvalidateAll()
	})
	reg, err := automation.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Boot(); err != nil {
		t.Fatal(err)
	}
	action.SetDefSource(reg.DefSource())
	action.GetLoader().InvalidateAll()
	return home
}

// TestReadOnlyActionsVerifiedAgainstInstalledPackages: every official read
// action on the list passes the installed-definition check when its
// package is installed and available in this build, and is outbound when
// it isn't. A repo change that adds a write step, a new bot method or a
// write-level sideEffects to one of them fails here.
func TestReadOnlyActionsVerifiedAgainstInstalledPackages(t *testing.T) {
	bootOfficial(t)
	src := action.CurrentDefSource()
	verified := 0
	for nt := range orggrant.ReadOnlyActions() {
		pkg, _, _ := strings.Cut(nt, ".")
		available := src.Package(pkg) != nil
		if got := orggrant.IsOutboundNode(workflow.WorkflowNode{Type: nt}); got == available {
			t.Errorf("%s: outbound = %v with its package available = %v", nt, got, available)
		}
		if available {
			verified++
		}
	}
	if verified == 0 && bot.PlatformCompiledIn("linkedin") {
		t.Fatal("no read action verified: are the official packages installed?")
	}
	t.Logf("%d/%d read actions verified", verified, len(orggrant.ReadOnlyActions()))
}

// TestEditedInstalledActionIsOutbound: the installed definition decides,
// not the name. Hand edits under ~/.monoagent (or a package update) that
// make a read action write turn it outbound; so does having no registry.
func TestEditedInstalledActionIsOutbound(t *testing.T) {
	const nodeType = "hackernews.list_comments"
	isOut := func() bool {
		action.GetLoader().InvalidateAll()
		return orggrant.IsOutboundNode(workflow.WorkflowNode{Type: nodeType})
	}
	edit := func(t *testing.T, home, rel string, change func(map[string]interface{})) {
		t.Helper()
		files, _ := filepath.Glob(filepath.Join(home, "automations", "hackernews", "*", filepath.FromSlash(rel)))
		if len(files) != 1 {
			t.Fatalf("installed %s: %v", rel, files)
		}
		raw, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatal(err)
		}
		var def map[string]interface{}
		if err := json.Unmarshal(raw, &def); err != nil {
			t.Fatal(err)
		}
		change(def)
		out, _ := json.Marshal(def)
		if err := os.WriteFile(files[0], out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	addStep := func(step map[string]interface{}) func(map[string]interface{}) {
		return func(def map[string]interface{}) {
			def["steps"] = append(def["steps"].([]interface{}), step)
		}
	}
	for _, c := range []struct {
		name   string
		file   string
		change func(map[string]interface{})
	}{
		{"a step marked sideEffect", "actions/list_comments.json",
			addStep(map[string]interface{}{"id": "reply", "type": "extract_text", "sideEffect": true})},
		{"a click", "actions/list_comments.json",
			addStep(map[string]interface{}{"id": "upvote", "type": "click", "selector": ".votearrow"})},
		{"an unlisted bot method", "actions/list_comments.json",
			addStep(map[string]interface{}{"id": "dm", "type": "call_bot_method", "methodName": "send_message"})},
		{"a call_action", "actions/list_comments.json",
			addStep(map[string]interface{}{"id": "post", "type": "call_action", "action": "submit_post"})},
		{"a write-level sideEffects", "actions/list_comments.json",
			func(def map[string]interface{}) { def["sideEffects"] = "write" }},
		{"no sideEffects", "actions/list_comments.json",
			func(def map[string]interface{}) { delete(def, "sideEffects") }},
		{"a fragment that writes", "fragments/check_item_id.json",
			addStep(map[string]interface{}{"id": "w", "type": "type", "selector": "textarea", "value": "hi"})},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := bootOfficial(t)
			if action.CurrentDefSource().Package("hackernews") == nil {
				t.Skip("hackernews is not available in this build")
			}
			if isOut() {
				t.Fatalf("%s is outbound before the edit", nodeType)
			}
			edit(t, home, c.file, c.change)
			if !isOut() {
				t.Fatalf("%s is not outbound after adding %s", nodeType, c.name)
			}
		})
	}

	t.Run("no registry", func(t *testing.T) {
		action.SetDefSource(nil)
		action.SetTestFallback(automations.FS())
		t.Cleanup(func() { action.SetTestFallback(nil) })
		if !isOut() {
			t.Fatalf("%s is not outbound without an automation registry to verify it", nodeType)
		}
	})
}

func TestOutboundClassification(t *testing.T) {
	cfg := func(k, v string) map[string]interface{} { return map[string]interface{}{k: v} }
	cases := []struct {
		node     workflow.WorkflowNode
		outbound bool
	}{
		{workflow.WorkflowNode{Type: "linkedin.send_dms"}, true},
		{workflow.WorkflowNode{Type: "linkedin.publish_post"}, true},
		{workflow.WorkflowNode{Type: "tiktok.follow_user"}, true},
		{workflow.WorkflowNode{Type: "x.send_dms"}, true},
		{workflow.WorkflowNode{Type: "hackernews.submit_post"}, true},
		{workflow.WorkflowNode{Type: "producthunt.comment_on_launch"}, true},
		{workflow.WorkflowNode{Type: "instagram.watch_stories"}, true},
		{workflow.WorkflowNode{Type: "gemini.generate_text"}, true},
		{workflow.WorkflowNode{Type: "browser.jev"}, true},
		{workflow.WorkflowNode{Type: "agent.ask"}, true},
		{workflow.WorkflowNode{Type: "ai.agent"}, true},
		{workflow.WorkflowNode{Type: "system.execute_command"}, true},
		{workflow.WorkflowNode{Type: "execute_command"}, true},
		{workflow.WorkflowNode{Type: "slack"}, true},
		{workflow.WorkflowNode{Type: "local-acme.do_anything"}, true},
		{workflow.WorkflowNode{Type: "somefuture.node"}, true},
		{workflow.WorkflowNode{Type: ""}, true},
		{workflow.WorkflowNode{Type: "http.request"}, false},
		{workflow.WorkflowNode{Type: "http.request", Config: cfg("method", "get")}, false},
		{workflow.WorkflowNode{Type: "http.request", Config: cfg("method", "HEAD")}, false},
		{workflow.WorkflowNode{Type: "http.request", Config: cfg("method", "POST")}, true},
		{workflow.WorkflowNode{Type: "http.request", Config: cfg("method", "{{ $json.m }}")}, true},
		{workflow.WorkflowNode{Type: "http_request", Config: cfg("method", "DELETE")}, true},
		{workflow.WorkflowNode{Type: "data.spreadsheet", Config: cfg("operation", "read_csv")}, false},
		{workflow.WorkflowNode{Type: "data.spreadsheet", Config: cfg("operation", "write_xlsx")}, true},
		{workflow.WorkflowNode{Type: "comm.email_read"}, false},
		{workflow.WorkflowNode{Type: "comm.email_send"}, true},
		{workflow.WorkflowNode{Type: "trigger.webhook"}, false},
		{workflow.WorkflowNode{Type: "trigger.send_dm"}, true}, // not a built-in trigger
		{workflow.WorkflowNode{Type: "trigger.cron"}, true},
		{workflow.WorkflowNode{Type: "core.if"}, false},
		{workflow.WorkflowNode{Type: "if"}, false},
	}
	for _, c := range cases {
		if got := orggrant.IsOutboundNode(c.node); got != c.outbound {
			t.Errorf("%s %v: outbound = %v, want %v", c.node.Type, c.node.Config, got, c.outbound)
		}
	}
}

// A workflow that sends LinkedIn DMs is granted irreversible: a person
// approves its calls even at autonomy mid (#287).
func TestGrantTierLinkedInDMIsIrreversible(t *testing.T) {
	bootOfficial(t)
	wf := &workflow.Workflow{Nodes: []workflow.WorkflowNode{
		{Type: "trigger.manual"},
		{Type: "linkedin.find_by_keyword", Name: "find"},
		{Type: "linkedin.send_dms", Name: "dm"},
	}}
	out := orggrant.OutboundNodes(wf)
	want := "dm (linkedin.send_dms)"
	if !bot.PlatformCompiledIn("linkedin") {
		// Without social support LinkedIn isn't installed, so nothing
		// verifies the search as a read.
		want = "dm (linkedin.send_dms)|find (linkedin.find_by_keyword)"
	}
	if strings.Join(out, "|") != want {
		t.Fatalf("outbound = %v", out)
	}
	if tier := orggrant.GrantTier(out); tier != orgdesign.TierIrreversible {
		t.Fatalf("tier = %s", tier)
	}
	wf.Nodes = wf.Nodes[:2]
	if !bot.PlatformCompiledIn("linkedin") {
		return
	}
	if tier := orggrant.GrantTier(orggrant.OutboundNodes(wf)); tier != orgdesign.TierConsequential {
		t.Fatalf("read-only workflow tier = %s", tier)
	}
}
