package workflowbundle

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

func TestWorkflowAutomationIDs(t *testing.T) {
	t.Parallel()
	// "foo" is an alias of the package local-foo; "core" is no automation.
	resolve := func(prefix, _ string) string {
		return map[string]string{"a": "a", "b": "b", "foo": "local-foo", "local-foo": "local-foo"}[prefix]
	}
	got := WorkflowAutomationIDs([]workflow.WorkflowFileNode{
		{Type: "b.x"}, {Type: "a.y"}, {Type: "b.z"}, {Type: "nodot"}, {Type: ".x"},
		{Type: "core.set"}, {Type: "foo.bar"}, {Type: "local-foo.baz"},
	}, resolve)
	if strings.Join(got, ",") != "a,b,local-foo" {
		t.Fatalf("WorkflowAutomationIDs = %v", got)
	}
}

func TestParseAutomationDomains(t *testing.T) {
	t.Parallel()
	got, err := testDeps().ParseAutomationDomains([]string{"a=x.com, *.x.com", "b=y.org", "a=z.net"})
	if err != nil || strings.Join(got["a"], ",") != "x.com,*.x.com,z.net" || strings.Join(got["b"], ",") != "y.org" {
		t.Fatalf("parse = %v, %v", got, err)
	}
	for _, bad := range []string{"a", "=x.com", "a="} {
		if _, err := testDeps().ParseAutomationDomains([]string{bad}); err == nil {
			t.Fatalf("%q should be rejected", bad)
		}
	}
}

func TestIsLocalAddress(t *testing.T) {
	t.Parallel()
	for _, h := range []string{"localhost", "localhost:8099", "127.0.0.1:8099", "[::1]:80", "dev.localhost", "printer.local", "192.168.1.10", "10.0.0.1:3000"} {
		if !IsLocalAddress(h) {
			t.Errorf("%q should be local", h)
		}
	}
	for _, h := range []string{"example.com", "*.example.com", "8.8.8.8", "localhost.example.com"} {
		if IsLocalAddress(h) {
			t.Errorf("%q should not be local", h)
		}
	}
}

func TestCanonicalNodeTypes(t *testing.T) {
	t.Parallel()
	resolve := func(p, _ string) string {
		return map[string]string{"foo": "local-foo", "hn": "hn", "bar": "local-bar"}[p]
	}
	in := []workflow.WorkflowFileNode{{Type: "foo.x"}, {Type: "hn.y"}, {Type: "bar.z"}, {Type: "core.set"}}
	got := CanonicalNodeTypes(in, resolve, map[string]BundledAutomation{"local-foo": {}, "hn": {}})
	want := []string{"local-foo.x", "hn.y", "bar.z", "core.set"} // bar: not bundled, left alone
	for i, w := range want {
		if got[i].Type != w {
			t.Fatalf("node %d = %q, want %q", i, got[i].Type, w)
		}
	}
	if in[0].Type != "foo.x" {
		t.Fatal("input nodes were mutated")
	}
}
