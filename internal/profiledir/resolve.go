package profiledir

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/profileorder"
)

// The one rule that turns what a person typed into a profile id, shared by the
// CLI's --profile, the MCP `profile` argument and the HTTP API's path segment.

// ErrUnknownProfile means no profile has that id or name.
var ErrUnknownProfile = errors.New("profile not found (checked both id and name)")

// ErrAmbiguousProfile means a name is shared by several profiles. It is never
// resolved by guessing: the caller must pass the id.
var ErrAmbiguousProfile = errors.New("profile name matches several profiles: pass the id")

// AmbiguousError is the ErrAmbiguousProfile of one name, with the ids that share it.
type AmbiguousError struct {
	Name string
	IDs  []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("profile name %q matches %d profiles (%s): pass the id", e.Name, len(e.IDs), strings.Join(e.IDs, ", "))
}

// Is makes errors.Is(err, ErrAmbiguousProfile) true.
func (e *AmbiguousError) Is(target error) bool { return target == ErrAmbiguousProfile }

// Candidates are the profiles nameOrID can mean: the profile with exactly that
// id if there is one (the reserved machine scope is never a profile id), else
// every profile with that name, case-insensitively, oldest first. Empty input
// names nothing. More than one candidate means the name is ambiguous.
func Candidates(ctx context.Context, db *sql.DB, nameOrID string) ([]string, error) {
	if nameOrID == "" {
		return nil, nil
	}
	if nameOrID != MachineProfileID {
		var id string
		err := db.QueryRowContext(ctx, `SELECT id FROM profiles WHERE id = ?`, nameOrID).Scan(&id)
		if err == nil {
			return []string{id}, nil
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT id, created_at FROM profiles WHERE LOWER(name) = LOWER(?)`, nameOrID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found []createdRow
	for rows.Next() {
		var r createdRow
		var created sql.NullString
		if err := rows.Scan(&r.id, &created); err != nil {
			return nil, err
		}
		r.created = created.String
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	profileorder.Sort(found, func(r createdRow) string { return r.created }, func(r createdRow) string { return r.id })
	var ids []string
	for _, r := range found {
		ids = append(ids, r.id)
	}
	return ids, nil
}

// AuthCandidates is Candidates done with the same database work whatever
// nameOrID names, for a caller whose latency must not tell which profiles exist
// (the HTTP API's bearer check): one query reads every profile and the match is
// made here, by Candidates' rule. decoy is a profile that exists (the lowest
// id; "" when there are none), so that a caller answering an unknown name can
// spend on it the work a real candidate costs.
func AuthCandidates(ctx context.Context, db *sql.DB, nameOrID string) (ids []string, decoy string, err error) {
	rows, err := db.QueryContext(ctx, `SELECT id, name, created_at FROM profiles ORDER BY id`)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var byID string
	var named []createdRow
	want := sqliteLower(nameOrID)
	for rows.Next() {
		var id string
		var name, created sql.NullString
		if err := rows.Scan(&id, &name, &created); err != nil {
			return nil, "", err
		}
		if decoy == "" {
			decoy = id
		}
		if nameOrID == "" {
			continue
		}
		if id == nameOrID && nameOrID != MachineProfileID {
			byID = id
		}
		if name.Valid && sqliteLower(name.String) == want {
			named = append(named, createdRow{id: id, created: created.String})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if byID != "" {
		return []string{byID}, decoy, nil
	}
	profileorder.Sort(named, func(r createdRow) string { return r.created }, func(r createdRow) string { return r.id })
	for _, r := range named {
		ids = append(ids, r.id)
	}
	return ids, decoy, nil
}

// sqliteLower is SQLite's LOWER(): ASCII letters only, as Candidates' query
// compares names.
func sqliteLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// createdRow is a profile id with its stored created_at, read to be ordered
// by profileorder (created_at has two stored formats, so SQL cannot order it).
type createdRow struct{ id, created string }

// Resolve returns the id of the profile nameOrID names. It fails with
// ErrUnknownProfile when none does, with an *AmbiguousError (ErrAmbiguousProfile)
// when a name is shared, and with the database's error when the lookup fails.
func Resolve(ctx context.Context, db *sql.DB, nameOrID string) (string, error) {
	ids, err := Candidates(ctx, db, nameOrID)
	if err != nil {
		return "", err
	}
	switch len(ids) {
	case 0:
		return "", ErrUnknownProfile
	case 1:
		return ids[0], nil
	}
	return "", &AmbiguousError{Name: nameOrID, IDs: ids}
}

// Describe lists every profile as "name (id)", "Work (a1), Home (b2)", oldest
// first by profileorder (a missing name shows as empty; unlike List it skips
// nothing and does not fall back to the id). It is empty when there are no
// profiles or the lookup fails; a row that cannot be read is left out.
func Describe(ctx context.Context, db *sql.DB) string {
	rows, err := db.QueryContext(ctx, `SELECT id, name, created_at FROM profiles`)
	if err != nil {
		return ""
	}
	defer rows.Close()
	type described struct{ id, name, created string }
	var found []described
	for rows.Next() {
		var d described
		var name, created sql.NullString
		if rows.Scan(&d.id, &name, &created) == nil {
			d.name, d.created = name.String, created.String
			found = append(found, d)
		}
	}
	profileorder.Sort(found, func(d described) string { return d.created }, func(d described) string { return d.id })
	parts := make([]string, 0, len(found))
	for _, d := range found {
		parts = append(parts, fmt.Sprintf("%s (%s)", d.name, d.id))
	}
	return strings.Join(parts, ", ")
}
