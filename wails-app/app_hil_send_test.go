//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The send-approval bindings only shell out to `monoagentcli hil …` / `coder set`.
func TestHilSendBindingsShellOutToCLI(t *testing.T) {
	bin, argsLog := chatFakeCLI(t,
		fakeChatReply{match: "hil resolve", stdout: `{"state":"sent"}`},
		fakeChatReply{match: "hil", stdout: `{"ok":true}`},
		fakeChatReply{match: "coder set", stdout: `{"enabled":true}`},
	)
	t.Setenv("MONOAGENTCLI_BIN", bin) // the resolve binding uses runMonoCLI
	a, _ := newCLIChatApp(t, bin)
	a.ctx = context.Background()
	a.selectForTest("work")

	a.HilSendList("pending")
	a.HilSendShow("s1")
	a.HilSendEdit("s1", "new text", "")
	if got := a.HilSendResolve("s1", true, "edited", "subj", true); got != `{"state":"sent"}` {
		t.Errorf("resolve returned %s", got)
	}
	a.HilSendResolve("s2", false, "ignored", "ignored", true)
	a.HilSendAttachment("s1", 2)
	a.HilSendRecipient("s1")
	a.HilSendAllowList()
	a.HilSendAllowAdd("email", "email", "me@x.test", "ada@x.test")
	a.HilSendAllowRemove("r1")
	a.HilSendSetApproval(false)

	got := strings.Join(readArgsLog(t, argsLog), "\n")
	for _, want := range []string{
		"--profile work --json hil sends --state pending",
		"hil show s1",
		"hil edit s1 --text new text",
		"hil resolve s1 --approve --edit-text edited --edit-subject subj --always-allow",
		"hil resolve s2 --reject",
		"hil attachment s1 2",
		"hil recipient s1",
		"hil allow list",
		"hil allow add --action email --platform email --account me@x.test --recipient ada@x.test",
		"hil allow remove r1",
		"coder set --send-approval=false",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing call %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "s2 --reject --edit") {
		t.Errorf("a reject carried edits:\n%s", got)
	}
}

// The inline preview encodes only the path the CLI vouched for.
func TestHilSendAttachmentDataEncodesTheCLIsPath(t *testing.T) {
	img := filepath.Join(t.TempDir(), "cover.png")
	if err := os.WriteFile(img, []byte("PNG"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newTestApp(t)
	a.ctx = context.Background()
	dir := t.TempDir()
	script := "#!/bin/sh\necho '{\"path\":\"" + img + "\",\"mime\":\"image/png\",\"size\":3}'\n"
	bin := filepath.Join(dir, fakeCLIName)
	if err := writeFakeCLI(bin, script); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", bin)
	got, err := a.HilSendAttachmentData("s1", 0)
	if err != nil || got != "data:image/png;base64,UE5H" {
		t.Fatalf("got %q, %v", got, err)
	}
}
