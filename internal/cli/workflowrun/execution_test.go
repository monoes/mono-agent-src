//go:build !windows

package workflowrun

import (
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/storage"
)

// seedExecutionDB is a migrated DB holding workflow w1 (profile default)
// and w2 (profile other), each with executions.
func seedExecutionDB(t *testing.T) (*testConfig, *storage.Database) {
	t.Helper()
	testhome.SetHome(t, t.TempDir())
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
	return &testConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "default"}, db
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

// A failed run's detail carries the error class of the run and of the
// failed node, with the node's fix hint.
func TestWorkflowExecutionDetailErrorClass(t *testing.T) {
	cfg, db := seedExecutionDB(t)
	if _, err := db.DB.Exec(`INSERT INTO workflow_executions (id, workflow_id, status, trigger_type, profile_id, error_message, error_class, created_at)
		VALUES ('e-bad','w1','FAILED','trigger.manual','default','status 429','rate_limited','2026-09-26 10:00:00');
		INSERT INTO workflow_execution_nodes (id, execution_id, node_id, node_name, status, error_message, error_class, error_hint, started_at)
		VALUES ('en2','e-bad','n1','Call API','FAILED','status 429','rate_limited','wait and retry','2026-09-26 10:00:01')`); err != nil {
		t.Fatal(err)
	}
	out, err := runWorkflowCmdIn(t, cfg, "", "execution", "e-bad")
	if err != nil {
		t.Fatal(err)
	}
	var d executionDetailJSON
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if d.ErrorClass != "rate_limited" || len(d.Nodes) != 1 || d.Nodes[0].ErrorClass != "rate_limited" || d.Nodes[0].ErrorHint != "wait and retry" {
		t.Fatalf("detail = %+v", d)
	}

	// A REJECTED run is finished: cancel leaves it alone.
	if _, err := db.DB.Exec(`UPDATE workflow_executions SET status = 'REJECTED' WHERE id = 'e-bad'`); err != nil {
		t.Fatal(err)
	}
	if res, err := cancelJSON(t, cfg, "e-bad"); err != nil || res.Status != "REJECTED" {
		t.Fatalf("rejected execution: %+v, %v", res, err)
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
	waitForExec(t, cmd.Process.Pid, filepath.Base(bin))
	return cmd
}

// waitForExec blocks until pid's command line contains want. Readable alone
// is not enough: between fork and exec the child reads back as a copy of
// this test binary, whose name also contains "monoagent". The wait is
// bounded by a deadline, not an iteration count, so a loaded machine only
// makes it slower. Only Linux reads the full argv, so elsewhere (where `ps
// comm=` shows whatever the OS reports) readable is all that is checked.
func waitForExec(t *testing.T, pid int, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cmd, alive, err := readProcessCommandLine(pid); err == nil && alive && (runtime.GOOS != "linux" || strings.Contains(cmd, want)) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pid %d never showed %q in its command line", pid, want)
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

func cancelJSON(t *testing.T, cfg *testConfig, id string) (cancelResultJSON, error) {
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
	// Wait is the only way to observe the exit (a dead child stays a zombie
	// until reaped); the bound is generous so a loaded machine can't flake it.
	done := make(chan error, 1)
	go func() { done <- proc.Wait() }()
	select {
	case <-done:
		ws, ok := proc.ProcessState.Sys().(syscall.WaitStatus)
		if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
			t.Fatalf("the process was not ended by SIGTERM: %v", proc.ProcessState)
		}
	case <-time.After(30 * time.Second):
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
	cfg, db := seedExecutionDB(t)
	sleep := exec.Command("sleep", "30")
	if err := sleep.Start(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = sleep.Process.Kill(); _ = sleep.Wait() })
	waitForExec(t, sleep.Process.Pid, "sleep")
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

// A run paused at core.wait (resume webhook or form) or at a node's reply
// links can only be resumed through its URL, so the detail names it.
func TestWorkflowExecutionDetailShowsResumeURL(t *testing.T) {
	cfg, db := seedExecutionDB(t)
	// The user's own terminal: an agent doesn't get a reply opened before
	// its decision was recorded (execution_reply_test.go).
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
	t.Setenv("MONOAGENT_PUBLIC_URL", "http://127.0.0.1:19721")
	if _, err := db.DB.Exec(`INSERT INTO workflow_executions (id, workflow_id, status, trigger_type, profile_id, created_at) VALUES
		('e-wait','w1','WAITING','trigger.manual','default','2026-09-26 10:00:00');
		INSERT INTO wait_replies (execution_id, node_id, run_index, workflow_id, token, kind, spec) VALUES
		('e-wait','wait',0,'w1','tokWait','webhook','{"kind":"webhook","execution_url":true}'),
		('e-wait','ask',0,'w1','tokAsk','approval','{"kind":"approval","approval_type":"double"}'),
		('e-done','wait',0,'w1','tokOld','webhook','{"kind":"webhook"}')`); err != nil {
		t.Fatal(err)
	}

	out, err := runWorkflowCmdIn(t, cfg, "", "execution", "e-wait")
	if err != nil {
		t.Fatal(err)
	}
	var d executionDetailJSON
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(d.Waiting) != 2 || d.Waiting[0].NodeID != "wait" || d.Waiting[0].URL != "http://127.0.0.1:19721/reply/tokWait" ||
		d.Waiting[1].Kind != "approval" || len(d.Waiting[1].Links) != 2 ||
		d.Waiting[1].Links[1].URL != "http://127.0.0.1:19721/reply/tokAsk/approve" {
		t.Fatalf("waiting_for = %+v", d.Waiting)
	}

	cfg.JSONOutput = false
	out, err = runWorkflowCmdIn(t, cfg, "", "execution", "e-wait")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Waiting at wait for a webhook reply: http://127.0.0.1:19721/reply/tokWait",
		"Approve: http://127.0.0.1:19721/reply/tokAsk/approve", "Disapprove: http://127.0.0.1:19721/reply/tokAsk/decline"} {
		if !strings.Contains(out, want) {
			t.Errorf("text detail lacks %q:\n%s", want, out)
		}
	}

	// A finished run's leftover reply is not offered.
	cfg.JSONOutput = true
	out, err = runWorkflowCmdIn(t, cfg, "", "execution", "e-done")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "waiting_for") {
		t.Errorf("finished execution shows waiting_for:\n%s", out)
	}
}
