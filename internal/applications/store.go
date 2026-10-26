// internal/applications/store.go
package applications

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Store provides CRUD operations for applications, backed by the tables
// created by data/migrations/031_applications.sql.
type Store struct {
	db *sql.DB
}

// NewStore creates a new Store backed by the given database.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func nullFloat(f *float64) interface{} {
	if f == nil {
		return nil
	}
	return *f
}

func nullBool(b *bool) interface{} {
	if b == nil {
		return nil
	}
	return *b
}

// validateAndDefault checks kind/detail consistency and required per-kind
// fields. A missing ProfileID is an error: there is no implicit profile. It
// does not touch the database.
func validateAndDefault(app *Application) error {
	if app.ProfileID == "" {
		return fmt.Errorf("%w: profile_id is required", ErrInvalidInput)
	}
	switch app.Kind {
	case KindJob:
		if app.Tender != nil {
			return fmt.Errorf("%w: kind is %q but tender details were supplied", ErrInvalidInput, KindJob)
		}
		if app.Job == nil {
			return fmt.Errorf("%w: kind %q requires job details", ErrInvalidInput, KindJob)
		}
		if app.Job.Title == "" {
			return fmt.Errorf("%w: job.title is required", ErrInvalidInput)
		}
		if app.Job.Company == "" {
			return fmt.Errorf("%w: job.company is required", ErrInvalidInput)
		}
		if app.Job.URL == "" {
			return fmt.Errorf("%w: job.url is required", ErrInvalidInput)
		}
	case KindTender:
		if app.Job != nil {
			return fmt.Errorf("%w: kind is %q but job details were supplied", ErrInvalidInput, KindTender)
		}
		if app.Tender == nil {
			return fmt.Errorf("%w: kind %q requires tender details", ErrInvalidInput, KindTender)
		}
		if app.Tender.Title == "" {
			return fmt.Errorf("%w: tender.title is required", ErrInvalidInput)
		}
		if app.Tender.IssuingOrg == "" {
			return fmt.Errorf("%w: tender.issuing_org is required", ErrInvalidInput)
		}
		if app.Tender.URL == "" {
			return fmt.Errorf("%w: tender.url is required", ErrInvalidInput)
		}
		if app.Tender.SubmissionDeadline == "" {
			return fmt.Errorf("%w: tender.submission_deadline is required", ErrInvalidInput)
		}
	default:
		return fmt.Errorf("%w: unknown kind %q (must be %q or %q)", ErrInvalidInput, app.Kind, KindJob, KindTender)
	}
	return nil
}

// Create inserts a new application plus its matching detail row and initial
// status-log entry, all in one transaction. On success app.ID, app.Status,
// app.CreatedAt, and app.UpdatedAt are populated.
func (s *Store) Create(ctx context.Context, app *Application) error {
	if err := validateAndDefault(app); err != nil {
		return err
	}

	app.ID = uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339)
	app.Status = StatusPending
	app.CreatedAt = now
	app.UpdatedAt = now

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("applications.Create: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO applications (id, profile_id, kind, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		app.ID, app.ProfileID, string(app.Kind), string(app.Status), app.CreatedAt, app.UpdatedAt,
	); err != nil {
		return fmt.Errorf("applications.Create: insert application: %w", err)
	}

	switch app.Kind {
	case KindJob:
		j := app.Job
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO job_details (application_id, title, company, url, location, description,
			        compensation_min, compensation_max, currency, job_type, is_remote, source, posted_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			app.ID, j.Title, j.Company, j.URL, j.Location, j.Description,
			nullFloat(j.CompensationMin), nullFloat(j.CompensationMax), j.Currency, j.JobType,
			nullBool(j.IsRemote), j.Source, j.PostedAt,
		); err != nil {
			return fmt.Errorf("applications.Create: insert job_details: %w", err)
		}
	case KindTender:
		td := app.Tender
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO tender_details (application_id, title, issuing_org, url, description,
			        submission_deadline, estimated_value, currency, required_certifications,
			        bid_documents_required, source, published_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			app.ID, td.Title, td.IssuingOrg, td.URL, td.Description, td.SubmissionDeadline,
			nullFloat(td.EstimatedValue), td.Currency, td.RequiredCertifications,
			td.BidDocumentsRequired, td.Source, td.PublishedAt,
		); err != nil {
			return fmt.Errorf("applications.Create: insert tender_details: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO application_status_log (id, application_id, from_status, to_status, actor, note, created_at)
		 VALUES (?, ?, NULL, ?, ?, ?, ?)`,
		uuid.NewString(), app.ID, string(StatusPending), string(ActorUser), "created", now,
	); err != nil {
		return fmt.Errorf("applications.Create: insert status log: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("applications.Create: commit: %w", err)
	}
	return nil
}

// allowedTransitions is the closed transition graph. rejected/cancelled
// have no outgoing edges (terminal).
var allowedTransitions = map[Status][]Status{
	StatusPending:        {StatusApplied, StatusCancelled, StatusAwaitingSubmit, StatusNeedsYou},
	StatusAwaitingSubmit: {StatusApplied, StatusCancelled, StatusNeedsYou},
	StatusNeedsYou:       {StatusAwaitingSubmit, StatusCancelled},
	StatusApplied:        {StatusRejected, StatusCancelled},
}

func isAllowedTransition(from, to Status) bool {
	for _, s := range allowedTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// SetStatus transitions an application's status, validating against the
// allowed transition graph and appending exactly one status_log row, both
// in a single transaction. Returns ErrNotFound if id/profileID doesn't
// match a row, ErrInvalidTransition if the edge isn't allowed.
func (s *Store) SetStatus(ctx context.Context, profileID, id string, to Status, actor Actor, note string) error {
	// BEGIN IMMEDIATE takes the write lock before the read: a deferred
	// transaction that reads and then writes is refused at once with
	// SQLITE_BUSY (the busy timeout does not apply) when another writer
	// commits in between, e.g. the fill queue while the person submits.
	tx, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("applications.SetStatus: begin tx: %w", err)
	}
	defer tx.Close()
	if err := beginImmediate(ctx, tx); err != nil {
		return fmt.Errorf("applications.SetStatus: begin tx: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if _, err := tx.ExecContext(context.Background(), `ROLLBACK`); err != nil {
			// The connection may still be inside the transaction: it must not go back to the pool.
			_ = tx.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()

	var current string
	err = tx.QueryRowContext(ctx,
		`SELECT status FROM applications WHERE id = ? AND profile_id = ?`, id, profileID,
	).Scan(&current)
	if err == sql.ErrNoRows {
		return fmt.Errorf("%w: application %q", ErrNotFound, id)
	}
	if err != nil {
		return fmt.Errorf("applications.SetStatus: lookup: %w", err)
	}

	from := Status(current)
	if to == StatusApplied && actor != ActorUser {
		return fmt.Errorf("%w: %q needs the user (application send)", ErrInvalidTransition, to)
	}
	if !isAllowedTransition(from, to) {
		return fmt.Errorf("%w: %q -> %q", ErrInvalidTransition, from, to)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx,
		`UPDATE applications SET status = ?, updated_at = ? WHERE id = ? AND profile_id = ?`,
		string(to), now, id, profileID,
	); err != nil {
		return fmt.Errorf("applications.SetStatus: update: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO application_status_log (id, application_id, from_status, to_status, actor, note, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		uuid.NewString(), id, string(from), string(to), string(actor), note, now,
	); err != nil {
		return fmt.Errorf("applications.SetStatus: insert status log: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("applications.SetStatus: commit: %w", err)
	}
	committed = true
	return nil
}

// StatusLog returns id's full transition history, oldest first. Returns
// ErrNotFound if id/profileID doesn't match a row.
func (s *Store) StatusLog(ctx context.Context, profileID, id string) ([]StatusLogEntry, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM applications WHERE id = ? AND profile_id = ?`, id, profileID,
	).Scan(&exists); err != nil {
		return nil, fmt.Errorf("applications.StatusLog: %w", err)
	}
	if exists == 0 {
		return nil, fmt.Errorf("%w: application %q", ErrNotFound, id)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, COALESCE(from_status, ''), to_status, actor, note, created_at
		 FROM application_status_log WHERE application_id = ? ORDER BY created_at ASC`, id)
	if err != nil {
		return nil, fmt.Errorf("applications.StatusLog: query: %w", err)
	}
	defer rows.Close()

	var out []StatusLogEntry
	for rows.Next() {
		var e StatusLogEntry
		var toStatus, actor string
		if err := rows.Scan(&e.ID, &e.FromStatus, &toStatus, &actor, &e.Note, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("applications.StatusLog: scan: %w", err)
		}
		e.ToStatus = Status(toStatus)
		e.Actor = Actor(actor)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("applications.StatusLog: rows: %w", err)
	}
	return out, nil
}

// AddTag attaches tag to id, scoped to profileID. Idempotent: adding an
// already-present tag is a no-op, not an error.
func (s *Store) AddTag(ctx context.Context, profileID, id, tag string) error {
	var exists int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM applications WHERE id = ? AND profile_id = ?`, id, profileID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("applications.AddTag: %w", err)
	}
	if exists == 0 {
		return fmt.Errorf("%w: application %q", ErrNotFound, id)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO application_tags (application_id, tag) VALUES (?, ?)`, id, tag,
	); err != nil {
		return fmt.Errorf("applications.AddTag: %w", err)
	}
	return nil
}

// RemoveTag detaches tag from id, scoped to profileID. A no-op if the tag
// wasn't present.
func (s *Store) RemoveTag(ctx context.Context, profileID, id, tag string) error {
	var exists int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM applications WHERE id = ? AND profile_id = ?`, id, profileID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("applications.RemoveTag: %w", err)
	}
	if exists == 0 {
		return fmt.Errorf("%w: application %q", ErrNotFound, id)
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM application_tags WHERE application_id = ? AND tag = ?`, id, tag,
	); err != nil {
		return fmt.Errorf("applications.RemoveTag: %w", err)
	}
	return nil
}

func floatPtr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	return &v.Float64
}

func boolPtr(v sql.NullBool) *bool {
	if !v.Valid {
		return nil
	}
	return &v.Bool
}

// Get returns the fully hydrated application (tags + matching detail
// struct) for id, scoped to profileID. Returns ErrNotFound if id doesn't
// exist under that profile.
func (s *Store) Get(ctx context.Context, profileID, id string) (*Application, error) {
	var app Application
	var kind, status string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, profile_id, kind, status, created_at, updated_at
		 FROM applications WHERE id = ? AND profile_id = ?`, id, profileID,
	).Scan(&app.ID, &app.ProfileID, &kind, &status, &app.CreatedAt, &app.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: application %q", ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("applications.Get: %w", err)
	}
	app.Kind = Kind(kind)
	app.Status = Status(status)

	switch app.Kind {
	case KindJob:
		var j JobDetails
		var compMin, compMax sql.NullFloat64
		var isRemote sql.NullBool
		err := s.db.QueryRowContext(ctx,
			`SELECT title, company, url, location, description, compensation_min, compensation_max,
			        currency, job_type, is_remote, source, posted_at
			 FROM job_details WHERE application_id = ?`, id,
		).Scan(&j.Title, &j.Company, &j.URL, &j.Location, &j.Description, &compMin, &compMax,
			&j.Currency, &j.JobType, &isRemote, &j.Source, &j.PostedAt)
		if err != nil {
			return nil, fmt.Errorf("applications.Get: job_details: %w", err)
		}
		j.CompensationMin = floatPtr(compMin)
		j.CompensationMax = floatPtr(compMax)
		j.IsRemote = boolPtr(isRemote)
		app.Job = &j
	case KindTender:
		var td TenderDetails
		var estValue sql.NullFloat64
		err := s.db.QueryRowContext(ctx,
			`SELECT title, issuing_org, url, description, submission_deadline, estimated_value,
			        currency, required_certifications, bid_documents_required, source, published_at
			 FROM tender_details WHERE application_id = ?`, id,
		).Scan(&td.Title, &td.IssuingOrg, &td.URL, &td.Description, &td.SubmissionDeadline, &estValue,
			&td.Currency, &td.RequiredCertifications, &td.BidDocumentsRequired, &td.Source, &td.PublishedAt)
		if err != nil {
			return nil, fmt.Errorf("applications.Get: tender_details: %w", err)
		}
		td.EstimatedValue = floatPtr(estValue)
		app.Tender = &td
	}

	tags, err := s.tagsFor(ctx, id)
	if err != nil {
		return nil, err
	}
	app.Tags = tags
	return &app, nil
}

// tagsFor returns id's tags, alphabetically sorted, or an empty (non-nil)
// slice if none.
func (s *Store) tagsFor(ctx context.Context, id string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT tag FROM application_tags WHERE application_id = ? ORDER BY tag ASC`, id)
	if err != nil {
		return nil, fmt.Errorf("applications.tagsFor: %w", err)
	}
	defer rows.Close()
	tags := []string{}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, fmt.Errorf("applications.tagsFor: scan: %w", err)
		}
		tags = append(tags, tag)
	}
	return tags, rows.Err()
}

// List returns applications for profileID matching filter, newest first.
// A zero-value ListFilter field matches any value. Never returns nil (an
// empty slice on no matches).
func (s *Store) List(ctx context.Context, profileID string, filter ListFilter) ([]Application, error) {
	query := `SELECT DISTINCT a.id FROM applications a`
	args := []interface{}{}
	where := []string{"a.profile_id = ?"}
	args = append(args, profileID)

	if filter.Tag != "" {
		query += ` JOIN application_tags t ON t.application_id = a.id`
		where = append(where, "t.tag = ?")
		args = append(args, filter.Tag)
	}
	if filter.Kind != "" {
		where = append(where, "a.kind = ?")
		args = append(args, string(filter.Kind))
	}
	if filter.Status != "" {
		where = append(where, "a.status = ?")
		args = append(args, string(filter.Status))
	}

	query += " WHERE " + where[0]
	for _, w := range where[1:] {
		query += " AND " + w
	}
	query += " ORDER BY a.created_at DESC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("applications.List: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("applications.List: scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("applications.List: rows: %w", err)
	}
	rows.Close()

	out := make([]Application, 0, len(ids))
	for _, id := range ids {
		app, err := s.Get(ctx, profileID, id)
		if err != nil {
			return nil, fmt.Errorf("applications.List: hydrating %s: %w", id, err)
		}
		out = append(out, *app)
	}
	return out, nil
}
