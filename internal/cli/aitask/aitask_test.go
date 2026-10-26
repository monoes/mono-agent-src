package aitask

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/aitask/budget"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
)

func TestMain(m *testing.M) { testhome.Main(m) }

type fake struct {
	cwd      string
	detached []string
	runtimes []string // the runtime each CreateConversation asked for
}

func (f *fake) Ready(context.Context) error                    { return nil }
func (f *fake) Confined(context.Context) bool                  { return false }
func (f *fake) DefaultBudget(context.Context) (float64, error) { return 5, nil }
func (f *fake) Policy(context.Context) (string, error)         { return "p", nil }
func (f *fake) Enforceable(string, string) error               { return nil }

func (f *fake) CreateConversation(_ context.Context, runtime, _, _ string) (aitask.Conversation, error) {
	f.runtimes = append(f.runtimes, runtime)
	return aitask.Conversation{ID: "conv-9", Cwd: f.cwd, Runtime: "claude"}, nil
}
func (f *fake) Detach(_ context.Context, id, _ string) error {
	f.detached = append(f.detached, id)
	return nil
}

func deps(t *testing.T, l func() aitask.Launcher) *Deps {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.db")
	return &Deps{
		JSONOutput: func() bool { return true },
		ProfileID:  func() string { return "p1" },
		DBPath:     func() string { return path },
		OpenDB: func() (*storage.Database, error) {
			db, err := storage.NewDatabase(path)
			if err != nil {
				return nil, err
			}
			if err := db.ApplyMigrations(); err != nil {
				db.Close()
				return nil, err
			}
			// A task runs only under a spend cap (internal/aitask/budget).
			if err := (&budget.Ledger{DB: db.DB}).SetCap(budget.ScopeGlobal, "", 100, "test"); err != nil {
				db.Close()
				return nil, err
			}
			return db, nil
		},
		RequireOperator: func(string) error { return nil },
		Launcher:        l,
	}
}

func run(t *testing.T, d *Deps, args ...string) (string, error) {
	t.Helper()
	cmd := New(*d)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.Execute()
	return out.String(), err
}

func TestSubmitPrintsContractATaskJSON(t *testing.T) {
	f := &fake{cwd: t.TempDir()}
	d := deps(t, func() aitask.Launcher { return f })
	out, err := run(t, d, "submit", "--kind", "generic", "--goal", "tidy", "--application", "app-3", "--queue-only")
	if err != nil {
		t.Fatal(err)
	}
	var task map[string]any
	if err := json.Unmarshal([]byte(out), &task); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	for _, k := range []string{"taskId", "kind", "conversationId", "status", "goal", "createdAt", "updatedAt", "applicationId"} {
		if task[k] == nil || task[k] == "" {
			t.Errorf("task JSON has no %s: %s", k, out)
		}
	}
	if task["status"] != "queued" || task["applicationId"] != "app-3" || len(f.detached) != 0 {
		t.Fatalf("%s detached=%v", out, f.detached)
	}
	id := task["taskId"].(string)
	started, err := run(t, d, "start", id)
	if err != nil || !strings.Contains(started, `"status":"running"`) || len(f.detached) != 1 {
		t.Fatalf("%v %s %v", err, started, f.detached)
	}
	list, err := run(t, d, "list", "--application", "app-3")
	if err != nil || strings.Count(list, `"taskId"`) != 1 {
		t.Fatalf("%v %s", err, list)
	}
	if _, err := run(t, d, "status", "at-0000000000000000"); clicore.ExitCodeFor(err) != 2 {
		t.Fatalf("an unknown task is exit 2: %v", err)
	}
}

func TestUnknownKindIsExit3WithTheCode(t *testing.T) {
	d := deps(t, func() aitask.Launcher { return &fake{cwd: t.TempDir()} })
	out, err := run(t, d, "submit", "--kind", "nope", "--goal", "x")
	if clicore.ExitCodeFor(err) != 3 || !strings.Contains(out, `"code":"unknown_kind"`) {
		t.Fatalf("%v %s", err, out)
	}
}

// The real launcher's readiness check: coder mode is off until the user turns it
// on, and a submit then says so with the fallback and creates nothing.
func TestSubmitWithCoderDisabledSaysFallbackOneshotAndCreatesNothing(t *testing.T) {
	d := deps(t, nil)
	out, err := run(t, d, "submit", "--goal", "x")
	if clicore.ExitCodeFor(err) != 3 || !strings.Contains(out, `"code":"coder_disabled"`) || !strings.Contains(out, `"fallback":"oneshot"`) {
		t.Fatalf("%v %s", err, out)
	}
	db, _ := d.OpenDB()
	defer db.Close()
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM ai_tasks`).Scan(&n); err != nil && err != sql.ErrNoRows || n != 0 {
		t.Fatalf("rows = %d (%v)", n, err)
	}
}

func TestWaitTimesOutWithExit1(t *testing.T) {
	d := deps(t, func() aitask.Launcher { return &fake{cwd: t.TempDir()} })
	out, err := run(t, d, "submit", "--goal", "x")
	if err != nil {
		t.Fatal(err)
	}
	var task struct{ TaskID string }
	_ = json.Unmarshal([]byte(out), &task)
	out, err = run(t, d, "wait", task.TaskID, "--timeout", "50ms")
	if clicore.ExitCodeFor(err) != 1 || !strings.Contains(out, `"code":"wait_timeout"`) {
		t.Fatalf("%v %s", err, out)
	}
}
