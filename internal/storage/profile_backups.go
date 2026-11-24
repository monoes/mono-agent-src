package storage

import (
	"os"
	"time"
)

const (
	// KeepProfileDeleteBackups and KeepProfileDeleteBackupsFor bound the
	// pre-delete snapshots: one is removed only when it is neither among the
	// newest KeepProfileDeleteBackups nor younger than KeepProfileDeleteBackupsFor.
	KeepProfileDeleteBackups    = 3
	KeepProfileDeleteBackupsFor = 30 * 24 * time.Hour
)

// PruneProfileDeleteBackupFiles removes the old pre-delete snapshots (paths
// oldest first, as ProfileDeleteBackups lists them) except current, and
// returns how many it removed and the bytes they held.
func PruneProfileDeleteBackupFiles(paths []string, current string, now time.Time) (int, int64) {
	removed, freed := 0, int64(0)
	for i, p := range paths {
		if p == current || i >= len(paths)-KeepProfileDeleteBackups {
			continue
		}
		fi, err := os.Stat(p)
		if err != nil || now.Sub(fi.ModTime()) < KeepProfileDeleteBackupsFor {
			continue
		}
		if os.Remove(p) == nil {
			removed++
			freed += fi.Size()
		}
	}
	return removed, freed
}

// ProfileDeleteBackupsSize is how many pre-delete snapshots there are and
// the bytes they take together.
func (d *Database) ProfileDeleteBackupsSize() (int, int64, error) {
	paths, err := d.ProfileDeleteBackups()
	if err != nil {
		return 0, 0, err
	}
	var total int64
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil {
			total += fi.Size()
		}
	}
	return len(paths), total, nil
}
