//go:build windows

package stop

import (
	"fmt"
	"os"
)

// syncDir on Windows only checks that the directory exists. A plain os.Open of a directory gives a read-only handle and
// FlushFileBuffers on it fails with "Access is denied", which made every durable write fail; Windows has no directory
// flush that works the way fsync(dir) does. What a durable write still does there: the file's own data and metadata are
// flushed (File.Sync = FlushFileBuffers on the file) before the write is reported, and NTFS journals the creation of the
// directory entry. The difference from Linux: a power cut right after a create can lose the directory entry of a file
// that was flushed, where Linux's directory fsync closes that window. internal/account does the same.
func syncDir(dir string) error {
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("stop: %s is not a directory", dir)
	}
	return nil
}
