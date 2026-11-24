package dbcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/cli/clicore"
)

func pruneRun(t *testing.T, d Deps) (string, error) {
	t.Helper()
	cmd := NewCommand(d)
	cmd.SetArgs([]string{"prune-backups"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	return out.String(), err
}

func seedSnapshots(t *testing.T, dbPath string, n int, age time.Duration) []string {
	t.Helper()
	dir := filepath.Join(filepath.Dir(dbPath), "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for i := 1; i <= n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("pre-delete-profile-20200101T0000%02d-001.db", i))
		if err := os.WriteFile(p, []byte("snapshot"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-age)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return paths
}

// Old snapshots beyond the newest three go; the newest three stay.
func TestPruneBackupsKeepsTheNewestThree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monoagent.db")
	paths := seedSnapshots(t, path, 6, 90*24*time.Hour)
	out, err := pruneRun(t, deps(path, true))
	if err != nil {
		t.Fatal(err)
	}
	var res PruneResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if res.Removed != 3 || res.Kept != 3 || res.FreedBytes != 3*int64(len("snapshot")) {
		t.Errorf("result %+v", res)
	}
	for i, p := range paths {
		_, statErr := os.Stat(p)
		if gone := os.IsNotExist(statErr); gone != (i < 3) {
			t.Errorf("snapshot %d gone=%v", i, gone)
		}
	}
}

// Recent snapshots stay however many there are.
func TestPruneBackupsKeepsRecentOnes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monoagent.db")
	seedSnapshots(t, path, 6, time.Hour)
	out, err := pruneRun(t, deps(path, true))
	if err != nil {
		t.Fatal(err)
	}
	var res PruneResult
	_ = json.Unmarshal([]byte(out), &res)
	if res.Removed != 0 || res.Kept != 6 {
		t.Errorf("result %+v", res)
	}
}

// An agent, chat or workflow caller is refused before anything is read.
func TestPruneBackupsIsTheOperatorsOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monoagent.db")
	paths := seedSnapshots(t, path, 6, 90*24*time.Hour)
	t.Setenv("CLAUDECODE", "1")
	_, err := pruneRun(t, deps(path, true))
	if err == nil || clicore.ExitCodeFor(err) != 3 {
		t.Fatalf("a marked caller: %v", err)
	}
	if _, statErr := os.Stat(paths[0]); statErr != nil {
		t.Error("the refused call removed a snapshot")
	}
}
