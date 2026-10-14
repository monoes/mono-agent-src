package task

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks/osmenu"
	"github.com/monoes/mono-agent/internal/testdb"
)

// newTaskOSTest is a task test database (the operator's environment) on a
// pretend Mac: a home folder of its own, checked, so that an install without
// --dest writes under it and never under the real one; this binary is a stub
// file in a temporary folder; refreshing the Services menu is counted, never
// run.
func newTaskOSTest(t *testing.T) (db, dest, cli string, refreshes *int) {
	t.Helper()
	db = newTaskTestDB(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	if got, err := os.UserHomeDir(); err != nil || got != home {
		t.Fatalf("the home folder is %q (%v), not the test's own %q: refusing to run near a real Services folder", got, err, home)
	}
	goos, exe, refresh := taskOSGOOS, taskOSExecutable, refreshServices
	t.Cleanup(func() { taskOSGOOS, taskOSExecutable, refreshServices = goos, exe, refresh })
	cli = taskOSStubCLI(t)
	n := 0
	taskOSGOOS = "darwin"
	taskOSExecutable = func() (string, error) { return cli, nil }
	refreshServices = func() { n++ }
	return db, filepath.Join(t.TempDir(), "Services"), cli, &n
}

// taskOSStubCLI is an executable file standing for an installed monoagentcli.
func taskOSStubCLI(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bin", "monoagentcli")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func execTaskOSSQL(t *testing.T, db, query string, args ...any) {
	t.Helper()
	raw, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.DB.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func addTaskOSProfile(t *testing.T, db, id, name string) {
	t.Helper()
	execTaskOSSQL(t, db, `INSERT INTO profiles (id, name) VALUES (?, ?)`, id, name)
}

// taskOSInstalled is `task os install --json`.
type taskOSInstalled struct {
	Profile struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"profile"`
	Path     string   `json:"path"`
	MenuItem string   `json:"menu_item"`
	CLI      string   `json:"cli"`
	Outcome  string   `json:"outcome"`
	Removed  []string `json:"removed"`
}

func TestTaskOSInstallBindsTheActiveProfile(t *testing.T) {
	db, dest, cli, refreshes := newTaskOSTest(t)
	var got taskOSInstalled
	mustTaskJSON(t, db, "", &got, "", "os", "install", "--dest", dest)
	want := filepath.Join(dest, "Add to MonoAgent Tasks (Default).workflow")
	if got.Profile.ID != "default" || got.Profile.Name != "Default" || got.Path != want || got.CLI != cli ||
		got.MenuItem != "Add to MonoAgent Tasks: Default" || got.Outcome != "installed" || got.Removed == nil {
		t.Errorf("install: %+v", got)
	}
	doc, err := os.ReadFile(filepath.Join(want, filepath.FromSlash(osmenu.DocumentPath)))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"cli='" + cli + "'", "db='" + db + "'", "profile='default'", "task add --stdin --source os"} {
		if !strings.Contains(string(doc), s) {
			t.Errorf("the workflow does not hold %q", s)
		}
	}
	if *refreshes != 0 {
		t.Error("a --dest install refreshed the real Services menu")
	}
}

func TestTaskOSInstallNamesAProfileByNameAndOnceIsEnough(t *testing.T) {
	db, dest, _, _ := newTaskOSTest(t)
	addTaskOSProfile(t, db, "work-id", "Work")
	var first, second taskOSInstalled
	mustTaskJSON(t, db, "Work", &first, "", "os", "install", "--dest", dest)
	mustTaskJSON(t, db, "work-id", &second, "", "os", "install", "--dest", dest)
	if first.Profile.ID != "work-id" || first.Outcome != "installed" || second.Outcome != "already_installed" || second.Path != first.Path {
		t.Errorf("first %+v, second %+v", first, second)
	}
	out, _, err := runTask(t, db, "Work", false, "", "os", "install", "--dest", dest)
	if err != nil || !strings.Contains(out, "Already installed") {
		t.Errorf("text: %q, %v", out, err)
	}
}

func TestTaskOSInstallSaysWhereTheItemIs(t *testing.T) {
	db, dest, cli, _ := newTaskOSTest(t)
	out, _, err := runTask(t, db, "", false, "", "os", "install", "--dest", dest)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`Installed "Add to MonoAgent Tasks: Default"`, "profile Default (default)", "  runs " + cli + "\n",
		"right-click it and choose Services", "System Settings, Keyboard, Keyboard", "Shortcuts, Services, Text", "--profile NAME task os install"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output does not say %q:\n%s", want, out)
		}
	}
}

func TestTaskOSRefusesAwayFromMacOS(t *testing.T) {
	db, dest, _, _ := newTaskOSTest(t)
	taskOSGOOS = "linux"
	for _, sub := range []string{"install", "status", "uninstall"} {
		doc := failedTaskJSON(t, db, "", 3, "os", sub, "--dest", dest)
		if msg, _ := doc["error"].(string); doc["code"] != "invalid_input" || !strings.Contains(msg, "this is linux") ||
			!strings.Contains(msg, "task add --stdin --source os") {
			t.Errorf("os %s: %v", sub, doc)
		}
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("something was written away from macOS")
	}
}

// An agent is refused as an agent on every platform, before the platform is looked at: P1's gate
// test runs the real commands as an agent on whatever machine it is on, a Linux runner too.
func TestTaskOSInstallAndUninstallAreTheOperators(t *testing.T) {
	db, dest, _, _ := newTaskOSTest(t)
	setenv(t, "CLAUDECODE", "1")
	for _, goos := range []string{"darwin", "linux", "windows"} {
		taskOSGOOS = goos
		for _, sub := range []string{"install", "uninstall"} {
			if doc := failedTaskJSON(t, db, "", 3, "os", sub, "--dest", dest); doc["code"] != "operator_only" {
				t.Errorf("os %s under an agent's marker on %s: %v", sub, goos, doc)
			}
		}
	}
	taskOSGOOS = "darwin"
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("an agent's install wrote the menu")
	}
	var st struct {
		Menus []any `json:"menus"`
	}
	mustTaskJSON(t, db, "", &st, "", "os", "status", "--dest", dest)
}

// A mistake in the arguments is exit 3 with the --json document, like every other one of the group
// (cobra's own Args check would be exit 1 and no document); the operator's two commands refuse an
// agent that makes one as an agent, first; nothing is written.
func TestTaskOSCommandsTakeNoArguments(t *testing.T) {
	db, dest, _, refreshes := newTaskOSTest(t)
	for _, sub := range []string{"install", "status", "uninstall"} {
		doc := failedTaskJSON(t, db, "", 3, "os", sub, "junk", "--dest", dest)
		if msg, _ := doc["error"].(string); doc["code"] != "invalid_input" || !strings.Contains(msg, "task os "+sub+" takes no arguments") {
			t.Errorf("os %s junk: %v", sub, doc)
		}
		if _, _, err := runTask(t, db, "", false, "", "os", sub, "junk", "--dest", dest); exitCode(err) != 3 {
			t.Errorf("os %s junk as text: exit %d (%v), want 3", sub, exitCode(err), err)
		}
	}
	for _, sub := range []string{"install", "uninstall"} {
		if doc := failedTaskJSON(t, db, "", 3, "os", sub, "junk", "--as", "bot", "--dest", dest); doc["code"] != "operator_only" {
			t.Errorf("os %s junk run by an agent: %v, want operator_only before the arguments are read", sub, doc)
		}
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) || *refreshes != 0 {
		t.Errorf("a refused call wrote or refreshed something (%d refreshes)", *refreshes)
	}
}

func TestTaskOSInstallRefusesATemporaryBuild(t *testing.T) {
	db, dest, _, _ := newTaskOSTest(t)
	taskOSExecutable = func() (string, error) { return "/var/folders/x/T/go-build123/b001/exe/monoagentcli", nil }
	doc := failedTaskJSON(t, db, "", 3, "os", "install", "--dest", dest)
	if msg, _ := doc["error"].(string); doc["code"] != "invalid_input" || !strings.Contains(msg, "is a temporary build") {
		t.Errorf("%v", doc)
	}
}

func TestTaskOSInstallKeepsWhatItDidNotWrite(t *testing.T) {
	db, dest, _, _ := newTaskOSTest(t)
	info := filepath.Join(dest, "Add to MonoAgent Tasks (Default).workflow", "Contents", "Info.plist")
	if err := os.MkdirAll(filepath.Dir(info), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(info, []byte("<plist><dict/></plist>"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc := failedTaskJSON(t, db, "", 3, "os", "install", "--dest", dest)
	if msg, _ := doc["error"].(string); doc["code"] != "invalid_input" || !strings.Contains(msg, "--force replaces it") {
		t.Fatalf("%v", doc)
	}
	var got taskOSInstalled
	mustTaskJSON(t, db, "", &got, "", "os", "install", "--dest", dest, "--force")
	if got.Outcome != "updated" {
		t.Errorf("install --force: %+v", got)
	}
}

func TestTaskOSInstallNeverTakesAnotherProfilesMenu(t *testing.T) {
	db, dest, _, _ := newTaskOSTest(t)
	addTaskOSProfile(t, db, "a-id", "Work/A")
	addTaskOSProfile(t, db, "b-id", "Work:A")
	var got taskOSInstalled
	mustTaskJSON(t, db, "a-id", &got, "", "os", "install", "--dest", dest)
	for _, args := range [][]string{{"os", "install", "--dest", dest}, {"os", "install", "--dest", dest, "--force"}} {
		doc := failedTaskJSON(t, db, "b-id", 3, args...)
		if msg, _ := doc["error"].(string); !strings.Contains(msg, "is the menu of profile a-id") {
			t.Errorf("%v: %v", args, doc)
		}
	}
}

func TestTaskOSRefreshesTheRealServicesFolderOnly(t *testing.T) {
	db, dest, _, refreshes := newTaskOSTest(t)
	var got taskOSInstalled
	mustTaskJSON(t, db, "", &got, "", "os", "install")
	home, err := os.UserHomeDir() // newTaskOSTest made it a temporary folder and checked it
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "Library", "Services", "Add to MonoAgent Tasks (Default).workflow"); got.Path != want || *refreshes != 1 {
		t.Fatalf("install into the Services folder of the test's HOME: %s, %d refreshes", got.Path, *refreshes)
	}
	mustTaskJSON(t, db, "", &got, "", "os", "install")
	if got.Outcome != "already_installed" || *refreshes != 1 {
		t.Errorf("an unchanged install refreshed the menu: %s, %d", got.Outcome, *refreshes)
	}
	var un struct {
		Removed []string `json:"removed"`
	}
	mustTaskJSON(t, db, "", &un, "", "os", "uninstall")
	if len(un.Removed) != 1 || *refreshes != 2 {
		t.Errorf("uninstall: %v, %d refreshes", un.Removed, *refreshes)
	}
	mustTaskJSON(t, db, "", &got, "", "os", "install", "--dest", dest)
	mustTaskJSON(t, db, "", &un, "", "os", "uninstall", "--dest", dest)
	if *refreshes != 2 {
		t.Errorf("a --dest install or uninstall refreshed the real Services menu: %d", *refreshes)
	}
}

func TestTaskOSStatusSaysWhichMenusAreCurrent(t *testing.T) {
	db, dest, _, _ := newTaskOSTest(t)
	var empty struct {
		Menus []any `json:"menus"`
	}
	mustTaskJSON(t, db, "", &empty, "", "os", "status", "--dest", dest)
	if empty.Menus == nil || len(empty.Menus) != 0 {
		t.Errorf("no menus: %#v", empty.Menus)
	}
	for _, p := range [][2]string{{"work-id", "Work"}, {"home-id", "Home"}, {"gone-id", "Gone"}} {
		addTaskOSProfile(t, db, p[0], p[1])
	}
	for _, p := range []string{"default", "work-id", "gone-id"} {
		var got taskOSInstalled
		mustTaskJSON(t, db, p, &got, "", "os", "install", "--dest", dest)
	}
	other := testdb.Path(t) // a second database, with a menu of its own in the same folder
	addTaskOSProfile(t, other, "else-id", "Elsewhere")
	var elsewhere taskOSInstalled
	mustTaskJSON(t, other, "else-id", &elsewhere, "", "os", "install", "--dest", dest)
	moved := taskOSStubCLI(t) // home's menu runs a monoagentcli that is then removed
	taskOSExecutable = func() (string, error) { return moved, nil }
	var home taskOSInstalled
	mustTaskJSON(t, db, "home-id", &home, "", "os", "install", "--dest", dest)
	if err := os.Remove(moved); err != nil {
		t.Fatal(err)
	}
	execTaskOSSQL(t, db, `UPDATE profiles SET name = 'Job' WHERE id = 'work-id'`)
	execTaskOSSQL(t, db, `DELETE FROM profiles WHERE id = 'gone-id'`)
	var st struct {
		Dir   string `json:"dir"`
		Menus []struct {
			Profile struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"profile"`
			State string `json:"state"`
			Why   string `json:"why"`
		} `json:"menus"`
	}
	mustTaskJSON(t, db, "", &st, "", "os", "status", "--dest", dest)
	got := map[string]string{}
	for _, m := range st.Menus {
		got[m.Profile.ID] = m.State + " | " + m.Profile.Name + " | " + m.Why
	}
	want := map[string]string{
		"default": "current | Default | ",
		"work-id": "stale | Job | the profile was renamed, or the menu was changed or written in an older format",
		"home-id": "stale | Home | monoagentcli is not at " + moved,
		"gone-id": "profile_gone |  | the profile was deleted",
		"else-id": "other_database |  | it files into another database",
	}
	if st.Dir != dest || !reflect.DeepEqual(got, want) {
		t.Errorf("status in %s:\n%v\nwant:\n%v", st.Dir, got, want)
	}
	out, _, err := runTask(t, db, "", false, "", "os", "status", "--dest", dest)
	for _, w := range []string{"current", "monoagentcli --profile work-id task os install", "profile gone",
		"monoagentcli --profile gone-id task os uninstall", "other database", "monoagentcli --db-path " + other + " task os status"} {
		if err != nil || !strings.Contains(out, w) {
			t.Errorf("the status text does not say %q (%v):\n%s", w, err, out)
		}
	}
}

// Every database has a profile "default": an install or an uninstall run with
// another database never takes or removes this database's menu (--force takes it).
func TestTaskOSInstallNeverTakesAnotherDatabasesMenu(t *testing.T) {
	db, dest, _, _ := newTaskOSTest(t)
	other := testdb.Path(t)
	var got taskOSInstalled
	mustTaskJSON(t, db, "", &got, "", "os", "install", "--dest", dest)
	doc := failedTaskJSON(t, other, "", 3, "os", "install", "--dest", dest)
	if msg, _ := doc["error"].(string); doc["code"] != "invalid_input" || !strings.Contains(msg, "files into another database") {
		t.Errorf("an install from another database: %v", doc)
	}
	var un struct {
		Removed []string `json:"removed"`
	}
	mustTaskJSON(t, other, "", &un, "", "os", "uninstall", "--dest", dest)
	if un.Removed == nil || len(un.Removed) != 0 {
		t.Errorf("an uninstall from another database removed %v", un.Removed)
	}
	mustTaskJSON(t, other, "", &got, "", "os", "install", "--dest", dest, "--force")
	if got.Outcome != "updated" {
		t.Errorf("install --force from another database: %+v", got)
	}
}

func TestTaskOSUninstallRemovesOneProfilesMenu(t *testing.T) {
	db, dest, _, _ := newTaskOSTest(t)
	addTaskOSProfile(t, db, "work-id", "Work")
	var def, work taskOSInstalled
	mustTaskJSON(t, db, "", &def, "", "os", "install", "--dest", dest)
	mustTaskJSON(t, db, "Work", &work, "", "os", "install", "--dest", dest)
	var un struct {
		ProfileID string   `json:"profile_id"`
		Removed   []string `json:"removed"`
	}
	mustTaskJSON(t, db, "Work", &un, "", "os", "uninstall", "--dest", dest)
	if un.ProfileID != "work-id" || !reflect.DeepEqual(un.Removed, []string{work.Path}) {
		t.Errorf("uninstall: %+v", un)
	}
	if _, err := os.Stat(def.Path); err != nil {
		t.Error("another profile's menu was removed")
	}
	mustTaskJSON(t, db, "Work", &un, "", "os", "uninstall", "--dest", dest)
	if un.Removed == nil || len(un.Removed) != 0 {
		t.Errorf("a second uninstall: %+v", un)
	}
	out, _, err := runTask(t, db, "Work", false, "", "os", "uninstall", "--dest", dest)
	if err != nil || !strings.Contains(out, "nothing to remove") {
		t.Errorf("text: %q, %v", out, err)
	}
}

func TestTaskOSUninstallFindsADeletedProfilesMenuByItsID(t *testing.T) {
	db, dest, _, _ := newTaskOSTest(t)
	addTaskOSProfile(t, db, "gone-id", "Gone")
	var got taskOSInstalled
	mustTaskJSON(t, db, "gone-id", &got, "", "os", "install", "--dest", dest)
	execTaskOSSQL(t, db, `DELETE FROM profiles WHERE id = 'gone-id'`)
	var un struct {
		Removed []string `json:"removed"`
	}
	mustTaskJSON(t, db, "gone-id", &un, "", "os", "uninstall", "--dest", dest)
	if !reflect.DeepEqual(un.Removed, []string{got.Path}) {
		t.Errorf("uninstall of a deleted profile's menu: %+v", un)
	}
}

// The menu's own command line (spec 7): from the operator's environment it is
// a capture in the Inbox; for a deleted profile it fails with the CLI's own
// error (the line the menu's notification shows) and files nothing; with an
// agent's marker and no --as, --source os is refused.
func TestTheMenusCommandLineIsACaptureOnlyForTheOperator(t *testing.T) {
	db := newTaskTestDB(t)
	args := []string{"add", "--stdin", "--source", "os", "--app=Safari"}
	var added addedJSON
	mustTaskJSON(t, db, "default", &added, "Reply to Sam\nabout the invoice", args...)
	if added.Task.Source.Kind != "os" || added.Task.Status != "inbox" || added.Task.Title != "Reply to Sam" {
		t.Errorf("the menu's capture: %+v", added.Task)
	}
	addTaskOSProfile(t, db, "gone-id", "Gone")
	execTaskOSSQL(t, db, `DELETE FROM profiles WHERE id = 'gone-id'`)
	out, _, err := runTask(t, db, "gone-id", true, "Reply to Sam", args...)
	var doc map[string]any
	_ = json.Unmarshal([]byte(out), &doc)
	if msg, _ := doc["error"].(string); exitCode(err) != 3 || msg != `initializing database: profile "gone-id" not found (checked both id and name)` {
		t.Errorf("the menu's command line for a deleted profile: exit %d, %s", exitCode(err), out)
	}
	if n := countTaskOSTasks(t, db); n != 1 {
		t.Errorf("%d tasks, want only the first capture", n)
	}
	setenv(t, "CLAUDECODE", "1")
	out, _, err = runTask(t, db, "default", true, "Reply to Sam", args...)
	doc = nil
	_ = json.Unmarshal([]byte(out), &doc)
	if msg, _ := doc["error"].(string); exitCode(err) != 3 || doc["code"] != "invalid_input" || !strings.Contains(msg, `source "os" is for captures`) {
		t.Errorf("an agent context filing as the macOS menu: exit %d, %s", exitCode(err), out)
	}
}

// countTaskOSTasks counts every task in the test database.
func countTaskOSTasks(t *testing.T, db string) int {
	t.Helper()
	raw, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var n int
	if err := raw.DB.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
