package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-rod/rod/lib/launcher"
	"github.com/monoes/mono-agent/internal/automation"
)

// TestAutomationFixtureFullRunPostsForm runs a write action end to end in a
// headless browser: the form POST is served in-process and asserted.
// Skipped when no browser is installed.
func TestAutomationFixtureFullRunPostsForm(t *testing.T) {
	if _, ok := launcher.LookPath(); !ok {
		t.Skip("no browser installed")
	}
	home := t.TempDir()
	dir := filepath.Join(home, "poster")
	files := map[string]string{
		"automation.json": `{"schema":"monoagent.automation/v1","id":"poster","name":"Poster","version":"0.1.0",
			"site":{"startUrl":"https://w.test/","domains":["w.test"]},
			"permissions":{"steps":["navigate","type","click","wait_for"],"scripts":[]},
			"actions":["post"],"policy":{"tier":"standard"}}`,
		"actions/post.json": `{"actionType":"post","automation":"poster","sideEffects":"write",
			"inputs":{"required":[{"name":"title","type":"string"}]},
			"steps":[
				{"id":"open","type":"navigate","url":"https://w.test/new"},
				{"id":"title","type":"type","selector":"#title","value":"{{title}}"},
				{"id":"send","type":"click","selector":"#send","sideEffect":true},
				{"id":"done","type":"wait_for","until":{"selector":"#done"},"timeout":10}
			]}`,
		"tests/fixtures/form.html": `<form method="post" action="/create"><input id="title" name="title"><button id="send">Go</button></form>`,
		"tests/fixtures/done.html": `<p id="done">created</p>`,
		"tests/post.routes.json":   `[{"match":"/new$","fixture":"form.html"},{"match":"/create$","fixture":"done.html"}]`,
		"tests/post.inputs.json":   `{"title":"hello"}`,
		"tests/post.expect.json":   `{"requests":[{"method":"POST","urlMatches":"/create$","bodyContains":"title=hello"}]}`,
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	run := func(args ...string) []fixtureResult {
		t.Helper()
		root := newRootCmd()
		var out strings.Builder
		root.SetOut(&out)
		root.SetErr(&strings.Builder{})
		root.SetArgs(append([]string{"automation", "test", dir, "--json", "--db-path", filepath.Join(home, "x.db")}, args...))
		_ = root.Execute()
		var res struct {
			Results []fixtureResult `json:"results"`
		}
		if err := json.Unmarshal([]byte(out.String()), &res); err != nil {
			t.Fatalf("stdout %q: %v", out.String(), err)
		}
		return res.Results
	}
	safe := run()
	if len(safe) != 2 || safe[1].Status != "skipped" || !strings.Contains(safe[1].Message, "--full") {
		t.Fatalf("safe run = %+v", safe)
	}
	full := run("--full")
	if len(full) != 2 || full[1].Status != "pass" {
		t.Fatalf("full run = %+v", full)
	}
	_ = automation.SourceLocal
}

// TestAutomationTestJSONExitCode: a failing test row makes the command fail
// with --json too, and stdout stays a single JSON document.
func TestAutomationTestJSONExitCode(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "broken")
	os.MkdirAll(filepath.Join(dir, "actions"), 0o755)
	// Invalid as a local package (no domains): the validate row fails.
	os.WriteFile(filepath.Join(dir, "automation.json"), []byte(`{"schema":"monoagent.automation/v1","id":"broken",
		"name":"B","version":"0.1.0","site":{"startUrl":"","domains":[]},"permissions":{"steps":["log"],"scripts":[]},
		"actions":["a"],"policy":{"tier":"standard"}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "actions", "a.json"), []byte(`{"actionType":"a","automation":"broken","steps":[{"id":"s","type":"log"}]}`), 0o644)

	out, _, err := runAutomationCLI(t, home, "automation", "test", dir, "--json")
	if err == nil {
		t.Fatal("automation test --json succeeded with a failing row")
	}
	var res struct {
		Results []fixtureResult `json:"results"`
	}
	dec := json.NewDecoder(strings.NewReader(out))
	if err := dec.Decode(&res); err != nil || len(res.Results) == 0 || res.Results[0].Status != "fail" {
		t.Fatalf("stdout %q: %v", out, err)
	}
	if dec.More() {
		t.Fatalf("more than one JSON document on stdout: %q", out)
	}
}
