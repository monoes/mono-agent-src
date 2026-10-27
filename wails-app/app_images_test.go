//go:build !windows

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// The media-vault bindings go through `monoagentcli media …`, not the
// database.
func TestImageVaultBindingsShellOut(t *testing.T) {
	log := filepath.Join(t.TempDir(), "args.log")
	row := `{"id":"img-002","seq":2,"path":"/v/img-002.png","filename":"img-002.png","size_bytes":9,"source":"upload","workflow_id":"","execution_id":"","label":"Logo","created_at":"2026-09-26T11:00:00Z","url":"/vault-image/img-002"}`
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, `echo "$*" >> '`+log+`'
case "$*" in
  *" media list "*) echo '[`+row+`]' ;;
  *" media search "*) echo '[]' ;;
  *" media get "*|*" media add "*) echo '`+row+`' ;;
  *" media data "*) echo '{"id":"img-002","mime_type":"image/png","size_bytes":3,"data_url":"data:image/png;base64,YWJj"}' ;;
  *" media stats"*) echo '{"count":2,"total_bytes":19}' ;;
  *" media export "*) echo '{"id":"img-002","path":"/out/logo.png"}' ;;
  *) echo '{}' ;;
esac
`))
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")
	orig := saveImageDialog
	var dialogs []runtime.SaveDialogOptions
	saveImageDialog = func(_ *App, opts runtime.SaveDialogOptions) (string, error) {
		dialogs = append(dialogs, opts)
		return "/out/logo.png", nil
	}
	t.Cleanup(func() { saveImageDialog = orig })
	var events []string
	recordImageEvents(t, func(name string, data map[string]interface{}) {
		events = append(events, fmt.Sprintf("%s %v", name, data))
	})

	if rows, err := a.GetVaultImages(0); err != nil || len(rows) != 1 || rows[0]["url"] != "/vault-image/img-002" || rows[0]["size_bytes"] != float64(9) {
		t.Fatalf("GetVaultImages = %v, %v", rows, err)
	}
	if rows, err := a.SearchVaultImages("-logo"); err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("SearchVaultImages = %#v, %v", rows, err)
	}
	if im, err := a.GetVaultImage("img-002"); err != nil || im["label"] != "Logo" {
		t.Fatalf("GetVaultImage = %v, %v", im, err)
	}
	if d, err := a.GetVaultImageData("img-002"); err != nil || d != "data:image/png;base64,YWJj" {
		t.Fatalf("GetVaultImageData = %q, %v", d, err)
	}
	if im, err := a.AddVaultImage("/pics/a.png", "Logo"); err != nil || im["id"] != "img-002" {
		t.Fatalf("AddVaultImage = %v, %v", im, err)
	}
	if _, err := a.AddVaultImage("/pics/b.png", ""); err != nil {
		t.Fatal(err)
	}
	if got := a.SaveVaultImageToFile("img-002", "logo.png"); got != "/out/logo.png" {
		t.Fatalf("SaveVaultImageToFile = %q", got)
	}
	if len(dialogs) != 1 || dialogs[0].Title != "Save Image" || dialogs[0].DefaultFilename != "logo.png" || dialogs[0].Filters[0].DisplayName != "Images" {
		t.Fatalf("the save dialog for an image: %+v", dialogs)
	}
	if err := a.UpdateVaultImageLabel("img-002", "New"); err != nil {
		t.Fatal(err)
	}
	if err := a.UpdateVaultImageLabel("img-002", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteVaultImage("img-002"); err != nil {
		t.Fatal(err)
	}
	if st, err := a.GetVaultStats(); err != nil || st["count"] != float64(2) || st["total_bytes"] != float64(19) {
		t.Fatalf("GetVaultStats = %v, %v", st, err)
	}
	want := []string{
		"--profile work --json media list --limit 200",
		"--profile work --json media search -- -logo",
		"--profile work --json media get -- img-002",
		"--profile work --json media data -- img-002",
		"--profile work --json media add --label Logo -- /pics/a.png",
		"--profile work --json media add -- /pics/b.png",
		"--profile work --json media get -- img-002",
		"--profile work --json media export -- img-002 /out/logo.png",
		"--profile work --json media label -- img-002 New",
		"--profile work --json media label -- img-002",
		"--profile work --json media delete -- img-002",
		"--profile work --json media stats",
	}
	if got := loggedArgs(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("argv:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Every edit made through the app refreshes open Media Vault pages.
	wantEvents := []string{
		"images:changed map[added:1 profileID:work]",
		"images:changed map[added:1 profileID:work]",
		"images:changed map[profileID:work updated:img-002]",
		"images:changed map[profileID:work updated:img-002]",
		"images:changed map[deleted:img-002 profileID:work]",
	}
	if strings.Join(events, "\n") != strings.Join(wantEvents, "\n") {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(events, "\n"), strings.Join(wantEvents, "\n"))
	}
}

// recordImageEvents routes the image bindings' events to rec for the test.
func recordImageEvents(t *testing.T, rec func(name string, data map[string]interface{})) {
	t.Helper()
	orig := emitImageEvent
	emitImageEvent = func(_ *App, name string, data map[string]interface{}) { rec(name, data) }
	t.Cleanup(func() { emitImageEvent = orig })
}

// A CLI failure (e.g. exit 2 for an unknown id) reaches the page as the
// CLI's stderr; SaveVaultImageToFile keeps its "error: …" string contract
// and never opens the dialog for an unknown image.
func TestImageVaultBindingsReportCLIErrors(t *testing.T) {
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, `echo 'vault image not found: "nope"' >&2; exit 2
`))
	a := newTestApp(t)
	a.ctx = context.Background()
	opened := false
	recordImageEvents(t, func(name string, _ map[string]interface{}) { t.Errorf("unexpected %s after a failed call", name) })
	orig := saveImageDialog
	saveImageDialog = func(*App, runtime.SaveDialogOptions) (string, error) { opened = true; return "/x", nil }
	t.Cleanup(func() { saveImageDialog = orig })

	if _, err := a.GetVaultImage("nope"); err == nil || err.Error() != `vault image not found: "nope"` {
		t.Fatalf("GetVaultImage err = %v", err)
	}
	if _, err := a.GetVaultImageData("nope"); err == nil {
		t.Fatal("GetVaultImageData: want error")
	}
	if err := a.DeleteVaultImage("nope"); err == nil {
		t.Fatal("DeleteVaultImage: want error")
	}
	if rows, err := a.GetVaultImages(10); err == nil || rows != nil {
		t.Fatalf("GetVaultImages = %v, %v", rows, err)
	}
	if got := a.SaveVaultImageToFile("nope", "x.png"); got != "error: image not found" || opened {
		t.Fatalf("SaveVaultImageToFile = %q, dialog opened %v", got, opened)
	}
}

// A file the extension saved (vault.save) is saved through a "Save File"
// dialog filtered to its own extension, not the images-only one, which could
// hide a PDF or have the OS add an image extension to its name.
func TestSaveVaultImageToFileOffersAFileItsOwnType(t *testing.T) {
	row := `{"id":"img-003","path":"/v/img-003.pdf","filename":"img-003.pdf","size_bytes":9,"source":"extension","label":"invoice.pdf","url":"/vault-image/img-003"}`
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, `case "$*" in
  *" media get "*) echo '`+row+`' ;;
  *" media export "*) echo '{"id":"img-003","path":"/out/invoice.pdf"}' ;;
  *) echo '{}' ;;
esac
`))
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")
	var dialogs []runtime.SaveDialogOptions
	orig := saveImageDialog
	saveImageDialog = func(_ *App, opts runtime.SaveDialogOptions) (string, error) {
		dialogs = append(dialogs, opts)
		return "/out/invoice.pdf", nil
	}
	t.Cleanup(func() { saveImageDialog = orig })

	if got := a.SaveVaultImageToFile("img-003", "invoice.pdf"); got != "/out/invoice.pdf" {
		t.Fatalf("SaveVaultImageToFile = %q", got)
	}
	want := runtime.SaveDialogOptions{Title: "Save File", DefaultFilename: "invoice.pdf", Filters: []runtime.FileFilter{{DisplayName: "PDF", Pattern: "*.pdf"}}}
	if len(dialogs) != 1 || fmt.Sprint(dialogs[0]) != fmt.Sprint(want) {
		t.Fatalf("the save dialog for a PDF: %+v, want %+v", dialogs, want)
	}

	for filename, title := range map[string]string{
		"img-001.PNG": "Save Image", "img-002.tiff": "Save Image", "img-004.svg": "Save Image", "": "Save Image",
		"img-005.md": "Save File", "img-006.csv": "Save File", "img-007.txt": "Save File",
	} {
		if got := saveVaultItemOptions(filename, "x").Title; got != title {
			t.Errorf("saveVaultItemOptions(%q).Title = %q, want %q", filename, got, title)
		}
	}
}
