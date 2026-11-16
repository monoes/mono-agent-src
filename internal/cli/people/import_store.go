package people

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"strings"
)

// maxImportFileBytes bounds a people or messages import file; the whole file
// is read and parsed in memory.
const maxImportFileBytes = 64 << 20

// readImportFile reads an import file, refusing one above maxImportFileBytes
// before it is read into memory.
func readImportFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("reading file %s: %w", path, err)
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > maxImportFileBytes {
		return nil, fmt.Errorf("file %s is %d MiB, above the %d MiB import limit; split it into smaller files", path, st.Size()>>20, maxImportFileBytes>>20)
	}
	// The size may change after the stat (or the file is not regular).
	data, err := io.ReadAll(io.LimitReader(f, maxImportFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading file %s: %w", path, err)
	}
	if len(data) > maxImportFileBytes {
		return nil, fmt.Errorf("file %s is above the %d MiB import limit; split it into smaller files", path, maxImportFileBytes>>20)
	}
	return data, nil
}

// importColumns maps the JSON key of a field to its people column. A key a row
// names overwrites the stored value, an empty or zero one included (that is how
// an import clears it); a key the row leaves out keeps the stored value.
var importColumns = []struct{ key, col string }{
	{"full_name", "full_name"},
	{"image_url", "image_url"},
	{"contact_details", "contact_details"},
	{"website", "website"},
	{"content_count", "content_count"},
	{"follower_count", "follower_count"},
	{"following_count", "following_count"},
	{"introduction", "introduction"},
	{"is_verified", "is_verified"},
	{"category", "category"},
	{"job_title", "job_title"},
}

// upsertPerson inserts one person, or on a conflict updates only the columns
// the row named. args are the 17 insert values, in column order.
func upsertPerson(tx *sql.Tx, raw map[string]interface{}, args []interface{}) error {
	set := []string{"updated_at = excluded.updated_at"}
	for _, c := range importColumns {
		if _, named := raw[c.key]; named {
			set = append(set, c.col+" = excluded."+c.col)
		}
	}
	_, err := tx.Exec(
		`INSERT INTO people (id, platform_username, platform, full_name, image_url,
		        contact_details, website, content_count, follower_count,
		        following_count, introduction, is_verified, category, job_title,
		        profile_id, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(platform_username, platform, profile_id)
		 DO UPDATE SET `+strings.Join(set, ", "), args...)
	return err
}
