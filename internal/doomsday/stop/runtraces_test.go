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

// The names HasRunTraces looks for are the names the code that makes a run's files really writes: renaming either file breaks
// this test instead of silently emptying the trace.
func TestHasRunTracesSeesTheFilesTheWitnessAndTheCounterReallyWrite(t *testing.T) {
	w := newWitness(t, nil)
	a, _ := newAnchor(t)
	c := GenCounter{Dir: w.Dir, Keys: testKeyer()}
	if _, err := c.NextGeneration(0, w, &a); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(w.Dir.Dir, "witness")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if !(RunDir{Dir: w.Dir.Dir}).HasRunTraces() {
		t.Fatal("the generation counter, as the code writes it, is not seen")
	}
	if err := os.Remove(filepath.Join(w.Dir.Dir, "generation-counter")); err != nil {
		t.Fatal(err)
	}
	d := RunDir{Dir: t.TempDir()}
	if err := (Witness{Dir: d, Keys: testKeyer()}).Init(); err != nil {
		t.Fatal(err)
	}
	if !d.HasRunTraces() {
		t.Fatal("the witness, as the code writes it, is not seen")
	}
}
