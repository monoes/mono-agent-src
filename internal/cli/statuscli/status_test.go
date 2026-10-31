package statuscli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

// status writes to the process's stdout, so its tests swap os.Stdout and run serially.
func capture(t *testing.T, fn func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	// Drain the pipe while the command runs: output larger than the pipe buffer
	// would otherwise block the writer forever.
	drained := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		drained <- b
	}()
	os.Stdout = w
	runErr := fn()
	os.Stdout = old
	w.Close()
	out := <-drained
	if runErr != nil {
		t.Fatal(runErr)
	}
	return string(out)
}

func TestStatus(t *testing.T) {
	path := testdb.Path(t)
	var root string
	d := func(jsonOut bool) Deps {
		return Deps{
			JSONOutput: func() bool { return jsonOut },
			OpenDB:     func() (*storage.Database, error) { return storage.NewDatabase(path) },
			DBPath:     func() string { return path },
			ProfileID:  func() string { return "default" },
			StatusDaemons: func(r string) map[string]interface{} {
				root = r
				return map[string]interface{}{"daemon": map[string]interface{}{"running": false}}
			},
		}
	}
	run := func(jsonOut bool) string {
		return capture(t, func() error {
			cmd := NewCommand(d(jsonOut))
			cmd.SetArgs(nil)
			return cmd.Execute()
		})
	}

	var doc map[string]interface{}
	if err := json.Unmarshal([]byte(run(true)), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["db_path"] != path || doc["people"] != float64(0) || doc["daemon"] == nil || root == "" {
		t.Errorf("status --json: %v (root %q)", doc, root)
	}
	text := run(false)
	for _, want := range []string{"Database Path", filepath.Base(path), "Social Lists"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
}
