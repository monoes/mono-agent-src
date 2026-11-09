package workflowio

import (
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"strings"
	"testing"
)

// QA-A U7: a file that fails `workflow validate` is imported, with a
// warning for each error.
func TestImportWarnsWhenInvalid(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	cfg := newTestConfig(t)
	const invalid = `{"name":"invalid","nodes":[
  {"id":"t","type":"trigger.manual","name":"T","position":{"x":0,"y":0},"config":{}},
  {"id":"s","type":"core.set","name":"S","position":{"x":1,"y":2},"config":{"value":"{{ $json.n * 2 }}"}}],
  "connections":[{"id":"c","source":"t","target":"s"}]}`
	_, out, _, err := run(t, testDeps(t, cfg), invalid, "import")
	var res struct {
		ID, Status string
		Warnings   []string
	}
	if err != nil || json.Unmarshal([]byte(out), &res) != nil || res.Status != ImportCreated ||
		!strings.Contains(strings.Join(res.Warnings, "\n"), "fails `workflow validate`: node \"s\": invalid expression") {
		t.Fatalf("import = %q, %v; want it created with a validate warning", out, err)
	}
}
