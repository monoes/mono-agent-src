// Package doctags is how profile documents get tags: find-or-create a
// profile's tag by name and link it to a document (at most MaxPerDocument
// each), unlink it, list a document's tags, and find documents by tag. Tags
// are the same rows people use (see peopletags). `monoagentcli profile
// documents tag` is the surface.
package doctags

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/peopletags"
	"github.com/monoes/mono-agent/internal/profiledir"
)

// MaxPerDocument caps the tags on one document.
const MaxPerDocument = 10

// Tag is a profile tag; documents and people share them.
type Tag = peopletags.Tag

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func documentExists(ctx context.Context, q queryRower, profileID, docID string) error {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM vault_documents WHERE id = ? AND profile_id = ?`, docID, profileID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("document %q: %w", docID, peopletags.ErrNotFound)
	}
	return err
}

// ErrTooManyTags reports that a tag would be new and the profile already holds the most the
// caller allows.
var ErrTooManyTags = errors.New("too many tags")

// Add links the profile's tag called name (case-insensitive) to a document,
// creating it when new. Linking twice is a no-op.
func Add(ctx context.Context, db *sql.DB, profileID, docID, name string) (Tag, error) {
	return AddLimited(ctx, db, profileID, docID, name, 0)
}

// AddLimited is Add that makes no new tag once the profile holds maxTags of them (0 is no
// limit; an existing tag is still linked): ErrTooManyTags. The count and the insert are one
// transaction that takes the write lock first (BEGIN IMMEDIATE on a connection of its own),
// so concurrent callers take turns and cannot overshoot the limit, and none of them fails on
// a read lock that cannot become a write lock.
func AddLimited(ctx context.Context, db *sql.DB, profileID, docID, name string, maxTags int) (Tag, error) {
	if profileID == "" {
		return Tag{}, profiledir.ErrNoProfile
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Tag{}, fmt.Errorf("empty tag name: %w", peopletags.ErrInvalid)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return Tag{}, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return Tag{}, err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
			// The connection may still be inside the transaction: it must not go back to the pool.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if err := documentExists(ctx, conn, profileID, docID); err != nil {
		return Tag{}, err
	}

	t := Tag{Name: name}
	err = conn.QueryRowContext(ctx, `SELECT id, name, color FROM tags WHERE LOWER(name) = LOWER(?) AND profile_id = ?`,
		name, profileID).Scan(&t.ID, &t.Name, &t.Color)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if maxTags > 0 {
			var have int
			if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM tags WHERE profile_id = ?`, profileID).Scan(&have); err != nil {
				return Tag{}, err
			}
			if have >= maxTags {
				return Tag{}, fmt.Errorf("the profile has %d tags, the most allowed here: %w", have, ErrTooManyTags)
			}
		}
		t.ID, t.Color = uuid.NewString(), peopletags.DefaultColor
		if _, err := conn.ExecContext(ctx, `INSERT INTO tags(id, name, color, profile_id) VALUES(?,?,?,?)`,
			t.ID, t.Name, t.Color, profileID); err != nil {
			return Tag{}, fmt.Errorf("creating tag %q: %w", name, err)
		}
	case err != nil:
		return Tag{}, err
	}

	var linked, count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM document_tags WHERE document_id = ? AND tag_id = ?`,
		docID, t.ID).Scan(&linked); err != nil {
		return Tag{}, err
	}
	if linked == 0 {
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM document_tags WHERE document_id = ?`, docID).Scan(&count); err != nil {
			return Tag{}, err
		}
		if count >= MaxPerDocument {
			return Tag{}, fmt.Errorf("document %q already has %d tags, the most allowed: %w", docID, count, peopletags.ErrInvalid)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO document_tags(document_id, tag_id) VALUES(?,?)`, docID, t.ID); err != nil {
			return Tag{}, fmt.Errorf("tagging %s: %w", docID, err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return Tag{}, err
	}
	committed = true
	return t, nil
}

// Remove unlinks a tag (by id or name) from a document; the tag itself stays.
func Remove(ctx context.Context, db *sql.DB, profileID, docID, tag string) error {
	if profileID == "" {
		return profiledir.ErrNoProfile
	}
	if err := documentExists(ctx, db, profileID, docID); err != nil {
		return err
	}
	t, err := peopletags.Find(ctx, db, profileID, tag)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `DELETE FROM document_tags WHERE document_id = ? AND tag_id = ?`, docID, t.ID)
	return err
}

// List returns one document's tags, ordered by name.
func List(ctx context.Context, db *sql.DB, profileID, docID string) ([]Tag, error) {
	if err := documentExists(ctx, db, profileID, docID); err != nil {
		return nil, err
	}
	byDoc, err := ByDocument(ctx, db, profileID, []string{docID})
	if err != nil {
		return nil, err
	}
	if t := byDoc[docID]; t != nil {
		return t, nil
	}
	return []Tag{}, nil
}

// ByDocument returns the tags of each of the given documents, keyed by
// document id, each list ordered by name. Documents without tags are left
// out. With no ids it returns every tagged document of the profile.
func ByDocument(ctx context.Context, db *sql.DB, profileID string, docIDs []string) (map[string][]Tag, error) {
	out := map[string][]Tag{}
	query := `SELECT dt.document_id, t.id, t.name, t.color
		FROM document_tags dt
		JOIN tags t ON t.id = dt.tag_id
		JOIN vault_documents d ON d.id = dt.document_id
		WHERE t.profile_id = ? AND d.profile_id = ?`
	args := []any{profileID, profileID}
	if len(docIDs) > 0 {
		query += ` AND dt.document_id IN (?` + strings.Repeat(",?", len(docIDs)-1) + `)`
		for _, id := range docIDs {
			args = append(args, id)
		}
	}
	rows, err := db.QueryContext(ctx, query+` ORDER BY t.name COLLATE NOCASE`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var t Tag
		if err := rows.Scan(&id, &t.ID, &t.Name, &t.Color); err != nil {
			return nil, err
		}
		out[id] = append(out[id], t)
	}
	return out, rows.Err()
}

// Search returns the ids of the profile's documents that carry every one of
// the given tags (by id or name, case-insensitive), oldest first. A tag that
// doesn't exist matches nothing.
func Search(ctx context.Context, db *sql.DB, profileID string, tags []string) ([]string, error) {
	if len(tags) == 0 {
		return nil, fmt.Errorf("no tags to search for: %w", peopletags.ErrInvalid)
	}
	args := []any{profileID}
	seen := map[string]bool{}
	for _, name := range tags {
		t, err := peopletags.Find(ctx, db, profileID, name)
		if errors.Is(err, peopletags.ErrNotFound) {
			return []string{}, nil
		}
		if err != nil {
			return nil, err
		}
		if !seen[t.ID] {
			seen[t.ID] = true
			args = append(args, t.ID)
		}
	}
	n := len(seen)
	args = append(args, n)
	rows, err := db.QueryContext(ctx, `SELECT d.id FROM vault_documents d
		JOIN document_tags dt ON dt.document_id = d.id
		WHERE d.profile_id = ? AND dt.tag_id IN (?`+strings.Repeat(",?", n-1)+`)
		GROUP BY d.id HAVING COUNT(DISTINCT dt.tag_id) = ?
		ORDER BY d.seq`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
