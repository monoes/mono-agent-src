// internal/applications/assets.go

package applications

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Caps on what a fill record may hold. A value past a cap is refused, not cut.
const (
	MaxFillFields     = 200
	MaxFieldLabelLen  = 200
	MaxFieldValueLen  = 4000
	MaxFieldSourceLen = 200
	MaxNoteLen        = 2000
	MaxRefIDLen       = 200
)

// RedactedValue replaces a field value that looks like a secret.
const RedactedValue = "[redacted]"

// LinkKind is what an application_links row points at.
type LinkKind string

const (
	LinkPerson   LinkKind = "person"
	LinkMessage  LinkKind = "message"
	LinkTask     LinkKind = "task"
	LinkDocument LinkKind = "document"
	LinkCapture  LinkKind = "capture"
	LinkHIL      LinkKind = "hil"
	// LinkConversation is the application's coder task chat; at most one per application.
	LinkConversation LinkKind = "conversation"
)

// ValidLinkKind reports whether k is one of the link kinds.
func ValidLinkKind(k LinkKind) bool {
	switch k {
	case LinkPerson, LinkMessage, LinkTask, LinkDocument, LinkCapture, LinkHIL, LinkConversation:
		return true
	}
	return false
}

// Link ties an application to a person, message, task, document or capture.
type Link struct {
	Kind  LinkKind `json:"kind"`
	RefID string   `json:"refId"`
}

// FillStatus is where the last form fill stands.
type FillStatus string

const (
	FillNone      FillStatus = "none"
	FillFilling   FillStatus = "filling"
	FillFilled    FillStatus = "filled"
	FillNeedsYou  FillStatus = "needs_you"
	FillSubmitted FillStatus = "submitted"
)

// ValidFillStatus reports whether s is a recordable fill status.
func ValidFillStatus(s FillStatus) bool {
	switch s {
	case FillFilling, FillFilled, FillNeedsYou, FillSubmitted:
		return true
	}
	return false
}

// FillField is one form field a fill wrote.
type FillField struct {
	Label     string `json:"label"`
	Value     string `json:"value"`
	Source    string `json:"source"`
	Generated bool   `json:"generated"`
}

// Fill is the last recorded form fill of an application.
type Fill struct {
	Status     FillStatus  `json:"status"`
	Screenshot string      `json:"screenshot"`
	UpdatedAt  string      `json:"updatedAt"`
	Fields     []FillField `json:"fields"`
	Note       string      `json:"note"`
}

var (
	secretLabel = regexp.MustCompile(`(?i)pass(word|code|phrase)?\b|secret|token|api[ _-]?key|(^|[^a-z])otp([^a-z]|$)|one[ _-]?time[ _-]?(code|pass)|(^|[^a-z])2fa([^a-z]|$)|(^|[^a-z])cvv([^a-z]|$)|(^|[^a-z])cvc([^a-z]|$)|card[ _-]?number|\bssn\b|private[ _-]?key`)
	secretValue = regexp.MustCompile(`^(sk-|ghp_|gho_|xox[bap]-|AKIA|eyJ|Bearer\s)|-----BEGIN|^[A-Za-z0-9+/_=-]{40,}$`)
)

// looksSecret reports whether a field with this label and value should not be stored.
func looksSecret(label, value string) bool {
	return secretLabel.MatchString(label) || secretValue.MatchString(strings.TrimSpace(value))
}

func (s *Store) requireApplication(ctx context.Context, profileID, id string) error {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM applications WHERE id = ? AND profile_id = ?`, id, profileID).Scan(&one)
	if err == sql.ErrNoRows {
		return fmt.Errorf("%w: application %q", ErrNotFound, id)
	}
	if err != nil {
		return fmt.Errorf("applications: lookup %q: %w", id, err)
	}
	return nil
}

func validRef(ref string) bool {
	return ref != "" && len(ref) <= MaxRefIDLen && !strings.ContainsAny(ref, "\x00\r\n")
}

// AddLink links kind/refID to the application. Adding the same link twice is a no-op.
func (s *Store) AddLink(ctx context.Context, profileID, id string, kind LinkKind, refID string) error {
	if !ValidLinkKind(kind) || !validRef(refID) {
		return fmt.Errorf("%w: link %q %q", ErrInvalidInput, kind, refID)
	}
	if err := s.requireApplication(ctx, profileID, id); err != nil {
		return err
	}
	if kind == LinkConversation {
		var other string
		err := s.db.QueryRowContext(ctx,
			`SELECT ref_id FROM application_links WHERE application_id = ? AND kind = 'conversation'`, id).Scan(&other)
		if err == nil && other != refID {
			return fmt.Errorf("%w: application already has a conversation link", ErrInvalidInput)
		}
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO application_links (application_id, kind, ref_id, profile_id, source, created_at) VALUES (?, ?, ?, ?, 'cli', ?)`,
		id, string(kind), refID, profileID, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("applications.AddLink: %w", err)
	}
	return nil
}

// RemoveLink removes a link; it reports whether one existed.
func (s *Store) RemoveLink(ctx context.Context, profileID, id string, kind LinkKind, refID string) (bool, error) {
	if !ValidLinkKind(kind) || !validRef(refID) {
		return false, fmt.Errorf("%w: link %q %q", ErrInvalidInput, kind, refID)
	}
	if err := s.requireApplication(ctx, profileID, id); err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM application_links WHERE application_id = ? AND kind = ? AND ref_id = ?`, id, string(kind), refID)
	if err != nil {
		return false, fmt.Errorf("applications.RemoveLink: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListLinks returns the application's links, oldest first. kind "" means all kinds.
func (s *Store) ListLinks(ctx context.Context, profileID, id string, kind LinkKind) ([]Link, error) {
	if kind != "" && !ValidLinkKind(kind) {
		return nil, fmt.Errorf("%w: link kind %q", ErrInvalidInput, kind)
	}
	if err := s.requireApplication(ctx, profileID, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT kind, ref_id FROM application_links WHERE application_id = ? AND profile_id = ? AND (? = '' OR kind = ?)
		 ORDER BY created_at, rowid`, id, profileID, string(kind), string(kind))
	if err != nil {
		return nil, fmt.Errorf("applications.ListLinks: %w", err)
	}
	defer rows.Close()
	links := []Link{}
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.Kind, &l.RefID); err != nil {
			return nil, fmt.Errorf("applications.ListLinks: scan: %w", err)
		}
		links = append(links, l)
	}
	return links, rows.Err()
}

// RecordFill replaces the application's fill record. Over-long input is refused;
// a field whose label or value looks like a secret is stored as RedactedValue.
func (s *Store) RecordFill(ctx context.Context, profileID, id string, f Fill) error {
	if !ValidFillStatus(f.Status) {
		return fmt.Errorf("%w: fill status %q", ErrInvalidInput, f.Status)
	}
	if len(f.Note) > MaxNoteLen || len(f.Fields) > MaxFillFields {
		return fmt.Errorf("%w: note or field count over the cap", ErrInvalidInput)
	}
	fields := make([]FillField, len(f.Fields))
	for i, fl := range f.Fields {
		if fl.Label == "" || len(fl.Label) > MaxFieldLabelLen || len(fl.Value) > MaxFieldValueLen || len(fl.Source) > MaxFieldSourceLen {
			return fmt.Errorf("%w: field %d label/value/source empty or over the cap", ErrInvalidInput, i)
		}
		if looksSecret(fl.Label, fl.Value) {
			fl.Value = RedactedValue
		}
		fields[i] = fl
	}
	if err := s.requireApplication(ctx, profileID, id); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("applications.RecordFill: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO application_fills (application_id, status, screenshot_path, note, updated_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(application_id) DO UPDATE SET status = excluded.status, screenshot_path = excluded.screenshot_path,
		   note = excluded.note, updated_at = excluded.updated_at`,
		id, string(f.Status), f.Screenshot, f.Note, now); err != nil {
		return fmt.Errorf("applications.RecordFill: upsert: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM application_fill_fields WHERE application_id = ?`, id); err != nil {
		return fmt.Errorf("applications.RecordFill: clear fields: %w", err)
	}
	for _, fl := range fields {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO application_fill_fields (application_id, label, value, source, generated) VALUES (?, ?, ?, ?, ?)`,
			id, fl.Label, fl.Value, fl.Source, fl.Generated); err != nil {
			return fmt.Errorf("applications.RecordFill: field: %w", err)
		}
	}
	return tx.Commit()
}

// GetFill returns the application's fill record; Status FillNone when nothing was recorded.
func (s *Store) GetFill(ctx context.Context, profileID, id string) (Fill, error) {
	f := Fill{Status: FillNone, Fields: []FillField{}}
	if err := s.requireApplication(ctx, profileID, id); err != nil {
		return f, err
	}
	err := s.db.QueryRowContext(ctx,
		`SELECT status, screenshot_path, note, updated_at FROM application_fills WHERE application_id = ?`, id,
	).Scan(&f.Status, &f.Screenshot, &f.Note, &f.UpdatedAt)
	if err == sql.ErrNoRows {
		f.Status = FillNone
		return f, nil
	}
	if err != nil {
		return f, fmt.Errorf("applications.GetFill: %w", err)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT label, value, source, generated FROM application_fill_fields WHERE application_id = ? ORDER BY rowid`, id)
	if err != nil {
		return f, fmt.Errorf("applications.GetFill: fields: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var fl FillField
		if err := rows.Scan(&fl.Label, &fl.Value, &fl.Source, &fl.Generated); err != nil {
			return f, fmt.Errorf("applications.GetFill: scan: %w", err)
		}
		f.Fields = append(f.Fields, fl)
	}
	return f, rows.Err()
}
