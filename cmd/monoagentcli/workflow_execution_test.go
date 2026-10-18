//go:build !windows

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/storage"
)

// seedExecutionDB is a migrated DB holding workflow w1 (profile default)
// and w2 (profile other), each with executions.
func seedExecutionDB(t *testing.T) (*globalConfig, *storage.Database) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MONOAGENT_DAEMON_HEARTBEAT", filepath.Join(t.TempDir(), "hb.json"))
	dbPath := filepath.Join(t.TempDir(), "e.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name, created_at) VALUES ('other', 'Other', '2026-01-01');
		INSERT INTO workflows (id, name, is_active, profile_id) VALUES ('w1','A',1,'default'),('w2','B',1,'other');
		INSERT INTO workflow_executions (id, workflow_id, status, trigger_type, profile_id, started_at, created_at) VALUES
		('e-done','w1','SUCCESS','trigger.manual','default','2026-09-26 10:00:00','2026-09-26 10:00:00'),
		('e-theirs','w2','RUNNING','trigger.manual','other','2026-09-26 10:00:00','2026-09-26 10:00:00')`); err != nil {
		t.Fatal(err)
	}
	return &globalConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "default"}, db
}

func TestWorkflowExecutionDetail(t *testing.T) {
	cfg, db := seedExecutionDB(t)
	if _, err := db.DB.Exec(`INSERT INTO workflow_execution_nodes
		(id, execution_id, node_id, node_name, status, input_items, output_items, started_at) VALUES
		('en1','e-done','n1','Get Secret','SUCCESS','not an item array','[{"json":{"api_key":"sk-live-secret","label":"not secret"}}]','2026-09-26 10:00:01')`); err != nil {
		t.Fatal(err)
	}

	out, err := runWorkflowCmdIn(t, cfg, "", "execution", "e-done")
	if err != nil {
		t.Fatal(err)
	}
	var d executionDetailJSON
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if d.ID != "e-done" || d.WorkflowID != "w1" || d.Status != "SUCCESS" || d.TriggerType != "trigger.manual" ||
		d.StartedAt != "2026-09-26 10:00:00" || d.FinishedAt != "" || len(d.Nodes) != 1 {
		t.Fatalf("detail = %+v", d)
	}
	n := d.Nodes[0]
	if n.NodeName != "Get Secret" || strings.Contains(string(n.OutputItems), "sk-live-secret") ||
		!strings.Contains(string(n.OutputItems), "***") || !strings.Contains(string(n.OutputItems), "not secret") {
		t.Errorf("output items not redacted: %s", n.OutputItems)
	}
	if string(n.InputItems) != `"not an item array"` {
		t.Errorf("unparseable items must be kept as a string, got %s", n.InputItems)
	}

	if _, err := runWorkflowCmdIn(t, cfg, "", "execution", "e-theirs"); exitCode(err) != 2 {
		t.Errorf("another profile's execution: exit %d, want 2", exitCode(err))
	}
	if _, err := runWorkflowCmdIn(t, cfg, "", "execution", "nope"); exitCode(err) != 2 {
		t.Errorf("unknown execution: exit %d, want 2", exitCode(err))
	}
}

// startMonoagentLookalike runs a symlink to sleep whose command line contains
// "monoagent", so signalWorkflowPID accepts it as one of ours.
func startMonoagentLookalike(t *testing.T) *exec.Cmd {
	t.Helper()
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	bin := filepath.Join(t.TempDir(), "monoagentcli-lookalike")
	// Keep the system binary intact: copying a signed executable can be
	// rejected by macOS while a symlink retains our identifying command line.
	if err := os.Symlink(sleepBin, bin); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start %s: %v", bin, err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	waitForExec(t, cmd.Process.Pid)
	return cmd
}

// waitForExec blocks until pid's command line is readable: Start returns
// after fork but possibly before exec, when it still reads back empty.
func waitForExec(t *testing.T, pid int) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if _, alive, err := readProcessCommandLine(pid); err == nil && alive {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pid %d never became inspectable", pid)
}

func addRunningExecution(t *testing.T, db *storage.Database, id string, pid int) {
	t.Helper()
	if _, err := db.DB.Exec(`INSERT INTO workflow_executions (id, workflow_id, status, profile_id, pid) VALUES (?, 'w1', 'RUNNING', 'default', ?)`,
		id, pid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, profile_id) VALUES (?, ?, 'w1', 'n', 'N', 'pending', 'default')`,
		"h-"+id, id); err != nil {
		t.Fatal(err)
	}
}

func executionStatus(t *testing.T, db *storage.Database, id string) (status, hil string) {
	t.Helper()
	_ = db.DB.QueryRow(`SELECT status FROM workflow_executions WHERE id = ?`, id).Scan(&status)
	_ = db.DB.QueryRow(`SELECT status FROM hil_pending WHERE execution_id = ?`, id).Scan(&hil)
	return status, hil
}

func cancelJSON(t *testing.T, cfg *globalConfig, id string) (cancelResultJSON, error) {
	t.Helper()
	out, err := runWorkflowCmdIn(t, cfg, "", "cancel", id)
	var res cancelResultJSON
	if err == nil {
		if jerr := json.Unmarshal([]byte(out), &res); jerr != nil {
			t.Fatalf("%v\n%s", jerr, out)
		}
	}
	return res, err
}

// Cancelling a running execution SIGTERMs its process, marks it CANCELLED
// and rejects its pending reviews.
func TestWorkflowCancelSignalsAndMarks(t *testing.T) {
	cfg, db := seedExecutionDB(t)
	proc := startMonoagentLookalike(t)
	addRunningExecution(t, db, "e-run", proc.Process.Pid)

	res, err := cancelJSON(t, cfg, "e-run")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Signalled || res.PreviousStatus != "RUNNING" || res.Status != "CANCELLED" || res.WorkflowID != "w1" {
		t.Fatalf("cancel = %+v", res)
	}
	if st, hil := executionStatus(t, db, "e-run"); st != "CANCELLED" || hil != "rejected" {
		t.Fatalf("status %q, review %q", st, hil)
	}
	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the execution's process was not stopped")
	}
}

// The daemon stamps its own pid on the executions it runs in-process;
// cancelling one of those must never signal the daemon.
func TestWorkflowCancelSparesTheDaemon(t *testing.T) {
	cfg, db := seedExecutionDB(t)
	proc := startMonoagentLookalike(t)
	if err := daemonhb.Write(daemonhb.Heartbeat{PID: proc.Process.Pid, TS: time.Now()}); err != nil {
		t.Fatal(err)
	}
	addRunningExecution(t, db, "e-sched", proc.Process.Pid)

	res, err := cancelJSON(t, cfg, "e-sched")
	if err != nil {
		t.Fatal(err)
	}
	if res.Signalled || res.Status != "CANCELLED" {
		t.Fatalf("cancel = %+v", res)
	}
	time.Sleep(100 * time.Millisecond)
	if _, alive, _ := readProcessCommandLine(proc.Process.Pid); !alive {
		t.Fatal("cancel signalled the daemon")
	}
}

// A pid that now belongs to some other program is refused, and the
// execution is left as it was.
func TestWorkflowCancelRefusesForeignProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signals")
	}
	cfg, db := seedExecutionDB(t)
	sleep := exec.Command("sleep", "30")
	if err := sleep.Start(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = sleep.Process.Kill(); _ = sleep.Wait() })
	waitForExec(t, sleep.Process.Pid)
	addRunningExecution(t, db, "e-reused", sleep.Process.Pid)

	if _, err := cancelJSON(t, cfg, "e-reused"); err == nil || !strings.Contains(err.Error(), "refusing to signal non-monoagent process") {
		t.Fatalf("err = %v", err)
	}
	if st, hil := executionStatus(t, db, "e-reused"); st != "RUNNING" || hil != "pending" {
		t.Fatalf("refused cancel changed the execution: %q, %q", st, hil)
	}
}

// A dead pid is nothing to signal; a finished execution is left alone; an
// unknown or another profile's execution is not found.
func TestWorkflowCancelEdgeCases(t *testing.T) {
	cfg, db := seedExecutionDB(t)
	gone := exec.Command("sleep", "0")
	if err := gone.Run(); err != nil {
		t.Skip(err)
	}
	addRunningExecution(t, db, "e-orphan", gone.Process.Pid)
	if res, err := cancelJSON(t, cfg, "e-orphan"); err != nil || res.Signalled || res.Status != "CANCELLED" {
		t.Fatalf("dead pid: %+v, %v", res, err)
	}

	if res, err := cancelJSON(t, cfg, "e-done"); err != nil || res.Status != "SUCCESS" {
		t.Fatalf("finished execution: %+v, %v", res, err)
	}
	if st, _ := executionStatus(t, db, "e-done"); st != "SUCCESS" {
		t.Fatalf("finished execution rewritten to %q", st)
	}

	for _, id := range []string{"e-theirs", "nope"} {
		if _, err := cancelJSON(t, cfg, id); exitCode(err) != 2 {
			t.Errorf("%s: exit %d, want 2", id, exitCode(err))
		}
	}
	if st, _ := executionStatus(t, db, "e-theirs"); st != "RUNNING" {
		t.Errorf("another profile's execution changed to %q", st)
	}
}

// readProcessCommandLine sees the calling process.
func TestReadProcessCommandLineSelf(t *testing.T) {
	cmdline, alive, err := readProcessCommandLine(os.Getpid())
	if err != nil || !alive || cmdline == "" {
		t.Fatalf("self: %q, alive=%v, %v", cmdline, alive, err)
	}
}
