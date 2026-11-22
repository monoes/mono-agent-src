package osmenu

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const cliPath = "/usr/local/bin/monoagentcli"

// noneGone says no profile was deleted.
func noneGone(string) bool { return false }

// mustRender renders the menu of profile id, named name.
func mustRender(t *testing.T, id, name, cli string) Bundle {
	t.Helper()
	b, err := Render(Spec{CLI: cli, DBPath: testDB, ProfileID: id, ProfileName: name})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// writeFile writes body at path, creating its folders.
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInstallWritesTheBundleOnceAndThenLeavesIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Services") // missing: Install creates it
	b := mustRender(t, "work-id", "Work", cliPath)
	res, err := Install(dir, b, false, noneGone)
	if err != nil || res.Outcome != Created || res.Path != filepath.Join(dir, b.Name) || res.Menu != b.Menu ||
		res.Removed == nil || len(res.Removed) != 0 {
		t.Fatalf("Install: %+v, %v", res, err)
	}
	if !Matches(res.Path, b) {
		t.Fatal("the files on disk are not the rendered bundle")
	}
	extra := filepath.Join(res.Path, "Contents", "QuickLook") // Automator adds such a folder; a rewrite would drop it
	if err := os.Mkdir(extra, 0o755); err != nil {
		t.Fatal(err)
	}
	again, err := Install(dir, b, false, noneGone)
	if err != nil || again.Outcome != Unchanged {
		t.Fatalf("second Install: %+v, %v", again, err)
	}
	if _, err := os.Stat(extra); err != nil {
		t.Error("an unchanged menu was rewritten")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("the folder holds %d entries, want the bundle alone (no temporary folder left)", len(entries))
	}
}

func TestInstallRewritesAStaleMenu(t *testing.T) {
	dir := t.TempDir()
	if _, err := Install(dir, mustRender(t, "work-id", "Work", "/old/monoagentcli"), false, noneGone); err != nil {
		t.Fatal(err)
	}
	b := mustRender(t, "work-id", "Work", cliPath)
	res, err := Install(dir, b, false, noneGone)
	if err != nil || res.Outcome != Updated || !Matches(res.Path, b) {
		t.Errorf("Install over a stale menu: %+v, %v", res, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("the folder holds %d entries, want the new bundle alone (the old one moved aside is gone)", len(entries))
	}
}

// When the new bundle cannot be moved in, the old one is put back.
func TestInstallPutsTheOldMenuBackWhenTheNewOneCannotGoIn(t *testing.T) {
	dir := t.TempDir()
	old := mustRender(t, "work-id", "Work", "/old/monoagentcli")
	if _, err := Install(dir, old, false, noneGone); err != nil {
		t.Fatal(err)
	}
	orig := rename
	t.Cleanup(func() { rename = orig })
	rename = func(from, to string) error {
		if base := filepath.Base(from); strings.HasPrefix(base, tmpPrefix) && !strings.HasSuffix(base, "-old") {
			return errors.New("injected: the move in failed")
		}
		return orig(from, to)
	}
	_, err := Install(dir, mustRender(t, "work-id", "Work", cliPath), false, noneGone)
	if err == nil || !strings.Contains(err.Error(), "injected: the move in failed") {
		t.Fatalf("Install: %v", err)
	}
	if !Matches(filepath.Join(dir, old.Name), old) {
		t.Error("the old menu was not put back")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("the folder holds %d entries, want the old bundle alone", len(entries))
	}
}

func TestInstallFollowsARenamedProfile(t *testing.T) {
	dir := t.TempDir()
	old, err := Install(dir, mustRender(t, "work-id", "Work", cliPath), false, noneGone)
	if err != nil {
		t.Fatal(err)
	}
	b := mustRender(t, "work-id", "Job", cliPath)
	res, err := Install(dir, b, false, noneGone)
	if err != nil || res.Outcome != Updated || !reflect.DeepEqual(res.Removed, []string{old.Path}) {
		t.Fatalf("Install after a rename: %+v, %v", res, err)
	}
	if menus, _ := List(dir); len(menus) != 1 || menus[0].Path != res.Path {
		t.Errorf("menus after a rename: %+v", menus)
	}
}

// On macOS's default disk a rename in case only names the same folder: the
// "older" menu is the new one, and must not be removed once it is written.
func TestInstallFollowsARenameInCaseOnly(t *testing.T) {
	dir := t.TempDir()
	if _, err := Install(dir, mustRender(t, "work-id", "work", cliPath), false, noneGone); err != nil {
		t.Fatal(err)
	}
	b := mustRender(t, "work-id", "Work", cliPath)
	res, err := Install(dir, b, false, noneGone)
	if err != nil || res.Outcome != Updated {
		t.Fatalf("Install: %+v, %v", res, err)
	}
	if menus, _ := List(dir); len(menus) != 1 || !Matches(res.Path, b) {
		t.Errorf("after a rename in case only: %+v", menus)
	}
}

func TestInstallKeepsWhatItDidNotWrite(t *testing.T) {
	dir := t.TempDir()
	b := mustRender(t, "work-id", "Work", cliPath)
	foreign := filepath.Join(dir, b.Name)
	writeFile(t, filepath.Join(foreign, "Contents", "Info.plist"),
		`<?xml version="1.0"?><plist version="1.0"><dict><key>NSServices</key><array/></dict></plist>`)
	_, err := Install(dir, b, false, noneGone)
	if !errors.Is(err, ErrTaken) || !strings.Contains(err.Error(), "was not written by monoagentcli task os install (--force replaces it)") {
		t.Fatalf("Install over a foreign bundle: %v", err)
	}
	if Matches(foreign, b) {
		t.Fatal("the foreign bundle was replaced without --force")
	}
	res, err := Install(dir, b, true, noneGone)
	if err != nil || res.Outcome != Updated || !Matches(res.Path, b) {
		t.Errorf("Install --force: %+v, %v", res, err)
	}
}

// Two profiles whose names clean to one name share a bundle name: the other
// profile's menu is never replaced while it exists, even with force; once that
// profile is deleted its menu is replaced.
func TestInstallNeverReplacesAnotherProfilesMenu(t *testing.T) {
	dir := t.TempDir()
	if _, err := Install(dir, mustRender(t, "other-id", "Work:A", cliPath), false, noneGone); err != nil {
		t.Fatal(err)
	}
	b := mustRender(t, "work-id", "Work/A", cliPath)
	for _, force := range []bool{false, true} {
		_, err := Install(dir, b, force, noneGone)
		if !errors.Is(err, ErrTaken) || !strings.Contains(err.Error(), "is the menu of profile other-id, whose name reads the same; rename one of the two profiles") {
			t.Fatalf("force=%v: %v", force, err)
		}
	}
	gone := func(id string) bool { return id == "other-id" }
	res, err := Install(dir, b, false, gone)
	if err != nil || res.Outcome != Updated || !Matches(res.Path, b) {
		t.Errorf("over a deleted profile's menu: %+v, %v", res, err)
	}
}

// Two databases can share a profile id (here the test fixture's "default"): a menu's identity is its database
// and its profile id. Another database's menu is taken only with force, and
// never removed by this database's uninstall.
func TestInstallKeepsAnotherDatabasesMenu(t *testing.T) {
	dir := t.TempDir()
	if _, err := Install(dir, mustRender(t, "default", "Default", cliPath), false, noneGone); err != nil {
		t.Fatal(err)
	}
	b, err := Render(Spec{CLI: cliPath, DBPath: "/tmp/other.db", ProfileID: "default", ProfileName: "Default"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Install(dir, b, false, noneGone)
	if !errors.Is(err, ErrTaken) || !strings.Contains(err.Error(), "files into another database, "+testDB+" (--force replaces it)") {
		t.Fatalf("Install from another database: %v", err)
	}
	if removed, err := Remove(dir, "/tmp/other.db", "default"); err != nil || len(removed) != 0 {
		t.Errorf("Remove from another database: %v, %v", removed, err)
	}
	res, err := Install(dir, b, true, noneGone)
	if err != nil || res.Outcome != Updated || !Matches(res.Path, b) {
		t.Errorf("Install --force from another database: %+v, %v", res, err)
	}
}

func TestListFindsOnlyManagedBundles(t *testing.T) {
	dir := t.TempDir()
	res, err := Install(dir, mustRender(t, "work-id", "Work", cliPath), false, noneGone)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.ReadFile(filepath.Join(res.Path, filepath.FromSlash(InfoPath)))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "Other.workflow", "Contents", "Info.plist"),
		`<plist version="1.0"><dict><key>NSServices</key><array/></dict></plist>`)
	writeFile(t, filepath.Join(dir, "Nested.workflow", "Contents", "Info.plist"),
		`<plist version="1.0"><dict><key>NSServices</key><array><dict><key>`+KeyProfileID+`</key><string>work-id</string></dict></array></dict></plist>`)
	writeFile(t, filepath.Join(dir, "Copy.bundle", "Contents", "Info.plist"), string(info))
	writeFile(t, filepath.Join(dir, "Binary.workflow", "Contents", "Info.plist"), "bplist00\x00\x01")
	writeFile(t, filepath.Join(dir, "File.workflow"), "not a folder")
	if err := os.Symlink(res.Path, filepath.Join(dir, "Link.workflow")); err != nil {
		t.Logf("no symlink here (%v): that case goes unchecked", err)
	}
	menus, err := List(dir)
	want := []Menu{{Path: res.Path, ProfileID: "work-id", DB: testDB, CLI: cliPath, Version: Version}}
	if err != nil || !reflect.DeepEqual(menus, want) {
		t.Errorf("List: %+v, %v; want only %+v", menus, err, want)
	}
	none, err := List(filepath.Join(dir, "missing"))
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("List of a missing folder: %#v, %v", none, err)
	}
}

func TestRemoveTakesOnlyThatProfilesMenus(t *testing.T) {
	dir := t.TempDir()
	mine, err := Install(dir, mustRender(t, "work-id", "Work", cliPath), false, noneGone)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := Install(dir, mustRender(t, "home-id", "Home", cliPath), false, noneGone)
	if err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(dir, "Foreign.workflow")
	writeFile(t, filepath.Join(foreign, "Contents", "Info.plist"), "<plist><dict/></plist>")
	leftover := filepath.Join(dir, tmpPrefix+"123-old") // an interrupted install's
	writeFile(t, filepath.Join(leftover, "Contents", "Info.plist"), "<plist><dict/></plist>")
	removed, err := Remove(dir, testDB, "work-id")
	if err != nil || !reflect.DeepEqual(removed, []string{mine.Path}) {
		t.Fatalf("Remove: %v, %v", removed, err)
	}
	if _, err := os.Stat(leftover); !errors.Is(err, fs.ErrNotExist) {
		t.Error("an interrupted install's temporary folder was left in place")
	}
	for _, p := range []string{theirs.Path, foreign} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed", p)
		}
	}
	again, err := Remove(dir, testDB, "work-id")
	if err != nil || again == nil || len(again) != 0 {
		t.Errorf("a second Remove: %#v, %v", again, err)
	}
}

func TestMatchesComparesTheNameAndBothFiles(t *testing.T) {
	dir := t.TempDir()
	b := mustRender(t, "work-id", "Work", cliPath)
	res, err := Install(dir, b, false, noneGone)
	if err != nil || !Matches(res.Path, b) {
		t.Fatalf("Install: %v", err)
	}
	renamed := b
	renamed.Name = "Add to MonoAgent Tasks (Job).workflow"
	if Matches(res.Path, renamed) {
		t.Error("a bundle under another name matched")
	}
	doc := filepath.Join(res.Path, filepath.FromSlash(DocumentPath))
	if err := os.WriteFile(doc, append(append([]byte{}, b.Document...), ' '), 0o644); err != nil {
		t.Fatal(err)
	}
	if Matches(res.Path, b) {
		t.Error("a changed document.wflow matched")
	}
}
