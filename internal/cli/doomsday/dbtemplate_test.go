package doomsday

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/monoes/mono-agent/internal/storage"
)

// Every test that needs a store used to apply all the migrations to an empty file (about 0.2 s plain, several seconds
// under -race on a loaded runner, times some two hundred tests). The migrated file is built once per test binary and
// each test gets a copy; its own ApplyMigrations call then finds everything recorded.
var (
	templateOnce  sync.Once
	templateBytes []byte
	templateErr   error
)

func migratedTemplate() ([]byte, error) {
	templateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "dd-template-")
		if err != nil {
			templateErr = err
			return
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "dd.db")
		db, err := storage.NewDatabase(path)
		if err != nil {
			templateErr = err
			return
		}
		if err := db.ApplyMigrations(); err != nil {
			db.DB.Close()
			templateErr = err
			return
		}
		// The last close checkpoints the WAL into the main file and removes the -wal and -shm files.
		if err := db.DB.Close(); err != nil {
			templateErr = err
			return
		}
		templateBytes, templateErr = os.ReadFile(path)
	})
	return templateBytes, templateErr
}

// newMigratedDatabase is storage.NewDatabase on a copy of the migrated template at path.
func newMigratedDatabase(path string) (*storage.Database, error) {
	b, err := migratedTemplate()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, err
	}
	return storage.NewDatabase(path)
}
