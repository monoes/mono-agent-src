package daemon

// Workflow-run memory (#38): feeds workflow outcomes into the profile
// knowledge graph (internal/monomind RunMemory). Inside `monoagentcli daemon`
// the engine's workflow.OnExecutionFinished hook re-evaluates a workflow as
// each of its runs finishes, and a sweeper reads the runs finished since a
// stored watermark (runs other processes finished) and ingests the workflows
// whose outcome changed.
// `monoagentcli config set memory.runs off` turns all of it off.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/jevcli"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

const (
	// runMemoryFirstSweep lets the daemon finish start-up (migrations,
	// recovery of stale runs) before the first sweep.
	runMemoryFirstSweep = 30 * time.Second
	runMemorySweepEvery = 5 * time.Minute
)

var runMemoryOnce sync.Once

// runMemoryStore opens the workflow store the run memory reads definitions from (the CLI's
// openWorkflowStore), set by StartRunMemory.
var runMemoryStore func(*storage.Database) *workflow.HybridWorkflowStore

// StartRunMemory starts the sweeper when args (the process's arguments without its name) run
// `monoagentcli daemon` (not its install/uninstall/restart subcommands). It opens its own
// handle on the database the daemon uses and lives as long as the process. openStore opens
// the workflow store. The CLI calls it once cobra has initialized.
func StartRunMemory(args []string, openStore func(*storage.Database) *workflow.HybridWorkflowStore) {
	runMemoryOnce.Do(func() {
		dbPath, ok := daemonInvocation(args)
		if !ok {
			return
		}
		runMemoryStore = openStore
		// Live updates from the daemon's engine; the sweeper catches up on
		// runs finished by other processes (one-shot `workflow run`).
		workflow.OnExecutionFinished = runMemoryExecutionFinished
		go runMemorySweeper(context.Background(), clicore.ExpandPath(dbPath))
	})
}

func runMemorySweeper(ctx context.Context, dbPath string) {
	t := time.NewTimer(runMemoryFirstSweep)
	defer t.Stop()
	var db *storage.Database
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if db == nil {
			d, err := storage.NewDatabase(dbPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: run memory: open database: %v\n", err)
				t.Reset(runMemorySweepEvery)
				continue
			}
			db = d
			defer db.Close()
		}
		if err := runMemoryFor(db.DB).Sweep(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "warning: run memory: %v\n", err)
		}
		t.Reset(runMemorySweepEvery)
	}
}

// runMemoryExecutionFinished is the daemon's workflow.OnExecutionFinished
// handler: it re-evaluates the workflow of executionID. Safe to call from a
// goroutine.
func runMemoryExecutionFinished(ctx context.Context, db *sql.DB, executionID, status string) {
	if db == nil || executionID == "" {
		return
	}
	var workflowID string
	if err := db.QueryRowContext(ctx, `SELECT workflow_id FROM workflow_executions WHERE id = ?`, executionID).Scan(&workflowID); err != nil {
		return
	}
	_ = runMemoryFor(db).Observe(ctx, workflowID)
}

var runMemories sync.Map // *sql.DB → *monomind.RunMemory

// runMemoryFor returns the one RunMemory per database, so concurrent hooks
// and sweeps serialise on its lock.
func runMemoryFor(db *sql.DB) *monomind.RunMemory {
	if m, ok := runMemories.Load(db); ok {
		return m.(*monomind.RunMemory)
	}
	m := &monomind.RunMemory{
		DB:     db,
		Source: newRunMemorySource(db, runMemoryStore(&storage.Database{DB: db})),
		// The same redaction the CLI applies to node errors it sends to Jev.
		Redact: jevcli.RedactRetryError,
	}
	actual, _ := runMemories.LoadOrStore(db, m)
	return actual.(*monomind.RunMemory)
}

// daemonInvocation reports whether args run `daemon` itself and returns the
// --db-path it uses.
func daemonInvocation(args []string) (dbPath string, ok bool) {
	dbPath = "~/.monoagent/monoagent.db"
	valueFlags := map[string]bool{"db-path": true, "output-dir": true, "config-dir": true, "workers": true,
		"log-file": true, "profile": true, "lang": true, "api-addr": true, "v1-addr": true}
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !hasVal && valueFlags[name] && i+1 < len(args) {
			i++
			val, hasVal = args[i], true
		}
		if name == "db-path" && hasVal {
			dbPath = val
		}
	}
	return dbPath, len(positional) == 1 && positional[0] == "daemon"
}

// runMemorySource implements monomind.RunSource over the workflow store
// (definitions may live in the file store) and the executions tables.
type runMemorySource struct {
	db    *sql.DB
	store interface {
		GetWorkflow(ctx context.Context, id string) (*workflow.Workflow, error)
	}
	classOnce sync.Once
	hasClass  bool
}

func newRunMemorySource(db *sql.DB, store interface {
	GetWorkflow(ctx context.Context, id string) (*workflow.Workflow, error)
}) *runMemorySource {
	return &runMemorySource{db: db, store: store}
}

func (s *runMemorySource) Workflow(ctx context.Context, id string) (*monomind.RunWorkflow, error) {
	w, err := s.store.GetWorkflow(ctx, id)
	if err != nil || w == nil {
		return nil, err
	}
	rw := &monomind.RunWorkflow{ID: w.ID, Name: w.Name, Description: w.Description, ProfileID: w.ProfileID, UpdatedAt: w.UpdatedAt}
	for _, n := range w.Nodes {
		rw.NodeTypes = append(rw.NodeTypes, n.Type)
	}
	return rw, nil
}

const runMemoryTerminal = `('SUCCESS','FAILED','SUCCESS_WITH_ERRORS','CANCELLED')`

// finishedJulian normalises the three stored timestamp shapes (see
// internal/summary sinceExpr); every engine timestamp is UTC.
const finishedJulian = `julianday(replace(substr(finished_at,1,19),'T',' '))`

func (s *runMemorySource) RecentRuns(ctx context.Context, workflowID string, n int) ([]monomind.RunRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, status, COALESCE(`+finishedJulian+`, 0), COALESCE(error_message,'')
		FROM workflow_executions WHERE workflow_id = ? AND status IN `+runMemoryTerminal+`
		ORDER BY created_at DESC LIMIT ?`, workflowID, n)
	if err != nil {
		return nil, err
	}
	var out []monomind.RunRecord
	for rows.Next() {
		var r monomind.RunRecord
		var jd float64
		if err := rows.Scan(&r.ExecutionID, &r.Status, &jd, &r.Error); err != nil {
			rows.Close()
			return nil, err
		}
		r.FinishedAt = fromJulian(jd)
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Status == "SUCCESS" || out[i].Status == "CANCELLED" {
			continue
		}
		if err := s.failedNode(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// failedNode fills the first failed node's name, error and (when the engine
// records one) error class.
func (s *runMemorySource) failedNode(ctx context.Context, r *monomind.RunRecord) error {
	s.classOnce.Do(func() { s.hasClass = s.columnExists(ctx, "workflow_execution_nodes", "error_class") })
	class := `''`
	if s.hasClass {
		class = `COALESCE(error_class,'')`
	}
	var name, msg, cls string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(node_name,''), COALESCE(error_message,''), `+class+`
		FROM workflow_execution_nodes WHERE execution_id = ? AND status = 'FAILED' ORDER BY rowid LIMIT 1`,
		r.ExecutionID).Scan(&name, &msg, &cls)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	r.FailedNode, r.ErrorClass = name, cls
	if msg != "" {
		r.Error = msg
	}
	return nil
}

func (s *runMemorySource) columnExists(ctx context.Context, table, column string) bool {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil && name == column {
			return true
		}
	}
	return false
}

func (s *runMemorySource) FinishedSince(ctx context.Context, since time.Time) ([]monomind.FinishedRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT workflow_id, COALESCE(profile_id,'default'), MAX(`+finishedJulian+`)
		FROM workflow_executions
		WHERE finished_at IS NOT NULL AND status IN `+runMemoryTerminal+` AND `+finishedJulian+` >= julianday(?)
		GROUP BY workflow_id, profile_id`, since.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []monomind.FinishedRun
	for rows.Next() {
		var f monomind.FinishedRun
		var jd float64
		if err := rows.Scan(&f.WorkflowID, &f.ProfileID, &jd); err != nil {
			return nil, err
		}
		f.FinishedAt = fromJulian(jd)
		out = append(out, f)
	}
	return out, rows.Err()
}

// fromJulian converts an SQLite julianday to a UTC time, to the second.
func fromJulian(jd float64) time.Time {
	if jd <= 0 {
		return time.Time{}
	}
	secs := (jd - 2440587.5) * 86400
	return time.Unix(int64(secs+0.5), 0).UTC()
}
