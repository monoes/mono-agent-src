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
	return StampFileWorkflowProfilesFor(dir, fallbackID, ownerOf, nil, nil)
}

// maxStampNames bounds how many .json names one stamp pass looks at.
const maxStampNames = 5000

// StampFileWorkflowProfilesFor is StampFileWorkflowProfiles with two more callbacks. eligible,
// checked without any lock, names the files worth opening at all (all of them when nil). locked
// runs the whole look-and-rewrite of one eligible file (the callback do) under whatever excludes
// the file's other writers, and may decline to run it (it must then check eligibility again). With
// locked nil every eligible file is handled without a lock. The pass stops at the first file locked
// reports ErrStampSkipped for: the lock is busy, and each further file would only wait for it too.
func StampFileWorkflowProfilesFor(dir, fallbackID string, ownerOf func(workflowID string) string, eligible func(fileID string) bool, locked func(fileID string, do func() error) error) ([]string, error) {
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
	looked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if looked++; looked > maxStampNames {
			break
		}
		name := e.Name()
		if eligible != nil && !eligible(strings.TrimSuffix(name, ".json")) {
			continue
		}
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
					return stamped, nil // the lock is busy: the rest waits for the next pass
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
