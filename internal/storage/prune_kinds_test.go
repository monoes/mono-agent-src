package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Adoption snapshots, up to one an hour, must not push out the snapshot taken before a migration (R12-08).
func TestPruneKeepsPreMigrateSnapshotsApartFromAdoptionOnes(t *testing.T) {
	dir := t.TempDir()
	real := []string{"pre-migrate-20261101T000000Z-v100.db", "pre-migrate-20261102T000000Z-v101.db", "pre-migrate-20261103T000000Z-v102.db"}
	for h := 10; h < 16; h++ {
		real = append(real, fmt.Sprintf("pre-migrate-20261104T%02d0000Z-v102-adopt.db", h))
	}
	for _, n := range real {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := prunePreMigrateSnapshots(dir, 3); err != nil {
		t.Fatal(err)
	}
	for _, n := range real[:3] {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("pre-migrate snapshot %s was pruned by adoption snapshots", n)
		}
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*-adopt.db"))
	if len(left) != keepAdoptionSnapshots {
		t.Errorf("%d adoption snapshots left, want %d", len(left), keepAdoptionSnapshots)
	}
}
