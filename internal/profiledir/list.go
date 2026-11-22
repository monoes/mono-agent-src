package profiledir

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/monoes/mono-agent/internal/profileorder"
)

// Listing the profiles a user actually has.
//
// The filesystem half of this package answers "where does profile X live";
// this answers "which profiles are there", for the surfaces that have to
// offer a choice — the extension's profile picker (profile.list on the
// request channel) and `capture list --profile`. Both need the same three
// things: the id, the name a person recognises, and which one a chooser
// preselects. That "default" is only the oldest profile (or the browser's
// bound one, see internal/extension), never a profile named or seeded as
// "Default": a new database has no profile at all.

// Profile is one profile as a chooser shows it. ID is the value that must
// travel on a capture envelope; Name is the label.
type Profile struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

// List returns every profile, oldest first, with exactly one marked
// Default: the oldest (created_at, then id). There is no active profile, so
// nothing is read from settings; the marker only gives a chooser something to
// preselect. A database with no profiles
// yields an empty list and no error — that is a legitimate state (a fresh
// install), not a failure.
//
// Ids that could not be used as a path component (see ValidProfileID) are
// skipped: nothing should be able to offer one as a capture destination.
// The CLI mints UUIDs, so this only ever fires on a hand-edited database.
func List(ctx context.Context, db *sql.DB) ([]Profile, error) {
	if db == nil {
		return nil, fmt.Errorf("profiledir: no database")
	}

	rows, err := db.QueryContext(ctx, `SELECT id, name, created_at FROM profiles`)
	if err != nil {
		return nil, fmt.Errorf("profiledir: list profiles: %w", err)
	}
	defer rows.Close()

	out := []Profile{}
	created := map[string]string{}
	for rows.Next() {
		var p Profile
		var name, createdAt sql.NullString
		if err := rows.Scan(&p.ID, &name, &createdAt); err != nil {
			continue
		}
		if !ValidProfileID(p.ID) {
			continue
		}
		p.Name = name.String
		if p.Name == "" {
			p.Name = p.ID
		}
		created[p.ID] = createdAt.String
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("profiledir: list profiles: %w", err)
	}

	sort.SliceStable(out, func(i, j int) bool {
		return profileorder.Older(created[out[i].ID], out[i].ID, created[out[j].ID], out[j].ID)
	})
	if len(out) > 0 {
		out[0].Default = true
	}
	return out, nil
}
