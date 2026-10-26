package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
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
	return StampFileWorkflowProfilesFor(dir, fallbackID, ownerOf, nil)
}

// StampFileWorkflowProfilesFor is StampFileWorkflowProfiles with locked, which runs the whole
// look-and-rewrite of one file (the callback do) under whatever excludes the file's other writers,
// and may decline to run it at all (a file it does not name is not opened). With locked nil every
// file is handled without a lock.
func StampFileWorkflowProfilesFor(dir, fallbackID string, ownerOf func(workflowID string) string, locked func(fileID string, do func() error) error) ([]string, error) {
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
		name := e.Name()
		var did string
		one := func() error {
			var err error
			did, err = stampOne(filepath.Join(dir, name), name, fallbackID, ownerOf)
			return err
		}
		if locked != nil {
			if err := locked(strings.TrimSuffix(name, ".json"), one); err != nil {
				if did != "" {
					stamped = append(stamped, did)
				}
				if errors.Is(err, ErrStampSkipped) {
					continue
				}
				return stamped, err
			}
		} else if err := one(); err != nil {
			return stamped, err
		}
		if did != "" {
			stamped = append(stamped, did)
		}
	}
	return stamped, nil
}

// ErrStampSkipped is what a locked callback returns when it could not take the lock: the file is
// left for the next pass.
var ErrStampSkipped = errors.New("workflow file left for the next pass")

// stampOne gives one ownerless workflow file its owner, or leaves it. It returns the workflow id
// when it rewrote the file. The file is read again just before it is replaced and left alone if it
// changed in between.
func stampOne(path, name, fallbackID string, ownerOf func(string) string) (string, error) {
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return "", nil // a link, pipe or device is not stamped (and never opened)
	}
	raw, err := readRegularFile(path)
	if err != nil {
		return "", nil
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(raw, &doc) != nil {
		return "", nil
	}
	var id string
	if json.Unmarshal(doc["id"], &id) != nil || id == "" {
		return "", nil // not a workflow file
	}
	var owner string
	_ = json.Unmarshal(doc["profile_id"], &owner)
	if owner != "" {
		return "", nil
	}
	profileID := fallbackID
	if ownerOf != nil {
		if o := ownerOf(id); o != "" {
			profileID = o
		}
	}
	if profileID == "" {
		return "", nil
	}
	quoted, _ := json.Marshal(profileID)
	doc["profile_id"] = quoted
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("stamping workflow file %s: %w", name, err)
	}
	if again, rerr := readRegularFile(path); rerr != nil || !bytes.Equal(again, raw) {
		return "", nil // edited meanwhile: not ours to overwrite
	}
	if err := writeFileAtomic(path, out); err != nil {
		return "", fmt.Errorf("stamping workflow file %s: %w", name, err)
	}
	return id, nil
}
