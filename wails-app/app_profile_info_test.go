//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileInfoBindingsShellOut(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "args.log")
	stdinLog := filepath.Join(dir, "stdin.log")
	photo := filepath.Join(dir, "me.png")
	if err := os.WriteFile(photo, []byte("PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, `echo "$*" >> '`+log+`'
case "$*" in
  *"info fields"*) echo '{"groups":[{"id":"identity","label":"Identity"}],"fields":[{"key":"identity.first_name","label":"First name","group":"identity","kind":"text"},{"key":"ids.passport","label":"Passport","group":"ids","kind":"text","sensitive":true}]}' ;;
  *"info get ids.passport --reveal"*) echo '{"profile":"work","entries":[{"key":"ids.passport","value":"X1234567","source":"user"}]}' ;;
  *"info get"*) echo '{"profile":"work","entries":[{"key":"ids.passport","value":"•••• 4567","masked":true,"source":"ai","origin":"cv.pdf"}]}' ;;
  *"info set"*) cat > '`+stdinLog+`' ;;
  *"info photo-path"*) echo '{"key":"photos.id","path":"`+photo+`"}' ;;
  *"info ai-fill"*) echo '{"asked":3,"filled":[{"key":"identity.first_name","origin":"cv.pdf"}],"skipped":2}' ;;
esac
`))
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")

	cat, err := a.GetProfileInfoFields()
	if err != nil || len(cat.Groups) != 1 || len(cat.Fields) != 2 || !cat.Fields[1].Sensitive {
		t.Fatalf("GetProfileInfoFields = %+v, %v", cat, err)
	}
	info, err := a.GetProfileInfo()
	if err != nil || len(info.Entries) != 1 || !info.Entries[0].Masked || info.Entries[0].Origin != "cv.pdf" {
		t.Fatalf("GetProfileInfo = %+v, %v", info, err)
	}
	if v, err := a.RevealProfileInfo("ids.passport"); err != nil || v != "X1234567" {
		t.Fatalf("RevealProfileInfo = %q, %v", v, err)
	}
	if err := a.SetProfileInfo("about.bio", "line1\nline2"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(stdinLog); string(b) != "line1\nline2" {
		t.Fatalf("stdin = %q", b)
	}
	if err := a.SetProfileInfo("about.bio", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.SetProfileInfoPhoto("photos.id", photo); err != nil {
		t.Fatal(err)
	}
	if u, err := a.GetProfileInfoPhoto("photos.id"); err != nil || u != "data:image/png;base64,UE5H" {
		t.Fatalf("GetProfileInfoPhoto = %q, %v", u, err)
	}
	res, err := a.AIFillProfileInfo("claude", "m1")
	if err != nil || res.Asked != 3 || len(res.Filled) != 1 || res.Skipped != 2 {
		t.Fatalf("AIFillProfileInfo = %+v, %v", res, err)
	}

	want := []string{
		"--profile work --json profile info fields",
		"--profile work --json profile info get",
		"--profile work --json profile info get ids.passport --reveal",
		"--profile work --json profile info set about.bio --stdin",
		"--profile work --json profile info rm about.bio",
		"--profile work --json profile info photo photos.id " + photo,
		"--profile work --json profile info photo-path photos.id",
		"--profile work --json profile info ai-fill --runtime claude --model m1",
	}
	if got := loggedArgs(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("CLI calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The CLI's validation message reaches the caller; no photo is "".
func TestProfileInfoErrorsAndNoPhoto(t *testing.T) {
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, `case "$*" in
  *"info photo-path"*) echo '{"key":"photos.id","path":""}' ;;
  *) echo 'invalid email address' >&2; exit 3 ;;
esac
`))
	a := newTestApp(t)
	a.ctx = context.Background()
	if err := a.SetProfileInfo("contact.email", "nope"); err == nil || !strings.Contains(err.Error(), "invalid email") {
		t.Fatalf("SetProfileInfo err = %v", err)
	}
	if u, err := a.GetProfileInfoPhoto("photos.id"); err != nil || u != "" {
		t.Fatalf("GetProfileInfoPhoto = %q, %v", u, err)
	}
}
