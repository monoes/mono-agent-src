//go:build !windows

package org

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/testhome"
)

// `org pending` is what the app's Human in Loop page renders: every item of
// every org that still waits for a person, in one shape, from monomind's
// {items:[…]} listings.
func TestOrgPendingListsWhatWaitsForAPerson(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	// Shapes as monomind 2.29.0 prints them with --all.
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo '{"v":1,"version":"2.29.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'
  exit 0
fi
[ "$3/$2" = "ops/questions" ] && { echo "questions.json is not valid JSON" >&2; exit 1; }
[ "$3" = "ops" ] && { echo '{"v":1,"org":"ops","items":[]}'; exit 0; }
case "$2" in
  questions) echo '{"v":1,"org":"bud","items":[
    {"questionId":"q-1","role":"lead","question":"Which market?","ts":10,"answer":null},
    {"questionId":"q-2","role":"lead","question":"old","ts":1,"answer":"EU"},
    {"questionId":"q-3","role":"lead","question":"moot","ts":2,"answer":null,"state":"dismissed"}]}' ;;
  approvals) echo '{"v":1,"org":"bud","items":[
    {"roleId":"writer","action":"Bash","question":"Approve Bash tool call?","ts":20,"approved":null,"requestId":"apr-1","input":{"command":"printf hi > note.txt"}},
    {"roleId":"writer","action":"WebFetch","question":"Approve WebFetch tool call?","ts":30,"approved":null,"requestId":"apr-2","input":{"url":"https://example.com"}},
    {"roleId":"writer","action":"Bash","question":"Approve Bash tool call?","ts":40,"approved":null,"requestId":null,"input":{"command":"ls"}},
    {"roleId":"writer","action":"Bash","ts":5,"approved":true,"requestId":"apr-0","input":{"command":"rm x"}}]}' ;;
  gates) echo '{"v":1,"org":"bud","items":[
    {"id":"gate-1","name":"publish","description":"Publish the post","roleId":"lead","status":"pending","createdAt":50},
    {"id":"gate-0","name":"old","description":"d","roleId":"lead","status":"approved","createdAt":3}]}' ;;
  *) echo "unexpected: $*" >&2; exit 1 ;;
esac
`
	bin := filepath.Join(t.TempDir(), "monomind")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monomind.EnvOverride, bin)
	root := t.TempDir()
	writeOrgFile(t, root, "bud.json", bashApprovalOrg)
	writeOrgFile(t, root, "ops.json", `{"name":"ops","goal":"g","roles":[{"id":"lead","title":"Lead","type":"boss","reports_to":null}]}`)

	cmd := newOrgCmd(&testConfig{DBPath: filepath.Join(t.TempDir(), "o.db"), ProfileID: "default"})
	cmd.SetArgs([]string{"--project", root, "pending"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	var err error
	out := captureStdout(t, func() { err = cmd.Execute() })
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Items []struct {
			Org       string            `json:"org"`
			Kind      string            `json:"kind"`
			Ref       string            `json:"ref"`
			Role      string            `json:"role"`
			Action    string            `json:"action"`
			Name      string            `json:"name"`
			Text      string            `json:"text"`
			Command   string            `json:"command"`
			Cwd       string            `json:"cwd"`
			RequestID string            `json:"request_id"`
			Input     map[string]string `json:"input"`
		} `json:"items"`
		Errors []struct {
			Org   string `json:"org"`
			Error string `json:"error"`
		} `json:"errors"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output %s: %v", out, err)
	}
	var refs []string
	for _, it := range got.Items {
		if it.Org != "bud" {
			t.Errorf("item of %q: %+v", it.Org, it)
		}
		refs = append(refs, it.Kind+":"+it.Ref)
	}
	want := []string{"question:q-1", "approval:apr-1", "approval:apr-2", "approval:writer:Bash:40", "gate:gate-1"}
	if len(refs) != len(want) {
		t.Fatalf("items = %v, want %v", refs, want)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Fatalf("items = %v, want %v", refs, want)
		}
	}
	folder := filepath.Join(root, ".monomind", "orgs", "bud", "workspace")
	if q := got.Items[0]; q.Text != "Which market?" || q.Role != "lead" {
		t.Errorf("question = %+v", q)
	}
	if a := got.Items[1]; a.RequestID != "apr-1" || a.Role != "writer" || a.Action != "Bash" || a.Command != "printf hi > note.txt" || a.Cwd != folder {
		t.Errorf("bash approval = %+v (want folder %s)", a, folder)
	}
	if a := got.Items[2]; a.RequestID != "apr-2" || a.Command != "" || a.Cwd != "" || a.Input["url"] != "https://example.com" {
		t.Errorf("fetch approval = %+v", a)
	}
	if a := got.Items[3]; a.RequestID != "" {
		t.Errorf("a request monomind gave no id must carry none: %+v", a)
	}
	if g := got.Items[4]; g.Name != "publish" || g.Text != "Publish the post" {
		t.Errorf("gate = %+v", g)
	}
	// One org's failing list is reported, not dropped without a word.
	if len(got.Errors) != 1 || got.Errors[0].Org != "ops" || got.Errors[0].Error == "" {
		t.Errorf("errors = %+v", got.Errors)
	}
}
