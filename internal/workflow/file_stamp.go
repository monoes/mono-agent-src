package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StampFileWorkflowProfiles gives every workflow file in dir whose profile_id
// is missing or empty an owner. ownerOf, when not nil, names the owner the
// workflow's SQLite mirror row already records (empty when there is no row or
// it has no owner); that owner wins. Otherwise the file goes to fallbackID,
// the owner legacy data inherits (see storage.Database.LegacyOwnerProfile).
// It is the file-store half of migration 099: with no fallback to 'default'
// anywhere, an ownerless file would otherwise belong to no profile at all.
// A file with no resolvable owner is left alone.
//
// Each file is rewritten atomically (temp file in the same directory, then
// rename over the original), changing only the profile_id key, so a crash
// leaves either the old or the new file, never neither. Files that are not
// workflow JSON, or already name a profile, are left untouched, which also
// makes the pass idempotent. It returns the ids it stamped.
func StampFileWorkflowProfiles(dir, fallbackID string, ownerOf func(workflowID string) string) ([]string, error) {
	if fallbackID == "" && ownerOf == nil {
		return nil, ErrNoProfile
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var stamped []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var doc map[string]json.RawMessage
		if json.Unmarshal(raw, &doc) != nil {
			continue
		}
		var id string
		if json.Unmarshal(doc["id"], &id) != nil || id == "" {
			continue // not a workflow file
		}
		var owner string
		_ = json.Unmarshal(doc["profile_id"], &owner)
		if owner != "" {
			continue
		}
		profileID := fallbackID
		if ownerOf != nil {
			if o := ownerOf(id); o != "" {
				profileID = o
			}
		}
		if profileID == "" {
			continue
		}
		quoted, _ := json.Marshal(profileID)
		doc["profile_id"] = quoted
		out, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return stamped, fmt.Errorf("stamping workflow file %s: %w", e.Name(), err)
		}
		if err := writeFileAtomic(path, out); err != nil {
			return stamped, fmt.Errorf("stamping workflow file %s: %w", e.Name(), err)
		}
		stamped = append(stamped, id)
	}
	return stamped, nil
}
