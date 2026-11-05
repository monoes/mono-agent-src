// Package evaluation runs n8n-style evaluations of a workflow: every row of
// the dataset its trigger.evaluation node names runs the workflow once (one
// execution of trigger type workflow.TriggerTypeEvaluation per row), the
// workflow's core.evaluation nodes record the case's inputs, outputs and
// metrics, and the run keeps the mean of each metric. Runs and their cases
// live in evaluation_runs and evaluation_results (migration 097).
package evaluation

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/dbconn"
	"github.com/monoes/mono-agent/internal/profiledir"
)

// Run statuses.
const (
	RunRunning   = "running"
	RunCompleted = "completed"
	RunError     = "error"
	RunCancelled = "cancelled"
)

// Case statuses.
const (
	CaseNew       = "new"
	CaseRunning   = "running"
	CaseSuccess   = "success"
	CaseError     = "error"
	CaseCancelled = "cancelled"
)

// Run error codes (n8n's test run error codes).
const (
	ErrTriggerNotFound      = "EVALUATION_TRIGGER_NOT_FOUND"
	ErrTriggerNotConfigured = "EVALUATION_TRIGGER_NOT_CONFIGURED"
	ErrTriggerDisabled      = "EVALUATION_TRIGGER_DISABLED"
	ErrMetricsNodeNotFound  = "SET_METRICS_NODE_NOT_FOUND"
	ErrMetricsNotConfigured = "SET_METRICS_NODE_NOT_CONFIGURED"
	ErrOutputsNotConfigured = "SET_OUTPUTS_NODE_NOT_CONFIGURED"
	ErrCantFetchTestCases   = "CANT_FETCH_TEST_CASES"
	ErrTestCasesNotFound    = "TEST_CASES_NOT_FOUND"
	ErrInterrupted          = "INTERRUPTED"
	ErrUnknown              = "UNKNOWN_ERROR"
	ErrFailedToExecute      = "FAILED_TO_EXECUTE_WORKFLOW"
	ErrNoMetricsCollected   = "NO_METRICS_COLLECTED"
	ErrInvalidMetrics       = "INVALID_METRICS"
)

// Kinds of data Record merges into a case.
const (
	KindInputs  = "inputs"
	KindOutputs = "outputs"
	KindMetrics = "metrics"
)

const timeLayout = "2006-01-02T15:04:05.000Z07:00"

// Run is one evaluation run of a workflow.
type Run struct {
	ID           string                 `json:"id"`
	WorkflowID   string                 `json:"workflow_id"`
	Status       string                 `json:"status"`
	ErrorCode    string                 `json:"error_code,omitempty"`
	ErrorMessage string                 `json:"error_message,omitempty"`
	Dataset      map[string]interface{} `json:"dataset"`
	// Metrics is the mean of each metric over the cases that succeeded.
	Metrics    map[string]float64 `json:"metrics"`
	CreatedAt  string             `json:"created_at"`
	FinishedAt string             `json:"finished_at,omitempty"`
	// CaseCount and Failed count the run's cases and those that did not
	// succeed.
	CaseCount int    `json:"case_count"`
	Failed    int    `json:"failed"`
	Cases     []Case `json:"cases,omitempty"`
}

// Case is one dataset row of a run.
type Case struct {
	ID           string                 `json:"id"`
	Position     int                    `json:"position"`
	RowID        string                 `json:"row_id,omitempty"`
	RowNumber    int                    `json:"row_number"`
	ExecutionID  string                 `json:"execution_id,omitempty"`
	Status       string                 `json:"status"`
	ErrorCode    string                 `json:"error_code,omitempty"`
	ErrorMessage string                 `json:"error_message,omitempty"`
	Inputs       map[string]interface{} `json:"inputs"`
	Outputs      map[string]interface{} `json:"outputs"`
	Metrics      map[string]interface{} `json:"metrics"`
	FinishedAt   string                 `json:"finished_at,omitempty"`
}

// Store reads and writes a profile's evaluation runs.
type Store struct {
	db      *sql.DB
	profile string
}

// NewStore returns the store of profile's runs in db. An empty profile is
// kept as is: every method then returns profiledir.ErrNoProfile.
func NewStore(db *sql.DB, profile string) *Store {
	return &Store{db: db, profile: profile}
}

func now() string { return time.Now().UTC().Format(timeLayout) }

func newID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func encode(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return "{}"
	}
	return string(b)
}

// CreateRun starts a run of workflowID over dataset (a description of where
// the rows came from).
func (s *Store) CreateRun(ctx context.Context, workflowID string, dataset map[string]interface{}) (*Run, error) {
	if s.profile == "" {
		return nil, profiledir.ErrNoProfile
	}
	r := &Run{ID: newID("evr_"), WorkflowID: workflowID, Status: RunRunning, Dataset: dataset,
		Metrics: map[string]float64{}, CreatedAt: now()}
	_, err := s.db.ExecContext(ctx, `INSERT INTO evaluation_runs (id, profile_id, workflow_id, status, dataset, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, r.ID, s.profile, workflowID, r.Status, encode(dataset), r.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("evaluation: create run: %w", err)
	}
	return r, nil
}

// FinishRun records a run's end: its status, error and metrics.
func (s *Store) FinishRun(ctx context.Context, r *Run) error {
	if s.profile == "" {
		return profiledir.ErrNoProfile
	}
	r.FinishedAt = now()
	_, err := s.db.ExecContext(ctx, `UPDATE evaluation_runs SET status = ?, error_code = ?, error_message = ?,
		metrics = ?, finished_at = ? WHERE id = ? AND profile_id = ?`,
		r.Status, r.ErrorCode, r.ErrorMessage, encode(r.Metrics), r.FinishedAt, r.ID, s.profile)
	if err != nil {
		return fmt.Errorf("evaluation: finish run: %w", err)
	}
	return nil
}

// AddCase adds the case of a dataset row to run r.
func (s *Store) AddCase(ctx context.Context, runID string, c *Case) error {
	if s.profile == "" {
		return profiledir.ErrNoProfile
	}
	c.ID = newID("evc_")
	c.Status = CaseNew
	c.Inputs, c.Outputs, c.Metrics = map[string]interface{}{}, map[string]interface{}{}, map[string]interface{}{}
	_, err := s.db.ExecContext(ctx, `INSERT INTO evaluation_results (id, run_id, position, row_id, row_number, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, c.ID, runID, c.Position, c.RowID, c.RowNumber, c.Status, now())
	if err != nil {
		return fmt.Errorf("evaluation: add case: %w", err)
	}
	return nil
}

// UpdateCase writes a case's execution, status and error; finished stamps
// its end.
func (s *Store) UpdateCase(ctx context.Context, c *Case, finished bool) error {
	if s.profile == "" {
		return profiledir.ErrNoProfile
	}
	var fin interface{}
	if finished {
		c.FinishedAt = now()
		fin = c.FinishedAt
	}
	_, err := s.db.ExecContext(ctx, `UPDATE evaluation_results SET execution_id = ?, status = ?, error_code = ?,
		error_message = ?, finished_at = COALESCE(?, finished_at) WHERE id = ?`,
		c.ExecutionID, c.Status, c.ErrorCode, c.ErrorMessage, fin, c.ID)
	if err != nil {
		return fmt.Errorf("evaluation: update case: %w", err)
	}
	return nil
}

// afterRecordRead is a test hook: it runs in Record between the read of the case's column and the write.
var afterRecordRead func()

// Record merges data into a case's inputs, outputs or metrics (kind). Keys
// the case already has keep their value: as in n8n, the first node to set
// a name wins. It is what core.evaluation calls while a case runs; the case
// must belong to runID.
func Record(ctx context.Context, db *sql.DB, runID, caseID, kind string, data map[string]interface{}) error {
	col := map[string]string{KindInputs: "inputs", KindOutputs: "outputs", KindMetrics: "metrics"}[kind]
	if col == "" {
		return fmt.Errorf("evaluation: record: unknown kind %q", kind)
	}
	// BEGIN IMMEDIATE: the merge reads before it writes, and a writer that commits in between would make the write
	// fail at once with SQLITE_BUSY_SNAPSHOT, which busy_timeout does not wait for (#1459).
	return dbconn.WithWriteTx(ctx, db, func(conn *sql.Conn) error {
		var raw string
		err := conn.QueryRowContext(ctx, `SELECT `+col+` FROM evaluation_results WHERE id = ? AND run_id = ?`, caseID, runID).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("evaluation: record: case %s of run %s not found", caseID, runID)
		}
		if err != nil {
			return fmt.Errorf("evaluation: record: %w", err)
		}
		if afterRecordRead != nil {
			afterRecordRead()
		}
		cur := map[string]interface{}{}
		_ = json.Unmarshal([]byte(raw), &cur)
		for k, v := range data {
			if _, set := cur[k]; !set {
				cur[k] = v
			}
		}
		if _, err := conn.ExecContext(ctx, `UPDATE evaluation_results SET `+col+` = ? WHERE id = ?`, encode(cur), caseID); err != nil {
			return fmt.Errorf("evaluation: record: %w", err)
		}
		return nil
	})
}

// LoadCase reads a case's recorded inputs, outputs and metrics into c.
func (s *Store) LoadCase(ctx context.Context, c *Case) error {
	if s.profile == "" {
		return profiledir.ErrNoProfile
	}
	var in, out, met string
	err := s.db.QueryRowContext(ctx, `SELECT inputs, outputs, metrics FROM evaluation_results WHERE id = ?`, c.ID).Scan(&in, &out, &met)
	if err != nil {
		return fmt.Errorf("evaluation: load case: %w", err)
	}
	c.Inputs, c.Outputs, c.Metrics = decode(in), decode(out), decode(met)
	return nil
}

func decode(raw string) map[string]interface{} {
	m := map[string]interface{}{}
	_ = json.Unmarshal([]byte(raw), &m)
	return m
}

// ListRuns returns workflowID's runs, newest first, without their cases.
func (s *Store) ListRuns(ctx context.Context, workflowID string) ([]Run, error) {
	if s.profile == "" {
		return nil, profiledir.ErrNoProfile
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.id, r.workflow_id, r.status, r.error_code, r.error_message, r.dataset,
		r.metrics, r.created_at, COALESCE(r.finished_at, ''),
		(SELECT COUNT(*) FROM evaluation_results c WHERE c.run_id = r.id),
		(SELECT COUNT(*) FROM evaluation_results c WHERE c.run_id = r.id AND c.status != 'success')
		FROM evaluation_runs r WHERE r.profile_id = ? AND r.workflow_id = ? ORDER BY r.created_at DESC, r.rowid DESC`,
		s.profile, workflowID)
	if err != nil {
		return nil, fmt.Errorf("evaluation: list runs: %w", err)
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanRun(sc interface{ Scan(...interface{}) error }) (Run, error) {
	var r Run
	var dataset, metrics string
	if err := sc.Scan(&r.ID, &r.WorkflowID, &r.Status, &r.ErrorCode, &r.ErrorMessage, &dataset, &metrics,
		&r.CreatedAt, &r.FinishedAt, &r.CaseCount, &r.Failed); err != nil {
		return r, fmt.Errorf("evaluation: read run: %w", err)
	}
	r.Dataset = decode(dataset)
	r.Metrics = map[string]float64{}
	_ = json.Unmarshal([]byte(metrics), &r.Metrics)
	return r, nil
}

// GetRun returns a run with its cases, or nil when the profile has none
// with that id.
func (s *Store) GetRun(ctx context.Context, id string) (*Run, error) {
	if s.profile == "" {
		return nil, profiledir.ErrNoProfile
	}
	r, err := scanRun(s.db.QueryRowContext(ctx, `SELECT r.id, r.workflow_id, r.status, r.error_code, r.error_message,
		r.dataset, r.metrics, r.created_at, COALESCE(r.finished_at, ''),
		(SELECT COUNT(*) FROM evaluation_results c WHERE c.run_id = r.id),
		(SELECT COUNT(*) FROM evaluation_results c WHERE c.run_id = r.id AND c.status != 'success')
		FROM evaluation_runs r WHERE r.id = ? AND r.profile_id = ?`, id, s.profile))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, position, row_id, row_number, execution_id, status, error_code,
		error_message, inputs, outputs, metrics, COALESCE(finished_at, '') FROM evaluation_results
		WHERE run_id = ? ORDER BY position`, id)
	if err != nil {
		return nil, fmt.Errorf("evaluation: read cases: %w", err)
	}
	defer rows.Close()
	r.Cases = []Case{}
	for rows.Next() {
		var c Case
		var in, out, met string
		if err := rows.Scan(&c.ID, &c.Position, &c.RowID, &c.RowNumber, &c.ExecutionID, &c.Status, &c.ErrorCode,
			&c.ErrorMessage, &in, &out, &met, &c.FinishedAt); err != nil {
			return nil, fmt.Errorf("evaluation: read case: %w", err)
		}
		c.Inputs, c.Outputs, c.Metrics = decode(in), decode(out), decode(met)
		r.Cases = append(r.Cases, c)
	}
	return &r, rows.Err()
}
