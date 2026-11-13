package main

import (
	"encoding/json"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestAutomationCLIArgs(t *testing.T) {
	ok := mustArgs(t)
	got := ok(automationCLIArgs("p1", "automation", "list"))
	want := []string{"--profile", "p1", "--json", "automation", "list"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if got, err := automationCLIArgs("", "record", "list"); err == nil || got != nil {
		t.Fatalf("no profile: got %v, %v", got, err)
	}
}

func TestAutomationInstallArgs(t *testing.T) {
	dry, err := automationInstallArgs("/x/a.mpkg", true, InstallSpec{ExpectSHA256: "ignored"})
	if err != nil || !reflect.DeepEqual(dry, []string{"automation", "install", "--dry-run", "--", "/x/a.mpkg"}) {
		t.Fatalf("dry run: %v %v", dry, err)
	}
	yes, err := automationInstallArgs("-evil.mpkg", false, InstallSpec{ExpectSHA256: "ab12", Replace: true})
	want := []string{"automation", "install", "--yes", "--expect-sha256", "ab12", "--replace", "--", "-evil.mpkg"}
	if err != nil || !reflect.DeepEqual(yes, want) {
		t.Fatalf("confirm: %v %v", yes, err)
	}
	if _, err := automationInstallArgs(" ", true, InstallSpec{}); err == nil {
		t.Fatal("empty path accepted")
	}
}

func TestAutomationLifecycleAndTrustArgs(t *testing.T) {
	got, err := automationLifecycleArgs("rollback", "hackernews")
	if err != nil || !reflect.DeepEqual(got, []string{"automation", "rollback", "--", "hackernews"}) {
		t.Fatalf("got %v %v", got, err)
	}
	if _, err := automationLifecycleArgs("pack", "x"); err == nil {
		t.Fatal("unknown verb accepted")
	}
	if _, err := automationLifecycleArgs("enable", ""); err == nil {
		t.Fatal("empty id accepted")
	}
	got, err = automationTrustArgs("acme", "no-live")
	if err != nil || !reflect.DeepEqual(got, []string{"automation", "trust", "--no-live", "--", "acme"}) {
		t.Fatalf("trust: %v %v", got, err)
	}
	if _, err := automationTrustArgs("acme", "everything"); err == nil {
		t.Fatal("unknown trust flag accepted")
	}
}

func TestAutomationTestExportAnalyzeArgs(t *testing.T) {
	got, _ := automationTestArgs("hn", "submit_post", true)
	if !reflect.DeepEqual(got, []string{"automation", "test", "--live", "--", "hn", "submit_post"}) {
		t.Fatalf("test: %v", got)
	}
	got, _ = automationExportArgs("hn", "/o/hn.mpkg", false)
	if !reflect.DeepEqual(got, []string{"automation", "export", "-o", "/o/hn.mpkg", "--", "hn"}) {
		t.Fatalf("export: %v", got)
	}
	got, err := actionExportArgs("hn.submit_post", "/o/a.mpkg")
	if err != nil || !reflect.DeepEqual(got, []string{"action", "export", "-o", "/o/a.mpkg", "--", "hn.submit_post"}) {
		t.Fatalf("action export: %v %v", got, err)
	}
	if _, err := actionExportArgs("submit_post", "/o"); err == nil {
		t.Fatal("unqualified action ref accepted")
	}
	got, _ = recordAnalyzeArgs("rec1", "hn", true)
	if !reflect.DeepEqual(got, []string{"record", "analyze", "--automation", "hn", "--allow-advanced", "--", "rec1"}) {
		t.Fatalf("analyze: %v", got)
	}
}

func TestAutomationRerecordArgs(t *testing.T) {
	got, err := automationRerecordArgs("acme", "contact.save_button")
	if err != nil || !reflect.DeepEqual(got, []string{"automation", "rerecord", "--", "acme", "contact.save_button"}) {
		t.Fatalf("got %v %v", got, err)
	}
	if _, err := automationRerecordArgs("acme", " "); err == nil {
		t.Fatal("empty key accepted")
	}
}

func TestRecordVerifyArgsAndInputsFile(t *testing.T) {
	got, err := recordVerifyArgs("/d", true, "/tmp/in.json")
	want := []string{"record", "verify", "--full", "--inputs-file", "/tmp/in.json", "--", "/d"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v %v", got, err)
	}
	if got, _ := recordVerifyArgs("/d", false, ""); !reflect.DeepEqual(got, []string{"record", "verify", "--", "/d"}) {
		t.Fatalf("safe: %v", got)
	}
	if _, err := recordVerifyArgs("", false, ""); err == nil {
		t.Fatal("empty draft dir accepted")
	}

	t.Setenv("TMPDIR", t.TempDir())
	path, err := writeInputsFile(map[string]string{"password": "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	st, err := os.Stat(path)
	// Windows has no permission bits (a file is always reported 0666); the file lives in the user's own temp dir there.
	if err != nil || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o600) {
		t.Fatalf("mode = %v %v", st.Mode(), err)
	}
	b, _ := os.ReadFile(path)
	var back map[string]string
	if json.Unmarshal(b, &back) != nil || back["password"] != "s3cret" {
		t.Fatalf("contents = %s", b)
	}
	if err := validateInputNames(map[string]string{"a=b": "x"}); err == nil {
		t.Fatal("name with = accepted")
	}
}

func TestRecordSaveArgs(t *testing.T) {
	got, err := recordSaveArgs("/d", SaveDraftSpec{
		As: "action", Automation: "acme", New: "acme-crm", Name: "create_contact",
		RenameInputs: map[string]string{"b": "beta", "a": "alpha", "same": "same", "empty": ""},
	})
	want := []string{"record", "save", "--as", "action", "--new", "acme-crm", "--name", "create_contact",
		"--rename-input", "a=alpha", "--rename-input", "b=beta", "--", "/d"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v %v\nwant %v", got, err, want)
	}
	got, _ = recordSaveArgs("/d", SaveDraftSpec{Automation: "acme"})
	if !reflect.DeepEqual(got, []string{"record", "save", "--automation", "acme", "--", "/d"}) {
		t.Fatalf("existing: %v", got)
	}
	got, _ = recordSaveArgs("/d", SaveDraftSpec{Automation: "acme", Force: true})
	if !reflect.DeepEqual(got, []string{"record", "save", "--automation", "acme", "--force", "--", "/d"}) {
		t.Fatalf("force: %v", got)
	}
	if _, err := recordSaveArgs("/d", SaveDraftSpec{As: "node"}); err == nil {
		t.Fatal("bad save-as accepted")
	}
	if _, err := recordSaveArgs("/d", SaveDraftSpec{RenameInputs: map[string]string{"a": "b=c"}}); err == nil {
		t.Fatal("new name with = accepted")
	}
}

func TestRedactArgs(t *testing.T) {
	got := strings.Join(redactArgs([]string{"record", "verify", "--input", "password=hunter2", "--", "/d"}), " ")
	if strings.Contains(got, "hunter2") || !strings.Contains(got, "password=***") {
		t.Fatalf("redacted = %s", got)
	}
}
