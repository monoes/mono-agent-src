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

// bashApprovalMonomind lists one pending Bash approval of the writer, one of a
// role the org file no longer has, and one WebFetch approval.
func bashApprovalMonomind(t *testing.T) {
	t.Helper()
	script := `#!/bin/sh
if [ "$1" = "--version" ] && [ "$2" = "--json" ]; then
  echo '{"v":1,"version":"2.29.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'
  exit 0
fi
case "$2" in
  approvals) echo '{"v":1,"org":"bud","items":[
    {"roleId":"writer","action":"Bash","question":"Approve Bash tool call?","ts":5,"approved":null,"requestId":"apr-1","resolvedBy":null,"input":{"command":"printf hi > note.txt"},"fingerprint":"keep-me"},
    {"roleId":"gone","action":"Bash","ts":6,"approved":null,"requestId":"apr-2","input":{"command":"ls"}},
    {"roleId":"writer","action":"WebFetch","ts":7,"approved":null,"requestId":"apr-3","input":{"url":"https://example.com"}}]}' ;;
  status) echo '{"v":1,"name":"bud","status":"running"}' ;;
  *) echo '{"v":1,"org":"bud","items":[]}' ;;
esac
`
	bin := filepath.Join(t.TempDir(), "approvals-monomind.sh") // outside the project: a project-local one is refused
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monomind.EnvOverride, bin)
}

const bashApprovalOrg = `{"name":"bud","goal":"g","run_config":{"workspace":"isolated"},"roles":[
	{"id":"lead","title":"Lead","type":"boss","reports_to":null},
	{"id":"writer","title":"Writer","type":"specialist","reports_to":"lead"}]}`

type approvalRow struct {
	RoleID      string  `json:"roleId"`
	RequestID   string  `json:"requestId"`
	Ref         string  `json:"ref"`
	Command     *string `json:"command"`
	Cwd         string  `json:"cwd"`
	Fingerprint string  `json:"fingerprint"`
}

func runApprovalCmd(t *testing.T, root string, args ...string) []approvalRow {
	t.Helper()
	cmd := newOrgCmd(&testConfig{DBPath: filepath.Join(t.TempDir(), "o.db"), ProfileID: "default"})
	cmd.SetArgs(append([]string{"--project", root}, args...))
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	var err error
	out := captureStdout(t, func() { err = cmd.Execute() })
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Org   string        `json:"org"`
		Items []approvalRow `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Org != "bud" {
		t.Fatalf("output %s: %v", out, err)
	}
	return got.Items
}

// #1766: `org approvals` names the command and the role's folder, which is what
// the app's approval cards show.
func TestOrgApprovalsCarryCommandAndFolder(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	bashApprovalMonomind(t)
	root := t.TempDir()
	writeOrgFile(t, root, "bud.json", bashApprovalOrg)

	rows := runApprovalCmd(t, root, "approvals", "bud")
	if len(rows) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
	folder := filepath.Join(root, ".monomind", "orgs", "bud", "workspace")
	w := rows[0]
	if w.Command == nil || *w.Command != "printf hi > note.txt" || w.Cwd != folder || w.RequestID != "apr-1" || w.Fingerprint != "keep-me" {
		t.Errorf("writer row = %+v (want folder %s, monomind's own fields kept)", w, folder)
	}
	if g := rows[1]; g.Command == nil || *g.Command != "ls" || g.Cwd != "" {
		t.Errorf("a role the org file does not have gets no folder: %+v", g)
	}
	if f := rows[2]; f.Command != nil || f.Cwd != "" {
		t.Errorf("a call with no command gets neither: %+v", f)
	}
}

func TestOrgChatHistoryApprovalsCarryCommandAndFolder(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	bashApprovalMonomind(t)
	root := t.TempDir()
	writeOrgFile(t, root, "bud.json", bashApprovalOrg)

	byRef := map[string]approvalRow{}
	for _, r := range runApprovalCmd(t, root, "chat", "history", "bud") {
		byRef[r.Ref] = r
	}
	w := byRef["apr-1"]
	if w.Command == nil || *w.Command != "printf hi > note.txt" || w.Cwd != filepath.Join(root, ".monomind", "orgs", "bud", "workspace") {
		t.Errorf("chat item = %+v", w)
	}
}
