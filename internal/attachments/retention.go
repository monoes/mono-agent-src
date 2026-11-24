package attachments

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DefaultRetention is how long a synced attachment is kept before Prune
// removes it: a year after it was written. Attachments are copies of mail
// that stays in the mailbox, so they can be fetched again by the next sync.
const DefaultRetention = 365 * 24 * time.Hour

// ProfileDir is the folder holding one profile's attachments, so that a
// profile's files are separate from every other profile's (R11-09).
func ProfileDir(profileID string) string {
	return filepath.Join(Dir(), safeSegment(profileID, "profile"))
}

// SaveForProfile is SaveDistinct into the profile's own folder:
// <root>/<profile>/<message>/<file>.
func SaveForProfile(profileID, messageID, filename string, data []byte, seen map[string]bool) (string, error) {
	return saveIn(ProfileDir(profileID), messageID, filename, data, seen)
}

// RemoveMessage deletes the files kept for one message of a profile (the
// profile folder layout). It reports nothing when there are none.
func RemoveMessage(profileID, messageID string) error {
	return removeMessageDir(filepath.Join(ProfileDir(profileID), safeSegment(messageID, "message")))
}

// RemoveLegacyMessage is RemoveMessage for the flat layout older syncs wrote
// (<root>/<message>/<file>).
func RemoveLegacyMessage(messageID string) error {
	return removeMessageDir(filepath.Join(Dir(), safeSegment(messageID, "message")))
}

func removeMessageDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !fi.IsDir() { // a planted link or file is not ours to delete
		return fmt.Errorf("attachments: %s is not a folder", dir)
	}
	return os.RemoveAll(dir)
}

// Prune removes every message folder under the attachments root, in either
// layout, whose newest file was written more than maxAge before now, and
// returns how many folders went. A maxAge of zero or less prunes nothing.
func Prune(maxAge time.Duration, now time.Time) (int, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	root := Dir()
	top, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	var first error
	for _, e := range top {
		if !e.IsDir() { // Lstat-based: a symlink entry is not a directory here
			continue
		}
		n, err := pruneFolder(filepath.Join(root, e.Name()), maxAge, now)
		removed += n
		if err != nil && first == nil {
			first = err
		}
	}
	return removed, first
}

// pruneFolder handles one top-level folder: a profile folder (whose children
// are message folders) or, in the flat layout, a message folder itself.
func pruneFolder(dir string, maxAge time.Duration, now time.Time) (int, error) {
	kids, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	hasFolder := false
	for _, k := range kids {
		if k.IsDir() {
			hasFolder = true
		}
	}
	if !hasFolder { // flat layout: this is a message folder
		if old, err := folderOlderThan(dir, maxAge, now); err != nil || !old {
			return 0, err
		}
		return 1, os.RemoveAll(dir)
	}
	removed := 0
	var first error
	for _, k := range kids {
		if !k.IsDir() {
			continue
		}
		msg := filepath.Join(dir, k.Name())
		old, err := folderOlderThan(msg, maxAge, now)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if old {
			if err := os.RemoveAll(msg); err != nil && first == nil {
				first = err
			} else if err == nil {
				removed++
			}
		}
	}
	_ = os.Remove(dir) // only succeeds once the profile folder is empty
	return removed, first
}

// folderOlderThan is true when no file in dir is newer than maxAge.
func folderOlderThan(dir string, maxAge time.Duration, now time.Time) (bool, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, f := range files {
		info, err := f.Info()
		if err != nil {
			return false, err
		}
		if now.Sub(info.ModTime()) <= maxAge {
			return false, nil
		}
	}
	return true, nil
}
