package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/storage"
)

const guardMsg = "an AI task's session may not do this"

// guardRig is a home with two profiles and a task of the given kind in p1.
func guardRig(t *testing.T, kind string) (taskID string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	asOperatorProcess(t)
	path := expandPath("~/.monoagent/monoagent.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`INSERT INTO profiles (id, name) VALUES ('p1','One')`, `INSERT INTO profiles (id, name) VALUES ('p2','Two')`} {
		if _, err := db.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	task, err := (&aitask.Store{DB: db.DB, Profile: "p1"}).Insert(aitask.Task{ID: aitask.NewID(), Kind: kind, ConversationID: "c", Status: aitask.StatusRunning, Goal: "g"})
	if err != nil {
		t.Fatal(err)
	}
	return task.ID
}

func runGuardRoot(t *testing.T, args ...string) error {
	t.Helper()
	root := newRootCmd()
	root.SetArgs(args)
	var err error
	captureStdout(t, func() { err = root.Execute() })
	return err
}

// In a confined task's session the root hook refuses what would leave the
// task's profile, through the real command tree.
func TestTaskSessionRefusesFlagsThatLeaveTheTask(t *testing.T) {
	id := guardRig(t, "draft-reply")
	asTaskSession(t, id, "tok")
	for name, args := range map[string][]string{
		"other profile":      {"--profile", "p2", "workflow", "list"},
		"other profile name": {"--profile", "Two", "workflow", "list"},
		"profile twice":      {"--profile", "p1", "--profile", "p2", "workflow", "list"},
		"profile twice same": {"--profile", "p1", "--profile=p1", "workflow", "list"},
		"db-path":            {"--profile", "p1", "--db-path", "/tmp/x.db", "workflow", "list"},
		"config-dir":         {"--profile", "p1", "--config-dir", "/tmp/c", "workflow", "list"},
		"output-dir":         {"--profile", "p1", "--output-dir", "/tmp/o", "workflow", "list"},
		"log-file":           {"--profile", "p1", "--log-file", "/tmp/l", "workflow", "list"},
		"all-profiles":       {"--profile", "p1", "workflow", "list", "--all-profiles"},
		"validate --file":    {"--profile", "p1", "workflow", "validate", "--file", "/etc/passwd"},
	} {
		err := runGuardRoot(t, args...)
		if err == nil || !strings.Contains(err.Error(), guardMsg) {
			t.Errorf("%s: not refused by the task-session guard: %v", name, err)
		}
	}
	for name, args := range map[string][]string{
		"own profile id":   {"--profile", "p1", "workflow", "list"},
		"own profile name": {"--profile", "One", "workflow", "list"},
	} {
		if err := runGuardRoot(t, args...); err != nil && strings.Contains(err.Error(), guardMsg) {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
}

// Only an agent in a confined kind's session is held to it.
func TestTaskSessionGuardLeavesOperatorsAndGenericTasksAlone(t *testing.T) {
	id := guardRig(t, "draft-reply")
	asOperatorProcess(t)
	t.Setenv(aitask.EnvTaskID, id) // the supervisor carries the id but is the operator
	if err := runGuardRoot(t, "--profile", "p2", "workflow", "list"); err != nil && strings.Contains(err.Error(), guardMsg) {
		t.Errorf("the operator was refused: %v", err)
	}
	// An unconfined kind (the generic one) is not held; the workflow/recording kinds are
	// confined now, so the guard holds their sessions to the task's profile.
	id = guardRig(t, "generic")
	asTaskSession(t, id, "tok")
	if err := runGuardRoot(t, "--profile", "p2", "workflow", "list"); err != nil && strings.Contains(err.Error(), guardMsg) {
		t.Errorf("a generic task was refused: %v", err)
	}
	for _, kind := range []string{"workflow-generate", "recording-analyze", "failed-run-fix"} {
		id = guardRig(t, kind)
		asTaskSession(t, id, "tok")
		if err := runGuardRoot(t, "--profile", "p2", "workflow", "list"); err == nil || !strings.Contains(err.Error(), guardMsg) {
			t.Errorf("a %s task session reached another profile: %v", kind, err)
		}
	}
	// A task that cannot be found fails closed.
	guardRig(t, "draft-reply")
	asTaskSession(t, "at-0000000000000000", "tok")
	if err := runGuardRoot(t, "--profile", "p1", "workflow", "list"); err == nil || !strings.Contains(err.Error(), guardMsg) {
		t.Errorf("an unknown task id was let through: %v", err)
	}
}

// A follow-up on a FINISHED confined task carries the task id (no token), so
// the guard holds that agent to its profile too.
func TestTaskSessionGuardHoldsAFinishedTasksFollowUp(t *testing.T) {
	id := guardRig(t, "draft-reply")
	db, err := storage.NewDatabase(expandPath("~/.monoagent/monoagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE ai_tasks SET status = 'complete' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	db.Close()
	asTaskSession(t, id, "") // the id only, as chat hands a closed task's follow-up
	for _, args := range [][]string{
		{"--profile", "p2", "workflow", "list"},
		{"--profile", "p1", "--profile", "p2", "workflow", "get", "x"},
		{"--profile", "p1", "workflow", "list", "--all-profiles"},
		{"--profile", "p1", "--db-path", "/tmp/x.db", "workflow", "list"},
	} {
		if err := runGuardRoot(t, args...); err == nil || !strings.Contains(err.Error(), guardMsg) {
			t.Errorf("%v: not refused: %v", args, err)
		}
	}
}

// A refused --db-path is never used for the lookup: the lookup is not even made.
func TestTaskSessionGuardRefusesForbiddenFlagsBeforeTheLookup(t *testing.T) {
	id := guardRig(t, "draft-reply")
	asTaskSession(t, id, "")
	old := taskProfileLookup
	taskProfileLookup = func(dbPath, taskID string) (string, string, string, error) {
		t.Errorf("the task was looked up in %q before the forbidden flag was refused", dbPath)
		return old(dbPath, taskID)
	}
	t.Cleanup(func() { taskProfileLookup = old })
	for _, flag := range []string{"--db-path", "--config-dir", "--output-dir", "--log-file"} {
		err := runGuardRoot(t, "--profile", "p1", flag, "/nonexistent/x", "workflow", "list")
		if err == nil || !strings.Contains(err.Error(), flag+" is not allowed") {
			t.Errorf("%s: %v", flag, err)
		}
	}
}

// The extension's kinds are confined kinds: the same guard holds them to their
// profile and database, and it coexists with the tab pin of browser commands.
func TestTaskSessionGuardHoldsTheExtensionKinds(t *testing.T) {
	for _, kind := range []string{aitask.KindExtensionFill, aitask.KindExtensionAsk} {
		id := guardRig(t, kind)
		asTaskSession(t, id, "tok")
		for name, args := range map[string][]string{
			"other profile": {"--profile", "p2", "browser", "a11y"},
			"profile twice": {"--profile", "p1", "--profile", "p2", "browser", "a11y"},
			"db-path":       {"--profile", "p1", "--db-path", "/tmp/x.db", "browser", "a11y"},
		} {
			if err := runGuardRoot(t, args...); err == nil || !strings.Contains(err.Error(), guardMsg) {
				t.Errorf("%s/%s: not refused by the task-session guard: %v", kind, name, err)
			}
		}
		// Its own profile passes the guard (whatever the browser then says).
		if err := runGuardRoot(t, "--profile", "p1", "browser", "a11y"); err != nil && strings.Contains(err.Error(), guardMsg) {
			t.Errorf("%s: its own profile was refused: %v", kind, err)
		}
	}
}
