package stop

import (
	"os"
)

// File is the part of *os.File the durable writes need.
type File interface {
	Write(p []byte) (int, error)
	Sync() error
	Close() error
}

// FS is the file-system seam of the run directory; tests inject faults (a failed fsync, a full disk, a power
// loss that drops every write after a chosen point).
type FS interface {
	CreateExclusive(path string, perm os.FileMode) (File, error)
	ReadFile(path string) ([]byte, error)
	Remove(path string) error
	SyncDir(dir string) error
	MkdirAll(dir string, perm os.FileMode) error
}

// OSFS is the real file system.
type OSFS struct{}

func (OSFS) CreateExclusive(path string, perm os.FileMode) (File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
}
func (OSFS) ReadFile(path string) ([]byte, error)        { return os.ReadFile(path) }
func (OSFS) Remove(path string) error                    { return os.Remove(path) }
func (OSFS) MkdirAll(dir string, perm os.FileMode) error { return os.MkdirAll(dir, perm) }

// SyncDir fsyncs a directory so a created or removed entry survives power loss.
func (OSFS) SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// writeDurable creates path exclusively, writes data, and fsyncs the file and its directory. On any failure it
// removes what it created (best effort) so a half-written file is not left behind as a record.
func writeDurable(fsys FS, dir, path string, data []byte) error {
	f, err := fsys.CreateExclusive(path, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = fsys.SyncDir(dir)
	}
	if err != nil {
		_ = fsys.Remove(path)
	}
	return err
}
