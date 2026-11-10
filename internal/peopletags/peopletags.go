// Package peopletags is how people get tags: find-or-create a profile's tag
// by name, link it to a person (at most MaxPerPerson each), unlink it, and
// recolour it. `monoagentcli people tag` is the surface; the GUI's People
// page calls that command for every change.
package peopletags

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/dbconn"
	"github.com/monoes/mono-agent/internal/profiledir"
)

// MaxPerPerson caps the tags on one person.
const MaxPerPerson = 10

// DefaultColor is a new tag's colour when none is given.
const DefaultColor = "#00b4d8"

var (
	// ErrNotFound reports a person or tag that isn't in the profile.
	ErrNotFound = errors.New("not found")
	// ErrInvalid reports a bad name or colour, or a person with no room.
	ErrInvalid = errors.New("invalid")
)

var colorRE = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)

// Tag is one of a profile's tags.
type Tag struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func checkColor(c string) error {
	if !colorRE.MatchString(c) {
		return fmt.Errorf("colour %q is not a #rgb, #rrggbb or #rrggbbaa hex colour: %w", c, ErrInvalid)
	}
	return nil
}

func personExists(ctx context.Context, q queryRower, profileID, personID string) error {
	var one int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM people WHERE id = ? AND profile_id = ?`, personID, profileID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("person %q: %w", personID, ErrNotFound)
	}
	return err
}

// Add links the profile's tag called name (case-insensitive) to a person,
// creating it when new. A colour given for an existing tag recolours it
// everywhere, which is what the People page offers when re-adding a tag.
func Add(ctx context.Context, db *sql.DB, profileID, personID, name, color string) (Tag, error) {
	if profileID == "" {
		return Tag{}, profiledir.ErrNoProfile
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Tag{}, fmt.Errorf("empty tag name: %w", ErrInvalid)
	}
	if color != "" {
		if err := checkColor(color); err != nil {
			return Tag{}, err
		}
	}
	// BEGIN IMMEDIATE: Add reads before it writes, and a writer that commits in between would make the write fail
	// at once with SQLITE_BUSY_SNAPSHOT, which busy_timeout does not wait for (#1460).
	var t Tag
	err := dbconn.WithWriteTx(ctx, db, func(tx *sql.Conn) (err error) {
		t, err = addTx(ctx, tx, profileID, personID, name, color)
		return err
	})
	if err != nil {
		return Tag{}, err
	}
	return t, nil
}

// addTx is Add's reads and writes, on the connection that holds the write lock.
func addTx(ctx context.Context, tx *sql.Conn, profileID, personID, name, color string) (Tag, error) {
	if err := personExists(ctx, tx, profileID, personID); err != nil {
		return Tag{}, err
	}

	t := Tag{Name: name}
	err := tx.QueryRowContext(ctx, `SELECT id, name, color FROM tags WHERE LOWER(name) = LOWER(?) AND profile_id = ?`,
		name, profileID).Scan(&t.ID, &t.Name, &t.Color)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		t.ID, t.Color = uuid.NewString(), color
		if t.Color == "" {
			t.Color = DefaultColor
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO tags(id, name, color, profile_id) VALUES(?,?,?,?)`,
			t.ID, t.Name, t.Color, profileID); err != nil {
			return Tag{}, fmt.Errorf("creating tag %q: %w", name, err)
		}
	case err != nil:
		return Tag{}, err
	case color != "" && !strings.EqualFold(color, t.Color):
		if _, err := tx.ExecContext(ctx, `UPDATE tags SET color = ? WHERE id = ? AND profile_id = ?`, color, t.ID, profileID); err != nil {
			return Tag{}, err
		}
		t.Color = color
	}

	var linked int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM people_tags WHERE person_id = ? AND tag_id = ?`,
		personID, t.ID).Scan(&linked); err != nil {
		return Tag{}, err
	}
	if linked == 0 {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM people_tags WHERE person_id = ?`, personID).Scan(&count); err != nil {
			return Tag{}, err
		}
		if count >= MaxPerPerson {
			return Tag{}, fmt.Errorf("person %q already has %d tags, the most allowed: %w", personID, count, ErrInvalid)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO people_tags(person_id, tag_id) VALUES(?,?)`, personID, t.ID); err != nil {
			return Tag{}, fmt.Errorf("tagging %s: %w", personID, err)
		}
	}
	return t, nil
}

// Remove unlinks a tag (by id or name) from a person; the tag itself stays.
func Remove(ctx context.Context, db *sql.DB, profileID, personID, tag string) error {
	if profileID == "" {
		return profiledir.ErrNoProfile
	}
	if err := personExists(ctx, db, profileID, personID); err != nil {
		return err
	}
	t, err := Find(ctx, db, profileID, tag)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `DELETE FROM people_tags WHERE person_id = ? AND tag_id = ?`, personID, t.ID)
	return err
}

// SetColor recolours a profile's tag (by id or name).
func SetColor(ctx context.Context, db *sql.DB, profileID, tag, color string) (Tag, error) {
	if profileID == "" {
		return Tag{}, profiledir.ErrNoProfile
	}
	if err := checkColor(color); err != nil {
		return Tag{}, err
	}
	t, err := Find(ctx, db, profileID, tag)
	if err != nil {
		return t, err
	}
	if _, err := db.ExecContext(ctx, `UPDATE tags SET color = ? WHERE id = ? AND profile_id = ?`, color, t.ID, profileID); err != nil {
		return t, err
	}
	t.Color = color
	return t, nil
}

// Find resolves a profile's tag by id, else by name (case-insensitive).
func Find(ctx context.Context, db *sql.DB, profileID, tag string) (Tag, error) {
	var t Tag
	err := db.QueryRowContext(ctx, `SELECT id, name, color FROM tags
		WHERE profile_id = ? AND (id = ? OR LOWER(name) = LOWER(?))
		ORDER BY id = ? DESC LIMIT 1`, profileID, tag, tag, tag).Scan(&t.ID, &t.Name, &t.Color)
	if errors.Is(err, sql.ErrNoRows) {
		return t, fmt.Errorf("tag %q: %w", tag, ErrNotFound)
	}
	return t, err
}

// List returns the profile's tags, or one person's when personID is set,
// ordered by name.
func List(ctx context.Context, db *sql.DB, profileID, personID string) ([]Tag, error) {
	query := `SELECT id, name, color FROM tags WHERE profile_id = ? ORDER BY name COLLATE NOCASE`
	args := []any{profileID}
	if personID != "" {
		if err := personExists(ctx, db, profileID, personID); err != nil {
			return nil, err
		}
		query = `SELECT t.id, t.name, t.color FROM tags t JOIN people_tags pt ON pt.tag_id = t.id
			WHERE pt.person_id = ? AND t.profile_id = ? ORDER BY t.name COLLATE NOCASE`
		args = []any{personID, profileID}
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Tag{}
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Color); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ByPerson returns the tags of each of the given people, keyed by person id,
// each list ordered by name. People not in the profile, or without tags, are
// left out. It is one query, for bulk-loading a page of people.
func ByPerson(ctx context.Context, db *sql.DB, profileID string, personIDs []string) (map[string][]Tag, error) {
	out := map[string][]Tag{}
	if len(personIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(personIDs)+2)
	for _, id := range personIDs {
		args = append(args, id)
	}
	args = append(args, profileID, profileID)
	rows, err := db.QueryContext(ctx, `
		SELECT pt.person_id, t.id, t.name, t.color
		FROM people_tags pt
		JOIN tags t ON t.id = pt.tag_id
		JOIN people p ON pt.person_id = p.id
		WHERE pt.person_id IN (?`+strings.Repeat(",?", len(personIDs)-1)+`) AND t.profile_id = ? AND p.profile_id = ?
		ORDER BY t.name COLLATE NOCASE`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var pid string
		var t Tag
		if err := rows.Scan(&pid, &t.ID, &t.Name, &t.Color); err != nil {
			return nil, err
		}
		out[pid] = append(out[pid], t)
	}
	return out, rows.Err()
}
