package stop

import (
	"os"
	"path/filepath"
	"testing"
)

// HasRunTraces is what lets a host that never read a run's row still refuse while the database is unreadable. A directory
// that is not there is a clean no; a per-run secret is a yes; a directory that cannot be listed is a yes (it cannot be ruled out).
func TestHasRunTraces(t *testing.T) {
	base := t.TempDir()
	if (RunDir{Dir: filepath.Join(base, "none")}).HasRunTraces() {
		t.Fatal("a missing run directory reports a trace")
	}
	dir := filepath.Join(base, "run")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1.stop"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if (RunDir{Dir: dir}).HasRunTraces() {
		t.Fatal("a stop marker alone is not a per-run secret")
	}
	if err := (RunDir{Dir: dir}).CreateSecret(1); err != nil {
		t.Fatal(err)
	}
	if !(RunDir{Dir: dir}).HasRunTraces() {
		t.Fatal("a per-run secret is a trace")
	}
	// Deleting only the secrets must not clear the traces: the witness and the generation counter are made by a run too.
	for _, name := range []string{"witness", "generation-counter"} {
		d := filepath.Join(base, "only-"+name)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if !(RunDir{Dir: d}).HasRunTraces() {
			t.Fatalf("a %s file alone is not a trace", name)
		}
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !(RunDir{Dir: file}).HasRunTraces() {
		t.Fatal("a run directory that cannot be listed is a trace")
	}
}
