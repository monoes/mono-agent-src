//go:build !windows

package workflow

import (
	"errors"
	"os"
)

// syncDir flushes a directory's entries (the rename) to disk.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}
