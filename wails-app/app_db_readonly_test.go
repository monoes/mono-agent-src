package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
)

// The app's handle reads what the CLI wrote, including the task board the
// app shows in process and its revision watch, and refuses every write.
func TestReadOnlyDBReadsButNeverWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monoagent.db")
	sdb, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := sdb.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, _, err := tasks.NewStore(sdb.DB).Add(ctx, "default", tasks.AddInput{Title: "card"}, tasks.Actor{Kind: tasks.Human}); err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()

	db, err := openReadOnlyDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var name string
	if err := db.QueryRow(`SELECT name FROM profiles WHERE id = 'default'`).Scan(&name); err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := db.Exec(`UPDATE profiles SET name = 'x' WHERE id = 'default'`); err == nil || !strings.Contains(err.Error(), "readonly") {
		t.Fatalf("write on the app's handle: %v", err)
	}

	a := &App{db: db, ctx: ctx}
	a.selectForTest("default")
	if out := a.TaskBoard(0); !strings.Contains(out, `"card"`) {
		t.Fatalf("TaskBoard on the read-only handle: %s", out)
	}
	if got := a.TaskPulse(); got["rev"] == nil {
		t.Fatalf("TaskPulse on the read-only handle: %v", got)
	}
	changed := make(chan struct{}, 1)
	stop := a.startTaskWatcher("default", 10*time.Millisecond, func(string, map[string]interface{}) {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	defer stop()
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("the task watcher read nothing on the read-only handle")
	}
}

// A first start before the CLI made the database creates an empty, owner-only file.
func TestReadOnlyDBCreatesAMissingFileOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monoagent.db")
	db, err := openReadOnlyDB(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 { // Windows has no permission bits
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}
