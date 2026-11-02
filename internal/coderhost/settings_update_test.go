package coderhost

import (
	"context"
	"database/sql"
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// patchSettings is the operator's write: a patch applied to the settings as they are when it is stored.
func patchSettings(t *testing.T, st *Store, conv, patch string) {
	t.Helper()
	if _, err := st.UpdateSettings("p", conv, func(_ context.Context, _ *sql.Conn, cur Settings) (Settings, error) {
		next, _, err := cur.Apply([]byte(patch))
		return next, err
	}); err != nil {
		t.Errorf("operator write %s: %v", patch, err)
	}
}

// interleave makes the first settings write that reads its settings start other(), wait long enough for it to
// finish if nothing stops it, and carry on: a write that holds the lock makes other wait for it, a read that
// is followed by a whole-object write lets other finish first. It returns a function that joins other.
func interleave(t *testing.T, other func()) (join func()) {
	t.Helper()
	var wg sync.WaitGroup
	var once sync.Once
	settingsAfterRead = func() {
		once.Do(func() {
			wg.Add(1)
			go func() { defer wg.Done(); other() }()
			time.Sleep(300 * time.Millisecond)
		})
	}
	t.Cleanup(func() { settingsAfterRead = nil })
	return wg.Wait
}

// Review round 1, P1-A: a master's SetChild patch of another key must not bring back the Mission toggle it
// read before the operator changed it.
func TestAStaleChildPatchCannotUndoTheOperatorsMissionToggle(t *testing.T) {
	for _, tc := range []struct {
		name         string
		start, patch string
		want         bool
	}{
		{"the operator turns it on", "false", `{"mission":true}`, true},
		{"the operator turns it off", "true", `{"mission":false}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newStoreT(t)
			m, _ := master(t, st, "coding")
			kid := spawn(t, st, m)
			patchSettings(t, st, kid, `{"mission":`+tc.start+`}`)
			join := interleave(t, func() { patchSettings(t, st, kid, tc.patch) })
			if _, _, err := st.SetChild(m, kid, []byte(`{"checkin_minutes":20}`)); err != nil {
				t.Fatal(err)
			}
			join()
			got, err := st.GetSettings("p", kid)
			if err != nil {
				t.Fatal(err)
			}
			if got.Mission != tc.want {
				t.Fatalf("mission = %v after the operator's write, want %v: the master's stale object was written back", got.Mission, tc.want)
			}
			if got.CheckinMinutes != 20 {
				t.Fatalf("checkin_minutes = %d: the master's own patch was lost", got.CheckinMinutes)
			}
		})
	}
}

// Two writers of different keys both survive, whichever stores first.
func TestConcurrentWritersOfDifferentKeysBothSurvive(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	kid := spawn(t, st, m)
	join := interleave(t, func() {
		if _, _, err := st.SetChild(m, kid, []byte(`{"mesh_alias":"worker-9"}`)); err != nil {
			t.Errorf("second writer: %v", err)
		}
	})
	if _, _, err := st.SetChild(m, kid, []byte(`{"silent_minutes":45}`)); err != nil {
		t.Fatal(err)
	}
	join()
	got, _ := st.GetSettings("p", kid)
	if got.MeshAlias != "worker-9" || got.SilentMinutes != 45 {
		t.Fatalf("alias %q, silent %d: one writer's key was lost", got.MeshAlias, got.SilentMinutes)
	}
}

// The writer's closure sees the settings of the moment of the write, and a failed one stores nothing.
func TestUpdateSettingsRollsBackAFailedChange(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	kid := spawn(t, st, m)
	_, err := st.UpdateSettings("p", kid, func(_ context.Context, _ *sql.Conn, cur Settings) (Settings, error) {
		cur.CheckinMinutes = 33
		return cur, errBoom
	})
	if err != errBoom {
		t.Fatalf("err = %v", err)
	}
	if got, _ := st.GetSettings("p", kid); got.CheckinMinutes == 33 {
		t.Fatal("a refused change was stored")
	}
	if _, err := st.UpdateSettings("p", kid, func(_ context.Context, _ *sql.Conn, cur Settings) (Settings, error) {
		cur.CheckinMinutes = -1
		return cur, nil
	}); err == nil {
		t.Fatal("an invalid result was stored")
	}
}

type boom string

func (b boom) Error() string { return string(b) }

const errBoom = boom("boom")

// The only writers of the settings document are SaveSettings (fixtures) and UpdateSettings: no production
// file outside settings.go names SaveSettings, in any package, and the document is written by no other SQL.
func TestSettingsAreWrittenOnlyThroughUpdateSettings(t *testing.T) {
	root, _ := filepath.Abs("../..")
	sqlWrite := func(v string) bool {
		l := strings.ToLower(v)
		return strings.Contains(l, "update coder_sessions") && strings.Contains(l, "settings")
	}
	for _, dir := range []string{"internal", "cmd", "wails-app"} {
		_ = filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() && (info.Name() == "node_modules" || info.Name() == "frontend") {
				if err == nil {
					return filepath.SkipDir
				}
				return nil
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			isOwner := rel == filepath.Join("internal", "coderhost", "settings.go")
			ast.Inspect(parse(t, path), func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.Ident:
					if v.Name == "SaveSettings" && !isOwner {
						t.Errorf("%s names SaveSettings: a whole-object write; use UpdateSettings", rel)
					}
				case *ast.BasicLit:
					if sqlWrite(v.Value) && !isOwner {
						t.Errorf("%s writes the settings column outside UpdateSettings", rel)
					}
				}
				return true
			})
			return nil
		})
	}
}
