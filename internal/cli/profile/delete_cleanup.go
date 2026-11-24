package profile

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/attachments"
	"github.com/monoes/mono-agent/internal/storage"
)

// pruneBackups removes the old pre-delete snapshots (paths oldest first, as
// ProfileDeleteBackups lists them) except current, and returns how many.
func pruneBackups(paths []string, current string, now time.Time) int {
	n, _ := storage.PruneProfileDeleteBackupFiles(paths, current, now)
	return n
}

// attachmentPaths reads the files the messages' metadata list as saved
// attachments, for the messages that satisfy where.
func attachmentPaths(ctx context.Context, db *sql.DB, where string, arg string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT metadata FROM person_messages WHERE `+where+` AND metadata LIKE '%"attachments"%'`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var raw sql.NullString
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var md struct {
			Attachments []struct {
				Path string `json:"path"`
			} `json:"attachments"`
		}
		if json.Unmarshal([]byte(raw.String), &md) != nil {
			continue
		}
		for _, a := range md.Attachments {
			if a.Path != "" {
				out[filepath.Clean(a.Path)] = true
			}
		}
	}
	return out, rows.Err()
}

// profileAttachments lists the synced attachment files only this profile's
// messages point at, and that sit in the attachments folder.
func profileAttachments(ctx context.Context, db *sql.DB, profileID string) ([]string, error) {
	mine, err := attachmentPaths(ctx, db, `profile_id = ?`, profileID)
	if err != nil {
		return nil, err
	}
	others, err := attachmentPaths(ctx, db, `profile_id <> ?`, profileID)
	if err != nil {
		return nil, err
	}
	root := filepath.Clean(attachments.Dir()) + string(filepath.Separator)
	var files []string
	for p := range mine {
		if strings.HasPrefix(p, root) && !others[p] {
			files = append(files, p)
		}
	}
	sort.Strings(files)
	return files, nil
}

// unreferenced drops from files every one a remaining message points at.
// The list was made before the delete committed; a sync that saved the same
// file for another profile's message since must not lose it.
func unreferenced(ctx context.Context, db *sql.DB, files []string) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	still, err := attachmentPaths(ctx, db, `id <> ?`, "")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range files {
		if !still[filepath.Clean(f)] {
			out = append(out, f)
		}
	}
	return out, nil
}

// plainFileUnder is true when f is a regular file (not a symlink) whose
// folder, with links resolved, is inside root. A link planted in the
// attachments folder must not turn a delete into one elsewhere.
func plainFileUnder(root, f string) bool {
	fi, err := os.Lstat(f)
	if err != nil {
		return true // gone already: Remove reports nothing for it
	}
	if !fi.Mode().IsRegular() {
		return false
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(f))
	if err != nil || root == "" {
		return false
	}
	return strings.HasPrefix(dir+string(filepath.Separator), filepath.Clean(root)+string(filepath.Separator))
}

// removeAttachments deletes the files, and the per-message folder that held
// them once empty. It returns how many files went and a warning per failure.
func removeAttachments(files []string) (int, []string) {
	n := 0
	var warnings []string
	root, _ := filepath.EvalSymlinks(attachments.Dir())
	for _, f := range files {
		if !plainFileUnder(root, f) {
			continue
		}
		if err := os.Remove(f); err != nil {
			if !os.IsNotExist(err) {
				warnings = append(warnings, "attachment "+f+" was not removed: "+err.Error())
			}
			continue
		}
		n++
		_ = os.Remove(filepath.Dir(f)) // only succeeds when empty
	}
	return n, warnings
}
